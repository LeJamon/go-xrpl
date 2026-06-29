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

// strandPermRemovals unions every BookStep's unconditional ("perm") removals in
// the strand. These are rippled's FlowOfferStream::permToRemove offers
// (self-crossed, authorization-failed, expired, deep-frozen, domain-removed,
// found-unfunded, and found-tiny). Only this perm subset propagates out of the
// flow as removableOffers; "became unfunded" / "became tiny" offers are deleted
// in-band from the working sandbox but are not returned for re-erasure on a
// discarded (FillOrKill/IoC-kill) crossing. Reference: rippled OfferStream.h
// permToRemove_; StrandFlow.h ofrsToRmOnFail = union of strand f.ofrsToRm.
func strandPermRemovals(strand Strand, dst map[[32]byte]bool) {
	for _, step := range strand {
		bs, ok := step.(*BookStep)
		if !ok {
			continue
		}
		for k := range bs.PermRemovals() {
			dst[k] = true
		}
	}
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
			// Keep unconditional ("perm") removals even on a flow exception —
			// rippled's permToRemove survives "even if the strand is not
			// applied". Reference: rippled OfferStream.h permToRemove_.
			for _, step := range strand {
				if bs, ok := step.(*BookStep); ok {
					for k := range bs.PermRemovals() {
						ofrsToRm[k] = true
					}
				}
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

	// restorePermRemovals unions every BookStep's unconditional ("perm")
	// removals back into ofrsToRm. These are rippled's FlowOfferStream
	// permToRemove offers (self-crossed, authorization-failed, expired,
	// deep-frozen, domain-removed, found-unfunded, found-tiny) which survive "even
	// if the strand is not applied" and are never rolled back by a limiting-step
	// reset. It guarantees ofrsToRm carries the full perm set on the failure and
	// success exits even though the per-step writes already accumulate it, matching
	// rippled's monotonic permToRemove. Reference: rippled OfferStream.h
	// permToRemove_; StrandFlow.h (ofrsToRm is passed by reference into every
	// rev/fwd and never reset).
	restorePermRemovals := func() {
		for _, step := range strand {
			bs, ok := step.(*BookStep)
			if !ok {
				continue
			}
			for k := range bs.PermRemovals() {
				ofrsToRm[k] = true
			}
		}
	}

	// failStrand returns the failed-strand result rippled produces when it
	// returns Result{strand, ofrsToRm}: success=false, zero in/out, the
	// offers-to-remove preserved, and inactive. Used for dry strands and for
	// the re-execution consistency guards below.
	failStrand := func() StrandResult {
		restorePermRemovals()
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
	// afView ("all funds" view) is a fresh child of the RUNNING sandbox, exactly
	// like rippled's afView(&baseView) (StrandFlow.h:135), where baseView is the
	// running multi-strand sandbox. It therefore reflects every prior committed
	// iteration (best-strand applies AND the per-iteration offerDeletes), so an
	// offer whose owner a PRIOR iteration drained reads unfunded here too. The
	// found-vs-became test then classifies it FOUND-unfunded and promotes it to a
	// perm removal that the multi-strand loop offerDeletes from the running view —
	// matching rippled. Anchoring afView to a pristine pre-flow baseline instead
	// kept such an offer classified BECAME (a discardable working-sandbox delete)
	// and left it in the book across a multi-iteration crossing (the 99243845
	// beyond-limit drained-maker offer that #1118 regressed).
	afView := NewChildSandbox(baseView)
	var limitStepOut EitherAmount

	// === REVERSE PASS ===
	// Single pass backwards with inline resets when limiting steps are found.
	// Reference: rippled StrandFlow.h lines 138-221
	stepOut := requestedOut

	// ofrsToRm now holds only perm removals (rippled's permToRemove_): every offer
	// the grooming rule adds here is paired with recordPermRm, while a BECAME
	// removal is a working-sandbox delete that the sb.Reset() below rolls back on a
	// limiting step. So, like rippled's monotonic permToRemove_, this set is never
	// discarded on a reset — a perm removal an over-walk produced is genuinely
	// removable (already unfunded/tiny in the pristine view) and survives even when
	// the strand is reset or fails. Reference: rippled OfferStream.h permToRemove_.
	for i := s - 1; i >= 0; i-- {
		step := strand[i]

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
			// Reference: rippled StrandFlow.h lines 180-217. rippled resets BOTH sb
			// (184) AND afView (185) on this branch (unlike the maxIn branch, which
			// resets only sb at 152), so afView is re-rooted on the running sandbox.
			sb.Reset()
			afView.Reset()
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

	restorePermRemovals()

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
