package payment

// AMM swap math functions matching rippled's AMMHelpers.h
// These are used by AMMLiquidity and AMMOffer to generate synthetic offers
// and calculate pool-conserving swaps.
//
// Reference: rippled/src/xrpld/app/misc/AMMHelpers.h

import (
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

func fromNumber(num tx.Amount, prototype tx.Amount) tx.Amount {
	return fromNumberWithGuard(num, prototype, state.RoundToNearest)
}

func fromNumberWithGuard(num tx.Amount, prototype tx.Amount, mode state.RoundingMode) tx.Amount {
	m := legacyNumberMath()
	return m.toAmount(m.fromAmount(num, mode), prototype, mode)
}

func (m numberMath) add(a, b state.XRPLNumber) state.XRPLNumber {
	return a.AddRounded(b, state.RoundToNearest)
}

func (m numberMath) sub(a, b state.XRPLNumber) state.XRPLNumber {
	return a.AddRounded(b.Negate(), state.RoundToNearest)
}

func (m numberMath) subRounded(a, b state.XRPLNumber, mode state.RoundingMode) state.XRPLNumber {
	return a.AddRounded(b.Negate(), mode)
}

func (m numberMath) fee(tfee uint16, mode state.RoundingMode) state.XRPLNumber {
	if tfee == 0 {
		return m.zero()
	}
	return m.int(int64(tfee)).DivRounded(m.int(100000), mode)
}

func (m numberMath) feeMult(tfee uint16, mode state.RoundingMode) state.XRPLNumber {
	return m.subRounded(m.one(), m.fee(tfee, mode), mode)
}

// AMMFeeMult returns (1 - tfee/100000) as a fee multiplier.
// tfee is in basis points (e.g., 500 = 0.5%).
// Reference: rippled AMMCore.h feeMult()
func AMMFeeMult(tfee uint16) tx.Amount {
	m := legacyNumberMath()
	return m.toAmount(m.feeMult(tfee, state.RoundToNearest), tx.Amount{}, state.RoundToNearest)
}

// AMMFeeMultHalf returns (1 - tfee/200000).
// Reference: rippled AMMCore.h feeMultHalf()
func AMMFeeMultHalf(tfee uint16) tx.Amount {
	m := legacyNumberMath()
	result := m.int(int64(tfee)).DivRounded(m.int(200000), state.RoundToNearest)
	return m.toAmount(m.sub(m.one(), result), tx.Amount{}, state.RoundToNearest)
}

// AMMGetFee returns tfee/100000 as an Amount using banker's rounding.
// Reference: rippled AMMCore.h getFee()
func AMMGetFee(tfee uint16) tx.Amount {
	return AMMGetFeeRounded(tfee, state.RoundToNearest)
}

func AMMGetFeeRounded(tfee uint16, mode state.RoundingMode) tx.Amount {
	m := legacyNumberMath()
	return m.toAmount(m.fee(tfee, mode), tx.Amount{}, mode)
}

// SwapAssetIn calculates how much you get out when swapping assetIn into the pool.
// Formula: out = poolOut - (poolIn * poolOut) / (poolIn + assetIn * feeMult(tfee))
// With fixAMMv1_1: explicit rounding to favor the AMM (minimize output).
// All arithmetic is done in IOU-like Number representation to handle mixed XRP/IOU.
// Reference: rippled AMMHelpers.h swapAssetIn()
func SwapAssetIn(poolIn, poolOut, assetIn tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	return swapAssetIn(legacyNumberMath(), poolIn, poolOut, assetIn, tfee, fixAMMv1_1)
}

func swapAssetIn(m numberMath, poolIn, poolOut, assetIn tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	nPoolIn := m.fromAmount(poolIn, state.RoundToNearest)
	nPoolOut := m.fromAmount(poolOut, state.RoundToNearest)
	nAssetIn := m.fromAmount(assetIn, state.RoundToNearest)

	if fixAMMv1_1 {
		// Each sub-computation rounds to favor the AMM (minimize output),
		// matching rippled's per-step Number::setround() calls.
		// Reference: rippled AMMHelpers.h swapAssetIn() lines 493-514

		// Number::setround(Number::upward)
		numerator := nPoolIn.MulRounded(nPoolOut, state.RoundUpward)
		fee := m.fee(tfee, state.RoundUpward)

		// Number::setround(Number::downward)
		fMult := m.subRounded(m.one(), fee, state.RoundDownward)
		assetFee := nAssetIn.MulRounded(fMult, state.RoundDownward)
		denom := nPoolIn.AddRounded(assetFee, state.RoundDownward)

		if denom.Signum() <= 0 {
			return zeroLikeAmount(poolOut)
		}

		// Number::setround(Number::upward)
		ratio := numerator.DivRounded(denom, state.RoundUpward)

		// Number::setround(Number::downward)
		swapOut := m.subRounded(nPoolOut, ratio, state.RoundDownward)

		if swapOut.Signum() < 0 {
			return zeroLikeAmount(poolOut)
		}
		// toAmount with Number::downward
		return m.toAmount(swapOut, poolOut, state.RoundDownward)
	}

	// Pre-fixAMMv1_1: simple formula using Number arithmetic throughout.
	// In rippled, pool.in/pool.out/assetIn are all Number types, so all
	// operations (*, /, +, -) use Number::operator which has 16-digit
	// mantissa with guard-based rounding. We must use numberMul/numberDiv
	// (not Amount.Mul/Amount.Div which use STAmount::multiply/divide).
	// Reference: rippled AMMHelpers.h swapAssetIn() pre-amendment path
	fMult := m.feeMult(tfee, state.RoundToNearest)
	assetFee := nAssetIn.Mul(fMult)
	denom := nPoolIn.Add(assetFee)
	if denom.IsZero() {
		return zeroLikeAmount(poolOut)
	}
	numerator := nPoolIn.Mul(nPoolOut)
	ratio := numerator.Div(denom)
	result := m.sub(nPoolOut, ratio)
	if result.Signum() < 0 {
		return zeroLikeAmount(poolOut)
	}
	return m.toAmountWithNativeRounding(result, poolOut, state.RoundDownward, state.RoundToNearest)
}

// SwapAssetOut calculates how much you must put in to get assetOut from the pool.
// Formula: in = ((poolIn * poolOut) / (poolOut - assetOut) - poolIn) / feeMult(tfee)
// With fixAMMv1_1: explicit rounding to favor the AMM (maximize input).
// All arithmetic is done in IOU-like Number representation to handle mixed XRP/IOU.
// Reference: rippled AMMHelpers.h swapAssetOut()
func SwapAssetOut(poolIn, poolOut, assetOut tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	return swapAssetOut(legacyNumberMath(), poolIn, poolOut, assetOut, tfee, fixAMMv1_1)
}

func swapAssetOut(m numberMath, poolIn, poolOut, assetOut tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	nPoolIn := m.fromAmount(poolIn, state.RoundToNearest)
	nPoolOut := m.fromAmount(poolOut, state.RoundToNearest)
	nAssetOut := m.fromAmount(assetOut, state.RoundToNearest)

	if fixAMMv1_1 {
		// Each sub-computation rounds to favor the AMM (maximize input),
		// matching rippled's per-step Number::setround() calls.
		// Reference: rippled AMMHelpers.h swapAssetOut() lines 562-587

		// Number::setround(Number::upward)
		numerator := nPoolIn.MulRounded(nPoolOut, state.RoundUpward)

		// Number::setround(Number::downward)
		denom := m.subRounded(nPoolOut, nAssetOut, state.RoundDownward)
		if denom.Signum() <= 0 {
			return toMaxAmount(poolIn)
		}

		// Number::setround(Number::upward)
		ratio := numerator.DivRounded(denom, state.RoundUpward)
		numerator2 := m.subRounded(ratio, nPoolIn, state.RoundUpward)
		fee := m.fee(tfee, state.RoundUpward)

		// Number::setround(Number::downward)
		fMult := m.subRounded(m.one(), fee, state.RoundDownward)

		// Number::setround(Number::upward)
		swapIn := numerator2.DivRounded(fMult, state.RoundUpward)

		if swapIn.Signum() < 0 {
			return zeroLikeAmount(poolIn)
		}
		// toAmount with Number::upward
		return m.toAmount(swapIn, poolIn, state.RoundUpward)
	}

	// Pre-fixAMMv1_1: simple formula using Number arithmetic throughout.
	// In rippled, pool.in/pool.out/assetOut are all Number types, so all
	// operations use Number::operator (16-digit mantissa, guard-based).
	// We must use numberMul/numberDiv (not Amount.Mul/Amount.Div).
	// Reference: rippled AMMHelpers.h swapAssetOut() pre-amendment path
	fMult := m.feeMult(tfee, state.RoundToNearest)
	denom := m.sub(nPoolOut, nAssetOut)
	if denom.IsZero() || denom.Signum() < 0 {
		return toMaxAmount(poolIn)
	}
	numerator := nPoolIn.Mul(nPoolOut)
	ratio := numerator.Div(denom)
	diff := m.sub(ratio, nPoolIn)
	result := diff.Div(fMult)
	if result.Signum() < 0 {
		return zeroLikeAmount(poolIn)
	}
	return m.toAmountWithNativeRounding(result, poolIn, state.RoundUpward, state.RoundToNearest)
}

// SolveQuadraticEq computes (-b + sqrt(b^2 - 4*a*c)) / (2*a).
// Reference: rippled AMMHelpers.cpp solveQuadraticEq()
func SolveQuadraticEq(a, b, c tx.Amount) tx.Amount {
	m := legacyNumberMath()
	result := solveQuadraticEq(m, m.fromAmount(a, state.RoundToNearest), m.fromAmount(b, state.RoundToNearest), m.fromAmount(c, state.RoundToNearest))
	return m.toAmount(result, a, state.RoundToNearest)
}

func solveQuadraticEq(m numberMath, a, b, c state.XRPLNumber) state.XRPLNumber {
	b2 := b.Mul(b)
	ac4 := m.int(4).Mul(a).Mul(c)
	d := m.sub(b2, ac4)
	num := b.Negate().Add(d.Root2())
	return num.Div(m.int(2).Mul(a))
}

// SolveQuadraticEqSmallest uses the citardauq formula for better numerical stability.
// Returns the smallest positive root, or nil if discriminant < 0.
// Reference: rippled AMMHelpers.cpp solveQuadraticEqSmallest()
func SolveQuadraticEqSmallest(a, b, c tx.Amount) *tx.Amount {
	m := legacyNumberMath()
	result := solveQuadraticEqSmallest(m, m.fromAmount(a, state.RoundToNearest), m.fromAmount(b, state.RoundToNearest), m.fromAmount(c, state.RoundToNearest))
	if result == nil {
		return nil
	}
	amount := m.toAmount(*result, a, state.RoundToNearest)
	return &amount
}

func solveQuadraticEqSmallest(m numberMath, a, b, c state.XRPLNumber) *state.XRPLNumber {
	d := m.sub(b.Mul(b), m.int(4).Mul(a).Mul(c))
	if d.Signum() < 0 {
		return nil
	}
	sqrtD := d.Root2()
	twoC := m.int(2).Mul(c)
	denom := b.Negate().Add(sqrtD)
	if b.Signum() > 0 {
		denom = m.sub(b.Negate(), sqrtD)
	}
	result := twoC.Div(denom)
	return &result
}

// ChangeSpotPriceQuality generates an AMM offer so that either the updated
// Spot Price Quality (SPQ) equals the LOB quality, or the AMM offer quality
// equals the LOB quality.
//
// When ok is false, blocked distinguishes the two failure modes that rippled
// signals via different control flow: blocked=false is a plain calc failure
// (rippled returns std::nullopt, allowing a maxOffer fallback), while
// blocked=true means the generated offer's quality is worse than the LOB tip
// (rippled Throws, which suppresses the AMM offer entirely). blocked is only
// ever set on the pre-fixAMMv1_1 path; post-fix never throws.
// Reference: rippled AMMHelpers.h changeSpotPriceQuality()
func ChangeSpotPriceQuality(poolIn, poolOut tx.Amount, quality Quality, tfee uint16, fixAMMv1_1 bool, outIsXRP bool) (in, out tx.Amount, ok, blocked bool) {
	return changeSpotPriceQuality(legacyNumberMath(), poolIn, poolOut, quality, tfee, fixAMMv1_1, outIsXRP)
}

func changeSpotPriceQuality(m numberMath, poolIn, poolOut tx.Amount, quality Quality, tfee uint16, fixAMMv1_1 bool, outIsXRP bool) (in, out tx.Amount, ok, blocked bool) {
	if !fixAMMv1_1 {
		return changeSpotPriceQualityPreFix(m, poolIn, poolOut, quality, tfee)
	}

	// Post-fixAMMv1_1: start with the XRP side for better rounding
	if outIsXRP {
		in, out, ok = getAMMOfferStartWithTakerGets(m, poolIn, poolOut, quality, tfee)
	} else {
		in, out, ok = getAMMOfferStartWithTakerPays(m, poolIn, poolOut, quality, tfee)
	}
	return in, out, ok, false
}

// changeSpotPriceQualityPreFix is the pre-fixAMMv1_1 implementation.
// Solves: i^2*(1-fee) + i*I*(2-fee) + I^2 - I*O/quality = 0
// Reference: rippled AMMHelpers.h changeSpotPriceQuality() pre-amendment path
func changeSpotPriceQualityPreFix(m numberMath, poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok, blocked bool) {
	qRate := qualityToRate(m, quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false, false
	}

	// Convert to Number for uniform arithmetic
	nPoolIn := m.fromAmount(poolIn, state.RoundToNearest)
	nPoolOut := m.fromAmount(poolOut, state.RoundToNearest)

	f := m.feeMult(tfee, state.RoundToNearest)

	a := f
	onePlusF := m.add(m.one(), f)
	b := nPoolIn.Mul(onePlusF)
	poolInSq := nPoolIn.Mul(nPoolIn)
	poolInOutRate := nPoolIn.Mul(nPoolOut).Mul(qRate)
	c := m.sub(poolInSq, poolInOutRate)

	// Check discriminant
	disc := m.sub(b.Mul(b), m.int(4).Mul(a).Mul(c))
	if disc.Signum() < 0 {
		return tx.Amount{}, tx.Amount{}, false, false
	}

	sqrtDisc := disc.Root2()
	neg_b := b.Negate()
	nTakerPaysPropose := m.add(neg_b, sqrtDisc).Div(m.int(2).Mul(a))

	if nTakerPaysPropose.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false, false
	}

	// Constraint: i <= O / q - I / f
	constraint := m.sub(nPoolOut.Mul(qRate), nPoolIn.Div(f))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false, false
	}
	if nTakerPaysPropose.Cmp(constraint) > 0 {
		nTakerPaysPropose = constraint
	}

	// Round takerPays UP -- matches rippled's toAmount() with Number::upward
	takerPays := m.toAmountWithNativeRounding(nTakerPaysPropose, poolIn, state.RoundUpward, state.RoundToNearest)
	takerGets := swapAssetIn(m, poolIn, poolOut, takerPays, tfee, false)

	// If the generated offer quality is worse than the target and not within
	// the relative-distance tolerance, rippled Throws rather than returning
	// nullopt. The throw suppresses the AMM offer entirely (no maxOffer
	// fallback), leaving the AMM blocked by the LOB tip.
	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		rd := RelativeDistance(offerQ, quality)
		if rd >= 1e-7 {
			return tx.Amount{}, tx.Amount{}, false, true
		}
	}

	return takerPays, takerGets, true, false
}

// getAMMOfferStartWithTakerGets generates AMM offer starting with takerGets.
// Used when pool output is XRP (IOU->XRP pair).
// Reference: rippled AMMHelpers.h getAMMOfferStartWithTakerGets()
func getAMMOfferStartWithTakerGets(m numberMath, poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(m, quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// All quadratic solving below uses to_nearest, which is the default rounding
	// mode of the number* / amm* helpers (rippled: NumberRoundModeGuard
	// mg(Number::to_nearest)).

	// Convert to Number for uniform arithmetic
	nPoolIn := m.fromAmount(poolIn, state.RoundToNearest)
	nPoolOut := m.fromAmount(poolOut, state.RoundToNearest)

	f := m.feeMult(tfee, state.RoundToNearest)
	two := m.int(2)

	a := m.one()
	// b = poolIn * (1 - 1/f) / quality.rate() - 2 * poolOut
	oneOverF := m.one().Div(f)
	oneMinusOneOverF := m.sub(m.one(), oneOverF)
	bTerm1 := nPoolIn.Mul(oneMinusOneOverF).Div(qRate)
	bTerm2 := two.Mul(nPoolOut)
	b := m.sub(bTerm1, bTerm2)

	// c = poolOut^2 - poolIn * poolOut / quality.rate()
	poolOutSq := nPoolOut.Mul(nPoolOut)
	poolInOutRate := nPoolIn.Mul(nPoolOut).Div(qRate)
	c := m.sub(poolOutSq, poolInOutRate)

	nTakerGets := solveQuadraticEqSmallest(m, a, b, c)
	if nTakerGets == nil || nTakerGets.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: o = poolOut - poolIn / (quality.rate() * f)
	qRateTimesF := qRate.Mul(f)
	constraint := m.sub(nPoolOut, nPoolIn.Div(qRateTimesF))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Cmp(*nTakerGets) < 0 {
		nTakerGets = &constraint
	}

	// Round takerGets downward to minimize the offer.
	// Reference: rippled toAmount with Number::downward (line 229)
	takerGets := m.toAmountWithNativeRounding(*nTakerGets, poolOut, state.RoundDownward, state.RoundToNearest)
	takerPays := swapAssetOut(m, poolIn, poolOut, takerGets, tfee, true)

	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		reduced := reduceOffer(m, takerGets)
		takerGets = reduced
		takerPays = swapAssetOut(m, poolIn, poolOut, takerGets, tfee, true)
		offerQ = QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
		if offerQ.WorseThan(quality) {
			return tx.Amount{}, tx.Amount{}, false
		}
	}

	return takerPays, takerGets, true
}

// getAMMOfferStartWithTakerPays generates AMM offer starting with takerPays.
// Used when pool input is XRP or IOU/IOU pair.
// Reference: rippled AMMHelpers.h getAMMOfferStartWithTakerPays()
func getAMMOfferStartWithTakerPays(m numberMath, poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(m, quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// All quadratic solving below uses to_nearest, which is the default rounding
	// mode of the number* / amm* helpers (rippled: NumberRoundModeGuard
	// mg(Number::to_nearest)).

	// Convert to Number for uniform arithmetic
	nPoolIn := m.fromAmount(poolIn, state.RoundToNearest)
	nPoolOut := m.fromAmount(poolOut, state.RoundToNearest)

	f := m.feeMult(tfee, state.RoundToNearest)

	a := f
	onePlusF := m.add(m.one(), f)
	b := nPoolIn.Mul(onePlusF)
	poolInSq := nPoolIn.Mul(nPoolIn)
	poolInOutRate := nPoolIn.Mul(nPoolOut).Mul(qRate)
	c := m.sub(poolInSq, poolInOutRate)

	nTakerPays := solveQuadraticEqSmallest(m, a, b, c)
	if nTakerPays == nil || nTakerPays.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: i = poolOut * quality.rate() - poolIn / f
	constraint := m.sub(nPoolOut.Mul(qRate), nPoolIn.Div(f))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Cmp(*nTakerPays) < 0 {
		nTakerPays = &constraint
	}

	// Round takerPays downward to minimize the offer and maximize quality.
	// Reference: rippled toAmount with Number::downward (line 298-299)
	takerPays := m.toAmountWithNativeRounding(*nTakerPays, poolIn, state.RoundDownward, state.RoundToNearest)
	takerGets := swapAssetIn(m, poolIn, poolOut, takerPays, tfee, true)

	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		reduced := reduceOffer(m, takerPays)
		takerPays = reduced
		takerGets = swapAssetIn(m, poolIn, poolOut, takerPays, tfee, true)
		offerQ = QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
		if offerQ.WorseThan(quality) {
			return tx.Amount{}, tx.Amount{}, false
		}
	}

	return takerPays, takerGets, true
}

// reduceOffer reduces an amount by multiplying by 0.9999 (towards zero).
// Reference: rippled AMMHelpers.h detail::reduceOffer()
func reduceOffer(m numberMath, amount tx.Amount) tx.Amount {
	pct := m.number(9999, -4, state.RoundToNearest)
	n := m.fromAmount(amount, state.RoundToNearest)
	// towards_zero so the result is always less than amount or zero,
	// matching rippled detail::reduceOffer (AMMHelpers.h).
	return m.toAmountWithNativeRounding(
		n.MulRounded(pct, state.RoundTowardsZero),
		amount,
		state.RoundDownward,
		state.RoundToNearest,
	)
}

// WithinRelativeDistance checks if two qualities are within a relative distance threshold.
// Reference: rippled AMMHelpers.h withinRelativeDistance(Quality, Quality, Number)
func WithinRelativeDistance(q1, q2 Quality, threshold float64) bool {
	if q1.Value == q2.Value {
		return true
	}
	rd := RelativeDistance(q1, q2)
	return rd < threshold
}

// qualityToRate converts a Quality to its rate representation as an Amount.
// In rippled, quality.rate() calls amountFromQuality(m_value) which converts
// the stored uint64 directly to an STAmount -- no inversion.
// Quality stores in/out, so rate() returns in/out as an STAmount.
// Reference: rippled Quality.h rate() -> amountFromQuality()
func qualityToRate(m numberMath, q Quality) state.XRPLNumber {
	if q.Value == 0 {
		return m.zero()
	}

	storedExp := int(q.Value >> 56)
	mantissa := int64(q.Value & 0x00FFFFFFFFFFFFFF)

	if mantissa == 0 {
		return m.zero()
	}

	exponent := storedExp - 100

	return m.number(mantissa, exponent, state.RoundToNearest)
}

// ToEitherAmt converts a tx.Amount to an EitherAmount.
func ToEitherAmt(amt tx.Amount) EitherAmount {
	return toEitherAmt(amt)
}

// toEitherAmt converts a tx.Amount to an EitherAmount.
func toEitherAmt(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return NewXRPEitherAmount(amt.Drops())
	}
	if amt.IsMPT() {
		id, ok := decodeMPTID(amt.MPTIssuanceID())
		if !ok {
			return EitherAmount{}
		}
		value, _ := amt.MPTRaw()
		return NewMPTEitherAmount(value, id)
	}
	return NewIOUEitherAmount(amt)
}

// zeroLikeAmount returns a zero amount matching the type (XRP or IOU) of the input.
func zeroLikeAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(0)
	}
	if amt.IsMPT() {
		return state.NewMPTAmountWithIssuanceID(0, amt.Issuer, amt.MPTIssuanceID())
	}
	return state.NewIssuedAmountFromValue(0, -100, amt.Currency, amt.Issuer)
}

// maxAmountLike returns the maximum amount for the type of the input, using
// cMaxValue/2 to match rippled's maxAmount<IOUAmount>() / maxAmount<STAmount>().
// Used by maxOffer() in AMMLiquidity for pre-fixAMMOverflowOffer fallback.
// Reference: rippled AMMLiquidity.cpp lines 99-109: maxAmount<T>()
func maxAmountLike(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(math.MaxInt64)
	}
	if amt.IsMPT() {
		return state.NewMPTAmountWithIssuanceID(math.MaxInt64, amt.Issuer, amt.MPTIssuanceID())
	}
	// Max IOU: mantissa = cMaxValue/2 = 9999999999999999/2 = 4999999999999999
	// exponent = cMaxOffset = 80
	// Reference: rippled AMMLiquidity.cpp line 106:
	//   return IOUAmount(STAmount::cMaxValue / 2, STAmount::cMaxOffset);
	return state.NewIssuedAmountFromValue(4999999999999999, 80, amt.Currency, amt.Issuer)
}

// toMaxAmount returns the full maximum amount for the type of the input.
// Uses cMaxValue (not halved) to match rippled's toMaxAmount<T>() from
// AmountConversions.h, used by swapAssetOut when denominator <= 0.
// Reference: rippled AmountConversions.h lines 153-173: toMaxAmount<T>()
func toMaxAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(math.MaxInt64)
	}
	if amt.IsMPT() {
		return state.NewMPTAmountWithIssuanceID(math.MaxInt64, amt.Issuer, amt.MPTIssuanceID())
	}
	// Full max IOU: mantissa = cMaxValue = 9999999999999999, exponent = cMaxOffset = 80
	return state.NewIssuedAmountFromValue(9999999999999999, 80, amt.Currency, amt.Issuer)
}
