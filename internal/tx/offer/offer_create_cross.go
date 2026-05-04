package offer

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// processCancelRequest handles an OfferSequence cancellation that piggybacks
// on the OfferCreate transaction. The cancellation must occur in BOTH
// sandboxes so that orphan state never survives the FillOrKill decision.
// Reference: rippled CreateOffer.cpp lines 608-621
func (o *OfferCreate) processCancelRequest(ctx *tx.ApplyContext, sb, sbCancel *payment.PaymentSandbox) tx.Result {
	if o.OfferSequence == nil {
		return tx.TesSUCCESS
	}
	sleCancel := peekOffer(ctx.View, ctx.AccountID, *o.OfferSequence)
	if sleCancel == nil {
		return tx.TesSUCCESS
	}
	result := offerDeleteInView(sb, sleCancel)
	// Delete in cancel sandbox (same operation)
	_ = offerDeleteInView(sbCancel, sleCancel)

	// Also update owner count (once, since we'll only apply one sandbox)
	if result == tx.TesSUCCESS && ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}
	return result
}

// crossOutcome captures everything takerCross hands back to applyGuts.
type crossOutcome struct {
	terminated  bool
	result      tx.Result
	applyMain   bool
	saTakerPays tx.Amount
	saTakerGets tx.Amount
	uRate       uint64
	crossed     bool
}

// invokeFlowCross wires the OfferCreate inputs and currently-active
// amendments into payment.FlowCross. Pulled out so the surrounding crossing
// flow stays under the line-budget — the call site is otherwise dominated by
// the long amendment list.
//
// Reference: rippled CreateOffer.cpp lines 706-712 (flowCross)
func (o *OfferCreate) invokeFlowCross(
	ctx *tx.ApplyContext,
	sb *payment.PaymentSandbox,
	saTakerPays, saTakerGets tx.Amount,
	bPassive, bSell bool,
) payment.FlowCrossResult {
	rules := ctx.Rules()
	return payment.FlowCross(
		sb, // Use main sandbox for crossing
		ctx.AccountID,
		saTakerGets, // What we're selling (taker pays to counterparty)
		saTakerPays, // What we want (taker receives from counterparty)
		ctx.TxHash,
		ctx.Config.LedgerSequence,
		bPassive, // For passive offers, only cross against strictly better quality
		bSell,    // For sell offers, deliver MAX (sell all input regardless of output)
		ctx.Config.ParentCloseTime,
		ctx.Config.ReserveBase,
		ctx.Config.ReserveIncrement,
		rules.Enabled(amendment.FeatureFixReducedOffersV1),
		rules.Enabled(amendment.FeatureFixReducedOffersV2),
		rules.Enabled(amendment.FeatureFixRmSmallIncreasedQOffers),
		rules.Enabled(amendment.FeatureFlowSortStrands),
		rules.Enabled(amendment.FeatureFixAMMv1_1),
		rules.Enabled(amendment.FeatureFixAMMv1_2),
		rules.Enabled(amendment.FeatureFixAMMOverflowOffer),
		rules.Enabled(amendment.FeatureFix1781),
		o.DomainID, // Domain ID for permissioned DEX offer crossing
	)
}

// takerCross performs the offer-crossing portion of applyGuts: tick-size
// rounding, FlowCross invocation, removable-offer cleanup, and computation
// of the remaining (un-crossed) amounts that need to be placed.
//
// Returns a crossOutcome describing whether applyGuts should terminate
// immediately (terminated=true) and, if not, the updated takerPays/takerGets
// and rate that the offer placement step should use.
//
// Reference: rippled CreateOffer.cpp applyGuts() lines 641-768
func (o *OfferCreate) takerCross(
	ctx *tx.ApplyContext,
	sb, sbCancel *payment.PaymentSandbox,
	saTakerPays, saTakerGets tx.Amount,
	uRate uint64,
	bPassive, bSell, bFillOrKill bool,
) crossOutcome {
	rules := ctx.Rules()

	// Apply tick size rounding if applicable
	// Reference: lines 643-685
	saTakerPays, saTakerGets = applyTickSize(ctx.View, saTakerPays, saTakerGets, bSell, rules)
	if isAmountZeroOrNegative(saTakerPays) || isAmountZeroOrNegative(saTakerGets) {
		// Offer rounded to zero
		return crossOutcome{terminated: true, result: tx.TesSUCCESS, applyMain: true}
	}

	// Recalculate rate after tick size
	uRate = state.GetRate(saTakerGets, saTakerPays)

	// Perform offer crossing using the main sandbox (sb)
	// Reference: lines 687-768
	// Note: Passive offers still cross, but only against offers with STRICTLY better quality.
	// The passive flag is passed to FlowCross which increments the quality threshold.
	// Reference: rippled CreateOffer.cpp lines 362-364
	var placeOffer struct {
		in  tx.Amount
		out tx.Amount
	}

	ctx.Log.Trace("offer crossing start",
		"takerPays", saTakerPays,
		"takerGets", saTakerGets,
		"passive", bPassive,
		"sell", bSell,
	)

	crossResult := o.invokeFlowCross(ctx, sb, saTakerPays, saTakerGets, bPassive, bSell)

	// Convert result amounts back.
	// Reference: rippled CreateOffer.cpp flowCross() result handling
	grossPaid := payment.FromEitherAmount(crossResult.TakerPaid)
	placeOffer.in = payment.FromEitherAmount(crossResult.TakerPaidNet)
	placeOffer.out = payment.FromEitherAmount(crossResult.TakerGot)

	result := crossResult.Result
	ctx.Log.Trace("offer crossing done",
		"result", result,
		"takerPaid", placeOffer.in,
		"takerGot", placeOffer.out,
	)

	// For offer crossing, tecPATH_DRY means no liquidity found to cross
	// This is not an error - we just place the offer with original amounts
	// Reference: rippled's flowCross always returns tesSUCCESS (CreateOffer.cpp line 509)
	if result == tx.TecPATH_DRY {
		result = tx.TesSUCCESS
	}

	if result != tx.TesSUCCESS {
		// Error during crossing - apply cancel sandbox
		return crossOutcome{terminated: true, result: result, applyMain: false}
	}

	// Check if account's funds were exhausted during crossing.
	// Reference: rippled CreateOffer.cpp lines 432-441.
	// Must use the PaymentSandbox with BalanceHook BEFORE applying it to the view,
	// matching rippled's accountFunds(psb, ...) call. BalanceHook subtracts
	// DeferredCredits, returning zero for self-crossing round-trips even when the
	// on-ledger balance is non-zero.
	var takerInBalance tx.Amount
	if crossResult.Sandbox != nil {
		takerInBalance = payment.AccountFundsInSandbox(crossResult.Sandbox, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
	} else {
		takerInBalance = tx.AccountFunds(sb, ctx.AccountID, saTakerGets, true, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
	}

	// Apply FlowCross sandbox changes to our main sandbox (sb)
	// Reference: rippled CreateOffer.cpp - sandbox changes must be applied
	// FlowCross creates a root sandbox, so we use ApplyToView with sb as the target
	if crossResult.Sandbox != nil {
		if err := crossResult.Sandbox.ApplyToView(sb); err != nil {
			return crossOutcome{terminated: true, result: tx.TefINTERNAL, applyMain: false}
		}
	}

	// NOTE: We do NOT manually adjust ctx.Account.Balance here.
	// In rippled, mTxnAccount lives inside the sandbox, so balance changes
	// from crossing are applied when the sandbox is applied. In goXRPL,
	// ctx.Account is separate, so we re-read the account balance from the
	// view AFTER applying the sandbox (see ApplyCreate lines 421-424).
	// Manually adjusting here would DOUBLE-COUNT the XRP changes.

	// Remove unfunded/self-crossed offers that were marked during crossing.
	// Must delete from BOTH sandboxes so that regardless of which one is applied
	// (sb for success, sbCancel for FillOrKill failure), orphan offers are cleaned up.
	// Reference: rippled CreateOffer.cpp lines 420-426: deletes from psb AND psbCancel.
	removeRemovableOffers(sb, sbCancel, crossResult.RemovableOffers)

	if isAmountZeroOrNegative(takerInBalance) {
		// Apply main sandbox with crossing results
		return crossOutcome{terminated: true, result: tx.TesSUCCESS, applyMain: true}
	}

	// Reference: line 744-745
	// Use isAmountZeroOrNegative because FromEitherAmount returns "0" for zero amounts,
	// not empty string ""
	crossed := false
	if !isAmountZeroOrNegative(placeOffer.in) || !isAmountZeroOrNegative(placeOffer.out) {
		crossed = true
	}

	remainingGets, remainingPays := computePostCrossAmounts(
		ctx, saTakerPays, saTakerGets, placeOffer.in, placeOffer.out, takerInBalance, bSell,
	)

	if outcome, done := evaluateFillOrKill(rules, saTakerGets, grossPaid, remainingGets, remainingPays, bFillOrKill); done {
		return outcome
	}

	// Adjust amounts for remaining offer
	// Reference: lines 766-767
	return crossOutcome{
		terminated:  false,
		result:      tx.TesSUCCESS,
		applyMain:   true,
		saTakerPays: remainingPays,
		saTakerGets: remainingGets,
		uRate:       uRate,
		crossed:     crossed,
	}
}

// evaluateFillOrKill decides what to do once we know the post-cross
// remainder. It returns (outcome, true) when the offer is either fully
// crossed or the FillOrKill flag forces an early termination, and
// (zero, false) when applyGuts should continue and place the remainder.
//
// Reference: rippled CreateOffer.cpp lines 757-795
func evaluateFillOrKill(
	rules *amendment.Rules,
	saTakerGets, grossPaid, remainingGets, remainingPays tx.Amount,
	bFillOrKill bool,
) (crossOutcome, bool) {
	fullyCrossed := isAmountZeroOrNegative(remainingGets) || isAmountZeroOrNegative(remainingPays)

	// Without fixFillOrKill, FoK requires TakerGets to be fully consumed
	// (GROSS paid >= original TakerGets), not just remaining being zero.
	// The proportional remaining calculation can yield zero even when TakerGets
	// isn't fully consumed (because TakerPays was fully satisfied at a better rate).
	// Reference: rippled CreateOffer.cpp: pre-amendment requires full TakerGets
	// consumption for FoK; post-amendment relaxes non-sell FoK.
	// Note: goXRPL uses partialPayment=true for FlowCross (unlike rippled which
	// passes partialPayment=!(txFlags & tfFillOrKill)), so FoK handling is manual.
	if fullyCrossed && bFillOrKill && !rules.Enabled(amendment.FeatureFixFillOrKill) {
		remainingWithGross := subtractAmounts(saTakerGets, grossPaid)
		if !isAmountZeroOrNegative(remainingWithGross) {
			// FoK not satisfied: TakerGets not fully consumed by GROSS amount.
			if rules.Enabled(amendment.FeatureFix1578) {
				return crossOutcome{terminated: true, result: tx.TecKILLED, applyMain: false}, true
			}
			return crossOutcome{terminated: true, result: tx.TesSUCCESS, applyMain: false}, true
		}
	}

	if fullyCrossed {
		return crossOutcome{terminated: true, result: tx.TesSUCCESS, applyMain: true}, true
	}
	return crossOutcome{}, false
}

// computePostCrossAmounts derives the un-crossed remainder of an offer based
// on the FlowCross result. The math mirrors rippled's flowCross afterCross
// computation: subtract the actually-crossed portion and re-derive the other
// side from the original quality.
//
// Reference: rippled CreateOffer.cpp lines 429-504
func computePostCrossAmounts(
	ctx *tx.ApplyContext,
	saTakerPays, saTakerGets tx.Amount,
	placeIn, placeOut tx.Amount,
	takerInBalance tx.Amount,
	bSell bool,
) (remainingGets, remainingPays tx.Amount) {
	rules := ctx.Rules()

	noCrossingHappened := isAmountZeroOrNegative(placeIn) && isAmountZeroOrNegative(placeOut)

	if isAmountZeroOrNegative(takerInBalance) {
		// Funds exhausted during crossing — no remaining offer
		// Reference: rippled CreateOffer.cpp lines 435-441
		return zeroAmount(saTakerGets), zeroAmount(saTakerPays)
	}
	if noCrossingHappened {
		// No crossing happened - return original amounts directly
		// Reference: rippled CreateOffer.cpp line 429: afterCross = takerAmount (unchanged)
		return saTakerGets, saTakerPays
	}
	if bSell {
		// Sell offer: subtract NET input from TakerGets, compute TakerPays by quality
		// Reference: rippled CreateOffer.cpp lines 447-489
		//   nonGatewayAmountIn = divideRound(actualAmountIn, gatewayXferRate, ...)
		//   afterCross.in -= nonGatewayAmountIn
		//   afterCross.out = divRound(afterCross.in, rate, ...) or divRoundStrict
		remainingGets = subtractAmounts(saTakerGets, placeIn) // placeIn is NET
		if isAmountNegative(remainingGets) {
			remainingGets = zeroAmount(saTakerGets)
		}
		rate := payment.QualityFromAmounts(
			payment.ToEitherAmount(saTakerGets),
			payment.ToEitherAmount(saTakerPays),
		).Rate()
		outNative := saTakerPays.IsNative()
		outCurrency := saTakerPays.Currency
		outIssuer := saTakerPays.Issuer
		if rules.Enabled(amendment.FeatureFixReducedOffersV1) {
			remainingPays = offerDivRoundStrict(remainingGets, rate, outNative, outCurrency, outIssuer, false)
		} else {
			remainingPays = offerDivRound(remainingGets, rate, outNative, outCurrency, outIssuer, true)
		}
		return remainingGets, remainingPays
	}
	// Non-sell offer: subtract output received from TakerPays, compute TakerGets by quality
	// Reference: rippled CreateOffer.cpp lines 491-503
	//   afterCross.out -= result.actualAmountOut
	//   afterCross.in = mulRound(afterCross.out, rate, takerAmount.in.issue(), true)
	remainingPays = subtractAmounts(saTakerPays, placeOut)
	if isAmountNegative(remainingPays) {
		remainingPays = zeroAmount(saTakerPays)
	}
	rate := payment.QualityFromAmounts(
		payment.ToEitherAmount(saTakerGets),
		payment.ToEitherAmount(saTakerPays),
	).Rate()
	outNative := saTakerGets.IsNative()
	outCurrency := saTakerGets.Currency
	outIssuer := saTakerGets.Issuer
	remainingGets = offerMulRound(remainingPays, rate, outNative, outCurrency, outIssuer, true)
	return remainingGets, remainingPays
}

// removeRemovableOffers deletes the offers FlowCross marked for removal from
// BOTH the main and cancel sandboxes. This guarantees orphan offers are
// cleaned up regardless of which sandbox the FillOrKill decision applies.
//
// Reference: rippled CreateOffer.cpp lines 420-426
func removeRemovableOffers(sb, sbCancel *payment.PaymentSandbox, removable map[[32]byte]bool) {
	for offerKey := range removable {
		offerKeylet := keylet.Keylet{Key: offerKey}
		removeOfferFromView(sb, offerKeylet)
		removeOfferFromView(sbCancel, offerKeylet)
	}
}

// removeOfferFromView deletes a single offer keylet from a view and adjusts
// the offer-owner's reserve count. Silently no-ops on missing/unparseable
// entries to mirror the original best-effort cleanup loop.
func removeOfferFromView(view *payment.PaymentSandbox, offerKeylet keylet.Keylet) {
	offerData, err := view.Read(offerKeylet)
	if err != nil || offerData == nil {
		return
	}
	offer, err := state.ParseLedgerOffer(offerData)
	if err != nil {
		return
	}
	ownerID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return
	}
	_ = offerDeleteInView(view, offer)
	adjustOwnerCountInView(view, ownerID, -1)
}
