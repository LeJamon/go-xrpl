package payment

// AMM swap math functions matching rippled's AMMHelpers.h
// These are used by AMMLiquidity and AMMOffer to generate synthetic offers
// and calculate pool-conserving swaps.
//
// Reference: rippled/src/xrpld/app/misc/AMMHelpers.h

import (
	"math"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
)

// ammOne returns 1 as an IOU Amount for arithmetic.
func ammOne() tx.Amount {
	return state.NewIssuedAmountFromValue(1e15, -15, "", "")
}

// toNumber converts any tx.Amount (XRP or IOU) to an IOU-like representation
// suitable for AMM arithmetic. In rippled, all AMM math operates through the
// unified Number type. XRP drops are converted to IOU with mantissa=drops, exponent=0.
// Reference: rippled XRPAmount::operator Number() { return drops(); }
// and Number::Number(rep mantissa) : Number{mantissa, 0} {}
func toNumber(amt tx.Amount) tx.Amount {
	if !amt.IsNative() {
		return amt
	}
	drops := amt.Drops()
	if drops == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	return state.NewIssuedAmountFromValue(drops, 0, "", "")
}

// fromNumber converts an IOU-like number back to the original amount type.
// If the original was XRP, converts back to XRP drops using precise integer arithmetic.
// If the original was IOU, restores the currency/issuer.
// Reference: rippled STAmount::operator=(Number const&) for Number->STAmount conversion
func fromNumber(num tx.Amount, original tx.Amount) tx.Amount {
	if original.IsNative() {
		// Convert IOU-like number back to XRP drops.
		// The Number has mantissa and exponent -- compute drops = mantissa * 10^exponent
		if num.IsNative() {
			return num
		}
		mantissa := num.Mantissa()
		exponent := num.Exponent()
		if mantissa == 0 {
			return state.NewXRPAmountFromInt(0)
		}
		drops := mantissa
		if exponent > 0 {
			for i := 0; i < exponent; i++ {
				drops *= 10
			}
		} else if exponent < 0 {
			for i := 0; i < -exponent; i++ {
				drops /= 10
			}
		}
		return state.NewXRPAmountFromInt(drops)
	}
	// Restore currency/issuer from original
	if num.IsNative() {
		return state.NewIssuedAmountFromValue(0, -100, original.Currency, original.Issuer)
	}
	return state.NewIssuedAmountFromValue(num.Mantissa(), num.Exponent(), original.Currency, original.Issuer)
}

// fromNumberRoundUp converts a Number back to the original amount type, rounding up.
// For XRP: rounds drops upward (ceil) instead of truncating.
// Reference: rippled toAmount() with Number::upward
func fromNumberRoundUp(num tx.Amount, original tx.Amount) tx.Amount {
	return fromNumberWithGuard(num, original, state.RoundUpward)
}

// fromNumberWithGuard converts an IOU-like Number back to the original amount type,
// using the XRPLNumber Guard mechanism to match rippled's Number::operator rep()
// for correct rounding when converting to XRP drops.
// For IOU amounts, the rounding mode only affects XRP conversion (per rippled's
// toAmount<T>() which only sets the rounding mode for XRP issues).
// Reference: rippled AmountConversions.h toAmount<T>() lines 125-151
//
//	and Number.cpp Number::operator rep() lines 480-512
func fromNumberWithGuard(num tx.Amount, original tx.Amount, mode state.RoundingMode) tx.Amount {
	if original.IsNative() {
		if num.IsNative() {
			return num
		}
		mantissa := num.Mantissa()
		exponent := num.Exponent()
		if mantissa == 0 {
			return state.NewXRPAmountFromInt(0)
		}
		// Use XRPLNumber's ToInt64WithMode for Guard-based conversion.
		// This matches rippled's Number::operator rep() with rounding mode.
		n := state.NewXRPLNumber(mantissa, exponent)
		drops := n.ToInt64WithMode(mode)
		return state.NewXRPAmountFromInt(drops)
	}
	// For IOU, just restore currency/issuer. The rounding mode does not apply.
	return fromNumber(num, original)
}

// ammAdd adds two amounts, ignoring errors (types always match in AMM math).
func ammAdd(a, b tx.Amount) tx.Amount {
	r, _ := a.Add(b)
	return r
}

// ammSub subtracts b from a, ignoring errors.
func ammSub(a, b tx.Amount) tx.Amount {
	r, _ := a.Sub(b)
	return r
}

// numberMul multiplies two IOU-like amounts using XRPLNumber arithmetic.
// This ensures Guard-based rounding is used, respecting the global rounding mode.
// Reference: rippled Number::operator*= in Number.cpp
func numberMul(a, b tx.Amount) tx.Amount {
	na := state.NewXRPLNumber(a.Mantissa(), a.Exponent())
	nb := state.NewXRPLNumber(b.Mantissa(), b.Exponent())
	result := na.Mul(nb)
	iou := result.ToIOUAmountValue()
	return state.NewIssuedAmountFromValue(iou.Mantissa(), iou.Exponent(), a.Currency, a.Issuer)
}

// numberDiv divides two IOU-like amounts using XRPLNumber arithmetic.
// This ensures Guard-based rounding is used, respecting the global rounding mode.
// Amount.Div() does NOT use XRPLNumber even when NumberSwitchover is on;
// this function provides the correct Number/Guard-based division for AMM math.
// Reference: rippled Number::operator/= in Number.cpp
func numberDiv(a, b tx.Amount) tx.Amount {
	na := state.NewXRPLNumber(a.Mantissa(), a.Exponent())
	nb := state.NewXRPLNumber(b.Mantissa(), b.Exponent())
	result := na.Div(nb)
	iou := result.ToIOUAmountValue()
	return state.NewIssuedAmountFromValue(iou.Mantissa(), iou.Exponent(), a.Currency, a.Issuer)
}

// AMMFeeMult returns (1 - tfee/100000) as a fee multiplier.
// tfee is in basis points (e.g., 500 = 0.5%).
// Reference: rippled AMMCore.h feeMult()
func AMMFeeMult(tfee uint16) tx.Amount {
	fee := AMMGetFee(tfee)
	return ammSub(ammOne(), fee)
}

// AMMFeeMultHalf returns (1 - tfee/200000).
// Reference: rippled AMMCore.h feeMultHalf()
func AMMFeeMultHalf(tfee uint16) tx.Amount {
	halfFee := state.NewIssuedAmountFromValue(int64(tfee), 0, "", "")
	denom := state.NewIssuedAmountFromValue(2e15, -10, "", "") // 200000
	result := numberDiv(halfFee, denom)
	return ammSub(ammOne(), result)
}

// AMMGetFee returns tfee/100000 as an Amount.
// Reference: rippled AMMCore.h getFee()
func AMMGetFee(tfee uint16) tx.Amount {
	if tfee == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	numerator := state.NewIssuedAmountFromValue(int64(tfee), 0, "", "")
	denominator := state.NewIssuedAmountFromValue(1e15, -10, "", "") // 100000
	return numberDiv(numerator, denominator)
}

// SwapAssetIn calculates how much you get out when swapping assetIn into the pool.
// Formula: out = poolOut - (poolIn * poolOut) / (poolIn + assetIn * feeMult(tfee))
// With fixAMMv1_1: explicit rounding to favor the AMM (minimize output).
// All arithmetic is done in IOU-like Number representation to handle mixed XRP/IOU.
// Reference: rippled AMMHelpers.h swapAssetIn()
func SwapAssetIn(poolIn, poolOut, assetIn tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	// Convert to Number (IOU) representation for arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)
	nAssetIn := toNumber(assetIn)

	if fixAMMv1_1 {
		// Save and restore rounding mode -- rippled uses saveNumberRoundMode RAII.
		// Reference: rippled AMMHelpers.h swapAssetIn() lines 493-514
		savedMode := state.SetNumberRound(state.RoundUpward)
		defer state.SetNumberRound(savedMode)

		// Number::setround(Number::upward)
		state.SetNumberRound(state.RoundUpward)
		numerator := numberMul(nPoolIn, nPoolOut)
		fee := AMMGetFee(tfee)

		// Number::setround(Number::downward)
		state.SetNumberRound(state.RoundDownward)
		fMult := ammSub(ammOne(), fee)
		assetFee := numberMul(nAssetIn, fMult)
		denom := ammAdd(nPoolIn, assetFee)

		if denom.Signum() <= 0 {
			return zeroLikeAmount(poolOut)
		}

		// Number::setround(Number::upward)
		state.SetNumberRound(state.RoundUpward)
		ratio := numberDiv(numerator, denom)

		// Number::setround(Number::downward)
		state.SetNumberRound(state.RoundDownward)
		swapOut := ammSub(nPoolOut, ratio)

		if swapOut.Signum() < 0 {
			return zeroLikeAmount(poolOut)
		}
		// toAmount with Number::downward
		return fromNumberWithGuard(swapOut, poolOut, state.RoundDownward)
	}

	// Pre-fixAMMv1_1: simple formula using Number arithmetic throughout.
	// In rippled, pool.in/pool.out/assetIn are all Number types, so all
	// operations (*, /, +, -) use Number::operator which has 16-digit
	// mantissa with guard-based rounding. We must use numberMul/numberDiv
	// (not Amount.Mul/Amount.Div which use STAmount::multiply/divide).
	// Reference: rippled AMMHelpers.h swapAssetIn() pre-amendment path
	fMult := AMMFeeMult(tfee)
	assetFee := numberMul(nAssetIn, fMult)
	denom := ammAdd(nPoolIn, assetFee)
	if denom.IsZero() {
		return zeroLikeAmount(poolOut)
	}
	numerator := numberMul(nPoolIn, nPoolOut)
	ratio := numberDiv(numerator, denom)
	result := ammSub(nPoolOut, ratio)
	if result.Signum() < 0 {
		return zeroLikeAmount(poolOut)
	}
	return fromNumberWithGuard(result, poolOut, state.RoundDownward)
}

// SwapAssetOut calculates how much you must put in to get assetOut from the pool.
// Formula: in = ((poolIn * poolOut) / (poolOut - assetOut) - poolIn) / feeMult(tfee)
// With fixAMMv1_1: explicit rounding to favor the AMM (maximize input).
// All arithmetic is done in IOU-like Number representation to handle mixed XRP/IOU.
// Reference: rippled AMMHelpers.h swapAssetOut()
func SwapAssetOut(poolIn, poolOut, assetOut tx.Amount, tfee uint16, fixAMMv1_1 bool) tx.Amount {
	// Convert to Number (IOU) representation for arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)
	nAssetOut := toNumber(assetOut)

	if fixAMMv1_1 {
		// Save and restore rounding mode -- rippled uses saveNumberRoundMode RAII.
		// Reference: rippled AMMHelpers.h swapAssetOut() lines 562-587
		savedMode := state.SetNumberRound(state.RoundUpward)
		defer state.SetNumberRound(savedMode)

		// Number::setround(Number::upward)
		state.SetNumberRound(state.RoundUpward)
		numerator := numberMul(nPoolIn, nPoolOut)

		// Number::setround(Number::downward)
		state.SetNumberRound(state.RoundDownward)
		denom := ammSub(nPoolOut, nAssetOut)
		if denom.Signum() <= 0 {
			return toMaxAmount(poolIn)
		}

		// Number::setround(Number::upward)
		state.SetNumberRound(state.RoundUpward)
		ratio := numberDiv(numerator, denom)
		numerator2 := ammSub(ratio, nPoolIn)
		fee := AMMGetFee(tfee)

		// Number::setround(Number::downward)
		state.SetNumberRound(state.RoundDownward)
		fMult := ammSub(ammOne(), fee)

		// Number::setround(Number::upward)
		state.SetNumberRound(state.RoundUpward)
		swapIn := numberDiv(numerator2, fMult)

		if swapIn.Signum() < 0 {
			return zeroLikeAmount(poolIn)
		}
		// toAmount with Number::upward
		return fromNumberWithGuard(swapIn, poolIn, state.RoundUpward)
	}

	// Pre-fixAMMv1_1: simple formula using Number arithmetic throughout.
	// In rippled, pool.in/pool.out/assetOut are all Number types, so all
	// operations use Number::operator (16-digit mantissa, guard-based).
	// We must use numberMul/numberDiv (not Amount.Mul/Amount.Div).
	// Reference: rippled AMMHelpers.h swapAssetOut() pre-amendment path
	fMult := AMMFeeMult(tfee)
	denom := ammSub(nPoolOut, nAssetOut)
	if denom.IsZero() || denom.Signum() < 0 {
		return toMaxAmount(poolIn)
	}
	numerator := numberMul(nPoolIn, nPoolOut)
	ratio := numberDiv(numerator, denom)
	diff := ammSub(ratio, nPoolIn)
	result := numberDiv(diff, fMult)
	if result.Signum() < 0 {
		return zeroLikeAmount(poolIn)
	}
	return fromNumberWithGuard(result, poolIn, state.RoundUpward)
}

// SolveQuadraticEq computes (-b + sqrt(b^2 - 4*a*c)) / (2*a).
// Reference: rippled AMMHelpers.cpp solveQuadraticEq()
func SolveQuadraticEq(a, b, c tx.Amount) tx.Amount {
	b2 := numberMul(b, b)
	four := state.NewIssuedAmountFromValue(4e15, -15, "", "")
	ac4 := numberMul(numberMul(four, a), c)
	d := ammSub(b2, ac4)

	sqrtD := d.Sqrt()

	neg_b := b.Negate()
	num := ammAdd(neg_b, sqrtD)
	two := state.NewIssuedAmountFromValue(2e15, -15, "", "")
	denom := numberMul(two, a)
	return numberDiv(num, denom)
}

// SolveQuadraticEqSmallest uses the citardauq formula for better numerical stability.
// Returns the smallest positive root, or nil if discriminant < 0.
// Reference: rippled AMMHelpers.cpp solveQuadraticEqSmallest()
func SolveQuadraticEqSmallest(a, b, c tx.Amount) *tx.Amount {
	b2 := numberMul(b, b)
	four := state.NewIssuedAmountFromValue(4e15, -15, "", "")
	ac4 := numberMul(numberMul(four, a), c)
	d := ammSub(b2, ac4)

	if d.Signum() < 0 {
		return nil
	}

	sqrtD := d.Sqrt()

	twoC := numberMul(state.NewIssuedAmountFromValue(2e15, -15, "", ""), c)

	var result tx.Amount
	if b.Signum() > 0 {
		neg_b := b.Negate()
		denom := ammSub(neg_b, sqrtD)
		result = numberDiv(twoC, denom)
	} else {
		neg_b := b.Negate()
		denom := ammAdd(neg_b, sqrtD)
		result = numberDiv(twoC, denom)
	}

	return &result
}

// ChangeSpotPriceQuality generates an AMM offer so that either the updated
// Spot Price Quality (SPQ) equals the LOB quality, or the AMM offer quality
// equals the LOB quality.
// Reference: rippled AMMHelpers.h changeSpotPriceQuality()
func ChangeSpotPriceQuality(poolIn, poolOut tx.Amount, quality Quality, tfee uint16, fixAMMv1_1 bool, outIsXRP bool) (in, out tx.Amount, ok bool) {
	if !fixAMMv1_1 {
		return changeSpotPriceQualityPreFix(poolIn, poolOut, quality, tfee)
	}

	// Post-fixAMMv1_1: start with the XRP side for better rounding
	if outIsXRP {
		return getAMMOfferStartWithTakerGets(poolIn, poolOut, quality, tfee)
	}
	return getAMMOfferStartWithTakerPays(poolIn, poolOut, quality, tfee)
}

// changeSpotPriceQualityPreFix is the pre-fixAMMv1_1 implementation.
// Solves: i^2*(1-fee) + i*I*(2-fee) + I^2 - I*O/quality = 0
// Reference: rippled AMMHelpers.h changeSpotPriceQuality() pre-amendment path
func changeSpotPriceQualityPreFix(poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Convert to Number for uniform arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)

	f := AMMFeeMult(tfee)

	a := f
	onePlusF := ammAdd(ammOne(), f)
	b := nPoolIn.Mul(onePlusF, false)
	poolInSq := nPoolIn.Mul(nPoolIn, false)
	poolInOutRate := nPoolIn.Mul(nPoolOut, false).Mul(qRate, false)
	c := ammSub(poolInSq, poolInOutRate)

	// Check discriminant
	four := state.NewIssuedAmountFromValue(4e15, -15, "", "")
	disc := ammSub(b.Mul(b, false), four.Mul(a, false).Mul(c, false))
	if disc.Signum() < 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	sqrtDisc := disc.Sqrt()
	neg_b := b.Negate()
	two := state.NewIssuedAmountFromValue(2e15, -15, "", "")
	nTakerPaysPropose := ammAdd(neg_b, sqrtDisc).Div(two.Mul(a, false), false)

	if nTakerPaysPropose.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: i <= O / q - I / f
	constraint := ammSub(nPoolOut.Mul(qRate, false), nPoolIn.Div(f, false))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}
	if nTakerPaysPropose.Compare(constraint) > 0 {
		nTakerPaysPropose = constraint
	}

	// Round takerPays UP -- matches rippled's toAmount() with Number::upward
	takerPays := fromNumberRoundUp(nTakerPaysPropose, poolIn)
	takerGets := SwapAssetIn(poolIn, poolOut, takerPays, tfee, false)

	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		rd := RelativeDistance(offerQ, quality)
		if rd >= 1e-7 {
			return tx.Amount{}, tx.Amount{}, false
		}
	}

	return takerPays, takerGets, true
}

// getAMMOfferStartWithTakerGets generates AMM offer starting with takerGets.
// Used when pool output is XRP (IOU->XRP pair).
// Reference: rippled AMMHelpers.h getAMMOfferStartWithTakerGets()
func getAMMOfferStartWithTakerGets(poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// NumberRoundModeGuard mg(Number::to_nearest) -- all quadratic solving uses to_nearest
	savedMode := state.SetNumberRound(state.RoundToNearest)
	defer state.SetNumberRound(savedMode)

	// Convert to Number for uniform arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)

	f := AMMFeeMult(tfee)
	two := state.NewIssuedAmountFromValue(2e15, -15, "", "")

	a := ammOne()
	// b = poolIn * (1 - 1/f) / quality.rate() - 2 * poolOut
	oneOverF := numberDiv(ammOne(), f)
	oneMinusOneOverF := ammSub(ammOne(), oneOverF)
	bTerm1 := numberDiv(numberMul(nPoolIn, oneMinusOneOverF), qRate)
	bTerm2 := numberMul(two, nPoolOut)
	b := ammSub(bTerm1, bTerm2)

	// c = poolOut^2 - poolIn * poolOut / quality.rate()
	poolOutSq := numberMul(nPoolOut, nPoolOut)
	poolInOutRate := numberDiv(numberMul(nPoolIn, nPoolOut), qRate)
	c := ammSub(poolOutSq, poolInOutRate)

	nTakerGets := SolveQuadraticEqSmallest(a, b, c)
	if nTakerGets == nil || nTakerGets.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: o = poolOut - poolIn / (quality.rate() * f)
	qRateTimesF := numberMul(qRate, f)
	constraint := ammSub(nPoolOut, numberDiv(nPoolIn, qRateTimesF))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Compare(*nTakerGets) < 0 {
		nTakerGets = &constraint
	}

	// Round takerGets downward to minimize the offer.
	// Reference: rippled toAmount with Number::downward (line 229)
	takerGets := fromNumberWithGuard(*nTakerGets, poolOut, state.RoundDownward)
	takerPays := SwapAssetOut(poolIn, poolOut, takerGets, tfee, true)

	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		reduced := reduceOffer(takerGets)
		takerGets = reduced
		takerPays = SwapAssetOut(poolIn, poolOut, takerGets, tfee, true)
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
func getAMMOfferStartWithTakerPays(poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// NumberRoundModeGuard mg(Number::to_nearest) -- all quadratic solving uses to_nearest
	savedMode := state.SetNumberRound(state.RoundToNearest)
	defer state.SetNumberRound(savedMode)

	// Convert to Number for uniform arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)

	f := AMMFeeMult(tfee)

	a := f
	onePlusF := ammAdd(ammOne(), f)
	b := numberMul(nPoolIn, onePlusF)
	poolInSq := numberMul(nPoolIn, nPoolIn)
	poolInOutRate := numberMul(numberMul(nPoolIn, nPoolOut), qRate)
	c := ammSub(poolInSq, poolInOutRate)

	nTakerPays := SolveQuadraticEqSmallest(a, b, c)
	if nTakerPays == nil || nTakerPays.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: i = poolOut * quality.rate() - poolIn / f
	constraint := ammSub(numberMul(nPoolOut, qRate), numberDiv(nPoolIn, f))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Compare(*nTakerPays) < 0 {
		nTakerPays = &constraint
	}

	// Round takerPays downward to minimize the offer and maximize quality.
	// Reference: rippled toAmount with Number::downward (line 298-299)
	takerPays := fromNumberWithGuard(*nTakerPays, poolIn, state.RoundDownward)
	takerGets := SwapAssetIn(poolIn, poolOut, takerPays, tfee, true)

	offerQ := QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
	if offerQ.WorseThan(quality) {
		reduced := reduceOffer(takerPays)
		takerPays = reduced
		takerGets = SwapAssetIn(poolIn, poolOut, takerPays, tfee, true)
		offerQ = QualityFromAmounts(toEitherAmt(takerPays), toEitherAmt(takerGets))
		if offerQ.WorseThan(quality) {
			return tx.Amount{}, tx.Amount{}, false
		}
	}

	return takerPays, takerGets, true
}

// reduceOffer reduces an amount by multiplying by 0.9999 (towards zero).
// Reference: rippled AMMHelpers.h detail::reduceOffer()
func reduceOffer(amount tx.Amount) tx.Amount {
	pct := state.NewIssuedAmountFromValue(9999e12, -16, "", "") // 0.9999
	n := toNumber(amount)
	return fromNumber(n.Mul(pct, false), amount)
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
func qualityToRate(q Quality) tx.Amount {
	if q.Value == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	storedExp := int(q.Value >> 56)
	mantissa := int64(q.Value & 0x00FFFFFFFFFFFFFF)

	if mantissa == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	exponent := storedExp - 100

	return state.NewIssuedAmountFromValue(mantissa, exponent, "", "")
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
	return NewIOUEitherAmount(amt)
}

// zeroLikeAmount returns a zero amount matching the type (XRP or IOU) of the input.
func zeroLikeAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(0)
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
	// Full max IOU: mantissa = cMaxValue = 9999999999999999, exponent = cMaxOffset = 80
	return state.NewIssuedAmountFromValue(9999999999999999, 80, amt.Currency, amt.Issuer)
}
