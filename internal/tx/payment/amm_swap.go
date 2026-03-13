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
// Reference: rippled STAmount::operator=(Number const&) for Number→STAmount conversion
func fromNumber(num tx.Amount, original tx.Amount) tx.Amount {
	if original.IsNative() {
		// Convert IOU-like number back to XRP drops.
		// The Number has mantissa and exponent — compute drops = mantissa * 10^exponent
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
	if original.IsNative() {
		if num.IsNative() {
			return num
		}
		mantissa := num.Mantissa()
		exponent := num.Exponent()
		if mantissa == 0 {
			return state.NewXRPAmountFromInt(0)
		}
		drops := mantissa
		hasRemainder := false
		if exponent > 0 {
			for i := 0; i < exponent; i++ {
				drops *= 10
			}
		} else if exponent < 0 {
			for i := 0; i < -exponent; i++ {
				if drops%10 != 0 {
					hasRemainder = true
				}
				drops /= 10
			}
			// Round up if any division had a remainder
			if hasRemainder {
				drops++
			}
		}
		return state.NewXRPAmountFromInt(drops)
	}
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
	result := halfFee.Div(denom, false)
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
	return numerator.Div(denominator, false)
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
		numerator := nPoolIn.Mul(nPoolOut, true) // round up

		fee := AMMGetFee(tfee)
		fMult := ammSub(ammOne(), fee)

		assetFee := nAssetIn.Mul(fMult, false) // round down
		denom := ammAdd(nPoolIn, assetFee)

		if denom.Signum() <= 0 {
			return zeroLikeAmount(poolOut)
		}

		ratio := numerator.Div(denom, true) // round up
		swapOut := ammSub(nPoolOut, ratio)

		if swapOut.Signum() < 0 {
			return zeroLikeAmount(poolOut)
		}
		return fromNumber(swapOut, poolOut)
	}

	// Pre-fixAMMv1_1: simple formula
	fMult := AMMFeeMult(tfee)
	assetFee := nAssetIn.Mul(fMult, false)
	denom := ammAdd(nPoolIn, assetFee)
	if denom.IsZero() {
		return zeroLikeAmount(poolOut)
	}
	numerator := nPoolIn.Mul(nPoolOut, false)
	ratio := numerator.Div(denom, false)
	result := ammSub(nPoolOut, ratio)
	if result.Signum() < 0 {
		return zeroLikeAmount(poolOut)
	}
	return fromNumber(result, poolOut)
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
		numerator := nPoolIn.Mul(nPoolOut, true) // round up

		denom := ammSub(nPoolOut, nAssetOut)
		if denom.Signum() <= 0 {
			return maxAmountLike(poolIn)
		}

		ratio := numerator.Div(denom, true) // round up
		numerator2 := ammSub(ratio, nPoolIn)

		fee := AMMGetFee(tfee)
		fMult := ammSub(ammOne(), fee)

		swapIn := numerator2.Div(fMult, true) // round up

		if swapIn.Signum() < 0 {
			return zeroLikeAmount(poolIn)
		}
		return fromNumber(swapIn, poolIn)
	}

	// Pre-fixAMMv1_1: simple formula
	fMult := AMMFeeMult(tfee)
	denom := ammSub(nPoolOut, nAssetOut)
	if denom.IsZero() || denom.Signum() < 0 {
		return maxAmountLike(poolIn)
	}
	numerator := nPoolIn.Mul(nPoolOut, false)
	ratio := numerator.Div(denom, false)
	diff := ammSub(ratio, nPoolIn)
	result := diff.Div(fMult, true) // round up
	if result.Signum() < 0 {
		return zeroLikeAmount(poolIn)
	}
	return fromNumber(result, poolIn)
}

// SolveQuadraticEq computes (-b + sqrt(b^2 - 4*a*c)) / (2*a).
// Reference: rippled AMMHelpers.cpp solveQuadraticEq()
func SolveQuadraticEq(a, b, c tx.Amount) tx.Amount {
	b2 := b.Mul(b, false)
	four := state.NewIssuedAmountFromValue(4e15, -15, "", "")
	ac4 := four.Mul(a, false).Mul(c, false)
	d := ammSub(b2, ac4)

	sqrtD := d.Sqrt()

	neg_b := b.Negate()
	num := ammAdd(neg_b, sqrtD)
	two := state.NewIssuedAmountFromValue(2e15, -15, "", "")
	denom := two.Mul(a, false)
	return num.Div(denom, false)
}

// SolveQuadraticEqSmallest uses the citardauq formula for better numerical stability.
// Returns the smallest positive root, or nil if discriminant < 0.
// Reference: rippled AMMHelpers.cpp solveQuadraticEqSmallest()
func SolveQuadraticEqSmallest(a, b, c tx.Amount) *tx.Amount {
	b2 := b.Mul(b, false)
	four := state.NewIssuedAmountFromValue(4e15, -15, "", "")
	ac4 := four.Mul(a, false).Mul(c, false)
	d := ammSub(b2, ac4)

	if d.Signum() < 0 {
		return nil
	}

	sqrtD := d.Sqrt()

	twoC := state.NewIssuedAmountFromValue(2e15, -15, "", "").Mul(c, false)

	var result tx.Amount
	if b.Signum() > 0 {
		neg_b := b.Negate()
		denom := ammSub(neg_b, sqrtD)
		result = twoC.Div(denom, false)
	} else {
		neg_b := b.Negate()
		denom := ammAdd(neg_b, sqrtD)
		result = twoC.Div(denom, false)
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

	// Round takerPays UP — matches rippled's toAmount() with Number::upward
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
// Used when pool output is XRP (IOU→XRP pair).
// Reference: rippled AMMHelpers.h getAMMOfferStartWithTakerGets()
func getAMMOfferStartWithTakerGets(poolIn, poolOut tx.Amount, quality Quality, tfee uint16) (in, out tx.Amount, ok bool) {
	qRate := qualityToRate(quality)
	if qRate.IsZero() {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Convert to Number for uniform arithmetic
	nPoolIn := toNumber(poolIn)
	nPoolOut := toNumber(poolOut)

	f := AMMFeeMult(tfee)
	two := state.NewIssuedAmountFromValue(2e15, -15, "", "")

	a := ammOne()
	// b = poolIn * (1 - 1/f) / quality.rate() - 2 * poolOut
	oneOverF := ammOne().Div(f, false)
	oneMinusOneOverF := ammSub(ammOne(), oneOverF)
	bTerm1 := nPoolIn.Mul(oneMinusOneOverF, false).Div(qRate, false)
	bTerm2 := two.Mul(nPoolOut, false)
	b := ammSub(bTerm1, bTerm2)

	// c = poolOut^2 - poolIn * poolOut / quality.rate()
	poolOutSq := nPoolOut.Mul(nPoolOut, false)
	poolInOutRate := nPoolIn.Mul(nPoolOut, false).Div(qRate, false)
	c := ammSub(poolOutSq, poolInOutRate)

	nTakerGets := SolveQuadraticEqSmallest(a, b, c)
	if nTakerGets == nil || nTakerGets.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: o = poolOut - poolIn / (quality.rate() * f)
	qRateTimesF := qRate.Mul(f, false)
	constraint := ammSub(nPoolOut, nPoolIn.Div(qRateTimesF, false))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Compare(*nTakerGets) < 0 {
		nTakerGets = &constraint
	}

	takerGets := fromNumber(*nTakerGets, poolOut)
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

	nTakerPays := SolveQuadraticEqSmallest(a, b, c)
	if nTakerPays == nil || nTakerPays.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	// Constraint: i = poolOut * quality.rate() - poolIn / f
	constraint := ammSub(nPoolOut.Mul(qRate, false), nPoolIn.Div(f, false))
	if constraint.Signum() <= 0 {
		return tx.Amount{}, tx.Amount{}, false
	}

	if constraint.Compare(*nTakerPays) < 0 {
		nTakerPays = &constraint
	}

	takerPays := fromNumber(*nTakerPays, poolIn)
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
// the stored uint64 directly to an STAmount — no inversion.
// Quality stores in/out, so rate() returns in/out as an STAmount.
// Reference: rippled Quality.h rate() → amountFromQuality()
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

// maxAmountLike returns the maximum amount for the type of the input.
func maxAmountLike(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return state.NewXRPAmountFromInt(math.MaxInt64)
	}
	// Max IOU: mantissa = 9999999999999999 (cMaxValue), exponent = 80 (cMaxOffset)
	// Reference: rippled STAmount.h cMaxValue = 9999999999999999, cMaxOffset = 80
	return state.NewIssuedAmountFromValue(9999999999999999, 80, amt.Currency, amt.Issuer)
}
