package payment

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

	// Recover from panics (FlowException / overflow equivalents).
	// In rippled, the entire strand execution is wrapped in try/catch for
	// FlowException. Panics from IOUAmount overflow or other arithmetic
	// errors serve the same role. When a panic occurs, return an empty
	// result (no output) matching rippled's catch behavior.
	// Reference: rippled StrandFlow.h flow() try/catch lines 126-298
	defer func() {
		if r := recover(); r != nil {
			result = StrandResult{
				Success:  false,
				In:       ZeroXRPEitherAmount(),
				Out:      ZeroXRPEitherAmount(),
				Sandbox:  nil,
				OffsToRm: ofrsToRm,
				Inactive: true,
			}
		}
	}()

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

	for i := s - 1; i >= 0; i-- {
		step := strand[i]

		actualIn, actualOut := step.Rev(sb, afView, ofrsToRm, stepOut)

		// Check if output is zero → strand is dry
		if step.IsZero(actualOut) {
			return StrandResult{
				Success:  false,
				In:       ZeroXRPEitherAmount(),
				Out:      ZeroXRPEitherAmount(),
				Sandbox:  nil,
				OffsToRm: ofrsToRm,
				Inactive: true,
			}
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
				return StrandResult{
					Success:  false,
					In:       ZeroXRPEitherAmount(),
					Out:      ZeroXRPEitherAmount(),
					Sandbox:  nil,
					OffsToRm: ofrsToRm,
					Inactive: true,
				}
			}

			// stepOut is not used after this (loop ends at i=0)
			_ = fwdIn
		} else if !step.EqualOut(actualOut, stepOut) {
			// Limiting step found — actualOut < requested stepOut
			// Reset BOTH sandboxes and re-execute ONLY this step
			// Reference: rippled StrandFlow.h lines 180-217
			sb.Reset()
			limitingStep = i

			// Re-execute with the limited output
			reStepOut := actualOut
			reIn, reOut := step.Rev(sb, afView, ofrsToRm, reStepOut)
			limitStepOut = reOut

			if step.IsZero(reOut) {
				return StrandResult{
					Success:  false,
					In:       ZeroXRPEitherAmount(),
					Out:      ZeroXRPEitherAmount(),
					Sandbox:  nil,
					OffsToRm: ofrsToRm,
					Inactive: true,
				}
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
				return StrandResult{
					Success:  false,
					In:       ZeroXRPEitherAmount(),
					Out:      ZeroXRPEitherAmount(),
					Sandbox:  nil,
					OffsToRm: ofrsToRm,
					Inactive: true,
				}
			}

			_ = fwdIn
			stepIn = fwdOut
		}
	}

	// Get final results from cached values
	strandIn := strand[0].CachedIn()
	strandOut := strand[s-1].CachedOut()

	if strandIn == nil || strandOut == nil {
		return StrandResult{
			Success:  false,
			In:       ZeroXRPEitherAmount(),
			Out:      ZeroXRPEitherAmount(),
			Sandbox:  nil,
			OffsToRm: ofrsToRm,
			Inactive: true,
		}
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

// ExecuteStrandReverse is a helper that only executes the reverse pass.
// Useful for quality estimation.
func ExecuteStrandReverse(
	view *PaymentSandbox,
	strand Strand,
	requestedOut EitherAmount,
) (EitherAmount, EitherAmount) {
	if len(strand) == 0 {
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
	}

	// Create sandbox for execution
	sb := NewChildSandbox(view)
	ofrsToRm := make(map[[32]byte]bool)

	// Work backwards
	out := requestedOut
	for i := len(strand) - 1; i >= 0; i-- {
		step := strand[i]
		actualIn, _ := step.Rev(sb, view, ofrsToRm, out)
		out = actualIn
	}

	// Return first step input and last step output
	firstCachedIn := strand[0].CachedIn()
	lastCachedOut := strand[len(strand)-1].CachedOut()

	if firstCachedIn == nil || lastCachedOut == nil {
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount()
	}

	return *firstCachedIn, *lastCachedOut
}

// StrandIterator allows iterating over strands for the flow algorithm
type StrandIterator struct {
	strands     []Strand
	results     []StrandResult
	activeIndex int
}

// NewStrandIterator creates an iterator over strands
func NewStrandIterator(strands []Strand) *StrandIterator {
	return &StrandIterator{
		strands:     strands,
		results:     make([]StrandResult, len(strands)),
		activeIndex: 0,
	}
}

// HasNext returns true if there are more strands to process
func (it *StrandIterator) HasNext() bool {
	return it.activeIndex < len(it.strands)
}

// Next returns the next strand and advances the iterator
func (it *StrandIterator) Next() (Strand, int) {
	if it.activeIndex >= len(it.strands) {
		return nil, -1
	}
	strand := it.strands[it.activeIndex]
	index := it.activeIndex
	it.activeIndex++
	return strand, index
}

// SetResult stores the result for a strand
func (it *StrandIterator) SetResult(index int, result StrandResult) {
	if index >= 0 && index < len(it.results) {
		it.results[index] = result
	}
}

// GetResult retrieves the result for a strand
func (it *StrandIterator) GetResult(index int) StrandResult {
	if index >= 0 && index < len(it.results) {
		return it.results[index]
	}
	return StrandResult{}
}

// Reset resets the iterator to the beginning
func (it *StrandIterator) Reset() {
	it.activeIndex = 0
}
