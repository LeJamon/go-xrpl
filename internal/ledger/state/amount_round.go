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

// maxNativeDrops is cMaxNativeN (10^17 drops) as a signed value. A native (XRP)
// magnitude above it exceeds the total supply and is out of range.
const maxNativeDrops = int64(MaxNativeDrops)

// guardNativeOffset enforces STAmount::canonicalize's native pre-loop check:
// log10(cMaxNativeN) == 17, so a non-zero magnitude with a scale-up offset above
// 17 is unconditionally out of range.
func guardNativeOffset(exponent int) {
	if exponent > 17 {
		panic("Native currency amount out of range")
	}
}

// guardNativeDrops enforces cMaxNativeN on a native drops magnitude (>= 0),
// mirroring STAmount::canonicalize's per-multiply and post-loop checks. rippled
// Throws here; go-xrpl panics, recovered as tefEXCEPTION at the tx-apply boundary
// and as a path-find failure on the RPC path.
func guardNativeDrops(value int64) {
	if value > maxNativeDrops {
		panic("Native currency amount out of range")
	}
}

// CanonicalizeDrops converts an IOU-style mantissa/exponent to XRP drops using
// rippled's non-strict native canonicalizeRound: a positive offset scales up,
// and a negative offset rounds away from zero by loop count (add 10 when one
// division loop ran, 9 when two or more). The cMaxNativeN range checks match
// STAmount::canonicalize: out of range before each scale-up multiply, and again
// on the final magnitude.
func CanonicalizeDrops(mantissa int64, exponent int) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}
	guardNativeOffset(exponent)
	for exponent > 0 {
		guardNativeDrops(value)
		value *= 10
		exponent--
	}
	if exponent < 0 {
		loops := 0
		for exponent < -1 {
			value /= 10
			exponent++
			loops++
		}
		adder := int64(10)
		if loops >= 2 {
			adder = 9
		}
		value = (value + adder) / 10
	}
	guardNativeDrops(value)
	if mantissa < 0 {
		return -value
	}
	return value
}

// CanonicalizeDropsStrict is the strict (canonicalizeRoundStrict) native variant:
// it tracks whether any digits were actually dropped and forces a round-up (add
// 10) only when rounding up away from a true remainder, otherwise adds 9.
func CanonicalizeDropsStrict(mantissa int64, exponent int, roundUp bool) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}
	guardNativeOffset(exponent)
	for exponent > 0 {
		guardNativeDrops(value)
		value *= 10
		exponent--
	}
	if exponent < 0 {
		hadRemainder := false
		for exponent < -1 {
			newValue := value / 10
			if value != newValue*10 {
				hadRemainder = true
			}
			value = newValue
			exponent++
		}
		adder := int64(9)
		if hadRemainder && roundUp {
			adder = 10
		}
		value = (value + adder) / 10
	}
	guardNativeDrops(value)
	if mantissa < 0 {
		return -value
	}
	return value
}

// canonicalizeDropsNoRound converts a positive (amount, offset) magnitude to XRP
// drops on the not-rounding-away-from-zero native path. The non-strict variant
// installs no Number rounding-mode guard, so post-switchover STAmount::canonicalize
// builds the native result through Number and rounds the discarded fraction
// to-nearest (banker's); the strict variant guards Number to towards-zero
// (mulRoundStrict) / downward (divRoundStrict), i.e. truncation. Pre-switchover
// both truncate.
func canonicalizeDropsNoRound(amount uint64, offset int, strict bool) int64 {
	if !strict && GetNumberSwitchover() {
		if amount == 0 || offset <= -20 {
			return 0
		}
		guardNativeOffset(offset)
		drops := XRPLNumber{mantissa: int64(amount), exponent: offset}.ToInt64WithMode(RoundToNearest)
		guardNativeDrops(drops)
		return drops
	}
	drops := int64(amount)
	if drops == 0 {
		return 0
	}
	guardNativeOffset(offset)
	for offset > 0 {
		guardNativeDrops(drops)
		drops *= 10
		offset--
	}
	for offset < 0 {
		drops /= 10
		offset++
	}
	guardNativeDrops(drops)
	return drops
}

// NativeRoundDrops finalizes a muldiv-round magnitude (amount, offset) as signed
// XRP drops — the single native (XRP-output) tail shared by the state and offer
// mul/div round variants. When rounding away from zero (addSlop) it canonicalizes
// via CanonicalizeDrops{,Strict}; otherwise it rescales to drops via
// canonicalizeDropsNoRound (non-strict rounds to-nearest post-switchover; strict
// and pre-switchover truncate). A positive round-up that collapses to zero yields
// 1 drop.
func NativeRoundDrops(amount uint64, offset int, resultNegative, roundUp, addSlop, strict bool) int64 {
	var drops int64
	if addSlop {
		if strict {
			drops = CanonicalizeDropsStrict(int64(amount), offset, roundUp)
		} else {
			drops = CanonicalizeDrops(int64(amount), offset)
		}
	} else {
		drops = canonicalizeDropsNoRound(amount, offset, strict)
	}
	if drops == 0 && roundUp && !resultNegative {
		drops = 1
	}
	if resultNegative {
		drops = -drops
	}
	return drops
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
	return NativeRoundDrops(amount, offset, resultNegative, roundUp, addSlop, false)
}

// MulRoundNative multiplies two Amounts and returns the result as XRP drops,
// using the native canonicalizeRound path (native=true) of rippled's
// mulRoundImpl. When both operands are native XRP it takes mulRoundImpl's
// native×native fast path: the product of the two drop values under an overflow
// guard.
func MulRoundNative(v1, v2 Amount, roundUp bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}
	if v1.IsNative() && v2.IsNative() {
		return mulNativeNative(v1.Drops(), v2.Drops())
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	return NativeRoundDrops(amount, offset, resultNegative, roundUp, addSlop, false)
}

// MulRoundNativeStrict multiplies two Amounts and returns the result as XRP
// drops, taking the strict native canonicalize path of rippled's
// mulRoundStrict (mulRoundImpl<canonicalizeRoundStrict, NumberRoundModeGuard>
// with a native asset). It canonicalizes the un-truncated muldiv product
// directly to drops via canonicalizeRoundStrict(native=true): when rounding
// away from zero a true sub-drop remainder forces a round-up, unlike the IOU
// MulRoundStrict which first collapses the product to a 16-digit mantissa and
// discards that remainder.
func MulRoundNativeStrict(v1, v2 Amount, roundUp bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}
	if v1.IsNative() && v2.IsNative() {
		return mulNativeNative(v1.Drops(), v2.Drops())
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp

	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	return NativeRoundDrops(amount, offset, resultNegative, roundUp, addSlop, true)
}

// DivRoundNativeStrict divides two Amounts and returns the result as XRP drops,
// taking the native canonicalize path of rippled's divRoundStrict
// (divRoundImpl<NumberRoundModeGuard> with a native asset). divRoundImpl always
// uses the non-strict canonicalizeRound for the away-from-zero branch (only
// mulRoundImpl is templated on the canonicalize function), so the round-up leg
// matches the non-strict DivRoundNative; strictness only changes the toward-zero
// leg, which truncates the un-truncated product to drops rather than rounding to
// nearest. Like MulRoundNativeStrict it canonicalizes the raw product, not an
// IOU-collapsed mantissa.
func DivRoundNativeStrict(num, den Amount, roundUp bool) int64 {
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
	if addSlop {
		drops := CanonicalizeDrops(int64(amount), offset)
		if drops == 0 && roundUp && !resultNegative {
			drops = 1
		}
		if resultNegative {
			drops = -drops
		}
		return drops
	}
	drops := canonicalizeDropsNoRound(amount, offset, true)
	if resultNegative {
		drops = -drops
	}
	return drops
}

// mulNativeNative reproduces rippled's mulRoundImpl native×native fast path: the
// product of the two drop values, guarded against a result exceeding cMaxNative
// before the multiply. The bounds are sqrt(cMaxNative) and cMaxNative/2^32; an
// out-of-range product panics ("Native value overflow") where rippled Throws.
func mulNativeNative(a, b int64) int64 {
	if a > b {
		a, b = b, a
	}
	minV, maxV := uint64(a), uint64(b)
	if minV > 3_000_000_000 {
		panic("Native value overflow")
	}
	if (maxV>>32)*minV > 2_095_475_792 {
		panic("Native value overflow")
	}
	return int64(minV * maxV)
}
