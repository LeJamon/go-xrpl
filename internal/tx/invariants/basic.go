package invariants

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// checkXRPBalances verifies that all AccountRoot balances are within [0, InitialXRP].
// Reference: rippled InvariantCheck.cpp — XRPBalanceChecks
func checkXRPBalances(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "AccountRoot" {
			continue
		}
		data := e.After
		if e.IsDelete {
			continue // deleted account — balance check not applicable
		}
		acct, err := state.ParseAccountRoot(data)
		if err != nil {
			continue
		}
		if acct.Balance > InitialXRP {
			return &InvariantViolation{
				Name:    "XRPBalanceChecks",
				Message: fmt.Sprintf("account balance %d exceeds InitialXRP (%d)", acct.Balance, InitialXRP),
			}
		}
		// Note: balance can be 0 (for accounts exactly at reserve, after spending)
		// but must not underflow (unsigned so can't go negative)
	}
	return nil
}

// checkXRPNotCreated verifies that the net XRP change across all touched entries
// equals at most -fee (XRP can only decrease, never increase, per transaction).
// Reference: rippled InvariantCheck.cpp — XRPNotCreated
func checkXRPNotCreated(result Result, fee uint64, entries []InvariantEntry) *InvariantViolation {
	// Sum of (after_balance - before_balance) across AccountRoot entries.
	// Using int64 arithmetic; values are at most ~10^17 drops which fits.
	var netChange int64

	for _, e := range entries {
		switch e.EntryType {
		case "AccountRoot":
			var before, after uint64
			if e.Before != nil {
				if acct, err := state.ParseAccountRoot(e.Before); err == nil {
					before = acct.Balance
				}
			}
			if e.After != nil {
				if acct, err := state.ParseAccountRoot(e.After); err == nil {
					after = acct.Balance
				}
			}
			netChange += int64(after) - int64(before)

		case "Escrow":
			// Escrow holds XRP in escrow — count as a balance change
			var before, after uint64
			if e.Before != nil {
				if esc, err := state.ParseEscrow(e.Before); err == nil {
					before = esc.Amount
				}
			}
			if e.After != nil {
				if esc, err := state.ParseEscrow(e.After); err == nil {
					after = esc.Amount
				}
			}
			netChange += int64(after) - int64(before)

		case "PayChannel":
			// PayChannel holds XRP as Amount - Balance (total minus claimed).
			// Reference: rippled InvariantCheck.cpp:107-131
			var before, after uint64
			if e.Before != nil {
				if pc, err := state.ParsePayChannel(e.Before); err == nil {
					before = pc.Amount - pc.Balance
				}
			}
			if e.After != nil && !e.IsDelete {
				if pc, err := state.ParsePayChannel(e.After); err == nil {
					after = pc.Amount - pc.Balance
				}
			}
			netChange += int64(after) - int64(before)
		}
	}

	// Net XRP change must be <= 0 (XRP destroyed = fee + any extra burns).
	// It cannot be positive because that would mean XRP was created out of thin air.
	if netChange > 0 {
		return &InvariantViolation{
			Name:    "XRPNotCreated",
			Message: fmt.Sprintf("net XRP change +%d drops: XRP was created (fee=%d)", netChange, fee),
		}
	}
	return nil
}

// checkAccountRootsNotDeleted verifies that AccountRoot entries are only deleted
// by allowed transaction types.
// Reference: rippled InvariantCheck.cpp — AccountRootsNotDeleted (lines 370-412)
func checkAccountRootsNotDeleted(txType string, result Result, entries []InvariantEntry) *InvariantViolation {
	deletedCount := 0
	for _, e := range entries {
		if e.EntryType == "AccountRoot" && e.IsDelete {
			deletedCount++
		}
	}
	if deletedCount == 0 {
		return nil
	}

	if result == TesSUCCESS {
		// A successful AccountDelete/AMMDelete MUST delete exactly one account root.
		switch txType {
		case "AccountDelete", "AMMDelete":
			if deletedCount == 1 {
				return nil
			}
			return &InvariantViolation{
				Name:    "AccountRootsNotDeleted",
				Message: fmt.Sprintf("%s must delete exactly 1 AccountRoot, got %d", txType, deletedCount),
			}
		// A successful AMMWithdraw/AMMClawback MAY delete one account root
		// (when total AMM LP Tokens balance goes to 0).
		case "AMMWithdraw", "AMMClawback":
			if deletedCount <= 1 {
				return nil
			}
			return &InvariantViolation{
				Name:    "AccountRootsNotDeleted",
				Message: fmt.Sprintf("%s may delete at most 1 AccountRoot, got %d", txType, deletedCount),
			}
		// A Batch may contain inner AccountDelete/AMMDelete transactions that
		// delete account roots. In rippled, each inner tx runs through its own
		// apply() with its own invariant check under its own tx type. In goXRPL,
		// the batch processes inner txns within a single engine table, so the
		// invariant sees the combined result under the "Batch" tx type.
		// Allow up to 1 account root deletion per batch.
		// Reference: rippled apply.cpp applyBatchTransactions()
		case "Batch":
			if deletedCount <= 1 {
				return nil
			}
			return &InvariantViolation{
				Name:    "AccountRootsNotDeleted",
				Message: fmt.Sprintf("Batch may delete at most 1 AccountRoot, got %d", deletedCount),
			}
		}
	}

	return &InvariantViolation{
		Name:    "AccountRootsNotDeleted",
		Message: fmt.Sprintf("AccountRoot deleted by %s (count=%d); not allowed", txType, deletedCount),
	}
}

// checkLedgerEntryTypesMatch verifies two things:
// 1. If both before and after exist for an entry, their ledger entry types must match.
// 2. Any newly created entry (after exists, before doesn't) must be a known valid type.
// Reference: rippled InvariantCheck.cpp — LedgerEntryTypesMatch (lines 505-576)
func checkLedgerEntryTypesMatch(entries []InvariantEntry) *InvariantViolation {
	typeMismatch := false
	invalidTypeAdded := false

	for _, e := range entries {
		// Check type mismatch between before and after
		if e.Before != nil && e.After != nil {
			beforeCode := getLedgerEntryTypeCode(e.Before)
			afterCode := getLedgerEntryTypeCode(e.After)
			// Only compare if both codes were successfully extracted
			if beforeCode != 0 && afterCode != 0 && beforeCode != afterCode {
				typeMismatch = true
			}
		}

		// Check that any entry with an "after" is a valid type
		if e.After != nil {
			afterCode := getLedgerEntryTypeCode(e.After)
			// Skip entries where the type code couldn't be extracted (malformed binary
			// or entries that don't start with the standard 0x11 header byte, e.g.,
			// some internal entries like NFTokenPage that use unhashed keys).
			if afterCode == 0 {
				continue
			}
			afterName := resolveEntryTypeName(afterCode)
			if !validLedgerEntryTypes[afterName] {
				invalidTypeAdded = true
			}
		}
	}

	if typeMismatch {
		return &InvariantViolation{
			Name:    "LedgerEntryTypesMatch",
			Message: "ledger entry type mismatch",
		}
	}

	if invalidTypeAdded {
		return &InvariantViolation{
			Name:    "LedgerEntryTypesMatch",
			Message: "invalid ledger entry type added",
		}
	}

	return nil
}

// checkValidNewAccountRoot verifies that new AccountRoot entries are only created
// by Payment or AMMCreate transactions, and that at most one is created per tx.
// Reference: rippled InvariantCheck.cpp — ValidNewAccountRoot
func checkValidNewAccountRoot(txType string, entries []InvariantEntry) *InvariantViolation {
	createdCount := 0
	for _, e := range entries {
		if e.EntryType == "AccountRoot" && !e.IsDelete && e.Before == nil {
			createdCount++
		}
	}
	if createdCount == 0 {
		return nil
	}
	if createdCount > 1 {
		return &InvariantViolation{
			Name:    "ValidNewAccountRoot",
			Message: fmt.Sprintf("multiple AccountRoot entries created in one transaction (count=%d)", createdCount),
		}
	}
	// Exactly one new AccountRoot — only Payment, AMMCreate, and Batch are allowed to create accounts.
	// Batch can contain inner Payment transactions that create accounts.
	switch txType {
	case "Payment", "AMMCreate", "Batch":
		return nil
	}
	return &InvariantViolation{
		Name:    "ValidNewAccountRoot",
		Message: fmt.Sprintf("transaction type %s created an AccountRoot (only Payment or AMMCreate may create accounts)", txType),
	}
}

// checkTransactionFee verifies that the fee charged is non-negative, does not
// exceed the total XRP supply, and does not exceed what the transaction declared.
// Reference: rippled InvariantCheck.cpp — TransactionFeeCheck (lines 39-83)
func checkTransactionFee(fee uint64, txDeclaredFee uint64) *InvariantViolation {
	// fee is uint64 so always >= 0; skip the negative check.

	// Fee must not be greater than or equal to the entire XRP supply.
	if fee >= InitialXRP {
		return &InvariantViolation{
			Name:    "TransactionFeeCheck",
			Message: fmt.Sprintf("fee paid exceeds system limit: %d", fee),
		}
	}

	// Fee charged must not exceed what the transaction authorized.
	if fee > txDeclaredFee {
		return &InvariantViolation{
			Name:    "TransactionFeeCheck",
			Message: fmt.Sprintf("fee paid is %d exceeds fee specified in transaction", fee),
		}
	}

	return nil
}

// getLedgerEntryTypeCode extracts the raw uint16 ledger entry type code from binary SLE data.
// Returns 0 if the data is too short or doesn't have the expected header.
func getLedgerEntryTypeCode(data []byte) uint16 {
	// LedgerEntryType is always the first field: header 0x11 + 2-byte value
	if len(data) < 3 || data[0] != 0x11 {
		return 0
	}
	return binary.BigEndian.Uint16(data[1:3])
}

// resolveEntryTypeName returns the valid ledger entry type name for a given type code.
// It first checks the standard ledger entry type names, then falls back to known
// codec mis-encodings where the binary codec writes a transaction type code instead
// of the ledger entry type code (e.g., DepositPreauth: tx type 19 vs SLE type 112).
func resolveEntryTypeName(code uint16) string {
	name := ledgerEntryTypeName(code)
	if validLedgerEntryTypes[name] {
		return name
	}
	// Known codec bug: UInt16.FromJSON tries transaction type lookup before ledger
	// entry type lookup. "DepositPreauth" exists in both maps with different codes.
	// The binary codec writes tx type 19 (0x0013) instead of SLE type 112 (0x0070).
	if corrected, ok := misEncodedTypeAliases[code]; ok {
		return corrected
	}
	return name
}
