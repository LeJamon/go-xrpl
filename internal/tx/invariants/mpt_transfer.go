package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type mptTransferChange struct {
	before    uint64
	beforeSet bool
	after     uint64
	afterSet  bool
	deleted   bool
}

// checkValidMPTTransfer enforces authorization for MPT transfers once the
// transfer invariant is active. AccountRoot pseudo-account status is captured
// from the pre-transaction image so deleting a broker or vault in the same
// transaction cannot erase the authorization exemption before this check runs.
func checkValidMPTTransfer(_ Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || (!rules.Enabled(amendment.FeatureMPTokensV2) && !rules.Enabled(amendment.FeatureFixCleanup3_4_0)) {
		return nil
	}
	if result != TesSUCCESS {
		return nil
	}

	changes := make(map[[24]byte]map[[20]byte]*mptTransferChange)
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
				byHolder = make(map[[20]byte]*mptTransferChange)
				changes[token.MPTokenIssuanceID] = byHolder
			}
			change := byHolder[token.Account]
			if change == nil {
				change = &mptTransferChange{}
				byHolder[token.Account] = change
			}
			if before {
				change.before = token.MPTAmount
				change.beforeSet = true
				if e.IsDelete {
					change.deleted = true
					deletedAuthorized[e.Key] = token.Flags&entry.LsfMPTAuthorized != 0
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

	for issuanceID, byHolder := range changes {
		issuanceRaw, err := view.Read(keylet.MPTIssuance(issuanceID))
		if err != nil {
			return invalidMPTTransfer(fmt.Sprintf("MPTokenIssuance: %v", err))
		}
		if issuanceRaw == nil {
			continue
		}
		issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
		if err != nil {
			return invalidMPTTransfer(fmt.Sprintf("MPTokenIssuance: %v", err))
		}
		issuer := mptIssuer(issuanceID)
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
			if before == after {
				continue
			}
			if after > before {
				receivers++
			} else {
				senders++
			}
			if !mptTransferAuthorized(view, issuanceID, issuer, account, issuance.Flags&entry.LsfMPTRequireAuth != 0, pseudoBefore, pseudoSeen, deletedAuthorized, change) {
				invalid = true
			}
		}
		if senders > 0 && receivers > 0 && (invalid || issuance.Flags&entry.LsfMPTCanTransfer == 0) {
			return &InvariantViolation{
				Name:    "ValidMPTTransfer",
				Message: "invalid MPToken transfer between holders",
			}
		}
	}
	return nil
}

func mptTransferAuthorized(
	view ReadView,
	issuanceID [24]byte,
	issuer, account [20]byte,
	requireAuth bool,
	pseudoBefore map[[20]byte]bool,
	pseudoSeen map[[20]byte]bool,
	deletedAuthorized map[[32]byte]bool,
	change *mptTransferChange,
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
	if !requireAuth {
		return true
	}
	if change.deleted {
		return deletedAuthorized[keylet.MPTokenByID(issuanceID, account).Key]
	}
	raw, err := view.Read(keylet.MPTokenByID(issuanceID, account))
	if err != nil || raw == nil {
		return false
	}
	token, err := state.ParseMPToken(raw)
	return err == nil && token.Flags&entry.LsfMPTAuthorized != 0
}

func invalidMPTTransfer(message string) *InvariantViolation {
	return &InvariantViolation{Name: "ValidMPTTransfer", Message: message}
}
