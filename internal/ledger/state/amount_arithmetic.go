package state

import (
	"errors"
	"math/big"
)

func (a Amount) Add(b Amount) (Amount, error) {
	if a.IsNative() != b.IsNative() {
		return Amount{}, errors.New("cannot add XRP and IOU amounts")
	}
	if a.IsNative() {
		return Amount{
			xrp:    a.xrp.Add(b.xrp),
			Native: true,
		}, nil
	}
	result := addIOUValues(a.iou, b.iou)
	return Amount{
		iou:      result,
		Currency: a.Currency,
		Issuer:   a.Issuer,
		Native:   false,
	}, nil
}

// Sub subtracts two amounts (must be same type)
func (a Amount) Sub(b Amount) (Amount, error) {
	return a.Add(b.Negate())
}

// addIOUValues adds two IOU values with proper exponent handling.
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Add() for Guard-based precision.
// Reference: IOUAmount::operator+= in IOUAmount.cpp lines 137-181
func addIOUValues(a, b IOUAmountValue) IOUAmountValue {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}

	// When switchover is on, delegate to XRPLNumber (Guard-based precision)
	// Reference: IOUAmount.cpp lines 149-153
	if GetNumberSwitchover() {
		na := NewXRPLNumber(a.mantissa, a.exponent)
		nb := NewXRPLNumber(b.mantissa, b.exponent)
		result := na.Add(nb)
		r := result.ToIOUAmountValue()
		return r
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

// Compare compares two amounts
// Returns -1 if a < b, 0 if a == b, 1 if a > b
func (a Amount) Compare(b Amount) int {
	if a.IsNative() && b.IsNative() {
		if a.xrp.drops < b.xrp.drops {
			return -1
		}
		if a.xrp.drops > b.xrp.drops {
			return 1
		}
		return 0
	}
	if !a.IsNative() && !b.IsNative() {
		return compareIOUValues(a.iou, b.iou)
	}
	// Mixed types - XRP comes first
	if a.IsNative() {
		return -1
	}
	return 1
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
	if a.IsNative() {
		// Use big.Int to avoid int64 overflow for large XRP amounts.
		// E.g. 150000000000 drops * 1000000000 overflows int64.
		// Reference: rippled uses uint128_t for XRP mulRatio.
		bigDrops := new(big.Int).SetInt64(a.Drops())
		bigNum := new(big.Int).SetInt64(int64(num))
		bigDen := new(big.Int).SetInt64(int64(den))
		product := new(big.Int).Mul(bigDrops, bigNum)
		result := new(big.Int).Div(product, bigDen)
		if roundUp {
			rem := new(big.Int).Mod(product, bigDen)
			if rem.Sign() != 0 {
				result.Add(result, big.NewInt(1))
			}
		}
		return NewXRPAmountFromInt(result.Int64())
	}

	if den == 0 || a.IsZero() {
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

	result := NewIssuedAmountFromValue(resultMant, exponent, a.Currency, a.Issuer)

	// Apply rounding AFTER normalization. Two cases round away from zero:
	//   roundUp && !neg: +1 to positive mantissa (round up)
	//   !roundUp && neg: -1 to negative mantissa (round more negative = away from zero)
	if hasRem {
		iou := result.IOU()
		if roundUp && !negative {
			if result.IsZero() {
				return NewIssuedAmountFromValue(MinMantissa, MinExponent, a.Currency, a.Issuer)
			}
			return NewIssuedAmountFromValue(iou.mantissa+1, iou.exponent, a.Currency, a.Issuer)
		}
		if !roundUp && negative {
			if result.IsZero() {
				return NewIssuedAmountFromValue(-MinMantissa, MinExponent, a.Currency, a.Issuer)
			}
			return NewIssuedAmountFromValue(iou.mantissa-1, iou.exponent, a.Currency, a.Issuer)
		}
	} else {
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
	for i := 0; i < n; i++ {
		result.Mul(result, ten)
	}
	return result
}

// Mul multiplies this Amount by another Amount.
// Reference: rippled's mulRound() in STAmount.cpp
// For IOU * IOU: result = (m1 * m2) * 10^(e1 + e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Mul() for Guard-based rounding.
func (a Amount) Mul(other Amount, roundUp bool) Amount {
	if a.IsZero() || other.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	// Handle XRP * XRP case
	if a.IsNative() && other.IsNative() {
		result := a.Drops() * other.Drops()
		return NewXRPAmountFromInt(result)
	}

	// For IOU multiplication, use precise big.Int arithmetic
	// result = (a.mantissa * other.mantissa) * 10^(a.exp + other.exp)
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := other.Mantissa()
	e2 := other.Exponent()

	// When switchover is on, delegate to XRPLNumber for Guard-based rounding
	if GetNumberSwitchover() && !a.IsNative() {
		negative := (m1 < 0) != (m2 < 0)
		if m1 < 0 {
			m1 = -m1
		}
		if m2 < 0 {
			m2 = -m2
		}
		na := NewXRPLNumber(m1, e1)
		nb := NewXRPLNumber(m2, e2)
		result := na.Mul(nb)
		iou := result.ToIOUAmountValue()
		rm := iou.mantissa
		if negative {
			rm = -rm
		}
		return NewIssuedAmountFromValue(rm, iou.exponent, a.Currency, a.Issuer)
	}

	// Handle sign
	negative := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Pre-normalize native (XRP) inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled multiply() lines 1382-1398
	if a.IsNative() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() {
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

	if roundUp {
		// Match rippled's mulRound(): muldiv_round(v1, v2, tenTo14, tenTo14-1)
		// For positive result with roundUp=true: add tenTo14-1 before dividing (ceiling division)
		// Reference: rippled mulRound() lines 1590-1612
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

	if roundUp {
		// Apply canonicalizeRound for mulRound()
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
		// Result is XRP - convert from mantissa/exponent to drops
		for resultExp > 0 {
			resultMant *= 10
			resultExp--
		}
		for resultExp < 0 {
			resultMant /= 10
			resultExp++
		}
		return NewXRPAmountFromInt(resultMant)
	}

	return NewIssuedAmountFromValue(resultMant, resultExp, a.Currency, a.Issuer)
}

// Div divides this Amount by another Amount.
// Reference: rippled's divRound() in STAmount.cpp
// For IOU / IOU: result = (m1 / m2) * 10^(e1 - e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Div() for Guard-based rounding.
func (a Amount) Div(other Amount, roundUp bool) Amount {
	if other.IsZero() {
		// Division by zero - return zero
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	if a.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	// Handle XRP / XRP case
	if a.IsNative() && other.IsNative() {
		result := a.Drops() / other.Drops()
		if roundUp && a.Drops()%other.Drops() != 0 {
			result++
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

	// Pre-normalize native (XRP) inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled divide() lines 1307-1324
	if a.IsNative() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() {
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

	if roundUp {
		// Match rippled's divRound(): muldiv_round(numVal, tenTo17, denVal, denVal-1)
		// When rounding away from zero, add denVal-1 before dividing.
		// Reference: rippled divRound() lines 1712-1713
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

	if roundUp {
		// Apply canonicalizeRound for divRound()
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
		// Result is XRP - convert from mantissa/exponent to drops
		for resultExp > 0 {
			resultMant *= 10
			resultExp--
		}
		for resultExp < 0 {
			resultMant /= 10
			resultExp++
		}
		return NewXRPAmountFromInt(resultMant)
	}

	return NewIssuedAmountFromValue(resultMant, resultExp, a.Currency, a.Issuer)
}
