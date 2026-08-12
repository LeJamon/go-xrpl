package invariants

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type clawbackEntry struct {
	key    [32]byte
	before []byte
	after  []byte
}

func checkValidClawback(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContext ...state.NumberContext) *InvariantViolation {
	if tx.TxType() != TypeClawback {
		return nil
	}

	var trustlinesChanged, mptokensChanged int
	var iou, mpt clawbackEntry
	for _, e := range entries {
		if e.Before != nil && e.EntryType == entry.TypeRippleState {
			trustlinesChanged++
			iou = clawbackEntry{key: e.Key, before: e.Before, after: e.After}
		}
		if e.Before != nil && e.EntryType == entry.TypeMPToken {
			mptokensChanged++
			mpt = clawbackEntry{key: e.Key, before: e.Before, after: e.After}
		}
	}

	if result != TesSUCCESS {
		if trustlinesChanged != 0 {
			return clawbackViolation("some trustlines were changed despite failure of the transaction")
		}
		if mptokensChanged != 0 {
			return clawbackViolation("some mptokens were changed despite failure of the transaction")
		}
		return nil
	}

	if trustlinesChanged > 1 {
		return clawbackViolation("more than one trustline changed")
	}
	if mptokensChanged > 1 {
		return clawbackViolation("more than one mptoken changed")
	}

	mptV2Enabled := rules != nil && rules.Enabled(amendment.FeatureMPTokensV2)
	if trustlinesChanged != 0 && mptokensChanged != 0 && mptV2Enabled {
		return clawbackViolation("trustline and MPToken both changed")
	}
	if trustlinesChanged == 0 && mptokensChanged == 0 {
		return nil
	}

	provider, ok := tx.(ClawbackAmountProvider)
	if !ok {
		return nil
	}
	amount := provider.ClawbackAmount()
	if !mptV2Enabled {
		if trustlinesChanged == 1 && !amount.IsMPT() && view != nil {
			return checkClawbackHolderBalance(tx, view)
		}
		return nil
	}
	if amount.IsMPT() {
		return checkMPTClawbackDelta(tx, amount, mpt)
	}
	return checkIOUClawbackDelta(tx, amount, iou, view, numberContext...)
}

func checkIOUClawbackDelta(tx Transaction, amount Amount, changed clawbackEntry, view ReadView, numberContext ...state.NumberContext) *InvariantViolation {
	issuer, err := state.DecodeAccountID(tx.TxAccount())
	if err != nil {
		return clawbackViolation("trustline clawback changed the wrong line")
	}
	holder, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return clawbackViolation("trustline clawback changed the wrong line")
	}

	if view != nil {
		if violation := checkClawbackHolderBalance(tx, view); violation != nil {
			return violation
		}
	}
	if changed.before == nil || changed.key != keylet.Line(holder, issuer, amount.Currency).Key {
		return clawbackViolation("trustline clawback changed the wrong line")
	}

	before, err := clawbackTrustLineBalance(changed.before, holder, issuer, amount.Currency, tx.TxAccount())
	if err != nil {
		return clawbackViolation("trustline clawback changed the wrong line")
	}
	after, err := clawbackTrustLineBalance(changed.after, holder, issuer, amount.Currency, tx.TxAccount())
	if err != nil {
		return clawbackViolation("trustline clawback changed the wrong line")
	}
	if before.Currency != amount.Currency || after.Currency != amount.Currency {
		return clawbackViolation("trustline clawback balance change is invalid")
	}
	clawAmount := amount
	clawAmount.Issuer = tx.TxAccount()
	if clawAmount.Signum() <= 0 {
		return clawbackViolation("trustline clawback amount is invalid")
	}
	if after.Compare(before) > 0 {
		return clawbackViolation("trustline clawback balance change is invalid")
	}
	context := state.NewNumberContext(state.MantissaScaleSmall, false)
	if len(numberContext) != 0 {
		context = numberContext[0]
	}
	delta, err := before.SubWithNumberContext(after, context, state.RoundToNearest)
	if err != nil {
		return clawbackViolation("trustline clawback balance change is invalid")
	}
	expected := clawAmount
	if before.Compare(clawAmount) < 0 {
		expected = before
	}
	if delta.Compare(expected) != 0 {
		return clawbackViolation("trustline clawback balance change is invalid")
	}
	return nil
}

func clawbackTrustLineBalance(data []byte, holder, issuer [20]byte, currency, issuerAddress string) (Amount, error) {
	if data == nil {
		return state.NewIssuedAmountFromValue(0, 0, currency, issuerAddress), nil
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		return Amount{}, err
	}
	balance := line.Balance
	if state.CompareAccountIDs(holder, issuer) > 0 {
		balance = balance.Negate()
	}
	balance.Issuer = issuerAddress
	return balance, nil
}

func checkMPTClawbackDelta(tx Transaction, amount Amount, changed clawbackEntry) *InvariantViolation {
	holderProvider, ok := tx.(ClawbackHolderProvider)
	if !ok || holderProvider.ClawbackHolder() == "" {
		return clawbackViolation("MPT clawback missing holder")
	}
	holder, err := state.DecodeAccountID(holderProvider.ClawbackHolder())
	if err != nil {
		return clawbackViolation("MPT clawback changed the wrong token")
	}
	idBytes, err := hex.DecodeString(amount.MPTIssuanceID())
	if err != nil || len(idBytes) != 24 {
		return clawbackViolation("MPT clawback changed the wrong token")
	}
	var issuanceID [24]byte
	copy(issuanceID[:], idBytes)
	if changed.before == nil || changed.after == nil {
		return clawbackViolation("MPT clawback token is missing")
	}
	if changed.key != keylet.MPToken(keylet.MPTIssuance(issuanceID).Key, holder).Key {
		return clawbackViolation("MPT clawback changed the wrong token")
	}

	before, err := state.ParseMPToken(changed.before)
	if err != nil {
		return clawbackViolation(fmt.Sprintf("could not parse MPToken SLE: %v", err))
	}
	after, err := state.ParseMPToken(changed.after)
	if err != nil {
		return clawbackViolation(fmt.Sprintf("could not parse MPToken SLE: %v", err))
	}
	if before.Account != holder || after.Account != holder || before.MPTokenIssuanceID != issuanceID || after.MPTokenIssuanceID != issuanceID {
		return clawbackViolation("MPT clawback changed the wrong token")
	}
	clawAmount, ok := amount.MPTRaw()
	if !ok || clawAmount <= 0 {
		return clawbackViolation("MPT clawback amount is invalid")
	}
	if after.MPTAmount > before.MPTAmount {
		return clawbackViolation("MPT clawback balance change is invalid")
	}
	expected := min(before.MPTAmount, uint64(clawAmount))
	if before.MPTAmount-after.MPTAmount != expected {
		return clawbackViolation("MPT clawback balance change is invalid")
	}
	return nil
}

func clawbackViolation(message string) *InvariantViolation {
	return &InvariantViolation{Name: "ValidClawback", Message: message}
}

func checkClawbackHolderBalance(tx Transaction, view ReadView) *InvariantViolation {
	provider, ok := tx.(ClawbackAmountProvider)
	if !ok {
		return nil
	}
	amount := provider.ClawbackAmount()
	issuer, err := state.DecodeAccountID(tx.TxAccount())
	if err != nil {
		return nil
	}
	holder, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return nil
	}
	lineData, err := view.Read(keylet.Line(holder, issuer, amount.Currency))
	if err != nil || lineData == nil {
		return nil
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		return clawbackViolation(fmt.Sprintf("could not parse RippleState SLE: %v", err))
	}
	balance := line.Balance
	if state.CompareAccountIDs(holder, issuer) > 0 {
		balance = balance.Negate()
	}
	if balance.Signum() < 0 {
		return clawbackViolation("trustline or MPT balance is negative")
	}
	return nil
}
