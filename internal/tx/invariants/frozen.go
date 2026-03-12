package invariants

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// TransfersNotFrozen
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — TransfersNotFrozen (lines 652-926)
//
// Tracks balance changes on trust lines, groups them by issuer, and validates
// that frozen trust lines don't improperly transfer funds.
//
// visitEntry phase:
//   - Track AccountRoot entries — store their account ID and data for
//     possible issuer lookup (global freeze flag).
//   - For RippleState entries (before and after): compute the balance change.
//     Determine the issuer (low vs high account), and record the balance
//     change as a sender or receiver relative to each side's potential issuer.
//
// finalize phase:
//   - For each issuer that has BOTH senders AND receivers (funds flowing
//     through that issuer): check global freeze, individual freeze, deep
//     freeze. If frozen, the transfer is blocked.
//   - AMMClawback transactions get special exemptions.
//   - Only enforced when featureDeepFreeze is enabled.

// frozenBalanceChange records a single trust line's balance change direction
// along with the trust line data itself (for freeze flag checks).
type frozenBalanceChange struct {
	// lineData is the binary SLE data of the trust line (the "after" state,
	// or the "before" state for deletions — matching rippled where after is
	// never null).
	lineData *state.RippleState
	// balanceChangeSign is -1 for senders, +1 for receivers (from the
	// perspective of the issuer being examined).
	balanceChangeSign int
}

// frozenIssuerChanges groups balance changes by senders (balance decrease)
// and receivers (balance increase) for a single issuer.
type frozenIssuerChanges struct {
	senders   []frozenBalanceChange
	receivers []frozenBalanceChange
}

// frozenIssueKey is the map key for grouping changes by {currency, issuer}.
// This matches rippled's Issue = {Currency, AccountID}.
type frozenIssueKey struct {
	currency string
	issuer   string // base58 address of the potential issuer
}

func checkTransfersNotFrozen(tx Transaction, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	// Phase 1: visitEntry — collect AccountRoot possible issuers and
	// RippleState balance changes.

	// possibleIssuers maps account address → parsed AccountRoot data.
	// Used to look up global freeze flag without hitting the view.
	possibleIssuers := make(map[string]*state.AccountRoot)

	// balanceChanges groups balance changes by {currency, issuer}.
	balanceChanges := make(map[frozenIssueKey]*frozenIssuerChanges)

	for _, e := range entries {
		// Determine the "after" data. In rippled, after is never null
		// (even for deletions, the erased SLE is passed as after).
		// In Go's CollectEntries, deleted entries have After=nil but
		// Before holds the data. Use Before as "after" for deletions.
		afterData := e.After
		if afterData == nil {
			afterData = e.Before
		}
		if afterData == nil {
			continue
		}

		// --- isValidEntry ---

		// Check type from afterData.
		afterType := getLedgerEntryType(afterData)

		if afterType == "AccountRoot" {
			// Store as possible issuer for finalize phase.
			acct, err := state.ParseAccountRoot(afterData)
			if err == nil && acct.Account != "" {
				possibleIssuers[acct.Account] = acct
			}
			continue
		}

		if afterType != "RippleState" {
			continue
		}

		// If before exists, verify it's also a RippleState.
		// Reference: rippled line 761-762
		if e.Before != nil {
			beforeType := getLedgerEntryType(e.Before)
			if beforeType != "RippleState" {
				continue
			}
		}

		// --- calculateBalanceChange ---
		// Parse the trust line from the "after" data (never nil in rippled;
		// in Go, for deletions we use Before).
		afterRS, err := state.ParseRippleState(afterData)
		if err != nil {
			continue
		}

		// Create a zero amount with same currency/issuer as the balance.
		zeroBalance := state.NewIssuedAmountFromValue(0, -100,
			afterRS.Balance.Currency, afterRS.Balance.Issuer)

		// Compute balance change = balanceAfter - balanceBefore.
		// For new trust lines (before==nil), balanceBefore = zeroed balance.
		// For deleted trust lines (isDelete), balanceAfter = zeroed balance.
		// Reference: rippled calculateBalanceChange (lines 765-792)
		var balanceBefore, balanceAfter state.Amount

		if e.Before != nil {
			beforeRS, err := state.ParseRippleState(e.Before)
			if err == nil {
				balanceBefore = beforeRS.Balance
			} else {
				balanceBefore = zeroBalance
			}
		} else {
			// New trust line — starting balance is zero.
			balanceBefore = zeroBalance
		}

		if e.IsDelete {
			// Deleted trust line — final balance treated as zero.
			// Reference: rippled lines 786-789
			balanceAfter = zeroBalance
		} else {
			if e.After != nil {
				// Use the actual after data (not the "before" fallback).
				actualAfterRS, err := state.ParseRippleState(e.After)
				if err == nil {
					balanceAfter = actualAfterRS.Balance
				} else {
					balanceAfter = zeroBalance
				}
			} else {
				balanceAfter = afterRS.Balance
			}
		}

		balanceChange, err := balanceAfter.Sub(balanceBefore)
		if err != nil {
			continue
		}
		if balanceChange.Signum() == 0 {
			continue
		}

		// --- recordBalanceChanges ---
		balanceChangeSign := balanceChange.Signum()
		currency := afterRS.Balance.Currency
		if currency == "" {
			// Try to get currency from limits.
			if afterRS.LowLimit.Currency != "" {
				currency = afterRS.LowLimit.Currency
			} else if afterRS.HighLimit.Currency != "" {
				currency = afterRS.HighLimit.Currency
			}
		}

		// Skip trust lines where LowLimit.Issuer == HighLimit.Issuer.
		// In rippled, the low and high accounts are always different.
		// In the Go codebase, a serialization quirk in TrustSet may store
		// the same account on both sides (e.g., when the issuer sets a limit
		// for a holder, the LimitAmount.Issuer = holder, not the issuer).
		// We can't determine transfer direction for such entries, so skip.
		if afterRS.LowLimit.Issuer == afterRS.HighLimit.Issuer {
			continue
		}

		// From low account's perspective: Issue = {currency, HighLimit.Issuer},
		// sign = balanceChangeSign.
		// From high account's perspective: Issue = {currency, LowLimit.Issuer},
		// sign = -balanceChangeSign.
		// Reference: rippled lines 816-824
		lowIssueKey := frozenIssueKey{currency: currency, issuer: afterRS.HighLimit.Issuer}
		highIssueKey := frozenIssueKey{currency: currency, issuer: afterRS.LowLimit.Issuer}

		recordFrozenBalance(balanceChanges, lowIssueKey, frozenBalanceChange{
			lineData:          afterRS,
			balanceChangeSign: balanceChangeSign,
		})
		recordFrozenBalance(balanceChanges, highIssueKey, frozenBalanceChange{
			lineData:          afterRS,
			balanceChangeSign: -balanceChangeSign,
		})
	}

	// Phase 2: finalize — validate each issuer's changes.

	// Determine enforcement.
	// Reference: rippled lines 706-707 — enforce = featureDeepFreeze enabled.
	enforce := rules != nil && rules.DeepFreezeEnabled()

	for issueKey, changes := range balanceChanges {
		// Find the issuer's AccountRoot.
		issuerAcct := findFrozenIssuer(issueKey.issuer, possibleIssuers, view)
		if issuerAcct == nil {
			// Issuer not found — invariant violation if enforcing.
			// Reference: rippled lines 714-725
			if enforce {
				return &InvariantViolation{
					Name:    "TransfersNotFrozen",
					Message: "issuer account not found",
				}
			}
			continue
		}

		// Validate this issuer's changes.
		if v := validateFrozenIssuerChanges(issuerAcct, changes, tx, enforce); v != nil {
			return v
		}
	}

	return nil
}

// recordFrozenBalance adds a balance change to the appropriate sender or
// receiver list for the given issue key.
// Reference: rippled TransfersNotFrozen::recordBalance (lines 794-806)
func recordFrozenBalance(
	balanceChanges map[frozenIssueKey]*frozenIssuerChanges,
	key frozenIssueKey,
	change frozenBalanceChange,
) {
	changes, ok := balanceChanges[key]
	if !ok {
		changes = &frozenIssuerChanges{}
		balanceChanges[key] = changes
	}
	if change.balanceChangeSign < 0 {
		changes.senders = append(changes.senders, change)
	} else {
		changes.receivers = append(changes.receivers, change)
	}
}

// findFrozenIssuer looks up the issuer's AccountRoot, first from the
// possibleIssuers cache (entries modified by the transaction), then from
// the view.
// Reference: rippled TransfersNotFrozen::findIssuer (lines 827-836)
func findFrozenIssuer(
	issuerAddr string,
	possibleIssuers map[string]*state.AccountRoot,
	view ReadView,
) *state.AccountRoot {
	if acct, ok := possibleIssuers[issuerAddr]; ok {
		return acct
	}
	if view == nil {
		return nil
	}
	issuerID, err := state.DecodeAccountID(issuerAddr)
	if err != nil {
		return nil
	}
	data, err := view.Read(keylet.Account(issuerID))
	if err != nil || data == nil {
		return nil
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil
	}
	return acct
}

// validateFrozenIssuerChanges checks whether any frozen trust line has
// improper transfers for a single issuer.
// Reference: rippled TransfersNotFrozen::validateIssuerChanges (lines 838-879)
func validateFrozenIssuerChanges(
	issuer *state.AccountRoot,
	changes *frozenIssuerChanges,
	tx Transaction,
	enforce bool,
) *InvariantViolation {
	// If there are no receivers or no senders, the transfer is between
	// holder(s) and the issuer directly. This is always allowed regardless
	// of freeze flags.
	// Reference: rippled lines 852-862
	if len(changes.receivers) == 0 || len(changes.senders) == 0 {
		return nil
	}

	globalFreeze := (issuer.Flags & state.LsfGlobalFreeze) != 0

	// Check both senders and receivers.
	// Reference: rippled lines 864-877
	allActors := make([]frozenBalanceChange, 0, len(changes.senders)+len(changes.receivers))
	allActors = append(allActors, changes.senders...)
	allActors = append(allActors, changes.receivers...)

	issuerAddr := issuer.Account

	for _, change := range allActors {
		// Determine if the issuer is the low account on this trust line.
		// high=true means the issuer is the low account (so the non-issuer
		// counterparty is the high account).
		// Reference: rippled line 868 — high = (line->sfLowLimit.getIssuer() == issuer->sfAccount)
		high := change.lineData.LowLimit.Issuer == issuerAddr

		if v := validateFrozenState(change, high, tx, enforce, globalFreeze); v != nil {
			return v
		}
	}

	return nil
}

// validateFrozenState checks a single trust line balance change against
// freeze flags.
// Reference: rippled TransfersNotFrozen::validateFrozenState (lines 881-926)
func validateFrozenState(
	change frozenBalanceChange,
	high bool,
	tx Transaction,
	enforce bool,
	globalFreeze bool,
) *InvariantViolation {
	// "freeze" only applies to senders (balance decrease). Checks the freeze
	// flag on the issuer's side of the trust line:
	//   high=true (issuer is low account) → check lsfLowFreeze
	//   high=false (issuer is high account) → check lsfHighFreeze
	//
	// "deepFreeze" is always checked (not gated on sender direction):
	//   high=true → lsfLowDeepFreeze; high=false → lsfHighDeepFreeze
	//
	// Reference: rippled lines 890-894
	var freeze bool
	if change.balanceChangeSign < 0 {
		if high {
			freeze = (change.lineData.Flags & state.LsfLowFreeze) != 0
		} else {
			freeze = (change.lineData.Flags & state.LsfHighFreeze) != 0
		}
	}

	var deepFreeze bool
	if high {
		deepFreeze = (change.lineData.Flags & state.LsfLowDeepFreeze) != 0
	} else {
		deepFreeze = (change.lineData.Flags & state.LsfHighDeepFreeze) != 0
	}

	frozen := globalFreeze || deepFreeze || freeze

	isAMMLine := (change.lineData.Flags & state.LsfAMMNode) != 0

	if !frozen {
		return nil
	}

	// AMMClawback exception: if the trust line is NOT an AMM line, or if
	// there's a global freeze, and the transaction is AMMClawback, allow it.
	// Reference: rippled lines 904-911
	if (!isAMMLine || globalFreeze) && tx.TxType() == TypeAMMClawback {
		return nil
	}

	// Frozen transfer detected.
	// Reference: rippled lines 913-925
	if enforce {
		return &InvariantViolation{
			Name:    "TransfersNotFrozen",
			Message: "frozen funds transfer detected",
		}
	}

	return nil
}
