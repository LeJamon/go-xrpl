package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// checkNoXRPTrustLines verifies that no RippleState (trust line) entry has XRP as
// the issue of either its LowLimit or HighLimit. rippled deliberately checks the
// limit issues "instead of relying on .native()", and inspects the after image
// of every touched trust line (for a delete, the erased SLE is the after).
// Reference: rippled InvariantCheck.cpp — NoXRPTrustLines (lines 581-610).
func checkNoXRPTrustLines(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	violations := newViolationSet(rules)
	for _, e := range entries {
		if e.EntryType != entry.TypeRippleState {
			continue
		}
		data := e.finalImage()
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
		var v *InvariantViolation
		if hasXRP {
			v = &InvariantViolation{
				Name:    "NoXRPTrustLines",
				Message: "RippleState entry uses XRP as currency (trust lines must use IOU currencies)",
			}
		}
		violations.assign(v)
	}
	return violations.found
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
func checkNoDeepFreezeTrustLinesWithoutFreeze(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	violations := newViolationSet(rules)
	for _, e := range entries {
		data := e.finalImage()
		if data == nil {
			continue
		}
		if afterType, err := state.DecodeType(data); err != nil || afterType != entry.TypeRippleState {
			continue
		}

		rs, err := e.rippleState(data)
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

		var v *InvariantViolation
		if (lowDeepFreeze && !lowFreeze) || (highDeepFreeze && !highFreeze) {
			v = &InvariantViolation{
				Name:    "NoDeepFreezeTrustLinesWithoutFreeze",
				Message: "a trust line with deep freeze flag without normal freeze was created",
			}
		}
		violations.assign(v)
	}
	return violations.found
}
