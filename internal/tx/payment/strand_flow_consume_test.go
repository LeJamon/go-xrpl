package payment

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// recoverFlowError runs fn and returns the flowError it panics with. It fails
// the test if fn does not panic with a flowError.
func recoverFlowError(t *testing.T, fn func()) flowError {
	t.Helper()
	var (
		fe     flowError
		caught bool
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				var ok bool
				fe, ok = r.(flowError)
				caught = ok
				if !ok {
					panic(r)
				}
			}
		}()
		fn()
	}()
	if !caught {
		t.Fatal("expected fn to panic with a flowError")
	}
	return fe
}

// TestThrowConsumeFailure_PropagatesTypedTER proves the typed-TER branch is live:
// a consume error carrying a typed ter.ResultError propagates its real code into
// the flowError, mirroring rippled's Throw<FlowException>(dr). Before the
// trustline helpers stopped flattening the TER, this branch was dead and every
// failure collapsed to tefINTERNAL.
func TestThrowConsumeFailure_PropagatesTypedTER(t *testing.T) {
	err := ter.Errorf(ter.TecPATH_DRY, "book step consume failed")
	fe := recoverFlowError(t, func() { throwConsumeFailure(err) })
	if fe.ter != ter.TecPATH_DRY {
		t.Errorf("flowError.ter = %v, want %v (typed TER must survive)", fe.ter, ter.TecPATH_DRY)
	}
}

// TestThrowConsumeFailure_UntypedMapsToInternal confirms an untyped error still
// maps to tefINTERNAL, matching rippled's Throw<FlowException>(tefINTERNAL) for
// unexpected state.
func TestThrowConsumeFailure_UntypedMapsToInternal(t *testing.T) {
	fe := recoverFlowError(t, func() { throwConsumeFailure(errors.New("unexpected")) })
	if fe.ter != ter.TefINTERNAL {
		t.Errorf("flowError.ter = %v, want %v for an untyped error", fe.ter, ter.TefINTERNAL)
	}
}
