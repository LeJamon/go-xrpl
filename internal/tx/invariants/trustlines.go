package invariants

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

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
