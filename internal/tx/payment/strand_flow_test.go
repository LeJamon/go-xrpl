package payment

import (
	"testing"

	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// fakeStep is a minimal Step used to drive ExecuteStrand's panic/recover paths.
// Its Rev callback either accumulates an output and then panics (to simulate a
// step that updated totals before a consume failure) or runs revFn directly.
type fakeStep struct {
	revFn func(out EitherAmount) (EitherAmount, EitherAmount)
	cache *EitherAmount
}

func (s *fakeStep) Rev(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, out EitherAmount) (EitherAmount, EitherAmount) {
	in, o := s.revFn(out)
	s.cache = &o
	return in, o
}

func (s *fakeStep) Fwd(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, in EitherAmount) (EitherAmount, EitherAmount) {
	return in, in
}
func (s *fakeStep) CachedIn() *EitherAmount  { return s.cache }
func (s *fakeStep) CachedOut() *EitherAmount { return s.cache }
func (s *fakeStep) DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection {
	return DebtDirectionIssues
}
func (s *fakeStep) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	q := qualityFromFloat64(1.0)
	return &q, DebtDirectionIssues
}
func (s *fakeStep) GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection) {
	q := qualityFromFloat64(1.0)
	return NewCLOBLikeQualityFunction(q), DebtDirectionIssues
}
func (s *fakeStep) IsZero(amt EitherAmount) bool           { return amt.IsZero() }
func (s *fakeStep) EqualIn(a, b EitherAmount) bool         { return a.Compare(b) == 0 }
func (s *fakeStep) EqualOut(a, b EitherAmount) bool        { return a.Compare(b) == 0 }
func (s *fakeStep) Inactive() bool                         { return false }
func (s *fakeStep) OffersUsed() uint32                     { return 0 }
func (s *fakeStep) DirectStepAccts() *[2][20]byte          { return nil }
func (s *fakeStep) BookStepBook() *Book                    { return nil }
func (s *fakeStep) LineQualityIn(v *PaymentSandbox) uint32 { return QualityOne }
func (s *fakeStep) ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount) {
	if s.cache == nil {
		return false, ZeroXRPEitherAmount()
	}
	return true, *s.cache
}

// TestExecuteStrand_FlowErrorDiscardsPhantomTotals proves that when a step fails
// to consume (rippled throws FlowException; we panic with flowError), ExecuteStrand
// discards any partially-accumulated totals and fails the whole strand — it never
// returns amounts that include a transfer that did not actually happen.
//
// This is the unit-level analogue of a failed offer consumption in BookStep.Rev:
// rippled updates totals/remainingOut before consumeOffer, so a naive "return
// false" would cache and return phantom liquidity. The fix panics through the
// in-flight pass and the recover discards it.
func TestExecuteStrand_FlowErrorDiscardsPhantomTotals(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			// Simulate a step that has already computed (and would have cached)
			// a non-zero output, then hits a failed consume mid-pass.
			throwFlowError(tx.TefINTERNAL)
			return out, out // unreachable
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000))

	require.False(t, result.Success, "strand must fail on flowError, not return phantom totals")
	require.True(t, result.Out.IsZero(), "failed strand must report zero output")
	require.True(t, result.In.IsZero(), "failed strand must report zero input")
	require.Nil(t, result.Sandbox, "failed strand must not carry a sandbox")
}

// TestExecuteStrand_NonFlowErrorPanicPropagates proves that a panic which is NOT
// a flowError (i.e. a genuine bug) is re-panicked out of ExecuteStrand rather than
// being silently swallowed into a "dry strand" result. rippled only catches
// FlowException; any other exception is fatal.
func TestExecuteStrand_NonFlowErrorPanicPropagates(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			panic("genuine bug, not a flowError")
		},
	}

	require.PanicsWithValue(t, "genuine bug, not a flowError", func() {
		ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000))
	})
}
