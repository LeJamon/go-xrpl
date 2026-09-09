package invariants

import (
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

type mptInvariantTransferChange struct {
	before    uint64
	beforeSet bool
	after     uint64
	afterSet  bool
	deleted   bool
}

// mptInvariantView adapts the invariant read view to the state-layer view used
// by the MPT authorization and freeze helpers. Invariant checks are read-only;
// the mutating methods fail closed if a helper ever attempts to write.
type mptInvariantView struct {
	ReadView
	rules           *amendment.Rules
	parentCloseTime uint32
}

func (v mptInvariantView) Rules() *amendment.Rules { return v.rules }

func (v mptInvariantView) ParentCloseTime() uint32 { return v.parentCloseTime }

func (mptInvariantView) Insert(keylet.Keylet, []byte) error {
	return errors.New("MPT invariant view is read-only")
}

func (mptInvariantView) Update(keylet.Keylet, []byte) error {
	return errors.New("MPT invariant view is read-only")
}

func (mptInvariantView) Erase(keylet.Keylet) error {
	return errors.New("MPT invariant view is read-only")
}

// checkValidMPTTransfer enforces authorization for MPT transfers once the
// transfer invariant is active. AccountRoot pseudo-account status is captured
// from the pre-transaction image so deleting a broker or vault in the same
// transaction cannot erase the authorization exemption before this check runs.
func checkValidMPTTransfer(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || (!rules.Enabled(amendment.FeatureMPTokensV2) && !rules.Enabled(amendment.FeatureFixCleanup3_4_0)) {
		return nil
	}
	if hasPrivilege(tx.TxType(), overrideFreeze) {
		return nil
	}
	fix340Enabled := rules.Enabled(amendment.FeatureFixCleanup3_4_0)
	parentCloseTime := uint32(0)
	if provider, ok := view.(interface{ ParentCloseTime() uint32 }); ok {
		parentCloseTime = provider.ParentCloseTime()
	}
	viewForMPT := mptInvariantView{ReadView: view, rules: rules, parentCloseTime: parentCloseTime}
	loanDefault := findLoanDefaultFreezeExemption(tx, view, rules)

	changes := make(map[[24]byte]map[[20]byte]*mptInvariantTransferChange)
	pseudoBefore := make(map[[20]byte]bool)
	pseudoSeen := make(map[[20]byte]bool)
	deletedAuthorized := make(map[[32]byte]bool)
	for _, e := range entries {
		if e.EntryType == entry.TypeAccountRoot && e.Before != nil {
			account, err := state.ParseAccountRoot(e.Before)
			if err != nil {
				return invalidMPTTransfer(fmt.Sprintf("AccountRoot before: %v", err))
			}
			id, err := state.DecodeAccountID(account.Account)
			if err != nil {
				return invalidMPTTransfer(fmt.Sprintf("AccountRoot account: %v", err))
			}
			pseudoBefore[id] = account.IsPseudoAccount()
			pseudoSeen[id] = true
		}
		if e.EntryType != entry.TypeMPToken {
			continue
		}
		setImage := func(data []byte, before bool) *InvariantViolation {
			if data == nil {
				return nil
			}
			token, err := state.ParseMPToken(data)
			if err != nil {
				return invalidMPTTransfer(fmt.Sprintf("MPToken: %v", err))
			}
			byHolder := changes[token.MPTokenIssuanceID]
			if byHolder == nil {
				byHolder = make(map[[20]byte]*mptInvariantTransferChange)
				changes[token.MPTokenIssuanceID] = byHolder
			}
			change := byHolder[token.Account]
			if change == nil {
				change = &mptInvariantTransferChange{}
				byHolder[token.Account] = change
			}
			if before {
				change.before = token.MPTAmount
				change.beforeSet = true
				if e.IsDelete {
					change.deleted = true
					deletedKey := e.Key
					if deletedKey == ([32]byte{}) {
						deletedKey = keylet.MPTokenByID(token.MPTokenIssuanceID, token.Account).Key
					}
					deletedAuthorized[deletedKey] = token.Flags&entry.LsfMPTAuthorized != 0
				}
			} else {
				change.after = token.MPTAmount
				change.afterSet = true
			}
			return nil
		}
		if v := setImage(e.Before, true); v != nil {
			return v
		}
		after := e.After
		if e.IsDelete {
			after = e.DeleteFinal
		}
		if after != nil {
			if v := setImage(after, false); v != nil {
				return v
			}
		} else if e.IsDelete && e.Before != nil {
			// A deleted MPToken has a zero final balance. Preserve that image
			// when metadata does not carry sMD_DeleteFinal.
			token, err := state.ParseMPToken(e.Before)
			if err != nil {
				return invalidMPTTransfer(fmt.Sprintf("deleted MPToken: %v", err))
			}
			byHolder := changes[token.MPTokenIssuanceID]
			change := byHolder[token.Account]
			change.after = 0
			change.afterSet = true
		}
	}
	if fix340Enabled && result != TesSUCCESS && len(deletedAuthorized) != 0 {
		return invalidMPTTransfer("MPToken deleted on failure")
	}
	isDEX := isMPTDEX(tx)

	for issuanceID, byHolder := range changes {
		issuanceRaw, err := view.Read(keylet.MPTIssuance(issuanceID))
		if err != nil {
			return invalidMPTTransfer(fmt.Sprintf("MPTokenIssuance: %v", err))
		}
		if issuanceRaw == nil {
			for _, change := range byHolder {
				if !change.deleted && change.afterSet && change.before != change.after {
					return invalidMPTTransfer("orphaned MPToken balance changed")
				}
			}
			continue
		}
		issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
		if err != nil {
			return invalidMPTTransfer(fmt.Sprintf("MPTokenIssuance: %v", err))
		}
		issuer := issuance.Issuer
		senders, receivers := 0, 0
		invalid := false
		for account, change := range byHolder {
			before := uint64(0)
			if change.beforeSet {
				before = change.before
			}
			after := uint64(0)
			if change.afterSet {
				after = change.after
			}
			if !change.afterSet || before == after {
				continue
			}
			if after > before {
				receivers++
			} else {
				senders++
			}
			if (!loanDefaultMPTFreezeExempt(loanDefault, issuanceID, account) && mptutil.IsFrozen(viewForMPT, issuanceID, account)) ||
				!mptTransferAuthorized(viewForMPT, issuanceID, issuer, account, issuance.Flags&entry.LsfMPTRequireAuth != 0, pseudoBefore, pseudoSeen, deletedAuthorized, change) {
				invalid = true
			}
		}
		waivesCanTransfer := tx.TxType() == protocol.TxTypeAMMWithdraw ||
			(fix320Enabled(rules) && (tx.TxType() == protocol.TxTypeVaultWithdraw ||
				tx.TxType() == protocol.TxTypeLoanBrokerCoverWithdraw || tx.TxType() == protocol.TxTypeLoanPay))
		canTransfer := issuance.Flags&entry.LsfMPTCanTransfer != 0 || waivesCanTransfer
		if senders > 0 && receivers > 0 && (invalid || !canTransfer || (isDEX && issuance.Flags&entry.LsfMPTCanTrade == 0)) {
			return &InvariantViolation{
				Name:    "ValidMPTTransfer",
				Message: "invalid MPToken transfer between holders",
			}
		}
		if fix340Enabled && result != TesSUCCESS && (senders > 0 || receivers > 0) {
			return invalidMPTTransfer("MPToken balance changed on failure")
		}
	}
	return nil
}

func mptTransferAuthorized(
	view state.LedgerView,
	issuanceID [24]byte,
	issuer, account [20]byte,
	requireAuth bool,
	pseudoBefore map[[20]byte]bool,
	pseudoSeen map[[20]byte]bool,
	deletedAuthorized map[[32]byte]bool,
	change *mptInvariantTransferChange,
) bool {
	if account == issuer || (pseudoSeen[account] && pseudoBefore[account]) {
		return true
	}
	if !pseudoSeen[account] {
		raw, err := view.Read(keylet.Account(account))
		if err == nil && raw != nil {
			if ar, parseErr := state.ParseAccountRoot(raw); parseErr == nil && ar.IsPseudoAccount() {
				return true
			}
		}
	}
	if change.deleted {
		return !requireAuth || deletedAuthorized[keylet.MPTokenByID(issuanceID, account).Key]
	}
	parentCloseTime := uint32(0)
	if provider, ok := view.(interface{ ParentCloseTime() uint32 }); ok {
		parentCloseTime = provider.ParentCloseTime()
	}
	return mptutil.RequireAuthWithTypeAt(view, issuanceID, account, mptutil.LegacyAuth, parentCloseTime).IsSuccess()
}

func fix320Enabled(rules *amendment.Rules) bool {
	return rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_2_0)
}

func isMPTDEX(tx Transaction) bool {
	switch tx.TxType() {
	case protocol.TxTypeAMMCreate, protocol.TxTypeAMMDeposit, protocol.TxTypeOfferCreate:
		return true
	case protocol.TxTypePayment:
		fields, err := tx.Flatten()
		if err != nil {
			return true
		}
		amount, ok := mptAmountAsset(fields["Amount"])
		if !ok {
			return false
		}
		sendMax, ok := fields["SendMax"]
		if !ok {
			return false
		}
		maxAsset, ok := mptAmountAsset(sendMax)
		return ok && !sameMPTAsset(amount, maxAsset)
	default:
		return false
	}
}

func mptAmountAsset(value any) (Asset, bool) {
	switch v := value.(type) {
	case string:
		return Asset{Currency: "XRP"}, true
	case map[string]any:
		if id, ok := v["mpt_issuance_id"].(string); ok && id != "" {
			return Asset{MPTIssuanceID: strings.ToUpper(id)}, true
		}
		currency, currencyOK := v["currency"].(string)
		issuer, issuerOK := v["issuer"].(string)
		if !currencyOK {
			return Asset{}, false
		}
		if currency == "XRP" && !issuerOK {
			return Asset{Currency: "XRP"}, true
		}
		if !issuerOK {
			return Asset{}, false
		}
		return Asset{Currency: currency, Issuer: issuer}, true
	default:
		return Asset{}, false
	}
}

func sameMPTAsset(a, b Asset) bool {
	if a.IsMPT() || b.IsMPT() {
		return a.IsMPT() && b.IsMPT() && strings.EqualFold(a.MPTIssuanceID, b.MPTIssuanceID)
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

func invalidMPTTransfer(message string) *InvariantViolation {
	return &InvariantViolation{Name: "ValidMPTTransfer", Message: message}
}

func loanDefaultMPTFreezeExempt(exemption *loanDefaultFreezeExemption, id [24]byte, account [20]byte) bool {
	if exemption == nil || !exemption.hasMPT || exemption.mptID != id {
		return false
	}
	for _, address := range []string{exemption.broker, exemption.vault} {
		accountID, err := state.DecodeAccountID(address)
		if err == nil && accountID == account {
			return true
		}
	}
	return false
}
