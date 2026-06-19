package payment

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// flowError is the typed panic value used to abort strand execution when a step
// fails to move funds. It mirrors rippled's FlowException, which BookStep throws
// from consumeOffer when an offer.send() returns a non-tesSUCCESS TER. The panic
// unwinds the in-flight reverse/forward pass (whose totals may already include the
// unconsumed transfer) and is caught in ExecuteStrand, which discards those totals
// and fails the whole strand — exactly as rippled's flow() catch does.
//
// Reference: rippled Steps.h FlowException + StrandFlow.h flow() catch (lines 295-298).
type flowError struct {
	ter ter.Result
}

// throwFlowError panics with a flowError carrying the given TER. The TER is the
// failed transfer's result code, matching rippled's Throw<FlowException>(dr).
func throwFlowError(ter ter.Result) {
	panic(flowError{ter: ter})
}

// throwConsumeFailure panics with a flowError after a consumeOffer/consumeAMMOffer
// call fails, mirroring rippled BookStep::consumeOffer where a non-tesSUCCESS
// offer.send() result is re-thrown as Throw<FlowException>(dr). If the consume
// error carries a typed TER (a *tx.ResultError) we propagate it; otherwise the
// failure is an unexpected internal error and maps to tefINTERNAL, matching
// rippled's Throw<FlowException>(tefINTERNAL) for unexpected state.
func throwConsumeFailure(err error) {
	if re, ok := ter.AsResultError(err); ok {
		throwFlowError(re.Code)
	}
	throwFlowError(ter.TefINTERNAL)
}

// strandOffersUsed sums the offers consumed by every step in the strand,
// mirroring rippled's offersUsed(Strand const&) free function (Steps.h). Both
// the success and failure StrandResult constructors set ofrsUsed to this value,
// so failed and dry strands report the offers they touched too. flow() then
// accumulates it into offersConsidered regardless of strand success, which feeds
// the maxOffersToConsider cap.
func strandOffersUsed(strand Strand) uint32 {
	var n uint32
	for _, step := range strand {
		n += step.OffersUsed()
	}
	return n
}

// ExecuteStrand executes a strand using the two-pass algorithm matching rippled's
// StrandFlow.h flow() function.
//
// The algorithm (single reverse pass with inline resets):
//  1. Work backwards from desired output, calling Rev() on each step
//  2. When a step limits (actualOut < requestedOut), IMMEDIATELY reset sandbox,
//     re-execute ONLY that step, then continue backwards
//  3. When step 0 exceeds maxIn, reset sandbox, re-execute step 0 with Fwd(maxIn)
//  4. Forward pass runs from limitingStep+1 to end
//
// This inline-reset approach is critical because steps may create side effects
// (e.g., trust lines that increase reserves) that would poison earlier steps
// if not reset.
//
// Reference: rippled/src/xrpld/app/paths/detail/StrandFlow.h flow()
func ExecuteStrand(
	baseView *PaymentSandbox,
	strand Strand,
	maxIn *EitherAmount,
	requestedOut EitherAmount,
) (result StrandResult) {
	if len(strand) == 0 {
		return StrandResult{
			Success:  false,
			In:       ZeroXRPEitherAmount(),
			Out:      ZeroXRPEitherAmount(),
			Sandbox:  nil,
			OffsToRm: nil,
			Inactive: true,
		}
	}

	s := len(strand)
	ofrsToRm := make(map[[32]byte]bool)

	// Recover only from flowError — the typed panic a step raises when it fails
	// to move funds (rippled's FlowException). On a flowError we discard any
	// partially-accumulated totals and fail the whole strand, matching rippled's
	// flow() catch (StrandFlow.h:295-298): it returns Result{strand, ofrsToRm},
	// the success=false strand result with zero in/out and the offers-to-remove
	// preserved. The FlowException's TER is not propagated to the strand result;
	// Flow() simply treats the strand as dry (it filters on !success || out==0).
	//
	// Any other panic value is a genuine bug (nil deref, arithmetic overflow,
	// etc.) and is re-panicked so it crashes loudly rather than being silently
	// swallowed into a "dry strand".
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(flowError); !ok {
				panic(r)
			}
			result = StrandResult{
				Success:    false,
				In:         ZeroXRPEitherAmount(),
				Out:        ZeroXRPEitherAmount(),
				Sandbox:    nil,
				OffsToRm:   ofrsToRm,
				OffersUsed: strandOffersUsed(strand),
				Inactive:   true,
			}
		}
	}()

	// failStrand returns the failed-strand result rippled produces when it
	// returns Result{strand, ofrsToRm}: success=false, zero in/out, the
	// offers-to-remove preserved, and inactive. Used for dry strands and for
	// the re-execution consistency guards below.
	failStrand := func() StrandResult {
		return StrandResult{
			Success:    false,
			In:         ZeroXRPEitherAmount(),
			Out:        ZeroXRPEitherAmount(),
			Sandbox:    nil,
			OffsToRm:   ofrsToRm,
			OffersUsed: strandOffersUsed(strand),
			Inactive:   true,
		}
	}

	// limitingStep initialized to s (= no limiting step found)
	// Reference: rippled StrandFlow.h line 130: size_t limitingStep = strand.size()
	limitingStep := s
	sb := NewChildSandbox(baseView)
	// afView: "all funds" view — determines if offers are unfunded
	// In rippled, this is a separate child of baseView that can be reset.
	// We use baseView directly since we never modify it.
	afView := baseView
	var limitStepOut EitherAmount

	// === REVERSE PASS ===
	// Single pass backwards with inline resets when limiting steps are found.
	// Reference: rippled StrandFlow.h lines 138-221
	stepOut := requestedOut

	// deferredDiscard maps each offer a limiting step removed during its
	// over-extended reverse walk to the strand index of that step. Whether the
	// removal survives depends on what ultimately limited the strand:
	//   - if a step closer to the input (lower index) limited further
	//     (limitingStep < recorded index), the over-walk requested more output
	//     than the input could buy, so those extra offers were never really
	//     reached — their removals are spurious and stay discarded.
	//   - otherwise the recording step is itself the final limiting step: the
	//     offers were genuinely reached and their owners drained by the actual
	//     cross, so rippled removes them — the removals must be restored.
	deferredDiscard := make(map[[32]byte]int)

	for i := s - 1; i >= 0; i-- {
		step := strand[i]

		// Snapshot the offers-to-remove set before this step's (possibly
		// over-extended) reverse walk, so a limiting-step reset can drop the
		// removals the over-walk added; see maxInCapped/deferredDiscard above for
		// when those are later restored.
		rmBefore := make(map[[32]byte]bool, len(ofrsToRm))
		for k := range ofrsToRm {
			rmBefore[k] = true
		}
		restoreRmAfterReset := func() {
			for k := range ofrsToRm {
				if !rmBefore[k] {
					delete(ofrsToRm, k)
				}
			}
		}

		actualIn, actualOut := step.Rev(sb, afView, ofrsToRm, stepOut)

		// Check if output is zero → strand is dry
		if step.IsZero(actualOut) {
			return failStrand()
		}

		if i == 0 && maxIn != nil && maxIn.Compare(actualIn) < 0 {
			// Step 0 exceeded maxIn
			// Reset sandbox and re-execute step 0 with Fwd(maxIn)
			// Reference: rippled StrandFlow.h lines 148-178
			sb.Reset()
			restoreRmAfterReset()
			limitingStep = 0

			fwdIn, fwdOut := step.Fwd(sb, afView, ofrsToRm, *maxIn)
			limitStepOut = fwdOut

			if step.IsZero(fwdOut) {
				return failStrand()
			}

			// Throwing out the sandbox can only increase liquidity, yet if the
			// re-executed first step still does not consume exactly maxIn then
			// something is very wrong — fail the strand.
			// Reference: rippled StrandFlow.h:165-178
			if !step.EqualIn(fwdIn, *maxIn) {
				return failStrand()
			}

			// stepOut is not used after this (loop ends at i=0)
		} else if !step.EqualOut(actualOut, stepOut) {
			// Limiting step found — actualOut < requested stepOut
			// Reset BOTH sandboxes and re-execute ONLY this step
			// Reference: rippled StrandFlow.h lines 180-217
			sb.Reset()
			for k := range ofrsToRm {
				if !rmBefore[k] {
					deferredDiscard[k] = i
				}
			}
			restoreRmAfterReset()
			limitingStep = i

			// Re-execute with the limited output
			reStepOut := actualOut
			reIn, reOut := step.Rev(sb, afView, ofrsToRm, reStepOut)
			limitStepOut = reOut

			if step.IsZero(reOut) {
				// A tiny input amount can cause this step to output zero
				// (e.g. 10^-80 IOU into an IOU -> XRP offer).
				return failStrand()
			}

			// Throwing out the sandbox can only increase liquidity, yet if the
			// re-executed limiting step still does not produce the limited
			// output then something is very wrong — fail the strand.
			// Reference: rippled StrandFlow.h:200-216
			if !step.EqualOut(reOut, reStepOut) {
				return failStrand()
			}

			// Continue backwards with the re-executed input
			stepOut = reIn
		} else {
			// Not limiting — continue to previous step
			stepOut = actualIn
		}
	}

	// Restore a deferred over-walk removal only when its recording step is the
	// final limiting step (no lower-index step reduced the throughput further).
	// If a step closer to the input limited more (limitingStep < idx), the cross
	// never actually reached that offer, so the removal stays discarded.
	for k, idx := range deferredDiscard {
		if limitingStep >= idx {
			ofrsToRm[k] = true
		}
	}

	// === FORWARD PASS ===
	// Execute from limitingStep+1 to end using Fwd()
	// Reference: rippled StrandFlow.h lines 224-254
	if limitingStep < s {
		stepIn := limitStepOut
		for i := limitingStep + 1; i < s; i++ {
			step := strand[i]

			fwdIn, fwdOut := step.Fwd(sb, afView, ofrsToRm, stepIn)

			if step.IsZero(fwdOut) {
				// A tiny input amount can cause this step to output zero.
				return failStrand()
			}

			// The limits should already have been found, so executing forward
			// from the limiting step should not find a new limit. If the input
			// consumed differs from what the previous step produced, something
			// is wrong — fail the strand.
			// Reference: rippled StrandFlow.h:236-252
			if !step.EqualIn(fwdIn, stepIn) {
				return failStrand()
			}

			stepIn = fwdOut
		}
	}

	// Get final results from cached values
	strandIn := strand[0].CachedIn()
	strandOut := strand[s-1].CachedOut()

	if strandIn == nil || strandOut == nil {
		return failStrand()
	}

	// Calculate totals
	var offersUsed uint32
	inactive := false
	for _, step := range strand {
		offersUsed += step.OffersUsed()
		if step.Inactive() {
			inactive = true
		}
	}

	return StrandResult{
		Success:    true,
		In:         *strandIn,
		Out:        *strandOut,
		Sandbox:    sb,
		OffsToRm:   ofrsToRm,
		OffersUsed: offersUsed,
		Inactive:   inactive,
	}
}
