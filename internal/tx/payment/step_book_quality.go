package payment

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

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
		qOne := qualityFromFloat64(1.0)
		q := s.adjustQualityWithFeesForQF(v, qOne, prevStepDir, true)
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
	q := s.adjustQualityWithFeesForQF(v, *res.quality, prevStepDir, false)
	return NewCLOBLikeQualityFunction(q), dir
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
	// Call tip() equivalent: determine if CLOB or AMM is the tip
	// Reference: rippled BookStep.cpp tip() lines 938-974
	lobQuality := s.getCLOBTipQuality(sb)

	if s.ammLiquidity != nil {
		// With fixAMMv1_1, pass a quality threshold to getAMMOffer so the AMM
		// doesn't generate tiny offers when its quality barely exceeds CLOB.
		// This prevents the payment engine from going into many iterations.
		// Reference: rippled BookStep.cpp tip() lines 962-967
		var qualityThreshold *Quality
		if s.ammLiquidity.fixAMMv1_1 && lobQuality != nil {
			qualityThreshold = s.tipQualityThreshold(*lobQuality)
		}

		ammOffer := s.getAMMOffer(sb, qualityThreshold)
		if ammOffer != nil {
			ammQ := ammOffer.Quality()
			if lobQuality == nil || ammQ.BetterThan(*lobQuality) {
				// AMM is tip. Return AMM's quality function.
				// Reference: rippled AMMOffer.cpp getQualityFunc()
				return s.ammOfferGetQualityFunc(ammOffer)
			}
		}
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

// adjustQualityWithFeesForQF adjusts a quality with transfer fees for the
// QualityFunction calculation. This implements the payment variant of
// adjustQualityWithFees.
//
// For payments: charges transfer fee based on prevStepDir and ownerPaysTransferFee.
// waiveOutFee=true waives the output transfer fee (used for AMM offers).
//
// Reference: rippled BookPaymentStep::adjustQualityWithFees() lines 328-359
func (s *BookStep) adjustQualityWithFeesForQF(v *PaymentSandbox, ofrQ Quality, prevStepDir DebtDirection, waiveOutFee bool) Quality {
	// trIn: charge transfer fee when previous step redeems
	trIn := QualityOne
	if Redeems(prevStepDir) && !s.book.In.IsXRP() {
		trIn = s.GetAccountTransferRate(v, s.book.In.Issuer)
		// If issuer == strandDst, no fee (parityRate)
		if s.book.In.Issuer == s.strandDst {
			trIn = QualityOne
		}
	}

	// trOut: charge transfer fee only if ownerPaysTransferFee and fee is not waived
	trOut := QualityOne
	if s.ownerPaysTransferFee && !waiveOutFee && !s.book.Out.IsXRP() {
		trOut = s.GetAccountTransferRate(v, s.book.Out.Issuer)
		if s.book.Out.Issuer == s.strandDst {
			trOut = QualityOne
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
	if !s.ownerPaysTransferFee {
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
	if !s.ownerPaysTransferFee {
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
	accountKey := keylet.Account(issuer)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return QualityOne
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return QualityOne
	}

	if account.TransferRate == 0 {
		return QualityOne
	}
	return account.TransferRate
}
