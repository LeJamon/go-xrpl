package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// checkNoXRPTrustLines verifies that no RippleState (trust line) entry has XRP as
// the issue of either its LowLimit or HighLimit. rippled deliberately checks the
// limit issues "instead of relying on .native()", and inspects the after image
// of every touched trust line (for a delete, the erased SLE is the after).
// Reference: rippled InvariantCheck.cpp — NoXRPTrustLines (lines 581-610).
func checkNoXRPTrustLines(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != entry.TypeRippleState {
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
		hasXRP, err := rippleStateHasXRPTrustLimit(data)
		if err != nil {
			return &InvariantViolation{
				Name:    "NoXRPTrustLines",
				Message: fmt.Sprintf("could not parse RippleState SLE: %v", err),
			}
		}
		if hasXRP {
			return &InvariantViolation{
				Name:    "NoXRPTrustLines",
				Message: "RippleState entry uses XRP as currency (trust lines must use IOU currencies)",
			}
		}
	}
	return nil
}

func rippleStateHasXRPTrustLimit(data []byte) (bool, error) {
	entryType, err := state.DecodeType(data)
	if err != nil || entryType != entry.TypeRippleState {
		return false, fmt.Errorf("not a RippleState entry")
	}

	// Inspect the asset bytes directly: badCurrency is a deserializable IOU
	// sentinel in rippled, but the JSON-oriented amount decoder rejects it.
	var lowFound, highFound, hasXRP bool
	err = state.WalkFields(data, func(field state.Field) error {
		if field.TypeCode != 6 || (field.FieldCode != 6 && field.FieldCode != 7) {
			return nil
		}
		if field.FieldCode == 6 {
			lowFound = true
		} else {
			highFound = true
		}
		if len(field.Value) == 0 {
			return fmt.Errorf("empty trust limit amount")
		}

		if field.Value[0]&0x80 == 0 {
			if field.Value[0]&0x20 == 0 {
				hasXRP = true
			}
			return nil
		}
		if len(field.Value) != 48 {
			return fmt.Errorf("issued trust limit has width %d", len(field.Value))
		}
		currencyIsZero := true
		for _, b := range field.Value[8:28] {
			if b != 0 {
				currencyIsZero = false
				break
			}
		}
		hasXRP = hasXRP || currencyIsZero
		return nil
	})
	if err != nil {
		return false, err
	}
	if !lowFound || !highFound {
		return false, fmt.Errorf("missing required trust limit amount")
	}
	return hasXRP, nil
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
		afterType, err := state.DecodeType(e.After)
		if err != nil || afterType != entry.TypeRippleState {
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
