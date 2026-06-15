package state

import "math/big"

// Shared big.Int constants for the muldiv-round core. Treated as immutable;
// muldivRound only reads them.
var (
	bigOne     = big.NewInt(1)
	bigTenTo14 = new(big.Int).SetUint64(100_000_000_000_000)     // 10^14
	bigTenTo17 = new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
)

// PrepareMulDivOperand returns the absolute mantissa and exponent of a,
// normalizing native (XRP) amounts up into the IOU mantissa range
// [10^15, 10^16). This is the per-operand preamble every mul/div round variant
// shares; the result sign is taken separately from a.IsNegative().
func PrepareMulDivOperand(a Amount) (mantissa int64, exponent int) {
	mantissa = a.Mantissa()
	exponent = a.Exponent()
	if a.IsNative() {
		if mantissa < 0 {
			mantissa = -mantissa
		}
		for mantissa < MinMantissa {
			mantissa *= 10
			exponent--
		}
	}
	if mantissa < 0 {
		mantissa = -mantissa
	}
	return mantissa, exponent
}

// muldivRound computes (x*y + slop) / divisor in exact big-integer arithmetic,
// where slop is (divisor-1) when addSlop is set (round away from zero) and 0
// otherwise. This is rippled's muldiv_round core.
func muldivRound(x, y, divisor *big.Int, addSlop bool) uint64 {
	n := new(big.Int).Mul(x, y)
	if addSlop {
		n.Add(n, new(big.Int).Sub(divisor, bigOne))
	}
	n.Div(n, divisor)
	return n.Uint64()
}

// MulMantissas computes (value1*value2 + slop) / 10^14, the multiply leg of the
// muldiv-round core. slop rounds away from zero when addSlop is set.
func MulMantissas(value1, value2 int64, addSlop bool) uint64 {
	return muldivRound(big.NewInt(value1), big.NewInt(value2), bigTenTo14, addSlop)
}

// DivMantissas computes (numVal*10^17 + slop) / denVal, the divide leg of the
// muldiv-round core. slop rounds away from zero when addSlop is set.
func DivMantissas(numVal, denVal int64, addSlop bool) uint64 {
	return muldivRound(big.NewInt(numVal), bigTenTo17, big.NewInt(denVal), addSlop)
}

// CanonicalizeRoundIOUOverflow reduces an over-large IOU mantissa back under
// MaxMantissa, matching rippled's canonicalizeRound overflow branch. Callers
// apply it only when rounding away from zero (resultNegative != roundUp).
func CanonicalizeRoundIOUOverflow(amount uint64, offset int) (uint64, int) {
	if amount > uint64(MaxMantissa) {
		for amount > 10*uint64(MaxMantissa) {
			amount /= 10
			offset++
		}
		amount += 9
		amount /= 10
		offset++
	}
	return amount, offset
}

// canonicalizeRoundNative converts an IOU-style mantissa/exponent to XRP drops.
// When doRound is set (rounding away from zero) it applies rippled's native
// canonicalizeRound loop-count rounding; otherwise it just rescales to drops.
//
// The no-round branch rescales by truncation where rippled's post-switchover
// native canonicalize rounds to-nearest via Number, and the native×native
// overflow Throw is not reproduced. Both gaps are currently unreachable: every
// caller passes roundUp=true with positive operands, which lands in the doRound
// branch.
func canonicalizeRoundNative(amount uint64, offset int, doRound bool) uint64 {
	if doRound {
		if offset < 0 {
			loops := 0
			for offset < -1 {
				amount /= 10
				offset++
				loops++
			}
			adder := uint64(10)
			if loops >= 2 {
				adder = 9
			}
			amount = (amount + adder) / 10
		}
		return amount
	}
	for offset < 0 {
		amount /= 10
		offset++
	}
	for offset > 0 {
		amount *= 10
		offset--
	}
	return amount
}

// FinalizeRoundIOU builds the signed IOU result. When useMode is set the result
// is constructed with the given Number rounding mode (the strict variants);
// otherwise the legacy non-mode constructor is used. A positive round-up that
// collapsed to zero returns the minimum representable value.
func FinalizeRoundIOU(amount uint64, offset int, resultNegative, roundUp bool, currency, issuer string, mode RoundingMode, useMode bool) Amount {
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	var result Amount
	if useMode {
		result = NewIssuedAmountFromValueRounded(mantissa, offset, currency, issuer, mode)
	} else {
		result = NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	}
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}
	return result
}

// finalizeRoundNative builds the signed XRP-drops result, returning 1 drop when
// a positive round-up collapsed to zero.
func finalizeRoundNative(amount uint64, resultNegative, roundUp bool) int64 {
	if roundUp && !resultNegative && amount == 0 {
		return 1
	}
	result := int64(amount)
	if resultNegative {
		result = -result
	}
	return result
}

// MulRoundStrict multiplies two Amounts using rippled's mulRoundStrict algorithm
// (canonicalizeRoundStrict + NumberRoundModeGuard(towards_zero)).
func MulRoundStrict(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	if addSlop {
		amount, offset = CanonicalizeRoundIOUOverflow(amount, offset)
	}
	return FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, RoundTowardsZero, true)
}

// MulRound multiplies two Amounts using rippled's mulRound (non-strict)
// algorithm (canonicalizeRound + DontAffectNumberRoundMode). The non-strict
// canonicalize adds 9 or 10 based on loop count rather than the actual
// remainder, and installs no Number rounding-mode guard.
func MulRound(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	if addSlop {
		amount, offset = CanonicalizeRoundIOUOverflow(amount, offset)
	}
	return FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, 0, false)
}

// DivRound divides two Amounts using rippled's divRound (non-strict) algorithm
// (canonicalizeRound + DontAffectNumberRoundMode).
func DivRound(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		panic("division by zero")
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	numVal, numOff := PrepareMulDivOperand(num)
	denVal, denOff := PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp

	amount := DivMantissas(numVal, denVal, addSlop)
	offset := numOff - denOff - 17
	if addSlop {
		amount, offset = CanonicalizeRoundIOUOverflow(amount, offset)
	}
	return FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, 0, false)
}

// DivRoundStrict divides two Amounts using rippled's divRoundStrict algorithm
// (canonicalizeRound + NumberRoundModeGuard). The guard mode is upward when
// rounding away from zero and downward otherwise.
func DivRoundStrict(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		panic("division by zero")
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	numVal, numOff := PrepareMulDivOperand(num)
	denVal, denOff := PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp

	amount := DivMantissas(numVal, denVal, addSlop)
	offset := numOff - denOff - 17
	if addSlop {
		amount, offset = CanonicalizeRoundIOUOverflow(amount, offset)
	}
	mode := RoundDownward
	if roundUp != resultNegative {
		mode = RoundUpward
	}
	return FinalizeRoundIOU(amount, offset, resultNegative, roundUp, currency, issuer, mode, true)
}

// DivRoundNative divides two Amounts and returns the result as XRP drops, using
// the native canonicalizeRound path (native=true) of rippled's divRoundImpl.
func DivRoundNative(num, den Amount, roundUp bool) int64 {
	if den.IsZero() {
		panic("division by zero")
	}
	if num.IsZero() {
		return 0
	}
	numVal, numOff := PrepareMulDivOperand(num)
	denVal, denOff := PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp

	amount := DivMantissas(numVal, denVal, addSlop)
	offset := numOff - denOff - 17
	amount = canonicalizeRoundNative(amount, offset, addSlop)
	return finalizeRoundNative(amount, resultNegative, roundUp)
}

// MulRoundNative multiplies two Amounts and returns the result as XRP drops,
// using the native canonicalizeRound path (native=true) of rippled's
// mulRoundImpl. See canonicalizeRoundNative for the two currently-unreachable
// faithfulness gaps (no-round truncation and the missing native×native overflow
// Throw).
func MulRoundNative(v1, v2 Amount, roundUp bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	amount = canonicalizeRoundNative(amount, offset, addSlop)
	return finalizeRoundNative(amount, resultNegative, roundUp)
}
