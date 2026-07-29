package state

import (
	"fmt"
	"math/big"
)

// Add returns a + b.
//
// Reference: rippled STAmount.cpp:387-390 throws "Can't add amounts
// that are't comparable!" whenever areComparable(v1, v2)
// (STAmount.cpp:132-141) returns false. For two Issue amounts that
// predicate compares native-ness AND currency, but deliberately NOT
// issuer; operator+ then tags the result with v1's currency and issuer
// (STAmount.cpp:395-401).
//
// go-xrpl's Amount doubles as both rippled's STAmount (a currency-tagged
// ledger value) and its Number (the unitless type the AMM math computes
// on — AMMHelpers.cpp). The AMM converts to currency-less amounts for
// calculation (toIOUForCalc / oneAmount / numAmount all leave Currency
// ""), then reapplies the real issue at the STAmount boundary. An
// empty Currency therefore marks the Number namespace, which has no
// areComparable gate. We mirror rippled's split: the native leg is
// always rejected, and a currency mismatch is rejected only when BOTH
// operands carry a real (non-empty) currency — catching mis-keyed
// ledger amounts (USD + EUR) without disturbing unitless arithmetic.
// An issuer mismatch stays tolerated, matching areComparable; the
// RippleState LowLimit/HighLimit reads DirectStepI's creditLimit
// consumes do not normalise the issuer the way rippled View.cpp:469-484
// does. The result is tagged with `a`'s currency and issuer.
func (a Amount) Add(b Amount) (Amount, error) {
	return a.AddRounded(b, RoundToNearest)
}

// AddRounded returns a + b, rounding the IOU result under mode. The AMM math
// uses this to reproduce rippled's NumberRoundModeGuard around additions.
func (a Amount) AddRounded(b Amount, mode RoundingMode) (Amount, error) {
	return a.addRounded(b, mode, NewNumberContext(MantissaScaleSmall, false))
}

// AddUniversal returns a + b using rippled 3.2's universal Number arithmetic.
func (a Amount) AddUniversal(b Amount) (Amount, error) {
	return a.addRounded(b, RoundToNearest, NewNumberContext(MantissaScaleLarge, true))
}

// AddWithNumberContext returns a + b using the transaction's Number scale.
func (a Amount) AddWithNumberContext(b Amount, ctx NumberContext, mode RoundingMode) (Amount, error) {
	return a.addRounded(b, mode, ctx)
}

func (a Amount) addRounded(b Amount, mode RoundingMode, ctx NumberContext) (Amount, error) {
	if a.mptRaw != nil || b.mptRaw != nil {
		if a.mptRaw == nil || b.mptRaw == nil {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot add MPT and non-MPT amounts")
		}
		if a.mptIssuanceID != b.mptIssuanceID {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot add different MPT issuances")
		}
		result := new(big.Int).Add(big.NewInt(*a.mptRaw), big.NewInt(*b.mptRaw))
		if !result.IsInt64() {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: MPT addition overflow")
		}
		return newMPTAmountLike(a, result.Int64()), nil
	}
	if a.IsNative() != b.IsNative() {
		return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot add XRP and IOU amounts")
	}
	if !a.IsNative() && a.Currency != "" && b.Currency != "" && a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot add amounts with different currencies")
	}
	if a.IsNative() {
		return Amount{
			xrp:    a.xrp.Add(b.xrp),
			Native: true,
		}, nil
	}
	result := addIOUValuesRoundedWithContext(a.iou, b.iou, mode, ctx)
	return Amount{
		iou:      result,
		Currency: a.Currency,
		Issuer:   a.Issuer,
		Native:   false,
	}, nil
}

// Sub subtracts two amounts (must be same type)
func (a Amount) Sub(b Amount) (Amount, error) {
	return a.SubRounded(b, RoundToNearest)
}

// SubRounded returns a - b, rounding the IOU result under mode.
func (a Amount) SubRounded(b Amount, mode RoundingMode) (Amount, error) {
	return a.subRounded(b, mode, NewNumberContext(MantissaScaleSmall, false))
}

// SubUniversal returns a - b using rippled 3.2's universal Number arithmetic.
func (a Amount) SubUniversal(b Amount) (Amount, error) {
	return a.subRounded(b, RoundToNearest, NewNumberContext(MantissaScaleLarge, true))
}

// SubWithNumberContext returns a - b using the transaction's Number scale.
func (a Amount) SubWithNumberContext(b Amount, ctx NumberContext, mode RoundingMode) (Amount, error) {
	return a.subRounded(b, mode, ctx)
}

func (a Amount) subRounded(b Amount, mode RoundingMode, ctx NumberContext) (Amount, error) {
	if a.mptRaw != nil || b.mptRaw != nil {
		if a.mptRaw == nil || b.mptRaw == nil {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot subtract MPT and non-MPT amounts")
		}
		if a.mptIssuanceID != b.mptIssuanceID {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: cannot subtract different MPT issuances")
		}
		result := new(big.Int).Sub(big.NewInt(*a.mptRaw), big.NewInt(*b.mptRaw))
		if !result.IsInt64() {
			return Amount{}, fmt.Errorf("temBAD_AMOUNT: MPT subtraction overflow")
		}
		return newMPTAmountLike(a, result.Int64()), nil
	}
	return a.addRounded(b.Negate(), mode, ctx)
}

// addIOUValues adds two IOU values using banker's rounding.
func addIOUValues(a, b IOUAmountValue) IOUAmountValue {
	return addIOUValuesRounded(a, b, RoundToNearest)
}

// addIOUValuesRounded adds two IOU values with proper exponent handling under mode.
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Add() for Guard-based precision.
// Reference: IOUAmount::operator+= in IOUAmount.cpp lines 137-181
func addIOUValuesRounded(a, b IOUAmountValue, mode RoundingMode) IOUAmountValue {
	return addIOUValuesRoundedWithContext(
		a,
		b,
		mode,
		NewNumberContext(MantissaScaleSmall, false),
	)
}

func addIOUValuesRoundedWithContext(
	a, b IOUAmountValue,
	mode RoundingMode,
	ctx NumberContext,
) IOUAmountValue {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}

	// When switchover is on, delegate to XRPLNumber (Guard-based precision)
	// Reference: IOUAmount.cpp lines 149-153
	if ctx.UniversalNumberEnabled() {
		na := ctx.Number(a.mantissa, a.exponent, mode)
		nb := ctx.Number(b.mantissa, b.exponent, mode)
		result := na.AddRounded(nb, mode)
		if ctx.Scale() != MantissaScaleSmall {
			return result.ToIOUAmountValueRounded(mode)
		}
		return result.ToIOUAmountValue()
	}

	// Legacy path (without fixUniversalNumber)
	// Align exponents
	aExp := a.exponent
	bExp := b.exponent
	aMant := a.mantissa
	bMant := b.mantissa

	// Align to the larger exponent
	for aExp < bExp {
		aMant /= 10
		aExp++
	}
	for bExp < aExp {
		bMant /= 10
		bExp++
	}

	result := aMant + bMant

	// Handle near-zero results
	if result >= -10 && result <= 10 {
		return ZeroIOUValue()
	}

	r := NewIOUAmountValue(result, aExp)
	return r
}

// Compare compares two comparable amounts and panics when their assets differ.
// Use CompareChecked when the asset relationship is not already guaranteed.
func (a Amount) Compare(b Amount) int {
	cmp, err := a.CompareChecked(b)
	if err != nil {
		panic(err)
	}
	return cmp
}

// CompareChecked compares two amounts after validating rippled's
// STAmount::areComparable asset rules.
func (a Amount) CompareChecked(b Amount) (int, error) {
	if !amountsComparable(a, b) {
		return 0, fmt.Errorf("temBAD_AMOUNT: cannot compare amounts with different assets")
	}
	if a.IsNative() && b.IsNative() {
		if a.xrp.drops < b.xrp.drops {
			return -1, nil
		}
		if a.xrp.drops > b.xrp.drops {
			return 1, nil
		}
		return 0, nil
	}
	if a.mptRaw != nil {
		if *a.mptRaw < *b.mptRaw {
			return -1, nil
		}
		if *a.mptRaw > *b.mptRaw {
			return 1, nil
		}
		return 0, nil
	}
	return compareIOUValues(a.iou, b.iou), nil
}

func amountsComparable(a, b Amount) bool {
	if a.IsNative() || b.IsNative() {
		return a.IsNative() && b.IsNative()
	}
	if a.mptRaw != nil || b.mptRaw != nil {
		return a.mptRaw != nil && b.mptRaw != nil &&
			a.mptIssuanceID == b.mptIssuanceID
	}
	return a.Currency == b.Currency
}

// compareIOUValues compares two IOU values using mantissa/exponent without float64 conversion.
func compareIOUValues(a, b IOUAmountValue) int {
	// Handle signs first
	aSign := a.Signum()
	bSign := b.Signum()
	if aSign < bSign {
		return -1
	}
	if aSign > bSign {
		return 1
	}
	if aSign == 0 && bSign == 0 {
		return 0
	}

	// Same sign - compare magnitudes
	// For positive values: larger exponent = larger value (if mantissas are normalized)
	if a.exponent > b.exponent {
		if aSign > 0 {
			return 1
		}
		return -1
	}
	if a.exponent < b.exponent {
		if aSign > 0 {
			return -1
		}
		return 1
	}

	// Same exponent - compare mantissas
	if a.mantissa < b.mantissa {
		return -1
	}
	if a.mantissa > b.mantissa {
		return 1
	}
	return 0
}

// MulRatio multiplies this amount by num/den with optional rounding up.
// Uses big.Int arithmetic to avoid overflow on large mantissa * num products.
// Includes roomToGrow precision enhancement matching rippled's IOUAmount mulRatio.
// Reference: IOUAmount.cpp mulRatio() lines 189-323
func (a Amount) MulRatio(num, den uint32, roundUp bool) Amount {
	return a.MulRatioWithNumberContext(
		num,
		den,
		roundUp,
		NewNumberContext(MantissaScaleSmall, false),
	)
}

// MulRatioWithNumberContext multiplies this amount by num/den under the
// selected ledger arithmetic semantics.
func (a Amount) MulRatioWithNumberContext(
	num, den uint32,
	roundUp bool,
	ctx NumberContext,
) Amount {
	if den == 0 {
		panic("division by zero")
	}
	if a.mptRaw != nil {
		product := new(big.Int).Mul(big.NewInt(*a.mptRaw), new(big.Int).SetUint64(uint64(num)))
		quotient := new(big.Int)
		remainder := new(big.Int)
		quotient.QuoRem(product, new(big.Int).SetUint64(uint64(den)), remainder)
		if remainder.Sign() != 0 {
			if product.Sign() >= 0 && roundUp {
				quotient.Add(quotient, big.NewInt(1))
			} else if product.Sign() < 0 && !roundUp {
				quotient.Sub(quotient, big.NewInt(1))
			}
		}
		if !quotient.IsInt64() {
			panic("MPT mulRatio overflow")
		}
		return newMPTAmountLike(a, quotient.Int64())
	}
	if a.IsNative() {
		bigDrops := new(big.Int).SetInt64(a.Drops())
		product := new(big.Int).Mul(bigDrops, new(big.Int).SetUint64(uint64(num)))
		result, remainder := new(big.Int), new(big.Int)
		result.QuoRem(product, new(big.Int).SetUint64(uint64(den)), remainder)
		if remainder.Sign() != 0 {
			if product.Sign() >= 0 && roundUp {
				result.Add(result, big.NewInt(1))
			} else if product.Sign() < 0 && !roundUp {
				result.Sub(result, big.NewInt(1))
			}
		}
		if !result.IsInt64() {
			panic("XRP mulRatio overflow")
		}
		return NewXRPAmountFromInt(result.Int64())
	}

	if a.IsZero() {
		return a
	}

	// For IOU: multiply mantissa by num/den
	mantissa := a.iou.Mantissa()
	negative := mantissa < 0
	if negative {
		mantissa = -mantissa
	}

	bigMant := new(big.Int).SetInt64(mantissa)
	bigNum := new(big.Int).SetUint64(uint64(num))
	bigDen := new(big.Int).SetUint64(uint64(den))

	// mul = mantissa * num (32-bit * 64-bit -> fits in 128 bits)
	mul := new(big.Int).Mul(bigMant, bigNum)

	low := new(big.Int).Div(mul, bigDen)
	rem := new(big.Int).Sub(mul, new(big.Int).Mul(low, bigDen))

	exponent := a.iou.Exponent()

	// roomToGrow: scale up to capture fractional digits from rem/den
	// Reference: IOUAmount.cpp lines 254-272
	if rem.Sign() != 0 {
		roomToGrow := mulRatioFL64 - log10Ceil(low)
		if roomToGrow > 0 {
			exponent -= roomToGrow
			scale := pow10Big(roomToGrow)
			low.Mul(low, scale)
			rem.Mul(rem, scale)
		}
		addRem := new(big.Int).Div(rem, bigDen)
		low.Add(low, addRem)
		rem.Sub(rem, new(big.Int).Mul(addRem, bigDen))
	}

	// mustShrink: scale down if low exceeds int64 range
	// Reference: IOUAmount.cpp lines 278-287
	hasRem := rem.Sign() != 0
	mustShrink := log10Ceil(low) - mulRatioFL64
	if mustShrink > 0 {
		sav := new(big.Int).Set(low)
		exponent += mustShrink
		scale := pow10Big(mustShrink)
		low.Div(low, scale)
		if !hasRem {
			hasRem = new(big.Int).Sub(sav, new(big.Int).Mul(low, scale)).Sign() != 0
		}
	}

	resultMant := low.Int64()

	// Normalize FIRST, then apply rounding, matching rippled's IOUAmount.cpp lines 289-319:
	//   std::int64_t mantissa = low.convert_to<std::int64_t>();
	//   if (neg) mantissa *= -1;
	//   IOUAmount result(mantissa, exponent);  // constructor normalizes
	//   if (hasRem) {
	//       if (roundUp && !neg)  return IOUAmount(result.mantissa() + 1, result.exponent());
	//       if (!roundUp && neg)  return IOUAmount(result.mantissa() - 1, result.exponent());
	//   }
	//   return result;
	if negative {
		resultMant = -resultMant
	}

	result := ctx.IssuedAmount(resultMant, exponent, a.Currency, a.Issuer, RoundToNearest)

	// Apply rounding AFTER normalization. Two cases round away from zero:
	//   roundUp && !neg: +1 to positive mantissa (round up)
	//   !roundUp && neg: -1 to negative mantissa (round more negative = away from zero)
	if hasRem {
		iou := result.IOU()
		if roundUp && !negative {
			if result.IsZero() {
				return ctx.IssuedAmount(MinMantissa, MinExponent, a.Currency, a.Issuer, RoundToNearest)
			}
			return ctx.IssuedAmount(
				iou.mantissa+1,
				iou.exponent,
				a.Currency,
				a.Issuer,
				RoundToNearest,
			)
		}
		if !roundUp && negative {
			if result.IsZero() {
				return ctx.IssuedAmount(-MinMantissa, MinExponent, a.Currency, a.Issuer, RoundToNearest)
			}
			return ctx.IssuedAmount(
				iou.mantissa-1,
				iou.exponent,
				a.Currency,
				a.Issuer,
				RoundToNearest,
			)
		}
	}

	return result
}

// mulRatioFL64 is floor(log10(math.MaxInt64)) = 18
// Reference: IOUAmount.cpp line 239-241
const mulRatioFL64 = 18

// log10Ceil returns ceil(log10(v)) for a big.Int.
// Returns -1 for v == 0, 0 for v == 1.
// Reference: IOUAmount.cpp lines 231-237
func log10Ceil(v *big.Int) int {
	if v.Sign() <= 0 {
		return -1
	}
	// Find smallest power of 10 >= v
	p := big.NewInt(1)
	idx := 0
	for p.Cmp(v) < 0 {
		p.Mul(p, big.NewInt(10))
		idx++
	}
	return idx
}

// pow10Big returns 10^n as a big.Int.
func pow10Big(n int) *big.Int {
	result := big.NewInt(1)
	ten := big.NewInt(10)
	for range n {
		result.Mul(result, ten)
	}
	return result
}

// divideIOUWithNumberContext mirrors rippled's divide(num, den, asset) for an issued-currency
// result: it forms the quotient muldiv(numMantissa, 10^17, denMantissa) + 5 at
// exponent numExp-denExp-17, then lets the standard IOU canonicalization round
// it back into [10^15, 10^16). That canonicalization is switchover-gated —
// round-to-nearest-ties-to-even when fixUniversalNumber is enabled (the mainnet
// regime), truncation otherwise — so GetRate and Amount.Div share one rounding
// rule instead of each hard-coding a different one.
//
// numMantissa/denMantissa are unsigned magnitudes; native/MPT operands must
// already be lifted into [10^15, 10^16) by the caller. The result carries the
// given sign, currency, and issuer.
func divideIOUWithNumberContext(
	numMantissa uint64,
	numExp int,
	denMantissa uint64,
	denExp int,
	negative bool,
	currency, issuer string,
	ctx NumberContext,
) Amount {
	mantissa, exponent := divideIOUComponents(numMantissa, numExp, denMantissa, denExp)
	signedMantissa := int64(mantissa)
	if negative {
		signedMantissa = -signedMantissa
	}
	return ctx.IssuedAmount(signedMantissa, exponent, currency, issuer, RoundToNearest)
}

func divideIOUComponents(numMantissa uint64, numExp int, denMantissa uint64, denExp int) (uint64, int) {
	q := new(big.Int).SetUint64(numMantissa)
	q.Mul(q, tenTo17)
	q.Div(q, new(big.Int).SetUint64(denMantissa))
	if !q.IsUint64() {
		panic("overflow in muldiv")
	}
	return q.Uint64() + 5, numExp - denExp - 17
}

// DivideNoIssue mirrors rippled's divide(num, den, noIssue()) used for rates.
func DivideNoIssue(num, den Amount) Amount {
	if den.IsZero() {
		panic("division by zero")
	}
	if num.IsZero() {
		return Amount{iou: ZeroIOUValue()}
	}

	numMantissa, numExponent := rateMantissa(num)
	denMantissa, denExponent := rateMantissa(den)
	mantissa, exponent := divideIOUComponents(numMantissa, numExponent, denMantissa, denExponent)
	n := XRPLNumber{
		negative: num.IsNegative() != den.IsNegative(),
		mantissa: mantissa,
		exponent: exponent,
		scale:    MantissaScaleLarge,
	}
	n.normalize(RoundToNearest)
	return Amount{iou: numberToIOUAmountValue(n)}
}

func numberToIOUAmountValue(n XRPLNumber) IOUAmountValue {
	mantissa, exponent := n.NormalizeToRange(uint64(MinMantissa), uint64(MaxMantissa))
	if mantissa == 0 || exponent < MinExponent {
		return ZeroIOUValue()
	}
	if exponent > MaxExponent {
		panic("XRPLNumber→IOUAmountValue overflow")
	}
	return IOUAmountValue{mantissa: mantissa, exponent: exponent}
}

// Mul multiplies this Amount by another Amount using banker's rounding.
func (a Amount) Mul(other Amount, roundUp bool) Amount {
	return a.MulRounded(other, roundUp, RoundToNearest)
}

// MulRounded multiplies this Amount by another Amount, rounding the IOU result
// under mode. The AMM math uses this to reproduce rippled's
// NumberRoundModeGuard around multiplications.
// Reference: rippled's mulRound() in STAmount.cpp
// For IOU * IOU: result = (m1 * m2) * 10^(e1 + e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Mul() for Guard-based rounding.
func (a Amount) MulRounded(other Amount, roundUp bool, mode RoundingMode) Amount {
	return a.mulRounded(
		other,
		roundUp,
		mode,
		NewNumberContext(MantissaScaleSmall, false),
	)
}

// MulWithNumberContext multiplies this Amount by another Amount under the
// transaction's Number semantics.
func (a Amount) MulWithNumberContext(
	other Amount,
	ctx NumberContext,
	roundUp bool,
	mode RoundingMode,
) Amount {
	return a.mulRounded(other, roundUp, mode, ctx)
}

func (a Amount) mulRounded(
	other Amount,
	roundUp bool,
	mode RoundingMode,
	ctx NumberContext,
) Amount {
	if a.IsZero() || other.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		if a.mptRaw != nil {
			return newMPTAmountLike(a, 0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}
	if a.mptRaw != nil && other.mptRaw != nil {
		product := new(big.Int).Mul(big.NewInt(*a.mptRaw), big.NewInt(*other.mptRaw))
		if !product.IsInt64() {
			panic("MPT value overflow")
		}
		return newMPTAmountLike(a, product.Int64())
	}

	// Handle XRP * XRP case
	if a.IsNative() && other.IsNative() {
		return NewXRPAmountFromInt(mulNativeNative(a.Drops(), other.Drops()))
	}

	// For IOU multiplication, use precise big.Int arithmetic
	// result = (a.mantissa * other.mantissa) * 10^(a.exp + other.exp)
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := other.Mantissa()
	e2 := other.Exponent()

	// When switchover is on, delegate to XRPLNumber for Guard-based rounding
	if ctx.UniversalNumberEnabled() && !a.IsNative() {
		negative := (m1 < 0) != (m2 < 0)
		if m1 < 0 {
			m1 = -m1
		}
		if m2 < 0 {
			m2 = -m2
		}
		na := ctx.Number(m1, e1, mode)
		nb := ctx.Number(m2, e2, mode)
		result := na.MulRounded(nb, mode)
		if a.mptRaw != nil {
			value := result.ToInt64WithMode(mode)
			if negative {
				value = -value
			}
			return newMPTAmountLike(a, value)
		}
		iou := result.ToIOUAmountValue()
		rm := iou.mantissa
		if negative {
			rm = -rm
		}
		return ctx.IssuedAmount(rm, iou.exponent, a.Currency, a.Issuer, mode)
	}

	// Handle sign
	negative := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Pre-normalize integral inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled multiply() lines 1382-1398
	if a.IsNative() || a.IsMPT() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() || other.IsMPT() {
		for m2 < MinMantissa {
			m2 *= 10
			e2--
		}
	}

	// Multiply mantissas (each in [10^15, 10^16) range, product in [10^30, 10^32) range)
	// Then divide by 10^14 to bring result to [10^16, 10^18) range.
	// Reference: rippled multiply() line 1406, mulRound() line 1590
	bigM1 := new(big.Int).SetUint64(uint64(m1))
	bigM2 := new(big.Int).SetUint64(uint64(m2))
	bigProduct := new(big.Int).Mul(bigM1, bigM2)
	bigTenTo14 := new(big.Int).SetUint64(tenTo14)

	// rippled adds the away-from-zero bias only when (resultNegative != roundUp);
	// a negative result with roundUp=true must NOT be inflated. For the positive
	// operands every current caller passes this reduces to `roundUp`.
	if negative != roundUp {
		rounding := new(big.Int).SetUint64(tenTo14 - 1)
		bigProduct.Add(bigProduct, rounding)
	}

	bigResult := new(big.Int).Div(bigProduct, bigTenTo14)

	if !roundUp {
		// Match rippled's multiply(): muldiv(v1, v2, tenTo14) + 7
		// Reference: rippled multiply() line 1406
		bigResult.Add(bigResult, big.NewInt(7))
	}

	resultExp := e1 + e2 + 14

	if negative != roundUp {
		// canonicalizeRound runs only on the away-from-zero leg, matching
		// rippled's mulRoundImpl gate (resultNegative != roundUp).
		// Reference: rippled canonicalizeRound() lines 1431-1464
		bigCMaxValue := new(big.Int).SetUint64(cMaxValue)
		tenCMaxValue := new(big.Int).Mul(big.NewInt(10), bigCMaxValue)
		ten := big.NewInt(10)

		if bigResult.Cmp(bigCMaxValue) > 0 {
			for bigResult.Cmp(tenCMaxValue) > 0 {
				bigResult.Div(bigResult, ten)
				resultExp++
			}
			bigResult.Add(bigResult, big.NewInt(9))
			bigResult.Div(bigResult, ten)
			resultExp++
		}
	}

	// Normalize the result to mantissa in [cMinValue, cMaxValue)
	bigMinMantissa := new(big.Int).SetInt64(MinMantissa)
	bigMaxMantissa := new(big.Int).SetUint64(cMaxValue)
	ten := big.NewInt(10)

	for bigResult.Cmp(bigMaxMantissa) >= 0 {
		bigResult.Div(bigResult, ten)
		resultExp++
	}
	for bigResult.Cmp(bigMinMantissa) < 0 && bigResult.Sign() != 0 {
		bigResult.Mul(bigResult, ten)
		resultExp--
	}

	resultMant := bigResult.Int64()
	if negative {
		resultMant = -resultMant
	}

	if a.IsNative() {
		return nativeAmountFromMagnitude(bigResult, resultExp, negative)
	}

	result := ctx.IssuedAmount(resultMant, resultExp, a.Currency, a.Issuer, mode)
	if a.mptRaw != nil {
		n := NewXRPLNumberRounded(result.IOU().Mantissa(), result.IOU().Exponent(), mode)
		return newMPTAmountLike(a, n.ToInt64WithMode(mode))
	}
	return result
}

// Div divides this Amount by another Amount.
// Reference: rippled's divRound() in STAmount.cpp
// For IOU / IOU: result = (m1 / m2) * 10^(e1 - e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Div() for Guard-based rounding.
func (a Amount) Div(other Amount, roundUp bool) Amount {
	return a.divWithNumberContext(
		other,
		roundUp,
		NewNumberContext(MantissaScaleSmall, false),
	)
}

// DivWithNumberContext divides this Amount by another Amount under the
// transaction's Number semantics.
func (a Amount) DivWithNumberContext(
	other Amount,
	ctx NumberContext,
	roundUp bool,
) Amount {
	return a.divWithNumberContext(other, roundUp, ctx)
}

func (a Amount) divWithNumberContext(
	other Amount,
	roundUp bool,
	ctx NumberContext,
) Amount {
	if other.IsZero() {
		// A zero denominator is an engine bug, never valid ledger data.
		// rippled throws "division by zero" here; returning a zero amount
		// would silently mask the bug and could commit a wrong ledger. The
		// panic is recovered at the tx-apply boundary (see xrpl_number.go).
		panic("division by zero")
	}

	if a.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		if a.mptRaw != nil {
			return newMPTAmountLike(a, 0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}
	if a.mptRaw != nil {
		return newMPTAmountLike(a, DivRoundMPTWithNumberContext(a, other, ctx, roundUp))
	}

	// Handle XRP / XRP case
	if a.IsNative() && other.IsNative() {
		result := a.Drops() / other.Drops()
		if roundUp && a.Drops()%other.Drops() != 0 {
			if (a.Drops() < 0) != (other.Drops() < 0) {
				result--
			} else {
				result++
			}
		}
		return NewXRPAmountFromInt(result)
	}

	// For IOU division, use precise big.Int arithmetic
	// Reference: rippled STAmount.cpp divide() and divRound()
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := other.Mantissa()
	e2 := other.Exponent()

	// rippled's divide() NEVER uses Number/switchover - it always uses
	// muldiv(numVal, tenTo17, denVal) + 5 regardless of getSTNumberSwitchover().
	// Reference: rippled STAmount.cpp divide() lines 1293-1336

	// Handle sign
	negative := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Pre-normalize integral inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled divide() lines 1307-1324
	if a.IsNative() || a.IsMPT() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() || other.IsMPT() {
		for m2 < MinMantissa {
			m2 *= 10
			e2--
		}
	}

	bigM1 := new(big.Int).SetUint64(uint64(m1))
	bigM2 := new(big.Int).SetUint64(uint64(m2))

	// Scale numerator by 10^17 for precision (matching rippled's tenTo17)
	// Reference: rippled divide() line 1333, divRound() line 1712
	bigM1.Mul(bigM1, new(big.Int).Set(tenTo17))

	// rippled adds the away-from-zero bias only when (resultNegative != roundUp);
	// a negative result with roundUp=true must NOT be inflated. For the positive
	// operands every current caller passes this reduces to `roundUp`.
	if negative != roundUp {
		rounding := new(big.Int).SetUint64(uint64(m2) - 1)
		bigM1.Add(bigM1, rounding)
	}

	bigResult := new(big.Int).Div(bigM1, bigM2)

	if !roundUp {
		// Match rippled's divide(): result = muldiv(numVal, tenTo17, denVal) + 5
		// Reference: rippled divide() line 1333
		bigResult.Add(bigResult, big.NewInt(5))
	}

	resultExp := e1 - e2 - 17 // -17 because we scaled by 10^17

	if negative != roundUp {
		// canonicalizeRound runs only on the away-from-zero leg, matching
		// rippled's divRoundImpl gate (resultNegative != roundUp).
		// Reference: rippled canonicalizeRound() lines 1431-1464
		bigCMaxValue := new(big.Int).SetUint64(cMaxValue)
		tenCMaxValue := new(big.Int).Mul(big.NewInt(10), bigCMaxValue)
		ten := big.NewInt(10)

		if bigResult.Cmp(bigCMaxValue) > 0 {
			for bigResult.Cmp(tenCMaxValue) > 0 {
				bigResult.Div(bigResult, ten)
				resultExp++
			}
			bigResult.Add(bigResult, big.NewInt(9))
			bigResult.Div(bigResult, ten)
			resultExp++
		}
	}

	// Normalize the result to mantissa in [cMinValue, cMaxValue)
	bigMinMantissa := new(big.Int).SetInt64(MinMantissa)
	bigMaxMantissa := new(big.Int).SetUint64(cMaxValue)
	ten := big.NewInt(10)
	mod := new(big.Int)

	if !roundUp {
		// For divide() (roundUp=false), the result goes through rippled's
		// STAmount constructor -> canonicalize() -> Number::normalize(), which
		// uses Guard rounding (round to nearest, tie to even).
		// We must track guard digits during normalization and apply rounding.
		// Reference: rippled Number.cpp Number::normalize() lines 178-227
		var guardDigit int64
		hasRemainder := false
		for bigResult.Cmp(bigMaxMantissa) >= 0 {
			if guardDigit != 0 {
				hasRemainder = true
			}
			bigResult.DivMod(bigResult, ten, mod)
			guardDigit = mod.Int64()
			resultExp++
		}
		// Apply round-to-nearest (tie to even) matching Number::normalize()
		mantissa := bigResult.Int64()
		if guardDigit > 5 || (guardDigit == 5 && (hasRemainder || mantissa%2 == 1)) {
			mantissa++
			if mantissa >= int64(cMaxValue) {
				mantissa /= 10
				resultExp++
			}
		}
		bigResult.SetInt64(mantissa)
	} else {
		for bigResult.Cmp(bigMaxMantissa) >= 0 {
			bigResult.Div(bigResult, ten)
			resultExp++
		}
	}

	for bigResult.Cmp(bigMinMantissa) < 0 && bigResult.Sign() != 0 {
		bigResult.Mul(bigResult, ten)
		resultExp--
	}

	resultMant := bigResult.Int64()
	if negative {
		resultMant = -resultMant
	}

	if a.IsNative() {
		return nativeAmountFromMagnitude(bigResult, resultExp, negative)
	}

	return NewIssuedAmountFromValue(resultMant, resultExp, a.Currency, a.Issuer)
}

func nativeAmountFromMagnitude(magnitude *big.Int, exponent int, negative bool) Amount {
	drops := new(big.Int).Set(magnitude)
	if exponent > 0 {
		drops.Mul(drops, pow10Big(exponent))
	} else if exponent < 0 {
		drops.Quo(drops, pow10Big(-exponent))
	}
	if drops.Sign() == 0 {
		return NewXRPAmountFromInt(0)
	}
	if !drops.IsInt64() {
		panic("Native currency amount out of range")
	}
	value := drops.Int64()
	guardNativeDrops(value)
	if negative {
		value = -value
	}
	return NewXRPAmountFromInt(value)
}
