package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// fakeStep is a minimal Step used to drive ExecuteStrand's panic/recover paths
// and re-execution guards. Its Rev callback either accumulates an output and then
// panics (to simulate a step that updated totals before a consume failure) or runs
// revFn directly. revFn closures may inspect a captured call counter to return
// different amounts on a re-execution, which is how the EqualIn/EqualOut guards are
// driven. The optional override hooks let a test force a guard to fail without
// relying on real amount arithmetic.
type fakeStep struct {
	revFn      func(out EitherAmount) (EitherAmount, EitherAmount)
	fwdFn      func(in EitherAmount) (EitherAmount, EitherAmount)
	offersUsed uint32
	equalInFn  func(a, b EitherAmount) bool
	equalOutFn func(a, b EitherAmount) bool
	cache      *EitherAmount
}

func (s *fakeStep) Rev(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, out EitherAmount) (EitherAmount, EitherAmount) {
	in, o := s.revFn(out)
	s.cache = &o
	return in, o
}

func (s *fakeStep) Fwd(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, in EitherAmount) (EitherAmount, EitherAmount) {
	if s.fwdFn != nil {
		fin, fout := s.fwdFn(in)
		s.cache = &fout
		return fin, fout
	}
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
func (s *fakeStep) IsZero(amt EitherAmount) bool { return amt.IsZero() }
func (s *fakeStep) EqualIn(a, b EitherAmount) bool {
	if s.equalInFn != nil {
		return s.equalInFn(a, b)
	}
	return a.Compare(b) == 0
}
func (s *fakeStep) EqualOut(a, b EitherAmount) bool {
	if s.equalOutFn != nil {
		return s.equalOutFn(a, b)
	}
	return a.Compare(b) == 0
}
func (s *fakeStep) Inactive() bool                         { return false }
func (s *fakeStep) OffersUsed() uint32                     { return s.offersUsed }
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
			throwFlowError(ter.TefINTERNAL)
			return out, out // unreachable
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000), nil)

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
		ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000), nil)
	})
}

// TestExecuteStrand_DryStrandReportsOffersUsed proves that a dry strand still
// reports the offers its steps consumed. rippled's failure StrandResult ctor sets
// ofrsUsed(offersUsed(strand)) — identical to the success ctor — and flow()
// accumulates it into offersConsidered before the !success continue, so dry/failed
// strands count toward the maxOffersToConsider cap.
func TestExecuteStrand_DryStrandReportsOffersUsed(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	// Rev returns zero output -> strand is dry -> failStrand().
	step := &fakeStep{
		offersUsed: 7,
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000), nil)

	require.False(t, result.Success, "dry strand must fail")
	require.Equal(t, uint32(7), result.OffersUsed, "dry strand must report offers its steps consumed")
}

// TestExecuteStrand_FlowErrorReportsOffersUsed proves the recover path also
// propagates offersUsed: a strand that panics with flowError reports the offers its
// steps touched, matching rippled's catch returning Result{strand, ofrsToRm} whose
// ctor sets ofrsUsed(offersUsed(strand)).
func TestExecuteStrand_FlowErrorReportsOffersUsed(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	step := &fakeStep{
		offersUsed: 4,
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			throwFlowError(ter.TefINTERNAL)
			return out, out
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000), nil)

	require.False(t, result.Success, "flowError strand must fail")
	require.Equal(t, uint32(4), result.OffersUsed, "flowError strand must report offers its steps consumed")
}

// TestFlow_DryStrandOffersConsideredReachesCap proves the M3 fix at the Flow level:
// a dry strand's OffersUsed feeds offersConsidered, which (under FlowSortStrands)
// caps the payment at maxOffersToConsider (1500). With a single dry strand reporting
// 1500 offers used, Flow must stop after one pass with no liquidity delivered.
func TestFlow_DryStrandOffersConsideredReachesCap(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	step := &fakeStep{
		offersUsed: 1500,
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
		},
	}
	strands := []Strand{{step}}

	// FlowSortStrands enabled so offersConsidered >= maxOffersToConsider breaks.
	result := Flow(sandbox, strands, NewXRPEitherAmount(10_000_000), false, nil, nil, nil, true, false)

	require.True(t, result.Out.IsZero(), "dry strand delivers nothing")
	require.Equal(t, ter.TecPATH_PARTIAL, result.Result, "non-partial payment with no delivery is tecPATH_PARTIAL")
}

// TestFlow_MaxTriesFailedProcessing proves the curTry >= maxTries bail returns
// telFAILED_PROCESSING. A strand that always delivers a single drop while owing far
// more output never converges, so Flow exhausts its retry budget. Mirrors rippled
// StrandFlow.h: ++curTry; if (curTry >= maxTries) return {telFAILED_PROCESSING,...}.
func TestFlow_MaxTriesFailedProcessing(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	// Always succeeds delivering exactly 1 drop regardless of requested output.
	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			one := NewXRPEitherAmount(1)
			return one, one
		},
	}
	strands := []Strand{{step}}

	result := Flow(sandbox, strands, NewXRPEitherAmount(10_000_000), true, nil, nil, nil, false, false)

	require.Equal(t, ter.TelFAILED_PROCESSING, result.Result, "loop must bail with telFAILED_PROCESSING after maxTries")
}

// TestFlow_OverDeliveryTefException proves the actualOut > outReq guard returns
// tefEXCEPTION and discards state. A strand that delivers more than requested
// surfaces the over-delivery exactly as rippled does (rippled asserts; goXRPL
// surfaces it via tefEXCEPTION instead of clamping it away).
func TestFlow_OverDeliveryTefException(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	// Delivers 15 XRP regardless of the requested 10 XRP.
	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			over := NewXRPEitherAmount(15_000_000)
			return over, over
		},
	}
	strands := []Strand{{step}}

	result := Flow(sandbox, strands, NewXRPEitherAmount(10_000_000), false, nil, nil, nil, false, false)

	require.Equal(t, ter.TefEXCEPTION, result.Result, "over-delivery must surface tefEXCEPTION")
	require.True(t, result.Out.IsZero(), "tefEXCEPTION discards delivered output")
	require.Nil(t, result.Sandbox, "tefEXCEPTION discards the sandbox")
}

// TestExecuteStrand_FirstStepMaxInGuard drives the step-0 maxIn re-execution guard
// (strand_flow.go: after Fwd(maxIn), !EqualIn(fwdIn, maxIn) -> failStrand). The Rev
// pass exceeds maxIn, forcing the re-execute-forward branch; the re-executed step
// reports a consumed input that differs from maxIn, which rippled treats as
// "something is very wrong" and fails the strand.
func TestExecuteStrand_FirstStepMaxInGuard(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	maxIn := NewXRPEitherAmount(5_000_000)
	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			// actualIn (10M) > maxIn (5M) -> enter the maxIn re-execution branch.
			in := NewXRPEitherAmount(10_000_000)
			return in, out
		},
		fwdFn: func(in EitherAmount) (EitherAmount, EitherAmount) {
			// Re-executed forward consumes 7M != maxIn 5M -> guard fails.
			return NewXRPEitherAmount(7_000_000), NewXRPEitherAmount(8_000_000)
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, &maxIn, NewXRPEitherAmount(20_000_000), nil)

	require.False(t, result.Success, "first-step maxIn mismatch must fail the strand")
	require.True(t, result.Out.IsZero())
}

// TestExecuteStrand_LimitingStepGuard drives the limiting-step re-execution guard
// (after re-executing Rev with the limited output, !EqualOut(reOut, reStepOut) ->
// failStrand). The first Rev under-produces (limiting), and the re-execution
// produces a still-different output, which rippled treats as fatal.
func TestExecuteStrand_LimitingStepGuard(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	var revCalls int
	step := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			revCalls++
			if revCalls == 1 {
				// actualOut 5M < requested 10M -> limiting branch, reStepOut = 5M.
				return NewXRPEitherAmount(5_000_000), NewXRPEitherAmount(5_000_000)
			}
			// Re-execution produces 4M != reStepOut 5M -> guard fails.
			return NewXRPEitherAmount(4_000_000), NewXRPEitherAmount(4_000_000)
		},
	}

	result := ExecuteStrand(sandbox, Strand{step}, nil, NewXRPEitherAmount(10_000_000), nil)

	require.False(t, result.Success, "limiting-step re-execution mismatch must fail the strand")
	require.True(t, result.Out.IsZero())
}

// TestExecuteStrand_ForwardPassGuard drives the forward-pass re-execution guard
// (!EqualIn(fwdIn, stepIn) -> failStrand). A two-step strand whose first step is
// limiting runs a forward pass over the second step; the second step's Fwd reports a
// consumed input differing from what the limiting step produced, which rippled
// treats as fatal.
func TestExecuteStrand_ForwardPassGuard(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	// step0 is the limiting step: under-produces 8M for a requested 10M.
	step0 := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			return NewXRPEitherAmount(8_000_000), NewXRPEitherAmount(8_000_000)
		},
	}
	// step1 produces exactly what is requested in Rev (not limiting), but its Fwd
	// reports a mismatched consumed input -> forward-pass guard fails.
	step1 := &fakeStep{
		revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
			return out, out
		},
		fwdFn: func(in EitherAmount) (EitherAmount, EitherAmount) {
			// fwdIn 9M != stepIn 8M.
			return NewXRPEitherAmount(9_000_000), NewXRPEitherAmount(9_000_000)
		},
	}

	result := ExecuteStrand(sandbox, Strand{step0, step1}, nil, NewXRPEitherAmount(10_000_000), nil)

	require.False(t, result.Success, "forward-pass re-execution mismatch must fail the strand")
	require.True(t, result.Out.IsZero())
}
