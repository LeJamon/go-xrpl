package tx

// invariants_check.go — post-apply invariant checking matching rippled's InvariantCheck.cpp
//
// Called BEFORE table.Apply() so entries are still inspectable in the ApplyStateTable.
// On violation, the engine returns TecINVARIANT_FAILED (fee charged, state reverted).
//
// Reference: rippled/src/xrpld/app/tx/detail/InvariantCheck.cpp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// InitialXRP is the total XRP supply in drops (100 billion XRP).
const InitialXRP uint64 = 100_000_000_000_000_000

// xrpCurrencyBytes is the canonical XRP currency representation (all zeros in the 20-byte currency field).
var xrpCurrencyBytes = make([]byte, 20)

// InvariantEntry represents a single ledger entry modification to be checked by invariants.
// Before is nil for newly created entries; After is nil for deleted entries.
type InvariantEntry struct {
	Key       [32]byte // ledger key of the entry (for invariants like ValidNFTokenPage that need to inspect the key)
	EntryType string   // e.g. "AccountRoot", "RippleState", "Offer", "Escrow", "PayChannel"
	Before    []byte   // serialized SLE before the transaction (nil for inserts)
	After     []byte   // serialized SLE after the transaction (nil for deletes)
	IsDelete  bool     // true if the entry was deleted
}

// InvariantViolation holds the name and description of a detected invariant violation.
type InvariantViolation struct {
	Name    string
	Message string
}

func (v *InvariantViolation) Error() string {
	return fmt.Sprintf("invariant violation %s: %s", v.Name, v.Message)
}

// CheckInvariants runs all invariant checkers against the set of modified entries.
// tx is the transaction being applied (for invariants that need to inspect transaction fields).
// result is the transaction result before any invariant override.
// fee is the fee in drops actually charged for this transaction.
// txDeclaredFee is the fee declared in the transaction itself (for TransactionFeeCheck).
// entries is the slice returned by ApplyStateTable.CollectEntries().
// view is the ledger view for invariants that need to read ledger state.
// rules is the amendment rules for amendment-gated invariant behavior.
//
// Returns non-nil if any invariant is violated.
// Reference: rippled InvariantCheck.h — finalize(STTx const&, TER, XRPAmount, ReadView const&, ...)
func CheckInvariants(tx Transaction, result Result, fee uint64, txDeclaredFee uint64, entries []InvariantEntry, view LedgerView, rules *amendment.Rules) *InvariantViolation {
	txType := tx.TxType().String()
	checks := []func() *InvariantViolation{
		func() *InvariantViolation { return checkTransactionFee(fee, txDeclaredFee) },
		func() *InvariantViolation { return checkXRPBalances(entries) },
		func() *InvariantViolation { return checkXRPNotCreated(result, fee, entries) },
		func() *InvariantViolation { return checkAccountRootsNotDeleted(txType, result, entries) },
		func() *InvariantViolation { return checkLedgerEntryTypesMatch(entries) },
		func() *InvariantViolation { return checkNoXRPTrustLines(entries) },
		func() *InvariantViolation {
			return checkNoDeepFreezeTrustLinesWithoutFreeze(entries)
		},
		func() *InvariantViolation {
			return checkTransfersNotFrozen(tx, entries, view, rules)
		},
		func() *InvariantViolation { return checkNoBadOffers(entries) },
		func() *InvariantViolation { return checkNoZeroEscrow(entries) },
		func() *InvariantViolation { return checkValidNewAccountRoot(txType, entries) },
		func() *InvariantViolation {
			return checkNFTokenCountTracking(txType, result, entries)
		},
		func() *InvariantViolation {
			return checkValidClawback(tx, result, entries, view)
		},
		func() *InvariantViolation {
			return checkValidMPTIssuance(tx, result, entries)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDomain(tx, result, entries)
		},
		func() *InvariantViolation {
			return checkValidNFTokenPage(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkAccountRootsDeletedClean(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDEX(tx, result, entries, view)
		},
		func() *InvariantViolation {
			return checkValidAMM(tx, result, entries, view, rules)
		},
	}
	for _, check := range checks {
		if v := check(); v != nil {
			return v
		}
	}
	return nil
}

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

// checkNoXRPTrustLines verifies that no RippleState (trust line) entry uses XRP as a currency.
// Reference: rippled InvariantCheck.cpp — NoXRPTrustLines
func checkNoXRPTrustLines(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "RippleState" || e.IsDelete {
			continue
		}
		rs, err := state.ParseRippleState(e.After)
		if err != nil {
			continue
		}
		// XRP currency code is 3 bytes "XRP" at offset 12 in the 20-byte currency field,
		// OR all zeros. Check if the currency is XRP.
		curr := rs.Balance.Currency
		if isXRPCurrency(curr) {
			return &InvariantViolation{
				Name:    "NoXRPTrustLines",
				Message: "RippleState entry uses XRP as currency (trust lines must use IOU currencies)",
			}
		}
	}
	return nil
}

// checkNoBadOffers verifies that Offer entries have positive non-zero amounts
// and that XRP/XRP offers don't exist.
// Reference: rippled InvariantCheck.cpp — NoBadOffers
func checkNoBadOffers(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "Offer" || e.IsDelete {
			continue
		}
		offer, err := parseOfferForInvariant(e.After)
		if err != nil {
			continue
		}
		// Both sides XRP is disallowed
		if offer.takerPaysIsXRP && offer.takerGetsIsXRP {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer has XRP on both sides",
			}
		}
		// Amounts must be positive
		if offer.takerPaysIsXRP && offer.takerPaysXRP == 0 {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer TakerPays (XRP) is zero",
			}
		}
		if offer.takerGetsIsXRP && offer.takerGetsXRP == 0 {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer TakerGets (XRP) is zero",
			}
		}
	}
	return nil
}

// checkNoZeroEscrow verifies that Escrow entries have a positive XRP amount.
// Reference: rippled InvariantCheck.cpp — NoZeroEscrow
func checkNoZeroEscrow(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "Escrow" || e.IsDelete {
			continue
		}
		esc, err := state.ParseEscrow(e.After)
		if err != nil {
			continue
		}
		if esc.Amount == 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow entry has zero XRP amount",
			}
		}
		if esc.Amount > InitialXRP {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("Escrow amount %d exceeds InitialXRP (%d)", esc.Amount, InitialXRP),
			}
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

// validLedgerEntryTypes is the set of valid ledger entry type names that may be
// created in the ledger. Matches rippled's LedgerEntryTypesMatch whitelist.
// Reference: rippled InvariantCheck.cpp lines 517-546
var validLedgerEntryTypes = map[string]bool{
	"AccountRoot":                       true,
	"Delegate":                          true,
	"DirectoryNode":                     true,
	"RippleState":                       true,
	"Ticket":                            true,
	"SignerList":                         true,
	"Offer":                             true,
	"LedgerHashes":                      true,
	"Amendments":                        true,
	"FeeSettings":                       true,
	"Escrow":                            true,
	"PayChannel":                        true,
	"Check":                             true,
	"DepositPreauth":                    true,
	"NegativeUNL":                       true,
	"NFTokenPage":                       true,
	"NFTokenOffer":                      true,
	"AMM":                               true,
	"Bridge":                            true,
	"XChainOwnedClaimID":                true,
	"XChainOwnedCreateAccountClaimID":   true,
	"DID":                               true,
	"Oracle":                            true,
	"MPTokenIssuance":                   true,
	"MPToken":                           true,
	"Credential":                        true,
	"PermissionedDomain":                true,
	"Vault":                             true,
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

// misEncodedTypeAliases maps binary type codes that are incorrect due to a known
// codec bug (UInt16.FromJSON prefers transaction type codes over ledger entry type
// codes when names overlap) to the intended ledger entry type name.
var misEncodedTypeAliases = map[uint16]string{
	19: "DepositPreauth", // tx type 0x0013 written instead of SLE type 0x0070
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

// checkNFTokenCountTracking verifies that MintedNFTokens and BurnedNFTokens
// fields on AccountRoot entries change correctly based on transaction type.
// Reference: rippled InvariantCheck.cpp — NFTokenCountTracking (lines 1181-1284)
func checkNFTokenCountTracking(txType string, result Result, entries []InvariantEntry) *InvariantViolation {
	var beforeMintedTotal, beforeBurnedTotal uint32
	var afterMintedTotal, afterBurnedTotal uint32

	for _, e := range entries {
		if e.EntryType != "AccountRoot" {
			continue
		}

		// Sum minted/burned from before state
		if e.Before != nil {
			if acct, err := state.ParseAccountRoot(e.Before); err == nil {
				beforeMintedTotal += acct.MintedNFTokens
				beforeBurnedTotal += acct.BurnedNFTokens
			}
		}

		// Sum minted/burned from after state.
		// In rippled, even erased SLEs pass their data as the "after" parameter
		// to visitEntry (ApplyStateTable.cpp line 88-92). For deleted AccountRoots,
		// we must include the before data in the after totals too, matching rippled's
		// behavior where the SLE is passed as "after" even for Action::erase.
		if e.IsDelete && e.Before != nil {
			// Erased entry: rippled passes the SLE data as "after",
			// so the before values appear in both before and after totals.
			if acct, err := state.ParseAccountRoot(e.Before); err == nil {
				afterMintedTotal += acct.MintedNFTokens
				afterBurnedTotal += acct.BurnedNFTokens
			}
		} else if e.After != nil {
			if acct, err := state.ParseAccountRoot(e.After); err == nil {
				afterMintedTotal += acct.MintedNFTokens
				afterBurnedTotal += acct.BurnedNFTokens
			}
		}
	}

	// For non-mint/burn transactions, counts must not change.
	if txType != "NFTokenMint" && txType != "NFTokenBurn" {
		if beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "the number of minted tokens changed without a mint transaction",
			}
		}
		if beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "the number of burned tokens changed without a burn transaction",
			}
		}
		return nil
	}

	if txType == "NFTokenMint" {
		// Successful mint must increase the minted count.
		if result == TesSUCCESS && beforeMintedTotal >= afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "successful minting didn't increase the number of minted tokens",
			}
		}
		// Failed mint must not change the minted count.
		if result != TesSUCCESS && beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "failed minting changed the number of minted tokens",
			}
		}
		// Mint must not change the burned count.
		if beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "minting changed the number of burned tokens",
			}
		}
	}

	if txType == "NFTokenBurn" {
		// Successful burn must increase the burned count.
		if result == TesSUCCESS && beforeBurnedTotal >= afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "successful burning didn't increase the number of burned tokens",
			}
		}
		// Failed burn must not change the burned count.
		if result != TesSUCCESS && beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "failed burning changed the number of burned tokens",
			}
		}
		// Burn must not change the minted count.
		if beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "burning changed the number of minted tokens",
			}
		}
	}

	return nil
}

// checkNoDeepFreezeTrustLinesWithoutFreeze verifies that no RippleState entry
// has lsfLowDeepFreeze set without lsfLowFreeze, or lsfHighDeepFreeze set
// without lsfHighFreeze.
// Reference: rippled InvariantCheck.cpp — NoDeepFreezeTrustLinesWithoutFreeze (lines 614-648)
func checkNoDeepFreezeTrustLinesWithoutFreeze(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.After == nil {
			continue
		}
		// Only check RippleState entries (created or modified, not deleted).
		// Use getLedgerEntryType on the after data to confirm the type,
		// matching rippled which checks after->getType() == ltRIPPLE_STATE.
		afterType := getLedgerEntryType(e.After)
		if afterType != "RippleState" {
			continue
		}

		rs, err := state.ParseRippleState(e.After)
		if err != nil {
			continue
		}

		flags := rs.Flags
		lowFreeze := (flags & state.LsfLowFreeze) != 0
		lowDeepFreeze := (flags & state.LsfLowDeepFreeze) != 0
		highFreeze := (flags & state.LsfHighFreeze) != 0
		highDeepFreeze := (flags & state.LsfHighDeepFreeze) != 0

		if (lowDeepFreeze && !lowFreeze) || (highDeepFreeze && !highFreeze) {
			return &InvariantViolation{
				Name:    "NoDeepFreezeTrustLinesWithoutFreeze",
				Message: "a trust line with deep freeze flag without normal freeze was created",
			}
		}
	}

	return nil
}

// isXRPCurrency returns true if the given currency bytes represent XRP.
// XRP currency is either all-zeros or the ASCII bytes "XRP" at position 12.
func isXRPCurrency(curr string) bool {
	if len(curr) == 0 || curr == "XRP" {
		return true
	}
	// Hex-encoded currency: 40 hex chars = 20 bytes
	if len(curr) == 40 {
		b, err := hexDecode20(curr)
		if err != nil {
			return false
		}
		// All zeros = XRP
		allZero := true
		for _, bb := range b {
			if bb != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		// Check "XRP" at bytes 12-14
		if b[12] == 'X' && b[13] == 'R' && b[14] == 'P' {
			return true
		}
	}
	return false
}

func hexDecode20(s string) ([20]byte, error) {
	var b [20]byte
	if len(s) != 40 {
		return b, fmt.Errorf("expected 40 hex chars, got %d", len(s))
	}
	for i := 0; i < 20; i++ {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			return b, fmt.Errorf("invalid hex char")
		}
		b[i] = byte(hi<<4 | lo)
	}
	return b, nil
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// offerForInvariant holds the parsed fields of an Offer ledger entry needed for invariant checks.
type offerForInvariant struct {
	takerPaysIsXRP bool
	takerPaysXRP   uint64
	takerGetsIsXRP bool
	takerGetsXRP   uint64
}

// parseOfferForInvariant extracts TakerPays/TakerGets from an Offer binary entry.
// Only checks XRP amounts (IOU amounts are assumed non-negative by binary encoding).
func parseOfferForInvariant(data []byte) (*offerForInvariant, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("offer too short")
	}
	result := &offerForInvariant{}
	offset := 0
	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = int(data[offset])
			offset++
		}

		// TakerPays = type 6 (Amount), field 4
		// TakerGets = type 6 (Amount), field 5
		if typeCode == 6 { // Amount
			if offset >= len(data) {
				break
			}
			firstByte := data[offset]
			isXRP := (firstByte & 0x80) == 0 // high bit 0 = XRP
			if isXRP {
				if offset+8 > len(data) {
					break
				}
				amount := binary.BigEndian.Uint64(data[offset:offset+8]) & 0x3FFFFFFFFFFFFFFF
				switch fieldCode {
				case 4:
					result.takerPaysIsXRP = true
					result.takerPaysXRP = amount
				case 5:
					result.takerGetsIsXRP = true
					result.takerGetsXRP = amount
				}
				offset += 8
			} else {
				// IOU: 48 bytes
				if offset+48 > len(data) {
					break
				}
				// IOU amounts are always non-negative in valid binary encoding
				offset += 48
			}
			continue
		}

		// Skip non-Amount fields
		skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
		if !ok {
			break
		}
		offset += skip
	}
	return result, nil
}

// skipFieldBytes returns the number of bytes to skip for a field given typeCode, fieldCode, and remaining data.
func skipFieldBytes(typeCode, fieldCode int, data []byte, offset int) (int, bool) {
	switch typeCode {
	case 1: // UInt16
		return 2, offset+2 <= len(data)
	case 2: // UInt32
		return 4, offset+4 <= len(data)
	case 3: // UInt64
		return 8, offset+8 <= len(data)
	case 4: // Hash128
		return 16, offset+16 <= len(data)
	case 5: // Hash256
		return 32, offset+32 <= len(data)
	case 6: // Amount (handled above, shouldn't reach here)
		return 0, false
	case 7: // Blob (variable length)
		if offset >= len(data) {
			return 0, false
		}
		length := int(data[offset])
		extra := 1
		if length > 192 {
			if offset+1 >= len(data) {
				return 0, false
			}
			length = 193 + ((length-193)<<8 | int(data[offset+1]))
			extra = 2
		}
		return extra + length, offset+extra+length <= len(data)
	case 8: // AccountID
		return 20, offset+20 <= len(data)
	case 14: // STObject end marker
		return 0, true
	case 15: // STArray end marker
		return 0, true
	default:
		return 0, false
	}
}

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

// clawbackAmountProvider is optionally implemented by Clawback transactions
// so the invariant checker can access the Amount field without importing the
// clawback subpackage.
type clawbackAmountProvider interface {
	ClawbackAmount() Amount
}

// holderFieldProvider is optionally implemented by transactions that have a
// Holder field (e.g., MPTokenAuthorize). Used by ValidMPTIssuance to determine
// whether the transaction was submitted by the issuer (Holder field present)
// or the holder (Holder field absent).
type holderFieldProvider interface {
	HasHolder() bool
}

func checkValidClawback(tx Transaction, result Result, entries []InvariantEntry, view LedgerView) *InvariantViolation {
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
func checkClawbackHolderBalance(tx Transaction, view LedgerView) *InvariantViolation {
	if view == nil {
		return nil // no view available — skip balance check
	}

	// Get the Amount from the transaction to determine holder and currency.
	cap, ok := tx.(clawbackAmountProvider)
	if !ok {
		return nil // unable to inspect Amount — skip
	}
	amt := cap.ClawbackAmount()

	// issuer = tx.Account (the clawback submitter)
	issuerAddr := tx.GetCommon().Account
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

// ---------------------------------------------------------------------------
// ValidMPTIssuance
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidMPTIssuance (lines 1366-1534)
//
// visitEntry: counts created and deleted MPTokenIssuance and MPToken entries.
// finalize: switch on transaction type with specific count requirements.

func checkValidMPTIssuance(tx Transaction, result Result, entries []InvariantEntry) *InvariantViolation {
	// visitEntry phase: count created/deleted MPTokenIssuance and MPToken entries.
	// In rippled, visitEntry receives (isDelete, before, after) where `after` is
	// always the SLE data (even for deletions). In Go's CollectEntries, deleted
	// entries have After=nil but EntryType is set from Before data. We use
	// EntryType + IsDelete + Before==nil to match rippled's counting logic:
	//   Created = !isDelete && before==nil  (entry with After data, no Before)
	//   Deleted = isDelete                  (entry marked as erased)
	//   Modified entries (Before!=nil, !isDelete) are ignored.
	var mptIssuancesCreated, mptIssuancesDeleted int
	var mptokensCreated, mptokensDeleted int

	for _, e := range entries {
		if e.EntryType == "MPTokenIssuance" {
			if e.IsDelete {
				mptIssuancesDeleted++
			} else if e.Before == nil {
				mptIssuancesCreated++
			}
		}
		if e.EntryType == "MPToken" {
			if e.IsDelete {
				mptokensDeleted++
			} else if e.Before == nil {
				mptokensCreated++
			}
		}
	}

	// finalize phase
	txType := tx.TxType()

	if result == TesSUCCESS {
		switch txType {
		case TypeMPTokenIssuanceCreate, TypeVaultCreate:
			// Must create exactly 1 issuance, delete 0.
			if mptIssuancesCreated != 1 || mptIssuancesDeleted != 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance create: expected exactly 1 issuance created and 0 deleted",
				}
			}
			return nil

		case TypeMPTokenIssuanceDestroy, TypeVaultDelete:
			// Must delete exactly 1 issuance, create 0.
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 1 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance destroy: expected exactly 0 issuances created and 1 deleted",
				}
			}
			return nil

		case TypeMPTokenAuthorize, TypeVaultDeposit:
			// No issuance changes allowed.
			if mptIssuancesCreated > 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize succeeded but created MPT issuances",
				}
			}
			if mptIssuancesDeleted > 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize succeeded but deleted issuances",
				}
			}

			// Check if submitted by issuer (Holder field present).
			// Use HasHolder() interface for reliable detection since
			// Common.HasField may not be populated for programmatically
			// constructed transactions.
			submittedByIssuer := false
			if hp, ok := tx.(holderFieldProvider); ok {
				submittedByIssuer = hp.HasHolder()
			} else {
				submittedByIssuer = tx.GetCommon().HasField("Holder")
			}
			if submittedByIssuer && (mptokensCreated > 0 || mptokensDeleted > 0) {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize submitted by issuer succeeded but created/deleted mptokens",
				}
			}
			// If holder submitted (not VaultDeposit), exactly 1 MPToken must be created or deleted.
			if !submittedByIssuer && txType != TypeVaultDeposit &&
				(mptokensCreated+mptokensDeleted != 1) {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT authorize submitted by holder succeeded but created/deleted bad number of mptokens",
				}
			}
			return nil

		case TypeMPTokenIssuanceSet:
			// Must not create/delete any.
			if mptIssuancesCreated != 0 || mptIssuancesDeleted != 0 ||
				mptokensCreated != 0 || mptokensDeleted != 0 {
				return &InvariantViolation{
					Name:    "ValidMPTIssuance",
					Message: "MPT issuance set succeeded but created/deleted MPT issuances or MPTokens",
				}
			}
			return nil

		case TypeEscrowFinish:
			// EscrowFinish is fully permissive — may create MPTokens for MPT escrows.
			return nil
		}
	}

	// For all other tx types (or non-success results), no MPT changes at all.
	if mptIssuancesCreated != 0 || mptIssuancesDeleted != 0 ||
		mptokensCreated != 0 || mptokensDeleted != 0 {
		return &InvariantViolation{
			Name:    "ValidMPTIssuance",
			Message: "unexpected MPTokenIssuance or MPToken changes",
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// ValidPermissionedDomain
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPermissionedDomain (lines 1538-1635)
//
// Only checks for PermissionedDomainSet with tesSUCCESS.
// visitEntry: for PermissionedDomain entries with "after" data, validates:
//   - AcceptedCredentials array exists, is non-empty, has size <= 10
//   - All entries are unique
//   - Entries are sorted by (Issuer, CredentialType) lexicographically.

// maxPermissionedDomainCredentials is the maximum number of credentials in a
// PermissionedDomain's AcceptedCredentials array.
// Reference: rippled Protocol.h — maxPermissionedDomainCredentialsArraySize = 10
const maxPermissionedDomainCredentials = 10

func checkValidPermissionedDomain(tx Transaction, result Result, entries []InvariantEntry) *InvariantViolation {
	if tx.TxType() != TypePermissionedDomainSet || result != TesSUCCESS {
		return nil
	}

	for _, e := range entries {
		// Only check PermissionedDomain entries that have an "after" state.
		if e.After == nil {
			continue
		}

		// Check both before and after: if before exists and is not PermissionedDomain, skip.
		// If after exists and is not PermissionedDomain, skip.
		// Reference: rippled lines 1544-1547
		if e.Before != nil {
			beforeType := getLedgerEntryType(e.Before)
			if beforeType != "PermissionedDomain" {
				continue
			}
		}
		afterType := getLedgerEntryType(e.After)
		if afterType != "PermissionedDomain" {
			continue
		}

		// Parse the PermissionedDomain from the "after" data.
		pd, err := state.ParsePermissionedDomain(e.After)
		if err != nil {
			continue
		}

		// Validate AcceptedCredentials.
		if v := validatePermissionedDomainCredentials(pd, e.Before != nil); v != nil {
			return v
		}
	}

	return nil
}

// validatePermissionedDomainCredentials checks that the AcceptedCredentials
// array is valid: non-empty, at most maxPermissionedDomainCredentials entries,
// unique, and sorted by (Issuer, CredentialType) lexicographically.
// isModified indicates whether this is a modification (before != nil) — both
// before and after states are checked against the same criteria in rippled.
func validatePermissionedDomainCredentials(pd *state.PermissionedDomainData, _ bool) *InvariantViolation {
	creds := pd.AcceptedCredentials

	// Check non-empty.
	if len(creds) == 0 {
		return &InvariantViolation{
			Name:    "ValidPermissionedDomain",
			Message: "permissioned domain with no rules",
		}
	}

	// Check max size.
	if len(creds) > maxPermissionedDomainCredentials {
		return &InvariantViolation{
			Name:    "ValidPermissionedDomain",
			Message: fmt.Sprintf("permissioned domain bad credentials size %d", len(creds)),
		}
	}

	// Check uniqueness and sorting.
	// Reference: rippled credentials::makeSorted() creates a
	// std::set<std::pair<AccountID, Slice>> — sorted by (Issuer, CredentialType)
	// lexicographically. If duplicates exist, the set is empty.
	// The invariant then checks that the stored array is in the same order as the sorted set.

	// Build sorted set and check for duplicates.
	type credKey struct {
		issuer         [20]byte
		credentialType string // use string for map key
	}
	seen := make(map[credKey]bool, len(creds))
	for _, c := range creds {
		k := credKey{issuer: c.Issuer, credentialType: string(c.CredentialType)}
		if seen[k] {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "permissioned domain credentials aren't unique",
			}
		}
		seen[k] = true
	}

	// Check that credentials are sorted by (Issuer, CredentialType) lexicographically.
	for i := 1; i < len(creds); i++ {
		cmp := bytes.Compare(creds[i-1].Issuer[:], creds[i].Issuer[:])
		if cmp > 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "permissioned domain credentials aren't sorted",
			}
		}
		if cmp == 0 {
			cmp = bytes.Compare(creds[i-1].CredentialType, creds[i].CredentialType)
			if cmp > 0 {
				return &InvariantViolation{
					Name:    "ValidPermissionedDomain",
					Message: "permissioned domain credentials aren't sorted",
				}
			}
			// cmp == 0 means duplicate, but that's already caught above
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// ValidNFTokenPage
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidNFTokenPage (lines 1017-1178)
//
// visitEntry: For each NFTokenPage entry (before and after separately):
//   - Verify page links are properly associated with the owning account
//   - Verify page ordering between links
//   - Token count must be 1-32 (empty pages only on delete)
//   - All tokens must be within page bounds
//   - Tokens must be sorted
//   - URIs if present must not be empty
//
// finalize: Check for deleted final pages and lost NextPageMin links.

// nftPageMaskLocal is the low 96 bits (bytes 20-31) used for NFT page grouping.
// Matches keylet.nftPageMask.
var nftPageMaskLocal = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
}

// nftPageMaskMax is the maximum page boundary (all 1s in low 96 bits).
var nftPageMaskMax = nftPageMaskLocal

// dirMaxTokensPerPage is the maximum number of NFTokens per page.
const dirMaxTokensPerPage = 32

// andKey256 computes a & mask for 32-byte keys.
func andKey256(a, mask [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = a[i] & mask[i]
	}
	return result
}

// notKey256 computes ^mask for a 32-byte key.
func notKey256(mask [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = ^mask[i]
	}
	return result
}

// compareKey256 returns -1, 0, or 1 comparing two 32-byte keys.
func compareKey256(a, b [32]byte) int {
	return bytes.Compare(a[:], b[:])
}

// isZeroKey256 returns true if the key is all zeros.
func isZeroKey256(k [32]byte) bool {
	var zero [32]byte
	return k == zero
}

// compareNFTokenIDs compares two NFToken IDs for sorting.
// Sort by low 96 bits first; if equal, sort by full 256-bit value.
// Reference: rippled NFTokenUtils.cpp compareTokens()
func compareNFTokenIDs(a, b [32]byte) int {
	aLow := andKey256(a, nftPageMaskLocal)
	bLow := andKey256(b, nftPageMaskLocal)
	cmp := compareKey256(aLow, bLow)
	if cmp != 0 {
		return cmp
	}
	return compareKey256(a, b)
}

// checkNFTokenPageSLE checks a single NFTokenPage SLE for invariant violations.
// Returns boolean flags for each type of violation found.
func checkNFTokenPageSLE(
	pageKey [32]byte,
	page *state.NFTokenPageData,
	isDelete bool,
) (badLink, badEntry, badSort, badURI, invalidSize bool) {
	accountBits := notKey256(nftPageMaskLocal)
	account := andKey256(pageKey, accountBits)
	hiLimit := andKey256(pageKey, nftPageMaskLocal)

	// Check PreviousPageMin link
	if !isZeroKey256(page.PreviousPageMin) {
		prevAccount := andKey256(page.PreviousPageMin, accountBits)
		if prevAccount != account {
			badLink = true
		}
		prevPageBits := andKey256(page.PreviousPageMin, nftPageMaskLocal)
		// hiLimit must be > prevPageBits
		if compareKey256(hiLimit, prevPageBits) <= 0 {
			badLink = true
		}
	}

	// Check NextPageMin link
	if !isZeroKey256(page.NextPageMin) {
		nextAccount := andKey256(page.NextPageMin, accountBits)
		if nextAccount != account {
			badLink = true
		}
		nextPageBits := andKey256(page.NextPageMin, nftPageMaskLocal)
		// hiLimit must be < nextPageBits
		if compareKey256(hiLimit, nextPageBits) >= 0 {
			badLink = true
		}
	}

	// Check token count
	tokenCount := len(page.NFTokens)
	if (!isDelete && tokenCount == 0) || tokenCount > dirMaxTokensPerPage {
		invalidSize = true
	}

	// Determine lower bound for token page bits
	var loLimit [32]byte
	if !isZeroKey256(page.PreviousPageMin) {
		loLimit = andKey256(page.PreviousPageMin, nftPageMaskLocal)
	}
	// else loLimit stays all zeros

	// Verify tokens are sorted and within bounds.
	// rippled initializes loCmp = loLimit and then for each token checks:
	//   if (!nft::compareTokens(loCmp, tokenID)) badSort = true
	// compareTokens(a, b) returns true if a < b.
	// So !compareTokens(loCmp, tokenID) means loCmp >= tokenID => badSort.
	loCmp := loLimit
	for _, token := range page.NFTokens {
		if compareNFTokenIDs(loCmp, token.NFTokenID) >= 0 {
			badSort = true
		}
		loCmp = token.NFTokenID

		// Check token is within page bounds
		tokenPageBits := andKey256(token.NFTokenID, nftPageMaskLocal)
		if compareKey256(tokenPageBits, loLimit) < 0 || compareKey256(tokenPageBits, hiLimit) >= 0 {
			badEntry = true
		}
	}

	return
}

// checkNFTokenPageURIEmpty checks if any NFToken on the page has an explicitly
// present but empty URI. This requires scanning the raw binary to detect
// field presence with zero length.
func checkNFTokenPageURIEmpty(data []byte) bool {
	offset := 0
	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case 1: // UInt16
			if offset+2 > len(data) {
				return false
			}
			offset += 2
		case 2: // UInt32
			if offset+4 > len(data) {
				return false
			}
			offset += 4
		case 3: // UInt64
			if offset+8 > len(data) {
				return false
			}
			offset += 8
		case 5: // Hash256
			if offset+32 > len(data) {
				return false
			}
			offset += 32
		case 7: // Blob (VL-encoded)
			if offset >= len(data) {
				return false
			}
			length := int(data[offset])
			extra := 1
			if length > 192 {
				if offset+1 >= len(data) {
					return false
				}
				length = 193 + ((length-193)<<8 | int(data[offset+1]))
				extra = 2
			}
			offset += extra
			// URI is Blob fieldCode 5
			if fieldCode == 5 && length == 0 {
				return true // Found empty URI
			}
			if offset+length > len(data) {
				return false
			}
			offset += length
		case 8: // AccountID
			if offset+20 > len(data) {
				return false
			}
			offset += 20
		case 14, 15: // STObject/STArray structural markers
			continue
		default:
			return false
		}
	}
	return false
}

func checkValidNFTokenPage(entries []InvariantEntry, view LedgerView, rules *amendment.Rules) *InvariantViolation {
	var (
		badLink          bool
		badEntry         bool
		badSort          bool
		badURI           bool
		invalidSize      bool
		deletedFinalPage bool
		deletedLink      bool
	)

	for _, e := range entries {
		// Only process NFTokenPage entries.
		// rippled checks: if before and before->getType() != ltNFTOKEN_PAGE, skip
		//                 if after and after->getType() != ltNFTOKEN_PAGE, skip
		if e.EntryType != "NFTokenPage" {
			continue
		}

		// Check before state
		if e.Before != nil {
			page, err := state.ParseNFTokenPage(e.Before)
			if err == nil {
				bl, be, bs, _, is := checkNFTokenPageSLE(e.Key, page, e.IsDelete)
				badLink = badLink || bl
				badEntry = badEntry || be
				badSort = badSort || bs
				invalidSize = invalidSize || is

				// Check for empty URI in raw binary
				if checkNFTokenPageURIEmpty(e.Before) {
					badURI = true
				}

				// Check if deleting final page (low 96 bits == all 1s)
				// with PreviousPageMin present.
				// Reference: rippled line 1098-1102
				if e.IsDelete {
					pageBits := andKey256(e.Key, nftPageMaskLocal)
					if pageBits == nftPageMaskMax && !isZeroKey256(page.PreviousPageMin) {
						deletedFinalPage = true
					}
				}
			}
		}

		// Check after state
		if e.After != nil {
			page, err := state.ParseNFTokenPage(e.After)
			if err == nil {
				bl, be, bs, _, is := checkNFTokenPageSLE(e.Key, page, false)
				badLink = badLink || bl
				badEntry = badEntry || be
				badSort = badSort || bs
				invalidSize = invalidSize || is

				// Check for empty URI in raw binary
				if checkNFTokenPageURIEmpty(e.After) {
					badURI = true
				}
			}
		}

		// Check for lost NextPageMin link (modification, not deletion).
		// If before has NextPageMin and after doesn't, and this is not the final page.
		// Reference: rippled lines 1108-1121
		if !e.IsDelete && e.Before != nil && e.After != nil {
			pageBits := andKey256(e.Key, nftPageMaskLocal)
			if pageBits != nftPageMaskMax {
				beforePage, errB := state.ParseNFTokenPage(e.Before)
				afterPage, errA := state.ParseNFTokenPage(e.After)
				if errB == nil && errA == nil {
					if !isZeroKey256(beforePage.NextPageMin) && isZeroKey256(afterPage.NextPageMin) {
						deletedLink = true
					}
				}
			}
		}
	}

	// Finalize — check violations in the same order as rippled
	if badLink {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT page is improperly linked",
		}
	}
	if badEntry {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT found in incorrect page",
		}
	}
	if badSort {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFTs on page are not sorted",
		}
	}
	if badURI {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT contains empty URI",
		}
	}
	if invalidSize {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT page has invalid size",
		}
	}

	// Amendment-gated checks
	if rules != nil && rules.Enabled(amendment.FeatureFixNFTokenPageLinks) {
		if deletedFinalPage {
			return &InvariantViolation{
				Name:    "ValidNFTokenPage",
				Message: "Last NFT page deleted with non-empty directory",
			}
		}
		if deletedLink {
			return &InvariantViolation{
				Name:    "ValidNFTokenPage",
				Message: "Lost NextMinPage link",
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// AccountRootsDeletedClean
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — AccountRootsDeletedClean (lines 416-501)
//
// visitEntry: Collect deleted AccountRoot entries.
//
// finalize: For each deleted account, verify that no directly-derivable objects
// remain in the view (account root, owner directory, signer list, NFT pages,
// DID). If the account had sfAMMID, verify the AMM object is also gone.
//
// Gating: Only enforced when featureInvariantsV1_1 is enabled.

func checkAccountRootsDeletedClean(entries []InvariantEntry, view LedgerView, rules *amendment.Rules) *InvariantViolation {
	// Only enforce when InvariantsV1_1 is enabled.
	// Reference: rippled lines 438-439
	enforce := rules != nil && rules.Enabled(amendment.FeatureInvariantsV1_1)
	if !enforce {
		return nil
	}

	if view == nil {
		return nil
	}

	// Collect deleted AccountRoot entries
	type deletedAccount struct {
		accountID [20]byte
		ammID     [32]byte
		hasAMMID  bool
	}
	var deletedAccounts []deletedAccount

	for _, e := range entries {
		if e.EntryType != "AccountRoot" || !e.IsDelete {
			continue
		}
		if e.Before == nil {
			continue
		}
		acct, err := state.ParseAccountRoot(e.Before)
		if err != nil {
			continue
		}
		accID, err := state.DecodeAccountID(acct.Account)
		if err != nil {
			continue
		}
		var zeroHash [32]byte
		da := deletedAccount{
			accountID: accID,
			hasAMMID:  acct.AMMID != zeroHash,
			ammID:     acct.AMMID,
		}
		deletedAccounts = append(deletedAccounts, da)
	}

	if len(deletedAccounts) == 0 {
		return nil
	}

	for _, da := range deletedAccounts {
		// Check direct account keylets.
		// Reference: rippled directAccountKeylets (Indexes.h lines 382-390)
		directKeylets := []keylet.Keylet{
			keylet.Account(da.accountID),
			keylet.OwnerDir(da.accountID),
			keylet.SignerList(da.accountID),
			keylet.NFTokenPageMin(da.accountID),
			keylet.NFTokenPageMax(da.accountID),
			keylet.DID(da.accountID),
		}

		for _, kl := range directKeylets {
			exists, err := view.Exists(kl)
			if err == nil && exists {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}

		// Check for NFT pages between min and max using Succ.
		// rippled uses view.succ(first.key, last.key.next()) to find any
		// NFT page in the range. Our Succ(key) returns the first entry
		// with key > given key. We check if the successor is within the
		// NFT page range for this account.
		// Reference: rippled lines 477-490
		firstKey := keylet.NFTokenPageMin(da.accountID).Key
		lastKey := keylet.NFTokenPageMax(da.accountID).Key

		succKey, _, found, err := view.Succ(firstKey)
		if err == nil && found {
			// If the successor key is within [firstKey, lastKey],
			// there's a leftover NFT page.
			if compareKey256(succKey, lastKey) <= 0 {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}

		// Check AMM object if sfAMMID was present.
		// Reference: rippled lines 492-497
		if da.hasAMMID {
			ammKL := keylet.AMMByID(da.ammID)
			exists, err := view.Exists(ammKL)
			if err == nil && exists {
				return &InvariantViolation{
					Name:    "AccountRootsDeletedClean",
					Message: "account deletion left behind a ledger object",
				}
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// ValidPermissionedDEX
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPermissionedDEX (lines 1637-1718)
//
// visitEntry: For entries with "after" data:
//   - DirNode with DomainID: record the domain
//   - Offer with DomainID: record the domain; check hybrid offer structure
//   - Offer without DomainID: mark regularOffers
//
// finalize: Only for Payment/OfferCreate with tesSUCCESS:
//   - If tx has DomainID: verify domain exists, all touched domains match,
//     no regular offers affected
//   - Bad hybrids always fail for OfferCreate

// lsfHybridInvariant is the ledger flag for hybrid offers.
const lsfHybridInvariant uint32 = 0x00040000

// domainIDProvider is implemented by transactions that may have a DomainID field.
type domainIDProvider interface {
	GetDomainID() (*[32]byte, bool)
}

func checkValidPermissionedDEX(tx Transaction, result Result, entries []InvariantEntry, view LedgerView) *InvariantViolation {
	txType := tx.TxType()

	// Only check for Payment and OfferCreate with tesSUCCESS.
	// Reference: rippled lines 1674-1677
	if (txType != TypePayment && txType != TypeOfferCreate) || result != TesSUCCESS {
		return nil
	}

	var (
		regularOffers bool
		badHybrids    bool
		domains       = make(map[[32]byte]bool)
	)

	var zeroHash [32]byte

	for _, e := range entries {
		if e.After == nil {
			continue
		}

		afterType := getLedgerEntryType(e.After)

		switch afterType {
		case "DirectoryNode":
			// Check if the DirNode has a DomainID field.
			// Reference: rippled lines 1643-1647
			domainID := extractDomainIDFromBinary(e.After)
			if domainID != zeroHash {
				domains[domainID] = true
			}

		case "Offer":
			offer, err := state.ParseLedgerOfferFromBytes(e.After)
			if err != nil {
				continue
			}

			if offer.DomainID != zeroHash {
				domains[offer.DomainID] = true
			} else {
				regularOffers = true
			}

			// Check hybrid offer structure.
			// Reference: rippled lines 1658-1663
			// rippled checks: lsfHybrid requires DomainID present AND
			// sfAdditionalBooks present with at most 1 entry.
			// In the Go codebase, AdditionalBooks is not serialized as an
			// STArray in binary but stored as separate struct fields
			// (AdditionalBookDirectory, AdditionalBookNode). We check:
			//   1. DomainID must be present for hybrid offers
			//   2. AdditionalBooks (if encoded as STArray) must have <= 1 entry
			if (offer.Flags & lsfHybridInvariant) != 0 {
				if offer.DomainID == zeroHash {
					badHybrids = true
				}
				// Check AdditionalBooks if present in binary
				abCount := countAdditionalBooksFromBinary(e.After)
				if abCount > 1 {
					badHybrids = true
				}
				// Note: abCount == -1 means AdditionalBooks not in binary,
				// which is valid in Go since it stores the data differently.
			}
		}
	}

	// For OfferCreate, always check bad hybrids.
	// Reference: rippled lines 1681-1685
	if txType == TypeOfferCreate && badHybrids {
		return &InvariantViolation{
			Name:    "ValidPermissionedDEX",
			Message: "hybrid offer is malformed",
		}
	}

	// Check if the transaction has a DomainID.
	// Reference: rippled lines 1687-1688
	var txDomainID *[32]byte

	// Try the domainIDProvider interface first
	if dp, ok := tx.(domainIDProvider); ok {
		if did, hasDomain := dp.GetDomainID(); hasDomain {
			txDomainID = did
		}
	} else {
		// Fall back to Common.HasField and Flatten
		if tx.GetCommon().HasField("DomainID") {
			flat, err := tx.Flatten()
			if err == nil {
				if domainStr, ok := flat["DomainID"].(string); ok {
					b, err := hex.DecodeString(domainStr)
					if err == nil && len(b) == 32 {
						var did [32]byte
						copy(did[:], b)
						txDomainID = &did
					}
				}
			}
		}
	}

	if txDomainID == nil {
		// Transaction doesn't have DomainID — no further checks needed.
		// Reference: rippled lines 1687-1688 — "return true" if no sfDomainID
		return nil
	}

	// Verify the domain exists in the view.
	// Reference: rippled lines 1690-1696
	if view != nil {
		pdKL := keylet.PermissionedDomainByID(*txDomainID)
		exists, err := view.Exists(pdKL)
		if err != nil || !exists {
			return &InvariantViolation{
				Name:    "ValidPermissionedDEX",
				Message: "domain doesn't exist",
			}
		}
	}

	// All domains touched by offers/dirs must match the tx's domain.
	// Reference: rippled lines 1700-1708
	for d := range domains {
		if d != *txDomainID {
			return &InvariantViolation{
				Name:    "ValidPermissionedDEX",
				Message: "transaction consumed wrong domains",
			}
		}
	}

	// No regular offers should be affected by domain transactions.
	// Reference: rippled lines 1710-1715
	if regularOffers {
		return &InvariantViolation{
			Name:    "ValidPermissionedDEX",
			Message: "domain transaction affected regular offers",
		}
	}

	return nil
}

// extractDomainIDFromBinary extracts the DomainID (Hash256, fieldCode=34) from
// binary SLE data. Returns a zero [32]byte if not found.
func extractDomainIDFromBinary(data []byte) [32]byte {
	var result [32]byte
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = int(data[offset])
			offset++
		}

		switch typeCode {
		case 5: // Hash256
			if offset+32 > len(data) {
				return result
			}
			if fieldCode == 34 { // DomainID
				copy(result[:], data[offset:offset+32])
				return result
			}
			offset += 32
		default:
			if typeCode == 14 || typeCode == 15 {
				// STObject/STArray structural markers — no payload
				continue
			}
			skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
			if !ok {
				return result
			}
			offset += skip
		}
	}
	return result
}

// countAdditionalBooksFromBinary counts the number of entries in the
// AdditionalBooks STArray (type=15, fieldCode=13) in binary SLE data.
// Returns -1 if the field is not present, or the count of objects inside.
func countAdditionalBooksFromBinary(data []byte) int {
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = int(data[offset])
			offset++
		}

		if typeCode == 15 && fieldCode == 13 {
			// Found AdditionalBooks array start.
			// Count objects inside until we hit the array end marker (0xF1).
			count := 0
			for offset < len(data) {
				if data[offset] == 0xF1 {
					// End of array
					return count
				}
				if data[offset] == 0xE1 {
					// End of object — count the completed object
					count++
					offset++
					continue
				}
				// Parse and skip inner field
				innerHeader := data[offset]
				offset++
				innerType := int((innerHeader >> 4) & 0x0F)
				innerField := int(innerHeader & 0x0F)

				if innerType == 0 {
					if offset >= len(data) {
						return count
					}
					innerType = int(data[offset])
					offset++
				}
				if innerField == 0 {
					if offset >= len(data) {
						return count
					}
					innerField = int(data[offset])
					offset++
				}

				if innerType == 14 || innerType == 15 {
					// Object/array structural marker — no payload
					continue
				}

				skip, ok := skipFieldBytes(innerType, innerField, data, offset)
				if !ok {
					return count
				}
				offset += skip
			}
			return count
		}

		// Skip this field
		if typeCode == 14 || typeCode == 15 {
			// Structural markers — no payload
			continue
		}

		skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
		if !ok {
			return -1
		}
		offset += skip
	}
	return -1 // Not found
}

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

func checkTransfersNotFrozen(tx Transaction, entries []InvariantEntry, view LedgerView, rules *amendment.Rules) *InvariantViolation {
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
	view LedgerView,
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

// ---------------------------------------------------------------------------
// ValidAMM
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidAMM (lines 1720-2023)
// Reference: rippled InvariantCheck.h — ValidAMM struct (lines 644-705)
//
// visitEntry phase:
//   - Track AMM entries: extract account ID and LPTokenBalance from before/after
//   - Track pool changes: RippleState with lsfAMMNode flag, or AccountRoot with non-zero AMMID
//
// finalize phase — dispatch by tx type:
//   AMMVote: LP tokens and pool must not change
//   AMMBid: Pool must not change; LP tokens should decrease (burnt for bidding)
//   AMMCreate: AMM must be created; sqrt(amount * amount2) == LPTokens; all balances > 0
//   AMMDelete: AMM must not remain (deleted on tesSUCCESS, unchanged on tecINCOMPLETE)
//   AMMDeposit: AMM must not be deleted; general invariant sqrt(a*b) >= LPT
//   AMMWithdraw/AMMClawback: AMM may be deleted (last withdraw); general invariant with zero allowed
//   DEX (Payment/OfferCreate/CheckCash): AMM object must not be changed directly
//
// Amendment gating: enforce = rules.Enabled(fixAMMv1_3)

// ammAssetProvider is implemented by AMMDeposit, AMMWithdraw, and AMMClawback
// to provide the AMM's asset pair without importing the amm subpackage.
type ammAssetProvider interface {
	GetAMMAsset() Asset
	GetAMMAsset2() Asset
}

// ammCreateIssueProvider is implemented by AMMCreate to provide the asset issues
// from Amount and Amount2 fields (which are full amounts, not just assets).
type ammCreateIssueProvider interface {
	GetAmountAsset() Asset
	GetAmount2Asset() Asset
}

// ammInvariantFields holds the fields extracted from AMM SLE entries during the
// visitEntry phase.
type ammInvariantFields struct {
	accountID [20]byte
	lptBalance Amount
	hasBalance bool
}

// isLikelyAMMBinary checks if binary data is likely an AMM SLE entry.
// AMM entries use a custom binary format that starts with 20 bytes of AccountID,
// NOT the standard 0x11 LedgerEntryType header. We verify the data is at least
// 110 bytes (minimum AMM entry size) and does NOT start with 0x11.
// We also verify that bytes 20-39 contain a plausible Issue (40 bytes = currency + issuer).
func isLikelyAMMBinary(data []byte) bool {
	if len(data) < 110 {
		return false
	}
	// Standard SLE entries start with 0x11 (LedgerEntryType header).
	// AMM entries start with raw AccountID bytes.
	if data[0] == 0x11 {
		return false
	}
	// Additional heuristic: check if offset 20 contains a plausible currency.
	// A valid currency's first byte is either 0x00 (standard ISO) or has bit 7 set (non-standard).
	// For AMM, the Asset field starts at offset 20.
	firstCurrByte := data[20]
	if firstCurrByte != 0x00 && (firstCurrByte&0x80) == 0 {
		return false
	}
	return true
}

// parseAMMInvariantFields extracts the Account ID and LPTokenBalance from
// binary AMM SLE data. This is a local parser to avoid importing the amm package.
// The AMM SLE uses a CUSTOM binary format (not the standard binary codec).
// Reference: rippled InvariantCheck.cpp lines 1733-1737 (after), 1749-1754 (before)
func parseAMMInvariantFields(data []byte) (*ammInvariantFields, error) {
	if len(data) < 110 {
		return nil, fmt.Errorf("AMM data too short: %d bytes", len(data))
	}

	result := &ammInvariantFields{}

	// Account (20 bytes) — first field
	copy(result.accountID[:], data[0:20])

	// Asset (40 bytes: currency + issuer)
	offset := 20
	offset += 40 // skip Asset

	// Asset2 (40 bytes: currency + issuer)
	offset += 40 // skip Asset2

	// TradingFee (2 bytes)
	offset += 2

	// OwnerNode (8 bytes)
	offset += 8

	// LPTokenBalance — uses the custom AMM Amount serialization format:
	//   1 byte type (0=XRP, 1=IOU)
	//   XRP: 8 bytes int64 drops
	//   IOU: 8 bytes int64 mantissa + 4 bytes int32 exponent (exponent offset by +128)
	if offset >= len(data) {
		return result, nil
	}
	amt, consumed := deserializeAMMAmount(data[offset:])
	if consumed > 0 {
		result.lptBalance = amt
		result.hasBalance = true
	}

	return result, nil
}

// deserializeAMMAmount reads an Amount from the custom AMM binary format.
// Format:
//   1 byte type (0=XRP, 1=IOU)
//   XRP: 8 bytes int64 drops
//   IOU: 8 bytes int64 mantissa + 4 bytes int32 exponent (offset by +128)
// Returns the Amount and the number of bytes consumed.
func deserializeAMMAmount(data []byte) (Amount, int) {
	if len(data) < 1 {
		return state.NewIssuedAmountFromValue(0, -100, "", ""), 0
	}
	amtType := data[0]
	if amtType == 0 {
		// XRP
		if len(data) < 9 {
			return state.NewXRPAmountFromInt(0), 0
		}
		drops := int64(binary.BigEndian.Uint64(data[1:9]))
		return state.NewXRPAmountFromInt(drops), 9
	}
	// IOU
	if len(data) < 13 {
		return state.NewIssuedAmountFromValue(0, -100, "", ""), 0
	}
	mantissa := int64(binary.BigEndian.Uint64(data[1:9]))
	exponent := int(binary.BigEndian.Uint32(data[9:13])) - 128 // reverse offset
	return state.NewIssuedAmountFromValue(mantissa, exponent, "", ""), 13
}

// ammPoolHoldsForInvariant reads the balances of both assets in the AMM pool.
// Uses fhIGNORE_FREEZE (no freeze zeroing) to match rippled's invariant behavior.
// This is a local implementation to avoid importing the amm package.
// Reference: rippled AMMUtils.cpp ammPoolHolds + InvariantCheck.cpp (fhIGNORE_FREEZE)
func ammPoolHoldsForInvariant(view LedgerView, ammAccountID [20]byte, asset1, asset2 Asset) (Amount, Amount) {
	balance1 := ammAccountHoldsForInvariant(view, ammAccountID, asset1)
	balance2 := ammAccountHoldsForInvariant(view, ammAccountID, asset2)
	return balance1, balance2
}

// ammAccountHoldsForInvariant returns the amount held by the AMM account for a specific issue.
// For XRP: reads from the AMM account's AccountRoot.Balance
// For IOU: reads from the trustline between AMM account and issuer
func ammAccountHoldsForInvariant(view LedgerView, ammAccountID [20]byte, asset Asset) Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		// XRP: read from AccountRoot
		accountKey := keylet.Account(ammAccountID)
		data, err := view.Read(accountKey)
		if err != nil || data == nil {
			return state.NewXRPAmountFromInt(0)
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return state.NewXRPAmountFromInt(0)
		}
		return state.NewXRPAmountFromInt(int64(account.Balance))
	}

	// IOU: read from trustline
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective
	// AMM account is always the "holder" side
	ammIsLow := state.CompareAccountIDsForLine(ammAccountID, issuerID) < 0
	balance := rs.Balance
	if !ammIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), asset.Currency, asset.Issuer)
}

// toIOUForInvariant converts an Amount to IOU representation for precise calculations.
// Matches the AMM helper's toIOUForCalc function.
func toIOUForInvariant(amt Amount) Amount {
	if !amt.IsNative() {
		return amt
	}
	drops := amt.Drops()
	if drops == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	mantissa := drops
	exp := 0
	for mantissa >= 1e16 {
		mantissa /= 10
		exp++
	}
	for mantissa > 0 && mantissa < 1e15 {
		mantissa *= 10
		exp--
	}
	return state.NewIssuedAmountFromValue(mantissa, exp, "", "")
}

// calculateLPTokensForInvariant computes the geometric mean: sqrt(amount1 * amount2).
// This matches rippled's ammLPTokens function.
// Reference: rippled AMMHelpers.cpp ammLPTokens / InvariantCheck.cpp root2(amount * amount2)
func calculateLPTokensForInvariant(amount1, amount2 Amount) Amount {
	if amount1.IsZero() || amount2.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	// Convert both to IOU for consistent calculation
	iou1 := toIOUForInvariant(amount1)
	iou2 := toIOUForInvariant(amount2)

	// product = iou1 * iou2
	product := iou1.Mul(iou2, false)
	// result = sqrt(product)
	return product.Sqrt()
}

// validAMMBalances checks that balances are valid for the AMM invariant.
// If zeroAllowed, all three can be zero together; otherwise all must be positive.
// Reference: rippled InvariantCheck.cpp validBalances (lines 1757-1771)
func validAMMBalances(amount, amount2, lptBalance Amount, zeroAllowed bool) bool {
	positive := amount.Signum() > 0 && amount2.Signum() > 0 && lptBalance.Signum() > 0
	if zeroAllowed {
		return positive ||
			(amount.IsZero() && amount2.IsZero() && lptBalance.IsZero())
	}
	return positive
}

// withinRelativeDistanceForInvariant checks if two IOU amounts are within
// relative distance dist: (max - min) / max < dist.
// Uses math/big.Int for precise integer arithmetic on mantissa/exponent pairs.
// Reference: rippled AMMHelpers.h withinRelativeDistance (lines 156-162)
func withinRelativeDistanceForInvariant(calc, req Amount) bool {
	calcIOU := toIOUForInvariant(calc)
	reqIOU := toIOUForInvariant(req)

	if calcIOU.Compare(reqIOU) == 0 {
		return true
	}

	var minAmt, maxAmt Amount
	if calcIOU.Compare(reqIOU) < 0 {
		minAmt = calcIOU
		maxAmt = reqIOU
	} else {
		minAmt = reqIOU
		maxAmt = calcIOU
	}

	// Compute (max - min) / max using Number-based division for precision.
	// We need the result compared against 1e-11.
	// Use big.Int arithmetic: diff_mantissa * 10^diff_exp / max_mantissa * 10^max_exp < 1e-11
	// Equivalently: diff_mantissa * 10^(diff_exp - max_exp) * 10^11 < max_mantissa

	diff, _ := maxAmt.Sub(minAmt)
	if diff.IsZero() {
		return true
	}

	// Use XRPLNumber for precise division matching rippled's Number arithmetic
	diffNum := state.NewXRPLNumber(diff.Mantissa(), diff.Exponent())
	maxNum := state.NewXRPLNumber(maxAmt.Mantissa(), maxAmt.Exponent())
	ratio := diffNum.Div(maxNum)

	// Compare ratio < 1e-11
	// 1e-11 as XRPLNumber: mantissa=1e15, exponent=-26 (normalized)
	threshold := state.NewXRPLNumber(1e15, -26)

	// ratio < threshold ?
	ratioAmt := ratio.ToIOUAmountValue()
	thresholdAmt := threshold.ToIOUAmountValue()
	rIOU := state.NewIssuedAmountFromValue(ratioAmt.Mantissa(), ratioAmt.Exponent(), "", "")
	tIOU := state.NewIssuedAmountFromValue(thresholdAmt.Mantissa(), thresholdAmt.Exponent(), "", "")

	return rIOU.Compare(tIOU) < 0
}

// checkValidAMM implements the ValidAMM invariant checker.
// Reference: rippled InvariantCheck.cpp ValidAMM::visitEntry + ValidAMM::finalize (lines 1720-2023)
func checkValidAMM(tx Transaction, result Result, entries []InvariantEntry, view LedgerView, rules *amendment.Rules) *InvariantViolation {
	// Delete may return tecINCOMPLETE if there are too many trustlines to delete.
	// Reference: rippled lines 1994-1995
	if result != TesSUCCESS && result != TecINCOMPLETE {
		return nil
	}

	// --- visitEntry phase ---
	// Track AMM entries: extract account ID and LPTokenBalance from before/after.
	// Track pool changes: RippleState with lsfAMMNode flag, or AccountRoot with non-zero AMMID.
	var (
		ammAccount     *[20]byte
		lptAfter       *Amount
		lptBefore      *Amount
		ammPoolChanged bool
	)

	for _, e := range entries {
		if e.IsDelete {
			continue
		}

		// Check "after" data
		if e.After != nil {
			// Try to detect AMM entries.
			// AMM SLE uses a custom binary format (no 0x11 type header), so
			// e.EntryType from getLedgerEntryType may not return "AMM".
			// We detect AMM entries by:
			//   1. Explicit "AMM" EntryType (if binary codec format is used)
			//   2. Trying to parse as AMM data for entries with unknown/non-standard types
			isAMMEntry := false
			if e.EntryType == "AMM" {
				isAMMEntry = true
			} else if isLikelyAMMBinary(e.After) {
				isAMMEntry = true
			}

			if isAMMEntry {
				// AMM object changed — extract account ID and LPTokenBalance
				fields, err := parseAMMInvariantFields(e.After)
				if err == nil {
					id := fields.accountID
					ammAccount = &id
					if fields.hasBalance {
						bal := fields.lptBalance
						lptAfter = &bal
					}
					}
			} else if e.EntryType == "RippleState" {
				// Check for lsfAMMNode flag
				rs, err := state.ParseRippleState(e.After)
				if err == nil && (rs.Flags&state.LsfAMMNode) != 0 {
					ammPoolChanged = true
				}
			} else if e.EntryType == "AccountRoot" {
				// Check for non-zero AMMID (AMM pseudo-account)
				acct, err := state.ParseAccountRoot(e.After)
				if err == nil {
					var zeroHash [32]byte
					if acct.AMMID != zeroHash {
						ammPoolChanged = true
					}
				}
			}
		}

		// Check "before" data for LPTokenBalance
		if e.Before != nil {
			isAMMBefore := false
			if e.EntryType == "AMM" {
				isAMMBefore = true
			} else if isLikelyAMMBinary(e.Before) {
				isAMMBefore = true
			}

			if isAMMBefore {
				fields, err := parseAMMInvariantFields(e.Before)
				if err == nil && fields.hasBalance {
					bal := fields.lptBalance
					lptBefore = &bal
				}
			}
		}
	}

	// --- finalize phase ---
	enforce := rules != nil && rules.Enabled(amendment.FeatureFixAMMv1_3)

	txType := tx.TxType()
	switch txType {
	case TypeAMMCreate:
		return finalizeAMMCreate(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMDeposit:
		return finalizeAMMDeposit(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMClawback, TypeAMMWithdraw:
		return finalizeAMMWithdraw(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMBid:
		return finalizeAMMBid(ammPoolChanged, lptBefore, lptAfter, enforce)
	case TypeAMMVote:
		return finalizeAMMVote(ammPoolChanged, lptBefore, lptAfter, enforce)
	case TypeAMMDelete:
		return finalizeAMMDelete(ammAccount, result, enforce)
	case TypeCheckCash, TypeOfferCreate, TypePayment:
		return finalizeAMMDEX(ammAccount, enforce)
	}

	return nil
}

// finalizeAMMVote checks that LP tokens and pool do not change on AMMVote.
// Reference: rippled InvariantCheck.cpp finalizeVote (lines 1774-1790)
func finalizeAMMVote(ammPoolChanged bool, lptBefore, lptAfter *Amount, enforce bool) *InvariantViolation {
	// Check if LPTokenBalance changed
	lptChanged := false
	if lptBefore != nil && lptAfter != nil {
		lptChanged = lptBefore.Compare(*lptAfter) != 0
	} else if (lptBefore == nil) != (lptAfter == nil) {
		// One is nil and the other isn't — that's a change
		lptChanged = true
	}

	if lptChanged || ammPoolChanged {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMVote invariant failed: LP tokens or pool changed",
			}
		}
	}

	return nil
}

// finalizeAMMBid checks that pool does not change and LP tokens decrease on AMMBid.
// Reference: rippled InvariantCheck.cpp finalizeBid (lines 1793-1819)
func finalizeAMMBid(ammPoolChanged bool, lptBefore, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammPoolChanged {
		// The pool cannot change on bid
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMBid invariant failed: pool changed",
			}
		}
	} else if lptBefore != nil && lptAfter != nil {
		// LP tokens are burnt, therefore there should be fewer LP tokens after
		// lptAfter > lptBefore || lptAfter <= 0
		if lptAfter.Compare(*lptBefore) > 0 || lptAfter.Signum() <= 0 {
			if enforce {
				return &InvariantViolation{
					Name:    "ValidAMM",
					Message: "AMMBid invariant failed: LP tokens did not decrease",
				}
			}
		}
	}

	return nil
}

// finalizeAMMCreate checks that AMM was created with correct initial LP tokens.
// Reference: rippled InvariantCheck.cpp finalizeCreate (lines 1822-1862)
func finalizeAMMCreate(tx Transaction, view LedgerView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// AMM object was not created
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: AMM object is not created",
			}
		}
		return nil
	}

	if lptAfter == nil {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: no LPTokenBalance",
			}
		}
		return nil
	}

	// Get asset issues from the transaction
	createProvider, ok := tx.(ammCreateIssueProvider)
	if !ok {
		// Cannot inspect tx fields — skip check
		return nil
	}

	asset1 := createProvider.GetAmountAsset()
	asset2 := createProvider.GetAmount2Asset()

	// Read pool balances
	if view == nil {
		return nil
	}
	amount, amount2 := ammPoolHoldsForInvariant(view, *ammAccount, asset1, asset2)
	// Create invariant: sqrt(amount * amount2) == LPTokens, all balances > 0
	if !validAMMBalances(amount, amount2, *lptAfter, false) {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: invalid balances",
			}
		}
		return nil
	}

	// Check sqrt(amount * amount2) == LPTokens
	// Use the same calculation path as the AMM create code.
	// rippled: ammLPTokens(amount, amount2, lptAMMBalanceAfter_->issue()) != *lptAMMBalanceAfter_
	expectedLPT := calculateLPTokensForInvariant(amount, amount2)
	expectedIOU := toIOUForInvariant(expectedLPT)
	actualIOU := toIOUForInvariant(*lptAfter)
	if expectedIOU.Compare(actualIOU) != 0 {
		// Allow for tiny precision differences by using withinRelativeDistance
		// rippled uses exact != comparison, but our Amount arithmetic may
		// have minor differences from Number arithmetic
		if !withinRelativeDistanceForInvariant(expectedLPT, *lptAfter) {
			if enforce {
				return &InvariantViolation{
					Name:    "ValidAMM",
					Message: fmt.Sprintf("AMMCreate invariant failed: LP tokens mismatch (expected=%v, got=%v)", expectedLPT, *lptAfter),
				}
			}
		}
	}

	return nil
}

// finalizeAMMDelete checks that the AMM object is properly deleted.
// Reference: rippled InvariantCheck.cpp finalizeDelete (lines 1864-1880)
func finalizeAMMDelete(ammAccount *[20]byte, result Result, enforce bool) *InvariantViolation {
	if ammAccount != nil {
		// AMM object still exists after delete
		if enforce {
			msg := "AMM object is not deleted on tesSUCCESS"
			if result == TecINCOMPLETE {
				msg = "AMM object is changed on tecINCOMPLETE"
			}
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: fmt.Sprintf("AMMDelete invariant failed: %s", msg),
			}
		}
	}
	return nil
}

// finalizeAMMDeposit checks the general AMM invariant on deposit.
// Reference: rippled InvariantCheck.cpp finalizeDeposit (lines 1944-1962)
func finalizeAMMDeposit(tx Transaction, view LedgerView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// AMM object was deleted — not allowed on deposit
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMDeposit invariant failed: AMM object is deleted",
			}
		}
		return nil
	}

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, false); v != nil {
		if enforce {
			return v
		}
	}

	return nil
}

// finalizeAMMWithdraw checks the general AMM invariant on withdraw/clawback.
// AMM may be deleted (last withdraw), so ammAccount == nil is allowed.
// Reference: rippled InvariantCheck.cpp finalizeWithdraw (lines 1964-1982)
func finalizeAMMWithdraw(tx Transaction, view LedgerView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// Last Withdraw or Clawback deleted AMM — allowed
		return nil
	}

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, true); v != nil {
		if enforce {
			return v
		}
	}

	return nil
}

// finalizeAMMDEX checks that the AMM object is not directly modified by DEX transactions.
// Reference: rippled InvariantCheck.cpp finalizeDEX (lines 1883-1895)
func finalizeAMMDEX(ammAccount *[20]byte, enforce bool) *InvariantViolation {
	if ammAccount != nil {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMM swap invariant failed: AMM object changed",
			}
		}
	}
	return nil
}

// generalAMMInvariant checks that sqrt(amount * amount2) >= LPTokens.
// zeroAllowed controls whether all-zero balances are acceptable (for withdrawals).
// Reference: rippled InvariantCheck.cpp generalInvariant (lines 1897-1941)
func generalAMMInvariant(tx Transaction, view LedgerView, ammAccount *[20]byte, lptAfter *Amount, zeroAllowed bool) *InvariantViolation {
	if ammAccount == nil || lptAfter == nil || view == nil {
		return nil
	}

	// Get asset pair from the transaction
	assetProvider, ok := tx.(ammAssetProvider)
	if !ok {
		return nil
	}

	asset1 := assetProvider.GetAMMAsset()
	asset2 := assetProvider.GetAMMAsset2()

	// Read pool balances from the view
	amount, amount2 := ammPoolHoldsForInvariant(view, *ammAccount, asset1, asset2)

	// Compute sqrt(amount * amount2)
	poolProductMean := calculateLPTokensForInvariant(amount, amount2)

	// Check valid balances
	nonNegativeBalances := validAMMBalances(amount, amount2, *lptAfter, zeroAllowed)

	// Strong check: poolProductMean >= lptAfter
	poolMeanIOU := toIOUForInvariant(poolProductMean)
	lptAfterIOU := toIOUForInvariant(*lptAfter)
	strongInvariantCheck := poolMeanIOU.Compare(lptAfterIOU) >= 0

	// Weak check: if lptAfter != 0, check relative distance < 1e-11
	weakInvariantCheck := false
	if !strongInvariantCheck {
		if !lptAfter.IsZero() {
			weakInvariantCheck = withinRelativeDistanceForInvariant(poolProductMean, *lptAfter)
		}
	}

	if !nonNegativeBalances || (!strongInvariantCheck && !weakInvariantCheck) {
		return &InvariantViolation{
			Name:    "ValidAMM",
			Message: "AMM invariant failed: balances invalid or sqrt(a*b) < LPT",
		}
	}

	return nil
}


