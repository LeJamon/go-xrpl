package payment

import (
	"sort"

	"github.com/LeJamon/goXRPLd/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// Flow executes payment across multiple strands, selecting the best quality paths.
//
// The algorithm matches rippled's StrandFlow.h flow() function:
//
// With FlowSortStrands enabled:
//  1. Each iteration re-sorts active strands by quality upper bound (best first)
//  2. Execute strands in order; take the FIRST successful strand (break inner loop)
//  3. Track total offers considered across ALL strands and iterations
//  4. Stop when total offers >= 1500 (maxOffersToConsider)
//
// Without FlowSortStrands:
//  1. Execute ALL active strands each iteration
//  2. Pick the strand with the best actual quality
//  3. No total offer limit (per-BookStep limit of 1000 still applies)
//
// Parameters:
//   - baseView: PaymentSandbox with ledger state
//   - strands: List of executable strands
//   - outReq: Requested output amount
//   - partialPayment: Whether partial payments are allowed
//   - limitQuality: Optional quality limit (nil means no limit)
//   - sendMax: Optional maximum input amount
//   - flowSortStrands: Whether the FlowSortStrands amendment is enabled
//
// Returns: FlowResult with actual amounts and state changes
func Flow(
	baseView *PaymentSandbox,
	strands []Strand,
	outReq EitherAmount,
	partialPayment bool,
	limitQuality *Quality,
	sendMax *EitherAmount,
	ammCtx *AMMContext,
	flowSortStrands ...bool,
) FlowResult {
	sortStrands := false
	if len(flowSortStrands) > 0 {
		sortStrands = flowSortStrands[0]
	}
	if len(strands) == 0 {
		return FlowResult{
			In:              ZeroXRPEitherAmount(),
			Out:             ZeroXRPEitherAmount(),
			Sandbox:         nil,
			RemovableOffers: nil,
			Result:          tx.TecPATH_DRY,
		}
	}

	// Create the main sandbox that accumulates all changes
	accumSandbox := NewChildSandbox(baseView)
	allOfrsToRm := make(map[[32]byte]bool)

	// Initialize result accumulators
	var totalIn, totalOut EitherAmount
	if outReq.IsNative {
		totalOut = ZeroXRPEitherAmount()
	} else {
		totalOut = ZeroIOUEitherAmount(outReq.IOU.Currency, outReq.IOU.Issuer)
	}
	if sendMax != nil {
		if sendMax.IsNative {
			totalIn = ZeroXRPEitherAmount()
		} else {
			totalIn = ZeroIOUEitherAmount(sendMax.IOU.Currency, sendMax.IOU.Issuer)
		}
	} else {
		totalIn = ZeroXRPEitherAmount()
	}

	// Track remaining output needed
	remainingOut := outReq

	// Track remaining input available (if sendMax specified)
	var remainingIn *EitherAmount
	if sendMax != nil {
		ri := *sendMax
		remainingIn = &ri
	}

	// ActiveStrands: next holds strands to be activated on next iteration.
	// cur holds strands being evaluated this iteration.
	// Reference: rippled StrandFlow.h ActiveStrands class
	next := make([]*Strand, 0, len(strands))
	for i := range strands {
		next = append(next, &strands[i])
	}

	const maxTries = 1000
	var maxOffersToConsider uint32 = 1500
	var offersConsidered uint32

	// Saved amounts for precision: sum from smallest to largest
	// Reference: rippled uses flat_multiset for savedIns/savedOuts
	var savedIns []EitherAmount
	var savedOuts []EitherAmount

	for curTry := uint32(0); curTry < maxTries; curTry++ {
		if remainingOut.IsZero() {
			break
		}
		if remainingIn != nil && (remainingIn.IsNegative() || remainingIn.IsZero()) {
			break
		}
		// activateNext: move next -> cur, optionally re-sorting by quality
		// Reference: rippled ActiveStrands::activateNext()
		var cur []*Strand
		if sortStrands && len(next) > 1 {
			// Re-sort strands by quality upper bound (higher quality = better = first)
			type strandQ struct {
				strand  *Strand
				quality Quality
			}
			var strandQuals []strandQ
			for _, s := range next {
				if s == nil {
					continue
				}
				q := GetStrandQuality(*s, accumSandbox)
				if q == nil {
					continue
				}
				// Filter by limitQuality
				if limitQuality != nil && q.WorseThan(*limitQuality) {
					continue
				}
				strandQuals = append(strandQuals, strandQ{strand: s, quality: *q})
			}
			// Stable sort by quality (better first)
			sort.SliceStable(strandQuals, func(i, j int) bool {
				return strandQuals[i].quality.BetterThan(strandQuals[j].quality)
			})
			cur = make([]*Strand, 0, len(strandQuals))
			for _, sq := range strandQuals {
				cur = append(cur, sq.strand)
			}
		} else {
			cur = next
		}
		next = make([]*Strand, 0, len(cur))

		if len(cur) == 0 {
			break
		}

		// Update AMMContext multiPath for this iteration
		// Reference: rippled StrandFlow.h line 654: ammContext.setMultiPath(activeStrands.size() > 1)
		if ammCtx != nil {
			ammCtx.SetMultiPath(len(cur) > 1)
		}

		// Limit output if one strand and limitQuality is set.
		// This reduces the output to generate exact requested limitQuality
		// when the path contains AMM (non-constant quality).
		// Reference: rippled StrandFlow.h lines 656-662
		limitRemainingOut := remainingOut
		if len(cur) == 1 && limitQuality != nil && cur[0] != nil {
			limitRemainingOut = limitOut(accumSandbox, *cur[0], remainingOut, *limitQuality)
		}
		adjustedRemOut := limitRemainingOut.Compare(remainingOut) != 0

		// Collect offers to remove from ALL strands in this iteration
		iterOfrsToRm := make(map[[32]byte]bool)

		type bestStrand struct {
			in      EitherAmount
			out     EitherAmount
			sandbox *PaymentSandbox
			quality Quality
		}
		var best *bestStrand
		var markInactiveOnUse int = -1

		for strandIndex := 0; strandIndex < len(cur); strandIndex++ {
			strand := cur[strandIndex]
			if strand == nil {
				continue
			}

			// For offer crossing with quality limit (without FlowSortStrands),
			// check strand quality upper bound
			// Reference: rippled StrandFlow.h lines 688-692
			if !sortStrands && limitQuality != nil {
				strandQ := GetStrandQuality(*strand, accumSandbox)
				if strandQ == nil || strandQ.WorseThan(*limitQuality) {
					continue
				}
			}

			// Clear AMM liquidity used flag before each strand attempt.
			// Reference: rippled StrandFlow.h line 687: ammContext.clear()
			if ammCtx != nil {
				ammCtx.Clear()
			}

			// Execute this strand with the potentially limited output.
			// Reference: rippled StrandFlow.h line 694: flow(sb, *strand, remainingIn, limitRemainingOut, j)
			result := ExecuteStrand(accumSandbox, *strand, remainingIn, limitRemainingOut)

			// Collect offers to remove from ALL strands (even failed ones)
			for k, v := range result.OffsToRm {
				iterOfrsToRm[k] = v
			}

			// Track total offers considered across ALL strands
			offersConsidered += result.OffersUsed

			if !result.Success || result.Out.IsZero() {
				continue
			}

			// Calculate actual quality
			q := QualityFromAmounts(result.In, result.Out)

			// Check quality limit.
			// limitOut() finds output to generate exact requested limitQuality.
			// But the actual limit quality might be slightly off due to round off.
			// Reference: rippled StrandFlow.h lines 720-731
			if limitQuality != nil && q.WorseThan(*limitQuality) {
				if !adjustedRemOut || !WithinRelativeDistance(q, *limitQuality, 1e-7) {
					continue
				}
			}

			if sortStrands {
				// FlowSortStrands: take the FIRST successful strand, then break
				// Reference: rippled StrandFlow.h lines 733-741
				if !result.Inactive {
					next = append(next, strand)
				}
				best = &bestStrand{
					in:      result.In,
					out:     result.Out,
					sandbox: result.Sandbox,
					quality: q,
				}
				// Push remaining strands to next
				for ri := strandIndex + 1; ri < len(cur); ri++ {
					next = append(next, cur[ri])
				}
				break
			}

			// Without FlowSortStrands: evaluate all strands, keep best
			// Reference: rippled StrandFlow.h lines 743-765
			next = append(next, strand)

			if best == nil || q.BetterThan(best.quality) ||
				(q.Value == best.quality.Value && result.Out.Compare(best.out) > 0) {
				if result.Inactive {
					// Mark for removal if this ends up being best
					markInactiveOnUse = len(next) - 1
				} else {
					markInactiveOnUse = -1
				}
				best = &bestStrand{
					in:      result.In,
					out:     result.Out,
					sandbox: result.Sandbox,
					quality: q,
				}
			}
		}

		// Determine if we should break after this iteration
		shouldBreak := false
		if sortStrands {
			shouldBreak = best == nil || offersConsidered >= maxOffersToConsider
		} else {
			shouldBreak = best == nil
		}
		if best != nil {
			// Remove inactive strand from next if it was the best
			if markInactiveOnUse >= 0 && markInactiveOnUse < len(next) {
				next = append(next[:markInactiveOnUse], next[markInactiveOnUse+1:]...)
				markInactiveOnUse = -1
			}

			savedIns = append(savedIns, best.in)
			savedOuts = append(savedOuts, best.out)

			// Recalculate remaining from totals for precision
			// Reference: rippled uses sum(savedOuts) and sum(savedIns)
			totalOut = sumAmounts(savedOuts)
			totalIn = sumAmounts(savedIns)
			remainingOut = outReq.Sub(totalOut)
			if remainingOut.IsNegative() {
				if outReq.IsNative {
					remainingOut = ZeroXRPEitherAmount()
				} else {
					remainingOut = ZeroIOUEitherAmount(outReq.IOU.Currency, outReq.IOU.Issuer)
				}
			}
			if sendMax != nil {
				ri := sendMax.Sub(totalIn)
				remainingIn = &ri
			}

			// Apply the best strand's sandbox changes
			if best.sandbox != nil {
				best.sandbox.Apply(accumSandbox)
			}
			// Update AMM iteration counter
			// Reference: rippled StrandFlow.h line 798: ammContext.update()
			if ammCtx != nil {
				ammCtx.Update()
			}
		}

		// Delete removable offers from the accumulating sandbox
		if len(iterOfrsToRm) > 0 {
			for k := range iterOfrsToRm {
				allOfrsToRm[k] = true
			}
			for k := range iterOfrsToRm {
				offerDeleteInSandbox(accumSandbox, k)
			}
		}

		if shouldBreak {
			break
		}
	}

	// Determine final result code
	// Reference: rippled StrandFlow.h lines 853-872:
	//   if (!partialPayment) → tecPATH_PARTIAL (couldn't deliver full amount)
	//   if (partialPayment && actualOut == 0) → tecPATH_DRY (delivered nothing)
	//   if (partialPayment && actualOut > 0) → success (partial delivery)
	resultCode := tx.TesSUCCESS

	if totalOut.Compare(outReq) < 0 {
		if !partialPayment {
			resultCode = tx.TecPATH_PARTIAL
		} else if totalOut.IsZero() {
			resultCode = tx.TecPATH_DRY
		}
	}

	return FlowResult{
		In:              totalIn,
		Out:             totalOut,
		Sandbox:         accumSandbox,
		RemovableOffers: allOfrsToRm,
		Result:          tx.Result(resultCode),
	}
}

// limitOut limits the output amount to produce the requested strand limitQuality
// when the path contains AMM (non-constant quality function). Reducing the output
// increases quality of AMM steps, increasing the strand's composite quality.
//
// The function composes QualityFunctions from all steps in the strand, then
// solves for the output that produces the desired average quality.
//
// Returns remainingOut unchanged if:
//   - any step returns nil QualityFunction
//   - the composed QF is constant (all CLOB, no AMM)
//   - the computed output is not less than remainingOut
//   - the computed output is within 1e-9 relative distance of remainingOut
//
// Reference: rippled StrandFlow.h limitOut() lines 369-413
func limitOut(v *PaymentSandbox, strand Strand, remainingOut EitherAmount, limitQuality Quality) EitherAmount {
	var qf *QualityFunction
	dir := DebtDirectionIssues
	for _, step := range strand {
		stepQF, stepDir := step.GetQualityFunc(v, dir)
		if stepQF == nil {
			return remainingOut
		}
		if qf == nil {
			qf = stepQF
		} else {
			qf.Combine(*stepQF)
		}
		dir = stepDir
	}

	// QualityFunction is constant (all CLOB, no AMM)
	if qf == nil || qf.IsConst() {
		return remainingOut
	}

	outAmt := qf.OutFromAvgQ(limitQuality)
	if outAmt == nil {
		return remainingOut
	}

	// Convert the Number result to an EitherAmount matching remainingOut's type
	var out EitherAmount
	if remainingOut.IsNative {
		// Convert IOU-style number to XRP drops using round-to-nearest (banker's rounding).
		// Reference: rippled StrandFlow.h limitOut() line 402: XRPAmount{*out}
		// which calls Number::operator rep() (round to nearest, even on tie).
		drops := canonicalizeDropsRound(outAmt.Mantissa(), outAmt.Exponent())
		out = NewXRPEitherAmount(drops)
	} else {
		// Preserve currency/issuer from remainingOut (outAmt has empty currency/issuer
		// because QualityFunction uses Number arithmetic with no issue info).
		out = NewIOUEitherAmount(tx.NewIssuedAmount(
			outAmt.Mantissa(), outAmt.Exponent(),
			remainingOut.IOU.Currency, remainingOut.IOU.Issuer))
	}

	// A tiny difference could be due to round off
	// Reference: rippled withinRelativeDistance(out, remainingOut, Number(1, -9))
	if withinRelativeDistanceAmounts(out, remainingOut, 1e-9) {
		return remainingOut
	}

	// Return min(out, remainingOut)
	if out.Compare(remainingOut) < 0 {
		return out
	}
	return remainingOut
}

// sumAmounts sums a slice of EitherAmounts.
// For better precision, sorts from smallest to largest before summing.
// Reference: rippled uses flat_multiset which auto-sorts, then std::accumulate.
func sumAmounts(amounts []EitherAmount) EitherAmount {
	if len(amounts) == 0 {
		return ZeroXRPEitherAmount()
	}
	if len(amounts) == 1 {
		return amounts[0]
	}
	// Sort ascending (smallest first) for precision
	sorted := make([]EitherAmount, len(amounts))
	copy(sorted, amounts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Compare(sorted[j]) < 0
	})
	result := sorted[0]
	for i := 1; i < len(sorted); i++ {
		result = result.Add(sorted[i])
	}
	return result
}

// offerDeleteInSandbox deletes an offer from a PaymentSandbox.
// This is the equivalent of rippled's offerDelete() called from StrandFlow.h lines 810-813
// to permanently remove unfunded/expired/degraded-quality offers discovered during flow.
//
// Reference: rippled StrandFlow.h:
//
//	for (auto const& o : ofrsToRm)
//	    if (auto ok = sb.peek(keylet::offer(o)))
//	        offerDelete(sb, ok, j);
func offerDeleteInSandbox(sb *PaymentSandbox, offerKey [32]byte) {
	offerKL := keylet.Keylet{Key: offerKey}
	offerData, err := sb.Read(offerKL)
	if err != nil || offerData == nil {
		return // Offer already deleted or not found
	}

	offer, err := state.ParseLedgerOffer(offerData)
	if err != nil {
		return
	}

	ownerID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(ownerID)
	state.DirRemove(sb, ownerDirKey, offer.OwnerNode, offerKey, false)

	// Remove from book directory
	bookDirKey := keylet.Keylet{Type: 100, Key: offer.BookDirectory}
	state.DirRemove(sb, bookDirKey, offer.BookNode, offerKey, false)

	// Erase the offer
	sb.Erase(offerKL)

	// Decrement owner count
	adjustOwnerCountInSandbox(sb, ownerID, -1, txHash, ledgerSeq)
}

// adjustOwnerCountInSandbox modifies an account's OwnerCount by delta in a PaymentSandbox.
// This is a standalone version used by offerDeleteInSandbox.
func adjustOwnerCountInSandbox(sb *PaymentSandbox, account [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) {
	accountKey := keylet.Account(account)
	accountData, err := sb.Read(accountKey)
	if err != nil || accountData == nil {
		return
	}

	accountRoot, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return
	}

	newCount := int(accountRoot.OwnerCount) + delta
	if newCount < 0 {
		newCount = 0
	}
	accountRoot.OwnerCount = uint32(newCount)
	accountRoot.PreviousTxnID = txHash
	accountRoot.PreviousTxnLgrSeq = ledgerSeq

	newData, err := state.SerializeAccountRoot(accountRoot)
	if err != nil {
		return
	}

	sb.Update(accountKey, newData)
}

// RippleCalculate is the main entry point for path-based payments.
// It converts paths to strands and executes the Flow algorithm.
//
// Parameters:
//   - view: LedgerView for reading state
//   - srcAccount: Source account sending the payment
//   - dstAccount: Destination account receiving the payment
//   - dstAmount: Amount to deliver to destination
//   - srcAmount: Maximum amount source will send (SendMax)
//   - paths: Payment paths from transaction
//   - addDefaultPath: Whether to include direct path
//   - partialPayment: Whether partial delivery is allowed
//   - limitQuality: Whether to limit exchange quality
//
// Returns:
//   - actualIn: Actual amount sent
//   - actualOut: Actual amount delivered
//   - removableOffers: Offers that should be removed
//   - sandbox: The PaymentSandbox containing all state changes
//   - result: Transaction result code
func RippleCalculate(
	view tx.LedgerView,
	srcAccount, dstAccount [20]byte,
	dstAmount tx.Amount,
	srcAmount *tx.Amount,
	paths [][]PathStep,
	addDefaultPath bool,
	partialPayment bool,
	limitQuality bool,
	txHash [32]byte,
	ledgerSeq uint32,
	opts ...RippleCalculateOption,
) (EitherAmount, EitherAmount, map[[32]byte]bool, *PaymentSandbox, tx.Result) {
	// Apply options
	var rcOpts rippleCalculateOpts
	for _, opt := range opts {
		opt(&rcOpts)
	}

	// Create PaymentSandbox from view
	sandbox := NewPaymentSandbox(view)
	sandbox.SetTransactionContext(txHash, ledgerSeq)

	// Convert paths to strands
	strands, strandResult := ToStrands(sandbox, srcAccount, dstAccount, dstAmount, srcAmount, paths, addDefaultPath)
	if strandResult != tx.TesSUCCESS || len(strands) == 0 {
		if strandResult == tx.TesSUCCESS {
			strandResult = tx.TecPATH_DRY
		}
		return ZeroXRPEitherAmount(), ZeroXRPEitherAmount(), nil, nil, strandResult
	}

	// Create AMMContext for this payment
	// Reference: rippled Flow.cpp line 85: AMMContext ammContext(src, false);
	ammCtx := NewAMMContext(srcAccount, false)

	// Configure BookSteps with amendment flags for payments.
	configureBookStepsForPayments(strands, rcOpts.parentCloseTime, rcOpts.fixReducedOffersV1, rcOpts.fixReducedOffersV2, rcOpts.fixRmSmallIncreasedQOffers)

	// Initialize AMM liquidity on BookSteps.
	// Reference: rippled BookStep constructor reads AMM SLE and creates AMMLiquidity.
	configureAMMOnBookSteps(sandbox, strands, ammCtx, rcOpts.parentCloseTime,
		rcOpts.fixAMMv1_1, rcOpts.fixAMMv1_2, rcOpts.fixAMMOverflowOffer)

	// Set multiPath after strands are built
	// Reference: rippled Flow.cpp line 112: ammContext.setMultiPath(strands.size() > 1)
	ammCtx.SetMultiPath(len(strands) > 1)

	// Configure BookSteps with domain ID for permissioned domain payments.
	if rcOpts.domainID != nil {
		setDomainOnBookSteps(strands, rcOpts.domainID)
	}

	// Convert amounts to EitherAmount
	outReq := ToEitherAmount(dstAmount)

	var sendMax *EitherAmount
	if srcAmount != nil {
		sm := ToEitherAmount(*srcAmount)
		sendMax = &sm
	}

	// Calculate limit quality if requested
	var qualityLimit *Quality
	if limitQuality && sendMax != nil {
		q := QualityFromAmounts(*sendMax, outReq)
		qualityLimit = &q
	}

	// Execute flow with FlowSortStrands amendment flag
	result := Flow(sandbox, strands, outReq, partialPayment, qualityLimit, sendMax, ammCtx, rcOpts.flowSortStrands)

	// Apply flow sandbox changes back to the main sandbox
	if result.Result == tx.TesSUCCESS || result.Result == tx.TecPATH_PARTIAL {
		if result.Sandbox != nil {
			result.Sandbox.Apply(sandbox)
		}
	}

	return result.In, result.Out, result.RemovableOffers, sandbox, result.Result
}

// RippleCalculateOption is a functional option for RippleCalculate
type RippleCalculateOption func(*rippleCalculateOpts)

type rippleCalculateOpts struct {
	parentCloseTime            uint32
	fixReducedOffersV1         bool
	fixReducedOffersV2         bool
	fixRmSmallIncreasedQOffers bool
	flowSortStrands            bool
	domainID                   *[32]byte
	// AMM amendment flags
	fixAMMv1_1          bool
	fixAMMv1_2          bool
	fixAMMOverflowOffer bool
}

// WithAmendments passes amendment flags and ledger timing to RippleCalculate,
// which configures BookSteps with the appropriate behavior flags.
func WithAmendments(parentCloseTime uint32, fixReducedOffersV1, fixReducedOffersV2, fixRmSmallIncreasedQOffers bool, flowSortStrands ...bool) RippleCalculateOption {
	return func(o *rippleCalculateOpts) {
		o.parentCloseTime = parentCloseTime
		o.fixReducedOffersV1 = fixReducedOffersV1
		o.fixReducedOffersV2 = fixReducedOffersV2
		o.fixRmSmallIncreasedQOffers = fixRmSmallIncreasedQOffers
		if len(flowSortStrands) > 0 {
			o.flowSortStrands = flowSortStrands[0]
		}
	}
}

// WithAMMAmendments passes AMM-specific amendment flags to RippleCalculate.
// Reference: rippled BookStep reads these from ctx.view.rules()
func WithAMMAmendments(fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer bool) RippleCalculateOption {
	return func(o *rippleCalculateOpts) {
		o.fixAMMv1_1 = fixAMMv1_1
		o.fixAMMv1_2 = fixAMMv1_2
		o.fixAMMOverflowOffer = fixAMMOverflowOffer
	}
}

// WithDomainID passes a permissioned domain ID to RippleCalculate, causing the
// flow engine to use domain book directories and filter offers by domain membership.
// Reference: rippled Payment.cpp doApply() passes ctx_.tx[~sfDomainID] to rippleCalculate
func WithDomainID(domainID *[32]byte) RippleCalculateOption {
	return func(o *rippleCalculateOpts) {
		o.domainID = domainID
	}
}

// configureBookStepsForPayments sets amendment flags on BookSteps within payment strands.
// These flags control OfferStream-level behavior during offer iteration.
// Reference: rippled OfferStream reads rules from view_ dynamically;
// the Go code passes them as booleans on each BookStep.
func configureBookStepsForPayments(strands []Strand, parentCloseTime uint32, fixReducedOffersV1, fixReducedOffersV2, fixRmSmallIncreasedQOffers bool) {
	for _, strand := range strands {
		for _, step := range strand {
			if bookStep, ok := step.(*BookStep); ok {
				bookStep.parentCloseTime = parentCloseTime
				bookStep.fixReducedOffersV1 = fixReducedOffersV1
				bookStep.fixReducedOffersV2 = fixReducedOffersV2
				bookStep.fixRmSmallIncreasedQOffers = fixRmSmallIncreasedQOffers
			}
		}
	}
}

// setDomainOnBookSteps sets the domain ID on all BookSteps in the given strands.
// This causes each BookStep to use the domain book directory and filter offers
// by domain membership during iteration.
// Reference: rippled RippleCalc::rippleCalculate passes domain to OfferStream
func setDomainOnBookSteps(strands []Strand, domainID *[32]byte) {
	for _, strand := range strands {
		for _, step := range strand {
			if bookStep, ok := step.(*BookStep); ok {
				bookStep.domainID = domainID
				bookStep.book.DomainID = domainID
			}
		}
	}
}

// configureAMMOnBookSteps initializes AMMLiquidity on each BookStep if an AMM pool
// exists for the book's in/out issues.
// Reference: rippled BookStep constructor lines 103-112
func configureAMMOnBookSteps(
	view *PaymentSandbox,
	strands []Strand,
	ammCtx *AMMContext,
	parentCloseTime uint32,
	fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer bool,
) {
	for _, strand := range strands {
		for _, step := range strand {
			bookStep, ok := step.(*BookStep)
			if !ok {
				continue
			}
			bookStep.initAMMLiquidity(view, ammCtx, parentCloseTime,
				fixAMMv1_1, fixAMMv1_2, fixAMMOverflowOffer)
		}
	}
}

// FlowV2 is an alternative flow implementation that matches rippled's FlowV2.
// It uses a slightly different iteration strategy.
func FlowV2(
	baseView *PaymentSandbox,
	strands []Strand,
	outReq EitherAmount,
	partialPayment bool,
	limitQuality *Quality,
	sendMax *EitherAmount,
) FlowResult {
	// For now, delegate to Flow
	return Flow(baseView, strands, outReq, partialPayment, limitQuality, sendMax, nil)
}
