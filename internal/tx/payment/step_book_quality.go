package payment

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// fixAMMv1_1Enabled reports whether fixAMMv1_1 governs this execution,
// nil-defaulting to the active-network value (enabled) for rules-free contexts
// such as pathfinding liquidity estimation, matching fix1515Enabled's convention.
func fixAMMv1_1Enabled(sb *PaymentSandbox) bool {
	rules := sb.Rules()
	return rules == nil || rules.Enabled(amendment.FeatureFixAMMv1_1)
}

// QualityUpperBound returns an upper bound on the quality of this step.
// It selects the tip offer (CLOB or AMM), then adjusts the tip quality for
// transfer fees exactly as GetQualityFunc does, so the two cannot drift.
// Reference: rippled BookStep.cpp qualityUpperBound() lines 582-606.
func (s *BookStep) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	dir := s.DebtDirection(v, StrandDirectionForward)

	tipQ, isAMM := s.tipOfferQuality(v)
	if tipQ == nil {
		return nil, dir
	}

	// AMM tip waives the output transfer fee; CLOB tip does not.
	// Reference: rippled BookStep.cpp lines 594-604.
	q := s.adjustQualityWithFees(v, *tipQ, prevStepDir, isAMM, isAMM)
	return &q, dir
}

// GetQualityFunc returns the QualityFunction for this step.
// For BookStep, this examines whether the tip offer is CLOB or AMM and returns
// the appropriate QualityFunction, adjusted for transfer fees.
// Reference: rippled BookStep.cpp getQualityFunc() lines 608-648
func (s *BookStep) GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection) {
	dir := s.DebtDirection(v, StrandDirectionForward)

	res := s.tipOfferQualityF(v)
	if res == nil {
		return nil, dir
	}

	// AMM (non-constant quality function)
	if !res.IsConst() {
		// Check if transfer fees need to be composed in.
		// For payments: adjustQualityWithFees with WaiveTransferFee::Yes and qOne
		// Reference: rippled BookStep.cpp lines 620-636
		qOne := qualityOne
		q := s.adjustQualityWithFees(v, qOne, prevStepDir, true, true)
		if q.Value == qOne.Value {
			// No fee adjustment needed
			return res, dir
		}
		// Compose fee QF with AMM QF
		feeQF := NewCLOBLikeQualityFunction(q)
		if feeQF == nil {
			return res, dir
		}
		feeQF.Combine(*res)
		return feeQF, dir
	}

	// CLOB (constant quality function)
	// Reference: rippled BookStep.cpp lines 639-647
	q := s.adjustQualityWithFees(v, *res.quality, prevStepDir, false, false)
	return NewCLOBLikeQualityFunction(q), dir
}

// tip selects the best offer between the CLOB tip and the AMM synthetic offer,
// returning the CLOB tip quality and/or the AMM offer. At most one is non-nil
// in the "AMM wins" case (ammOffer set); when CLOB wins, ammOffer is nil and
// lobQuality holds the tip. Returns (nil, nil) when there are no offers.
//
// This is the single shared tip resolution used by both QualityUpperBound and
// GetQualityFunc, so the fixAMMv1_1 quality-threshold logic cannot drift.
// Reference: rippled BookStep.cpp tip() lines 938-974.
func (s *BookStep) tip(sb *PaymentSandbox) (lobQuality *Quality, ammOffer *AMMOffer) {
	lobQuality = s.getCLOBTipQuality(sb)

	if s.ammLiquidity != nil {
		// With fixAMMv1_1, pass a quality threshold to getAMMOffer so the AMM
		// doesn't generate tiny offers when its quality barely exceeds CLOB.
		// This prevents the payment engine from going into many iterations.
		// Reference: rippled BookStep.cpp tip() lines 962-967
		var qualityThreshold *Quality
		if fixAMMv1_1Enabled(sb) && lobQuality != nil {
			qualityThreshold = s.tipQualityThreshold(*lobQuality)
		}

		offer := s.getAMMOffer(sb, qualityThreshold)
		if offer != nil {
			ammQ := offer.Quality()
			if lobQuality == nil || ammQ.BetterThan(*lobQuality) {
				return nil, offer
			}
		}
	}

	return lobQuality, nil
}

// tipOfferQuality returns the tip quality and whether the tip is an AMM offer.
// Returns (nil, false) if there is no offer.
// Reference: rippled BookStep.cpp tipOfferQuality() lines 976-988.
func (s *BookStep) tipOfferQuality(sb *PaymentSandbox) (*Quality, bool) {
	lobQuality, ammOffer := s.tip(sb)
	if ammOffer != nil {
		q := ammOffer.Quality()
		return &q, true
	}
	if lobQuality == nil {
		return nil, false
	}
	return lobQuality, false
}

// tipOfferQualityF returns the QualityFunction for the tip (best) offer,
// choosing between CLOB and AMM. Returns nil if no offer exists.
//
// For CLOB offers: returns a CLOB-like QF with the tip quality.
// For AMM offers: returns the AMM's QualityFunction (may be non-constant
// for single-path or constant for multi-path).
//
// Reference: rippled BookStep.cpp tipOfferQualityF() lines 990-1000
func (s *BookStep) tipOfferQualityF(sb *PaymentSandbox) *QualityFunction {
	lobQuality, ammOffer := s.tip(sb)
	if ammOffer != nil {
		// AMM is tip. Return AMM's quality function.
		// Reference: rippled AMMOffer.cpp getQualityFunc()
		return s.ammOfferGetQualityFunc(ammOffer)
	}

	// CLOB is tip (or no offers at all)
	if lobQuality == nil {
		return nil
	}
	return NewCLOBLikeQualityFunction(*lobQuality)
}

// tipQualityThreshold returns the quality threshold to use for AMM offer
// generation in tipOfferQualityF. For offer crossing, if the taker's quality
// limit is better than the CLOB tip, don't use a threshold (let AMM generate
// max offer). Otherwise use the CLOB quality as threshold.
// For payments, always use the CLOB quality.
// Reference: rippled BookOfferCrossingStep::qualityThreshold() lines 479-486
// Reference: rippled BookPaymentStep::qualityThreshold() line 305
func (s *BookStep) tipQualityThreshold(lobQuality Quality) *Quality {
	// For offer crossing with AMM in single-path mode:
	// if qualityLimit is strictly better than lobQuality, return nil
	// so AMM generates its max offer (limitOut handles the quality cap)
	if s.qualityLimit != nil && s.ammLiquidity != nil &&
		!s.ammLiquidity.ammContext.MultiPath() &&
		s.qualityLimit.BetterThan(lobQuality) {
		return nil
	}
	q := lobQuality
	return &q
}

// ammOfferGetQualityFunc returns the QualityFunction for an AMM offer.
// Multi-path: returns CLOB-like QF (constant quality).
// Single-path: returns AMM QF (non-constant, slope-based).
// Reference: rippled AMMOffer.cpp getQualityFunc() lines 130-137
func (s *BookStep) ammOfferGetQualityFunc(offer *AMMOffer) *QualityFunction {
	if offer.ammLiquidity.ammContext.MultiPath() {
		return NewCLOBLikeQualityFunction(offer.Quality())
	}
	return NewAMMQualityFunction(offer.balanceIn, offer.balanceOut, offer.ammLiquidity.tradingFee)
}

// adjustQualityWithFees adjusts a quality with transfer fees. It mirrors
// rippled's two TDerived specialisations, dispatched by offerCrossing (the step
// type, chosen via ctx.offerCrossing): payments (false) use
// BookPaymentStep::adjustQualityWithFees; offer crossing (true) uses
// BookOfferCrossingStep::adjustQualityWithFees. This is independent of
// ownerPaysTransferFee, which in the payment branch conditionally charges trOut
// just as BookPaymentStep does.
//
// isAMM marks the tip as an AMM offer; waiveOutFee waives the output transfer
// fee (AMM never pays the out fee on the upper-bound estimate). For payments
// these are independent inputs; for crossing, isAMM gates the whole adjustment.
//
// Reference: rippled BookStep.cpp lines 328-359 (payment) and 519-558 (crossing).
func (s *BookStep) adjustQualityWithFees(v *PaymentSandbox, ofrQ Quality, prevStepDir DebtDirection, waiveOutFee, isAMM bool) Quality {
	// rate(id): parityRate when XRP or id is the strand destination.
	rate := func(issuer [20]byte, isXRP bool) uint32 {
		if isXRP || issuer == s.strandDst {
			return QualityOne
		}
		return s.GetAccountTransferRate(v, issuer)
	}

	var trIn, trOut uint32

	if s.offerCrossing {
		// Offer crossing. The quality upper bound assumes no fee unless the
		// single-path AMM out amount is non-constant under fixAMMv1_1; in all
		// other cases (pre-fix, CLOB, or multi-path AMM) the quality is
		// returned unadjusted. AMM never pays the out fee here.
		// Reference: rippled BookStep.cpp lines 519-558.
		multiPath := s.ammLiquidity != nil && s.ammLiquidity.ammContext.MultiPath()
		if !fixAMMv1_1Enabled(v) || !isAMM || multiPath {
			return ofrQ
		}
		trIn = QualityOne
		if Redeems(prevStepDir) {
			trIn = rate(s.book.In.Issuer, s.book.In.IsXRP())
		}
		trOut = QualityOne
	} else {
		// Payment. Charge trIn when the previous step redeems; charge trOut
		// only when the offer owner pays the transfer fee and it is not waived.
		// Reference: rippled BookStep.cpp lines 328-359.
		trIn = QualityOne
		if Redeems(prevStepDir) {
			trIn = rate(s.book.In.Issuer, s.book.In.IsXRP())
		}
		trOut = QualityOne
		if s.ownerPaysTransferFee && !waiveOutFee {
			trOut = rate(s.book.Out.Issuer, s.book.Out.IsXRP())
		}
	}

	// q1 = getRate(STAmount(trOut), STAmount(trIn)) = trIn / trOut
	trOutAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(trOut), 0, "", ""))
	trInAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(trIn), 0, "", ""))
	q1 := QualityFromAmounts(trInAmt, trOutAmt)

	return q1.Compose(ofrQ)
}

// transferRateIn returns the transfer rate for incoming currency.
// No fee when: XRP, issuer is strandDst, or previous step issues.
// Reference: rippled BookStep.cpp forEachOffer() rate lambda (lines 728-731) + trIn (line 734-735)
func (s *BookStep) transferRateIn(sb *PaymentSandbox, prevStepDir DebtDirection) uint32 {
	if s.book.In.IsXRP() || s.book.In.Issuer == s.strandDst {
		return QualityOne
	}

	// Only charge transfer fee when previous step redeems
	if !Redeems(prevStepDir) {
		return QualityOne
	}

	return s.GetAccountTransferRate(sb, s.book.In.Issuer)
}

// transferRateOut returns the transfer rate for outgoing currency.
// No fee when: XRP, issuer is strandDst, or ownerPaysTransferFee is false.
// Reference: rippled BookStep.cpp forEachOffer() rate lambda (lines 728-731) + trOut (line 737-738)
func (s *BookStep) transferRateOut(sb *PaymentSandbox) uint32 {
	if s.book.Out.IsXRP() || s.book.Out.Issuer == s.strandDst {
		return QualityOne
	}

	if !s.ownerPaysTransferFee {
		return QualityOne
	}

	return s.GetAccountTransferRate(sb, s.book.Out.Issuer)
}

// getOfrInRate returns the per-offer input transfer rate.
// In offer crossing mode, exempts transfer fee when offer owner == strand source
// (i.e., the taker is crossing their own offer from the input side).
// Reference: rippled BookOfferCrossingStep::getOfrInRate() (BookStep.cpp lines 491-502)
func (s *BookStep) getOfrInRate(offerOwner [20]byte, trIn uint32) uint32 {
	if !s.offerCrossing {
		return trIn // Payment mode — no exemption
	}
	// Offer crossing: check if offer owner == previous DirectStep's source
	if directStep, ok := s.prevStep.(*DirectStepI); ok {
		if offerOwner == directStep.src {
			return QualityOne // Self-pay exemption
		}
	}
	return trIn
}

// getOfrOutRate returns the per-offer output transfer rate.
// In offer crossing mode, exempts transfer fee when offer owner == strand destination
// AND the previous step is a BookStep (i.e., bridged crossing, second leg).
// Reference: rippled BookOfferCrossingStep::getOfrOutRate() (BookStep.cpp lines 506-517)
func (s *BookStep) getOfrOutRate(offerOwner [20]byte, trOut uint32) uint32 {
	if !s.offerCrossing {
		return trOut // Payment mode — no exemption
	}
	// Offer crossing: check if previous step is BookStep AND owner == strandDst
	if _, ok := s.prevStep.(*BookStep); ok {
		if offerOwner == s.strandDst {
			return QualityOne // Self-pay exemption
		}
	}
	return trOut
}

// GetAccountTransferRate gets the transfer rate from an account
func (s *BookStep) GetAccountTransferRate(sb *PaymentSandbox, issuer [20]byte) uint32 {
	return GetTransferRate(sb, issuer)
}
