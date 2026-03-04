package tx

// invariants_check.go — post-apply invariant checking matching rippled's InvariantCheck.cpp
//
// Called BEFORE table.Apply() so entries are still inspectable in the ApplyStateTable.
// On violation, the engine returns TecINVARIANT_FAILED (fee charged, state reverted).
//
// Reference: rippled/src/xrpld/app/tx/detail/InvariantCheck.cpp

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// InitialXRP is the total XRP supply in drops (100 billion XRP).
const InitialXRP uint64 = 100_000_000_000_000_000

// xrpCurrencyBytes is the canonical XRP currency representation (all zeros in the 20-byte currency field).
var xrpCurrencyBytes = make([]byte, 20)

// InvariantEntry represents a single ledger entry modification to be checked by invariants.
// Before is nil for newly created entries; After is nil for deleted entries.
type InvariantEntry struct {
	EntryType string // e.g. "AccountRoot", "RippleState", "Offer", "Escrow", "PayChannel"
	Before    []byte // serialized SLE before the transaction (nil for inserts)
	After     []byte // serialized SLE after the transaction (nil for deletes)
	IsDelete  bool   // true if the entry was deleted
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
// txType is the transaction type name (e.g., "Payment", "OfferCreate").
// result is the transaction result before any invariant override.
// fee is the fee in drops charged for this transaction.
// entries is the slice returned by ApplyStateTable.CollectEntries().
//
// Returns non-nil if any invariant is violated.
func CheckInvariants(txType string, result Result, fee uint64, entries []InvariantEntry) *InvariantViolation {
	checks := []func() *InvariantViolation{
		func() *InvariantViolation { return checkXRPBalances(entries) },
		func() *InvariantViolation { return checkXRPNotCreated(result, fee, entries) },
		func() *InvariantViolation { return checkAccountRootsNotDeleted(txType, result, entries) },
		func() *InvariantViolation { return checkNoXRPTrustLines(entries) },
		func() *InvariantViolation { return checkNoBadOffers(entries) },
		func() *InvariantViolation { return checkNoZeroEscrow(entries) },
		func() *InvariantViolation { return checkValidNewAccountRoot(txType, entries) },
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
		acct, err := sle.ParseAccountRoot(data)
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
				if acct, err := sle.ParseAccountRoot(e.Before); err == nil {
					before = acct.Balance
				}
			}
			if e.After != nil {
				if acct, err := sle.ParseAccountRoot(e.After); err == nil {
					after = acct.Balance
				}
			}
			netChange += int64(after) - int64(before)

		case "Escrow":
			// Escrow holds XRP in escrow — count as a balance change
			var before, after uint64
			if e.Before != nil {
				if esc, err := sle.ParseEscrow(e.Before); err == nil {
					before = esc.Amount
				}
			}
			if e.After != nil {
				if esc, err := sle.ParseEscrow(e.After); err == nil {
					after = esc.Amount
				}
			}
			netChange += int64(after) - int64(before)

		case "PayChannel":
			// PayChannel holds XRP as Amount - Balance (total minus claimed).
			// Reference: rippled InvariantCheck.cpp:107-131
			var before, after uint64
			if e.Before != nil {
				if pc, err := sle.ParsePayChannel(e.Before); err == nil {
					before = pc.Amount - pc.Balance
				}
			}
			if e.After != nil && !e.IsDelete {
				if pc, err := sle.ParsePayChannel(e.After); err == nil {
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
		rs, err := sle.ParseRippleState(e.After)
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
		esc, err := sle.ParseEscrow(e.After)
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

