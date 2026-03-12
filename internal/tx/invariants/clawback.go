package invariants

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// ValidClawback
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidClawback (lines 1288-1362)
//
// For ttCLAWBACK only:
//   - visitEntry: count modified RippleState entries (before exists) and
//     modified MPToken entries (before exists).
//   - finalize (success): at most 1 trust line changed; at most 1 MPToken
//     changed. If 1 trust line changed, holder balance must be non-negative.
//   - finalize (failure): no trust lines or MPTokens should have changed.

func checkValidClawback(tx Transaction, result Result, entries []InvariantEntry, view ReadView) *InvariantViolation {
	if tx.TxType() != TypeClawback {
		return nil
	}

	// visitEntry phase: count entries where before exists and type matches.
	// In rippled, visitEntry checks (before && before->getType() == ltXXX).
	// Entries with before present include both modified and deleted entries.
	// Created entries (before == nil) are NOT counted.
	var trustlinesChanged, mptokensChanged int
	for _, e := range entries {
		if e.Before == nil {
			continue
		}
		if e.EntryType == "RippleState" {
			trustlinesChanged++
		}
		if e.EntryType == "MPToken" {
			mptokensChanged++
		}
	}

	// finalize phase
	if result == TesSUCCESS {
		if trustlinesChanged > 1 {
			return &InvariantViolation{
				Name:    "ValidClawback",
				Message: "more than one trustline changed",
			}
		}
		if mptokensChanged > 1 {
			return &InvariantViolation{
				Name:    "ValidClawback",
				Message: "more than one mptoken changed",
			}
		}

		// If exactly 1 trust line changed, verify holder balance is non-negative.
		if trustlinesChanged == 1 {
			if v := checkClawbackHolderBalance(tx, view); v != nil {
				return v
			}
		}
	} else {
		// On failure, no trust lines or MPTokens should have changed.
		if trustlinesChanged != 0 {
			return &InvariantViolation{
				Name:    "ValidClawback",
				Message: "some trustlines were changed despite failure of the transaction",
			}
		}
		if mptokensChanged != 0 {
			return &InvariantViolation{
				Name:    "ValidClawback",
				Message: "some mptokens were changed despite failure of the transaction",
			}
		}
	}

	return nil
}

// checkClawbackHolderBalance reads the trust line from the view and verifies
// that the holder's balance is non-negative after clawback.
// Reference: rippled InvariantCheck.cpp lines 1328-1342 — uses accountHolds().
func checkClawbackHolderBalance(tx Transaction, view ReadView) *InvariantViolation {
	if view == nil {
		return nil // no view available — skip balance check
	}

	// Get the Amount from the transaction to determine holder and currency.
	cap, ok := tx.(ClawbackAmountProvider)
	if !ok {
		return nil // unable to inspect Amount — skip
	}
	amt := cap.ClawbackAmount()

	// issuer = tx.Account (the clawback submitter)
	issuerAddr := tx.TxAccount()
	issuerID, err := state.DecodeAccountID(issuerAddr)
	if err != nil {
		return nil
	}

	// holder = Amount.Issuer (for IOU clawback, the Issuer field is the holder)
	holderAddr := amt.Issuer
	holderID, err := state.DecodeAccountID(holderAddr)
	if err != nil {
		return nil
	}
	currency := amt.Currency

	// Read the trust line between holder and issuer
	lineKey := keylet.Line(holderID, issuerID, currency)
	lineData, err := view.Read(lineKey)
	if err != nil || lineData == nil {
		// Trust line doesn't exist — balance is effectively zero, which is non-negative.
		return nil
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return nil
	}

	// accountHolds logic: get balance in holder's terms.
	// Trust line balance: positive = low account owes high.
	// If holder > issuer, negate to put balance in holder terms.
	balance := rs.Balance
	if state.CompareAccountIDs(holderID, issuerID) > 0 {
		balance = balance.Negate()
	}

	if balance.Signum() < 0 {
		return &InvariantViolation{
			Name:    "ValidClawback",
			Message: "trustline balance is negative",
		}
	}

	return nil
}
