package state

import "math/big"

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
