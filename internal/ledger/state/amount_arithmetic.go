package state

import (
	"math/big"
)

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

	// mul = mantissa * num (32-bit * 64-bit → fits in 128 bits)
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

// MulRoundStrict multiplies two Amounts using rippled's mulRoundStrict algorithm.
// This differs from Amount.Mul() in that it uses:
// 1. muldiv_round(v1, v2, 10^14, rounding) instead of big.Int product + normalize
// 2. canonicalizeRoundStrict for overflow handling
// 3. Number::towards_zero rounding mode during normalization
// Reference: STAmount.cpp mulRoundImpl with canonicalizeRoundStrict + NumberRoundModeGuard
func MulRoundStrict(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	// Normalize native/MPT values to IOU range [10^15, 10^16)
	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	// Make mantissas positive for multiplication
	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	// rounding = (resultNegative != roundUp) ? 10^14 - 1 : 0
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)  // 10^14
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999) // 10^14 - 1
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRoundStrict: only when resultNegative != roundUp
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// Create the result with Number in towards_zero mode
	// This affects how normalization rounds during STAmount construction
	guard := NewNumberRoundModeGuard(RoundTowardsZero)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// MulRound multiplies two Amounts using rippled's mulRound (non-strict) algorithm.
// This is the legacy version with "slop" that uses canonicalizeRound instead of
// canonicalizeRoundStrict. The key difference: canonicalizeRound adds 9 or 10
// based on loop count (not actual remainder), and uses DontAffectNumberRoundMode
// (no-op) instead of NumberRoundModeGuard(towards_zero).
// Reference: STAmount.cpp mulRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func MulRound(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	// Normalize native/MPT values to IOU range [10^15, 10^16)
	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	// Make mantissas positive for multiplication
	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRound (non-strict): uses loop count, NOT actual remainder.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1432-1464
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// DontAffectNumberRoundMode: NO guard (no-op), unlike strict which uses towards_zero
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// DivRound divides two Amounts using rippled's divRound (non-strict) algorithm.
// This is the legacy version with "slop" that uses canonicalizeRound.
// Reference: STAmount.cpp divRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func DivRound(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// divmod with rounding: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		// Round up: add (denVal - 1) before division
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	// canonicalizeRound (non-strict): same as MulRound
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// DivRoundNative divides two Amounts and returns the result as XRP drops (int64),
// using native canonicalization matching rippled's canonicalizeRound(native=true).
// When the output asset is native (XRP), rippled's divRoundImpl calls
// canonicalizeRound with native=true, which uses a different rounding path
// than the IOU overflow case. This function matches that native path exactly.
// Reference: STAmount.cpp divRoundImpl + canonicalizeRound(native=true) lines 1434-1451
func DivRoundNative(num, den Amount, roundUp bool) int64 {
	if den.IsZero() || num.IsZero() {
		return 0
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// divmod with rounding: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	// canonicalizeRound(native=true): use native rounding path.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1434-1451
	if resultNegative != roundUp {
		if offset < 0 {
			loops := 0
			for offset < -1 {
				amount /= 10
				offset++
				loops++
			}
			var adder uint64 = 10
			if loops >= 2 {
				adder = 9
			}
			amount = (amount + adder) / 10
		}
	} else {
		// When resultNegative == roundUp (i.e., no rounding needed),
		// still need to convert to drops (offset → 0).
		for offset < 0 {
			amount /= 10
			offset++
		}
		for offset > 0 {
			amount *= 10
			offset--
		}
	}

	if roundUp && !resultNegative && amount == 0 {
		return 1
	}

	result := int64(amount)
	if resultNegative {
		result = -result
	}
	return result
}

// MulRoundNative multiplies two Amounts and returns the result as XRP drops (int64),
// using native canonicalization matching rippled's canonicalizeRound(native=true).
// When the output asset is native (XRP), rippled's mulRoundImpl calls
// canonicalizeRound with native=true, which uses a different rounding path
// than the IOU overflow case. This function matches that native path exactly.
// Reference: STAmount.cpp mulRoundImpl + canonicalizeRound(native=true) lines 1434-1451
func MulRoundNative(v1, v2 Amount, roundUp bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRound(native=true): use native rounding path.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1434-1451
	if resultNegative != roundUp {
		if offset < 0 {
			loops := 0
			for offset < -1 {
				amount /= 10
				offset++
				loops++
			}
			var adder uint64 = 10
			if loops >= 2 {
				adder = 9
			}
			amount = (amount + adder) / 10
		}
	} else {
		// When resultNegative == roundUp, no special rounding needed.
		// Still convert to drops (offset → 0).
		for offset < 0 {
			amount /= 10
			offset++
		}
		for offset > 0 {
			amount *= 10
			offset--
		}
	}

	if roundUp && !resultNegative && amount == 0 {
		return 1
	}

	result := int64(amount)
	if resultNegative {
		result = -result
	}
	return result
}

// DivRoundStrict divides two Amounts using rippled's divRoundStrict algorithm.
// Reference: STAmount.cpp divRoundImpl with NumberRoundModeGuard
func DivRoundStrict(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOffset := num.Exponent()
	denVal := den.Mantissa()
	denOffset := den.Exponent()

	// Normalize native values to IOU range
	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOffset--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOffset--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	// rounding = (resultNegative != roundUp) ? denVal - 1 : 0
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
	bigNum := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	bigDen := new(big.Int).SetInt64(denVal)
	if resultNegative != roundUp {
		bigNum.Add(bigNum, new(big.Int).Sub(bigDen, big.NewInt(1)))
	}
	bigResult := new(big.Int).Div(bigNum, bigDen)

	amount := bigResult.Uint64()
	offset := numOffset - denOffset - 17

	// canonicalizeRound (used in divRoundImpl)
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// Create result with appropriate Number rounding mode
	// divRoundStrict uses upward if (roundUp ^ resultNegative), else downward
	var mode RoundingMode
	if roundUp != resultNegative {
		mode = RoundUpward
	} else {
		mode = RoundDownward
	}
	guard := NewNumberRoundModeGuard(mode)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}
