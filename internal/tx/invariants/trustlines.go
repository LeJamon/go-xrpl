package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// checkNoXRPTrustLines verifies that no RippleState (trust line) entry has XRP as
// the issue of either its LowLimit or HighLimit. rippled deliberately checks the
// limit issues "instead of relying on .native()", and inspects the after image
// of every touched trust line (for a delete, the erased SLE is the after).
// Reference: rippled InvariantCheck.cpp — NoXRPTrustLines (lines 581-610).
func checkNoXRPTrustLines(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "RippleState" {
			continue
		}
		// rippled uses the "after" image, which for a delete is the erased SLE;
		// CollectEntries leaves that in Before with After nil.
		data := e.After
		if data == nil {
			data = e.Before
		}
		if data == nil {
			continue
		}
		rs, err := state.ParseRippleState(data)
		if err != nil {
			return &InvariantViolation{
				Name:    "NoXRPTrustLines",
				Message: fmt.Sprintf("could not parse RippleState SLE: %v", err),
			}
		}
		// rippled fires only on issue() == xrpIssue() — the all-zero currency; a
		// badCurrency limit does not trip this invariant (it is rejected at
		// TrustSet preflight with temBAD_CURRENCY and never reaches a ledger).
		if isNativeXRPCurrency(rs.LowLimit.Currency) || isNativeXRPCurrency(rs.HighLimit.Currency) {
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
		// Confirm the type from the after data, matching rippled which checks
		// after->getType() == ltRIPPLE_STATE.
		afterType := state.EntryType(e.After)
		if afterType != "RippleState" {
			continue
		}

		rs, err := state.ParseRippleState(e.After)
		if err != nil {
			return &InvariantViolation{
				Name:    "NoDeepFreezeTrustLinesWithoutFreeze",
				Message: fmt.Sprintf("could not parse RippleState SLE: %v", err),
			}
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
