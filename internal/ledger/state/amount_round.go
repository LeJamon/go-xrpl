package state

import "math/big"

// Shared big.Int constants for the muldiv-round core. Treated as immutable;
// muldivRound only reads them.
var (
	bigOne     = big.NewInt(1)
	bigTenTo14 = new(big.Int).SetUint64(100_000_000_000_000)     // 10^14
	bigTenTo17 = new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
)

const maxInt64Value uint64 = 1<<63 - 1

// PrepareMulDivOperand returns the absolute mantissa and exponent of a,
// normalizing native (XRP) amounts up into the IOU mantissa range
// [10^15, 10^16). This is the per-operand preamble every mul/div round variant
// shares; the result sign is taken separately from a.IsNegative().
func PrepareMulDivOperand(a Amount) (mantissa int64, exponent int) {
	mantissa = a.Mantissa()
	exponent = a.Exponent()
	if a.IsNative() || a.IsMPT() {
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

func MulRoundMPTWithNumberContext(v1, v2 Amount, ctx NumberContext, roundUp bool) int64 {
	return mulRoundMPT(v1, v2, ctx, roundUp, false)
}

func MulRoundMPTStrictWithNumberContext(v1, v2 Amount, ctx NumberContext, roundUp bool) int64 {
	return mulRoundMPT(v1, v2, ctx, roundUp, true)
}

func mulRoundMPT(v1, v2 Amount, ctx NumberContext, roundUp, strict bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}
	value1, offset1 := PrepareMulDivOperand(v1)
	value2, offset2 := PrepareMulDivOperand(v2)
	resultNegative := v1.IsNegative() != v2.IsNegative()
	addSlop := resultNegative != roundUp
	amount := MulMantissas(value1, value2, addSlop)
	offset := offset1 + offset2 + 14
	return finalizeMPTRound(amount, offset, resultNegative, roundUp, addSlop, strict, strict, ctx)
}

func DivRoundMPTWithNumberContext(num, den Amount, ctx NumberContext, roundUp bool) int64 {
	return divRoundMPT(num, den, ctx, roundUp, false)
}

func DivRoundMPTStrictWithNumberContext(num, den Amount, ctx NumberContext, roundUp bool) int64 {
	return divRoundMPT(num, den, ctx, roundUp, true)
}

func divRoundMPT(num, den Amount, ctx NumberContext, roundUp, strict bool) int64 {
	if den.IsZero() {
		panic("division by zero")
	}
	if num.IsZero() {
		return 0
	}
	numVal, numOffset := PrepareMulDivOperand(num)
	denVal, denOffset := PrepareMulDivOperand(den)
	resultNegative := num.IsNegative() != den.IsNegative()
	addSlop := resultNegative != roundUp
	amount := DivMantissas(numVal, denVal, addSlop)
	offset := numOffset - denOffset - 17
	return finalizeMPTRound(amount, offset, resultNegative, roundUp, addSlop, strict, false, ctx)
}

func finalizeMPTRound(
	amount uint64,
	offset int,
	resultNegative, roundUp, addSlop, strict, strictCanonicalize bool,
	ctx NumberContext,
) int64 {
	if amount == 0 || offset <= -20 {
		if roundUp && !resultNegative {
			return 1
		}
		return 0
	}
	if addSlop {
		amount, offset = canonicalizeIntegralRound(amount, offset, roundUp, strictCanonicalize)
	} else {
		amount = canonicalizeMPTNoRound(amount, offset, strict, ctx)
		offset = 0
	}
	if offset > 18 {
		panic("MPT amount out of range")
	}
	for offset > 0 {
		if amount > maxInt64Value/10 {
			panic("MPT amount out of range")
		}
		amount *= 10
		offset--
	}
	for offset < 0 {
		amount /= 10
		offset++
	}
	if amount > maxInt64Value {
		panic("MPT amount out of range")
	}
	if amount == 0 && roundUp && !resultNegative {
		amount = 1
	}
	value := int64(amount)
	if resultNegative {
		value = -value
	}
	return value
}

func canonicalizeIntegralRound(amount uint64, offset int, roundUp, strict bool) (uint64, int) {
	if offset >= 0 {
		return amount, offset
	}
	loops := 0
	hadRemainder := false
	for offset < -1 {
		newAmount := amount / 10
		hadRemainder = hadRemainder || amount != newAmount*10
		amount = newAmount
		offset++
		loops++
	}
	adder := uint64(10)
	if strict {
		adder = 9
		if hadRemainder && roundUp {
			adder = 10
		}
	} else if loops >= 2 {
		adder = 9
	}
	return (amount + adder) / 10, offset + 1
}

func canonicalizeMPTNoRound(amount uint64, offset int, strict bool, ctx NumberContext) uint64 {
	if !strict && ctx.UniversalNumberEnabled() {
		if amount > maxInt64Value {
			panic("MPT amount out of range")
		}
		value := newXRPLNumberRaw(int64(amount), offset).ToInt64WithMode(RoundToNearest)
		if value < 0 {
			panic("MPT amount out of range")
		}
		return uint64(value)
	}
	for offset > 0 {
		if amount > maxInt64Value/10 {
			panic("MPT amount out of range")
		}
		amount *= 10
		offset--
	}
	for offset < 0 {
		amount /= 10
		offset++
	}
	return amount
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
func canonicalizeDropsNoRound(amount uint64, offset int, strict bool, ctx NumberContext) int64 {
	if !strict && ctx.UniversalNumberEnabled() {
		if amount == 0 || offset <= -20 {
			return 0
		}
		guardNativeOffset(offset)
		drops := newXRPLNumberRaw(int64(amount), offset).ToInt64WithMode(RoundToNearest)
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

func NativeRoundDropsWithNumberContext(
	amount uint64,
	offset int,
	resultNegative, roundUp, addSlop, strict bool,
	ctx NumberContext,
) int64 {
	var drops int64
	if addSlop {
		if strict {
			drops = CanonicalizeDropsStrict(int64(amount), offset, roundUp)
		} else {
			drops = CanonicalizeDrops(int64(amount), offset)
		}
	} else {
		drops = canonicalizeDropsNoRound(amount, offset, strict, ctx)
	}
	if drops == 0 && roundUp && !resultNegative {
		drops = 1
	}
	if resultNegative {
		drops = -drops
	}
	return drops
}

func FinalizeRoundIOUWithNumberContext(
	amount uint64,
	offset int,
	resultNegative, roundUp bool,
	currency, issuer string,
	mode RoundingMode,
	useMode bool,
	ctx NumberContext,
) Amount {
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	resultMode := RoundToNearest
	if useMode {
		resultMode = mode
	}
	result := ctx.IssuedAmount(mantissa, offset, currency, issuer, resultMode)
	if roundUp && !resultNegative && result.IsZero() {
		return ctx.IssuedAmount(MinMantissa, MinExponent, currency, issuer, RoundToNearest)
	}
	return result
}

func MulRoundStrictWithNumberContext(
	v1, v2 Amount,
	currency, issuer string,
	ctx NumberContext,
	roundUp bool,
) Amount {
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
	return FinalizeRoundIOUWithNumberContext(
		amount, offset, resultNegative, roundUp, currency, issuer, RoundTowardsZero, true, ctx,
	)
}

func MulRoundWithNumberContext(
	v1, v2 Amount,
	currency, issuer string,
	ctx NumberContext,
	roundUp bool,
) Amount {
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
	return FinalizeRoundIOUWithNumberContext(
		amount, offset, resultNegative, roundUp, currency, issuer, 0, false, ctx,
	)
}

func DivRoundWithNumberContext(
	num, den Amount,
	currency, issuer string,
	ctx NumberContext,
	roundUp bool,
) Amount {
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
	return FinalizeRoundIOUWithNumberContext(
		amount, offset, resultNegative, roundUp, currency, issuer, 0, false, ctx,
	)
}

func DivRoundStrictWithNumberContext(
	num, den Amount,
	currency, issuer string,
	ctx NumberContext,
	roundUp bool,
) Amount {
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
	return FinalizeRoundIOUWithNumberContext(
		amount, offset, resultNegative, roundUp, currency, issuer, mode, true, ctx,
	)
}

func DivRoundNativeWithNumberContext(
	num, den Amount,
	ctx NumberContext,
	roundUp bool,
) int64 {
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
	return NativeRoundDropsWithNumberContext(
		amount, offset, resultNegative, roundUp, addSlop, false, ctx,
	)
}

func MulRoundNativeWithNumberContext(
	v1, v2 Amount,
	ctx NumberContext,
	roundUp bool,
) int64 {
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
	return NativeRoundDropsWithNumberContext(
		amount, offset, resultNegative, roundUp, addSlop, false, ctx,
	)
}

func MulRoundNativeStrictWithNumberContext(
	v1, v2 Amount,
	ctx NumberContext,
	roundUp bool,
) int64 {
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
	return NativeRoundDropsWithNumberContext(
		amount, offset, resultNegative, roundUp, addSlop, true, ctx,
	)
}

func DivRoundNativeStrictWithNumberContext(
	num, den Amount,
	ctx NumberContext,
	roundUp bool,
) int64 {
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
	drops := canonicalizeDropsNoRound(amount, offset, true, ctx)
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
	product := int64(minV * maxV)
	guardNativeDrops(product)
	return product
}
