package state

// XRPLNumber implements rippled's Number class with Guard-based precision.
//
// The Number class uses a wider exponent range [-32768, 32768] than IOUAmount
// [-96, 80] and employs a Guard mechanism that preserves digits discarded
// during scale-down, enabling banker's rounding (round-half-to-even). When
// fixUniversalNumber is enabled, IOUAmount arithmetic delegates here.
//
// Mantissa scale. Since rippled 3.1.0 the normalized mantissa range is
// runtime-selectable between two scales:
//   - "small": mantissa in [10^15, 10^16-1], rangeLog 15 (the historical range,
//     matching STAmount for IOUs).
//   - "large": mantissa in [10^18, 10^19-1], rangeLog 18. This represents every
//     int64 drops/MPT value exactly (maxRep = 2^63-1 lies inside the range).
//
// rippled selects the scale via a thread_local range_, installed for the whole
// transaction (preflight, preclaim, calculateBaseFee, doApply): large when no
// transaction rules are set, or when SingleAssetVault or LendingProtocol is
// enabled; small otherwise. Its process-wide default (outside any transaction
// context) is large.
//
// go-xrpl removed Number's global mutable state (see the switchover and rounding
// mode below) to keep concurrent amount math race-free. Following the same
// philosophy, the scale is not a mutable global: each XRPLNumber carries its own
// scale, and the entry points that create Numbers from a transaction context
// derive it from *amendment.Rules (see MantissaScaleForRules). Every current
// arithmetic caller runs inside transaction processing without SingleAssetVault
// or LendingProtocol, so they use the small scale — the only scale reachable on
// v3.0.0 — via the unscaled constructors, which default to small. This is
// exactly equivalent to rippled installing its small range for those steps.
//
// Because a normalized large-scale mantissa (up to 10^19-1) exceeds int64, the
// internal representation is an unsigned mantissa plus a sign flag, matching
// rippled. The external view (Mantissa/Exponent) collapses back to a signed
// 63-bit mantissa.
//
// Panic contract: Add / Mul / Div / normalize / root2 / ToIOUAmountValue panic
// on overflow, divide-by-zero, and NaN inputs — matching rippled's
// Throw<std::overflow_error>. Arithmetic overflow during evaluation is caught by
// recover() points in the tx engine and surfaced as a TER; the node never
// crashes from a peer-fed amount overflow.

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
)

// XRPLNumber exponent bounds and the zero sentinel exponent.
const (
	xrplNumMinExponent = -32768
	xrplNumMaxExponent = 32768
	// xrplNumZeroExponent is Number{}'s exponent: std::numeric_limits<int>::lowest().
	xrplNumZeroExponent = -2147483648
	// xrplNumMaxRep is the largest signed 63-bit mantissa, std::int64_t max.
	xrplNumMaxRep uint64 = 9223372036854775807
)

// MantissaScale selects the normalized mantissa range for an XRPLNumber.
type MantissaScale int

const (
	// MantissaScaleSmall is the historical IOU range [10^15, 10^16-1]. It is the
	// zero value, so the unscaled constructors default to it.
	MantissaScaleSmall MantissaScale = iota
	// MantissaScaleLargeLegacy uses the large range while preserving the
	// historical rounding behavior at math.MaxInt64.
	MantissaScaleLargeLegacy
	// MantissaScaleLarge is the [10^18, 10^19-1] range that represents every
	// int64 value exactly, with the cusp-rounding fix enabled.
	MantissaScaleLarge
)

// params returns the (min, max) normalized mantissa and the rangeLog (log10 of
// min) for the scale.
func (s MantissaScale) params() (minM, maxM uint64, rangeLog int) {
	if s != MantissaScaleSmall {
		return 1_000_000_000_000_000_000, 9_999_999_999_999_999_999, 18
	}
	return 1_000_000_000_000_000, 9_999_999_999_999_999, 15
}

// MantissaScaleForRulesWithFix selects the exact rippled Number range. The
// large range predates the cusp-rounding fix, so amendment-enabled ledgers use
// LargeLegacy until fixCleanup3_2_0 is enabled. With no Rules context rippled
// uses the corrected Large range.
func MantissaScaleForRulesWithFix(hasRules, singleAssetVault, lendingProtocol, fixCleanup320 bool) MantissaScale {
	if !hasRules {
		return MantissaScaleLarge
	}
	if !singleAssetVault && !lendingProtocol {
		return MantissaScaleSmall
	}
	if fixCleanup320 {
		return MantissaScaleLarge
	}
	return MantissaScaleLargeLegacy
}

// NumberContext creates XRPLNumber values in one immutable mantissa scale.
// Transaction code derives one context from the ledger rules and passes its
// values explicitly, so concurrent ledgers cannot affect each other's math.
type NumberContext struct {
	scale           MantissaScale
	universalNumber bool
}

// NewNumberContext returns a Number context fixed to scale and arithmetic
// behavior.
func NewNumberContext(scale MantissaScale, universalNumber bool) NumberContext {
	return NumberContext{scale: scale, universalNumber: universalNumber}
}

// Scale returns the context's mantissa scale.
func (c NumberContext) Scale() MantissaScale {
	return c.scale
}

// UniversalNumberEnabled reports whether STAmount and IOUAmount operations use
// XRPLNumber arithmetic.
func (c NumberContext) UniversalNumberEnabled() bool {
	return c.universalNumber
}

// Number creates a Number in the context's scale under mode.
func (c NumberContext) Number(mantissa int64, exponent int, mode RoundingMode) XRPLNumber {
	return newNumber(mantissa, exponent, c.scale, mode)
}

// New is an alias for Number.
func (c NumberContext) New(mantissa int64, exponent int, mode RoundingMode) XRPLNumber {
	return c.Number(mantissa, exponent, mode)
}

// Int creates an integral Number in the context's scale.
func (c NumberContext) Int(value int64) XRPLNumber {
	return c.Number(value, 0, RoundToNearest)
}

// FromInt creates an integral Number in the context's scale under mode.
func (c NumberContext) FromInt(value int64, mode RoundingMode) XRPLNumber {
	return c.Number(value, 0, mode)
}

// FromAmount converts an asset amount into a Number. XRP is measured in drops,
// MPTs in their integral units, and IOUs retain their decimal representation.
func (c NumberContext) FromAmount(amount Amount, mode RoundingMode) XRPLNumber {
	if amount.IsNative() {
		return c.FromInt(amount.Drops(), mode)
	}
	if raw, ok := amount.MPTRaw(); ok {
		return c.FromInt(raw, mode)
	}
	return c.Number(amount.Mantissa(), amount.Exponent(), mode)
}

// ToAmount converts number to the asset kind and issue carried by prototype.
// Integral assets use mode at their integer boundary; IOUs use it while
// normalizing to the STAmount mantissa range.
func (c NumberContext) ToAmount(number XRPLNumber, prototype Amount, mode RoundingMode) Amount {
	if prototype.IsNative() {
		return NewXRPAmountFromInt(number.ToInt64WithMode(mode))
	}
	if _, ok := prototype.MPTRaw(); ok {
		value := number.ToInt64WithMode(mode)
		if prototype.IsMPT() {
			return NewMPTAmountWithIssuanceID(value, prototype.Issuer, prototype.MPTIssuanceID())
		}
		return NewMPTAmountDirect(value, prototype.Currency, prototype.Issuer)
	}
	iou := number.ToIOUAmountValueRounded(mode)
	return Amount{
		iou:      iou,
		Currency: prototype.Currency,
		Issuer:   prototype.Issuer,
	}
}

// ToAmountWithNativeRounding mirrors rippled's toAmount(asset, number, mode):
// the explicit mode applies only to XRP, while IOU and MPT conversion uses the
// rounding mode already active for the surrounding Number expression.
func (c NumberContext) ToAmountWithNativeRounding(
	number XRPLNumber,
	prototype Amount,
	nativeMode, ambientMode RoundingMode,
) Amount {
	if prototype.IsNative() {
		return c.ToAmount(number, prototype, nativeMode)
	}
	return c.ToAmount(number, prototype, ambientMode)
}

// IOUAmountValue constructs and normalizes an IOU amount under the context.
func (c NumberContext) IOUAmountValue(mantissa int64, exponent int, mode RoundingMode) IOUAmountValue {
	return newIOUAmountValueRoundedWithContext(mantissa, exponent, mode, c)
}

// IssuedAmount constructs and normalizes an issued amount under the context.
func (c NumberContext) IssuedAmount(
	mantissa int64,
	exponent int,
	currency, issuer string,
	mode RoundingMode,
) Amount {
	return Amount{
		iou:      c.IOUAmountValue(mantissa, exponent, mode),
		Currency: currency,
		Issuer:   issuer,
	}
}

func (s MantissaScale) cuspRoundingFixEnabled() bool {
	return s == MantissaScaleLarge
}

// RoundingMode controls how XRPLNumber rounds during normalization. rippled
// stores the active mode in a thread_local (Number::mode_); go-xrpl threads it
// explicitly so concurrent amount math is race-free and deterministic. Every
// operation defaults to RoundToNearest; the mode-sensitive strict-rounding and
// AMM paths call the matching *Rounded variant.
type RoundingMode int

const (
	RoundToNearest   RoundingMode = iota // banker's rounding (default)
	RoundTowardsZero                     // always truncate towards zero
	RoundDownward                        // round towards negative infinity
	RoundUpward                          // round towards positive infinity
)

// XRPLNumber is a decimal floating-point value with Guard-based rounding.
type XRPLNumber struct {
	negative bool
	mantissa uint64
	exponent int
	scale    MantissaScale
}

// xrplGuard preserves discarded digits during scale-down operations, using BCD
// storage in a uint64 for 16 guard digits (rippled Number::Guard).
type xrplGuard struct {
	digits uint64 // 16 BCD guard digits
	xbit   bool   // a non-zero digit has been shifted off the end
	sbit   bool   // sign of the guard digits (true = negative)
}

func (g *xrplGuard) setNegative() { g.sbit = true }

// push adds a digit, shifting existing digits right.
func (g *xrplGuard) push(d uint) {
	g.xbit = g.xbit || (g.digits&0x000000000000000F) != 0
	g.digits >>= 4
	g.digits |= uint64(d&0x0F) << 60
}

// pop removes and returns the most significant guard digit.
func (g *xrplGuard) pop() uint {
	d := uint((g.digits & 0xF000000000000000) >> 60)
	g.digits <<= 4
	return d
}

func (g *xrplGuard) setDropped() { g.xbit = true }

// round returns the rounding direction: 1 up, -1 down, 0 exactly half.
func (g *xrplGuard) round(mode RoundingMode) int {
	if mode == RoundTowardsZero {
		return -1
	}
	if mode == RoundDownward {
		if g.sbit && (g.digits > 0 || g.xbit) {
			return 1
		}
		return -1
	}
	if mode == RoundUpward {
		if g.sbit {
			return -1
		}
		if g.digits > 0 || g.xbit {
			return 1
		}
		return -1
	}
	// to_nearest (banker's rounding)
	if g.digits > 0x5000000000000000 {
		return 1
	}
	if g.digits < 0x5000000000000000 {
		return -1
	}
	if g.xbit {
		return 1
	}
	return 0
}

// externalToInternal converts a signed int64 mantissa to (sign, magnitude),
// handling math.MinInt64 without overflow (rippled Number::externalToInternal).
func externalToInternal(mantissa int64) (negative bool, m uint64) {
	if mantissa >= 0 {
		return false, uint64(mantissa)
	}
	if mantissa >= -9223372036854775807 { // -maxRep
		return true, uint64(-mantissa)
	}
	// mantissa == math.MinInt64
	return true, xrplNumMaxRep + 1
}

// NewXRPLNumber creates a small-scale Number normalized with banker's rounding.
func NewXRPLNumber(mantissa int64, exponent int) XRPLNumber {
	return newNumber(mantissa, exponent, MantissaScaleSmall, RoundToNearest)
}

// NewXRPLNumberFromUint creates a positive small-scale Number from the full
// uint64 mantissa domain.
func NewXRPLNumberFromUint(mantissa uint64, exponent int) XRPLNumber {
	n := XRPLNumber{mantissa: mantissa, exponent: exponent, scale: MantissaScaleSmall}
	n.normalize(RoundToNearest)
	return n
}

// NewXRPLNumberRounded creates a small-scale Number normalized under mode.
func NewXRPLNumberRounded(mantissa int64, exponent int, mode RoundingMode) XRPLNumber {
	return newNumber(mantissa, exponent, MantissaScaleSmall, mode)
}

// NewXRPLNumberScaled creates a Number in the given scale, normalized under mode.
func NewXRPLNumberScaled(mantissa int64, exponent int, scale MantissaScale, mode RoundingMode) XRPLNumber {
	return newNumber(mantissa, exponent, scale, mode)
}

var xrplNumberPattern = regexp.MustCompile(`^([-+]?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

// ParseXRPLNumber parses the canonical decimal/scientific NUMBER syntax and
// normalizes it directly in scale. It does not pass through the codec's legacy
// small-scale parser, so large-scale transaction arithmetic retains its digits.
func ParseXRPLNumber(value string, scale MantissaScale, mode RoundingMode) (number XRPLNumber, err error) {
	match := xrplNumberPattern.FindStringSubmatch(value)
	if match == nil {
		return XRPLNumber{}, fmt.Errorf("invalid Number %q", value)
	}

	magnitude, parseErr := strconv.ParseUint(match[2]+match[3], 10, 64)
	if parseErr != nil {
		return XRPLNumber{}, fmt.Errorf("invalid Number %q: %w", value, parseErr)
	}
	exponent := -int64(len(match[3]))
	if match[4] != "" {
		parsed, parseErr := strconv.ParseInt(match[4], 10, 32)
		if parseErr != nil {
			return XRPLNumber{}, fmt.Errorf("invalid Number %q: %w", value, parseErr)
		}
		if parsed == math.MinInt32 {
			return XRPLNumber{}, fmt.Errorf("invalid Number %q: exponent out of range", value)
		}
		exponent += parsed
	}
	if int64(int(exponent)) != exponent {
		return XRPLNumber{}, fmt.Errorf("invalid Number %q: exponent out of range", value)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			number = XRPLNumber{}
			err = fmt.Errorf("invalid Number %q: %v", value, recovered)
		}
	}()
	return normalizeFromBig(match[1] == "-", new(big.Int).SetUint64(magnitude), int(exponent), scale, mode), nil
}

// NewXRPLNumberFromInt creates a small-scale Number from a plain integer.
func NewXRPLNumberFromInt(mantissa int64) XRPLNumber {
	return newNumber(mantissa, 0, MantissaScaleSmall, RoundToNearest)
}

func newNumber(mantissa int64, exponent int, scale MantissaScale, mode RoundingMode) XRPLNumber {
	neg, m := externalToInternal(mantissa)
	n := XRPLNumber{negative: neg, mantissa: m, exponent: exponent, scale: scale}
	n.normalize(mode)
	return n
}

// newXRPLNumberRaw builds a Number from an external (mantissa, exponent) without
// normalizing. Used where the caller needs the exact digits preserved before an
// int64 conversion (canonicalizeDropsNoRound); the scale is irrelevant because
// the only consumer, ToInt64WithMode, reads the external view.
func newXRPLNumberRaw(mantissa int64, exponent int) XRPLNumber {
	neg, m := externalToInternal(mantissa)
	return XRPLNumber{negative: neg, mantissa: m, exponent: exponent}
}

// intConst builds an integer Number in the receiver's scale (for the curve-fit
// constants inside root2).
func (n XRPLNumber) intConst(v int64) XRPLNumber {
	return newNumber(v, 0, n.scale, RoundToNearest)
}

// oneVal returns 1 in the receiver's scale, already normalized.
func (n XRPLNumber) oneVal() XRPLNumber {
	minM, _, rangeLog := n.scale.params()
	return XRPLNumber{mantissa: minM, exponent: -rangeLog, scale: n.scale}
}

// zero returns the canonical zero in the receiver's scale.
func (n XRPLNumber) zero() XRPLNumber {
	return XRPLNumber{mantissa: 0, exponent: xrplNumZeroExponent, scale: n.scale}
}

// xrplNumberZero returns a small-scale canonical zero.
func xrplNumberZero() XRPLNumber {
	return XRPLNumber{mantissa: 0, exponent: xrplNumZeroExponent}
}

// IsZero reports whether the number is zero.
func (n XRPLNumber) IsZero() bool { return n.mantissa == 0 }

// Signum returns -1, 0, or +1 as n is negative, zero, or positive.
func (n XRPLNumber) Signum() int {
	switch {
	case n.mantissa == 0:
		return 0
	case n.negative:
		return -1
	default:
		return 1
	}
}

// Cmp compares n and y, returning -1, 0, or +1.
func (n XRPLNumber) Cmp(y XRPLNumber) int {
	return n.Sub(y).Signum()
}

// Truncate drops the fractional part of n toward zero, mirroring rippled's
// Number::truncate().
func (n XRPLNumber) Truncate() XRPLNumber {
	if n.mantissa == 0 || n.exponent >= 0 {
		return n
	}
	q := new(big.Int).SetUint64(n.mantissa)
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-n.exponent)), nil)
	q.Quo(q, div)
	return normalizeFromBig(n.negative, q, 0, n.scale, RoundToNearest)
}

// Equal reports whether two Numbers have identical value. Matching rippled's
// operator==, the scale is not part of identity.
func (n XRPLNumber) Equal(other XRPLNumber) bool {
	return n.negative == other.negative && n.mantissa == other.mantissa && n.exponent == other.exponent
}

// Negate returns the additive inverse.
func (n XRPLNumber) Negate() XRPLNumber {
	if n.mantissa == 0 {
		return n
	}
	n.negative = !n.negative
	return n
}

// Mantissa returns the external (signed 63-bit) mantissa.
func (n XRPLNumber) Mantissa() int64 {
	m := n.mantissa
	if m > xrplNumMaxRep {
		m /= 10
	}
	if n.negative {
		return -int64(m)
	}
	return int64(m)
}

// Exponent returns the external exponent, adjusted when the internal mantissa
// exceeds the 63-bit range.
func (n XRPLNumber) Exponent() int {
	if n.mantissa > xrplNumMaxRep {
		return n.exponent + 1
	}
	return n.exponent
}

func (n XRPLNumber) MantissaScale() MantissaScale { return n.scale }

// bringIntoRange restores a rounded mantissa to the normalized range or clamps
// to zero on exponent underflow (rippled Guard::bringIntoRange).
func bringIntoRange(negative *bool, m *uint64, e *int, minM uint64) {
	if *m < minM {
		*m *= 10
		*e--
	}
	if *e < xrplNumMinExponent {
		*negative = false
		*m = 0
		*e = xrplNumZeroExponent
	}
}

// doRoundUp applies the guard's round-up decision and re-ranges (rippled
// Guard::doRoundUp). It panics on exponent overflow.
func (g *xrplGuard) doRoundUp(negative *bool, m *uint64, e *int, minM, maxM uint64, fixCusp bool, mode RoundingMode, loc string) {
	if r := g.round(mode); r == 1 || (r == 0 && (*m&1) == 1) {
		if fixCusp && (*m >= maxM || *m >= xrplNumMaxRep) {
			g.push(uint(*m % 10))
			*m /= 10
			*e++
			g.doRoundUp(negative, m, e, minM, maxM, fixCusp, mode, loc)
			return
		}
		*m++
		if !fixCusp && (*m > maxM || *m > xrplNumMaxRep) {
			*m /= 10
			*e++
		}
	}
	bringIntoRange(negative, m, e, minM)
	if *e > xrplNumMaxExponent {
		panic(loc)
	}
}

// doRoundDown applies the guard's round-down decision and re-ranges (rippled
// Guard::doRoundDown).
func (g *xrplGuard) doRoundDown(negative *bool, m *uint64, e *int, minM uint64, mode RoundingMode) {
	if r := g.round(mode); r == 1 || (r == 0 && (*m&1) == 1) {
		*m--
		if *m < minM {
			*m *= 10
			*e--
		}
	}
	bringIntoRange(negative, m, e, minM)
}

// normalize brings the (uint64) mantissa into the scale's range with Guard
// rounding under mode (rippled doNormalize for a 64-bit mantissa).
func (n *XRPLNumber) normalize(mode RoundingMode) {
	minM, maxM, _ := n.scale.params()
	if n.mantissa == 0 {
		*n = n.zero()
		return
	}
	m := n.mantissa
	e := n.exponent
	neg := n.negative

	for m < minM && e > xrplNumMinExponent {
		m *= 10
		e--
	}
	var g xrplGuard
	if neg {
		g.setNegative()
	}
	for m > maxM {
		if e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		g.push(uint(m % 10))
		m /= 10
		e++
	}
	if e < xrplNumMinExponent || m < minM {
		*n = n.zero()
		return
	}
	// Cut m down to fit int64 so rounding happens in that range; doRoundUp can
	// grow the mantissa back up to maxM to fill the range.
	if m > xrplNumMaxRep {
		if e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		g.push(uint(m % 10))
		m /= 10
		e++
	}
	g.doRoundUp(&neg, &m, &e, minM, maxM, n.scale.cuspRoundingFixEnabled(), mode, "XRPLNumber::normalize overflow")
	n.negative = neg
	n.mantissa = m
	n.exponent = e
}

// Add returns n + y using banker's rounding.
func (n XRPLNumber) Add(y XRPLNumber) XRPLNumber { return n.AddRounded(y, RoundToNearest) }

// AddRounded returns n + y rounded under mode (rippled operator+=).
func (n XRPLNumber) AddRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	if y.IsZero() {
		return n
	}
	if n.IsZero() {
		return y
	}
	if n.Equal(y.Negate()) {
		return n.zero()
	}
	minM, maxM, _ := n.scale.params()

	xn := n.negative
	xm := n.mantissa
	xe := n.exponent
	yn := y.negative
	ym := y.mantissa
	ye := y.exponent

	var g xrplGuard
	if xe < ye {
		if xn {
			g.setNegative()
		}
		for xe < ye {
			g.push(uint(xm % 10))
			xm /= 10
			xe++
		}
	} else if xe > ye {
		if yn {
			g.setNegative()
		}
		for xe > ye {
			g.push(uint(ym % 10))
			ym /= 10
			ye++
		}
	}

	if xn == yn {
		// Same sign: add magnitudes with a 128-bit intermediate.
		lo, carry := bits.Add64(xm, ym, 0)
		if carry != 0 || lo > maxM || lo > xrplNumMaxRep {
			q, r := bits.Div64(carry, lo, 10) // carry <= 1 < 10
			g.push(uint(r))
			xm = q
			xe++
		} else {
			xm = lo
		}
		g.doRoundUp(&xn, &xm, &xe, minM, maxM, n.scale.cuspRoundingFixEnabled(), mode, "XRPLNumber::addition overflow")
	} else {
		// Different sign: subtract magnitudes.
		if xm > ym {
			xm -= ym
		} else {
			xm = ym - xm
			xe = ye
			xn = yn
		}
		for xm < minM && xm <= xrplNumMaxRep/10 {
			xm = xm*10 - uint64(g.pop())
			xe--
		}
		g.doRoundDown(&xn, &xm, &xe, minM, mode)
	}

	r := XRPLNumber{negative: xn, mantissa: xm, exponent: xe, scale: n.scale}
	r.normalize(mode)
	return r
}

// Sub returns n - y.
func (n XRPLNumber) Sub(y XRPLNumber) XRPLNumber { return n.Add(y.Negate()) }

// Mul returns n * y using banker's rounding.
func (n XRPLNumber) Mul(y XRPLNumber) XRPLNumber { return n.MulRounded(y, RoundToNearest) }

// MulRounded returns n * y rounded under mode (rippled operator*=).
func (n XRPLNumber) MulRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	if n.IsZero() {
		return n
	}
	if y.IsZero() {
		return y
	}
	minM, maxM, _ := n.scale.params()

	zn := n.negative != y.negative
	ze := n.exponent + y.exponent

	zm := new(big.Int).Mul(new(big.Int).SetUint64(n.mantissa), new(big.Int).SetUint64(y.mantissa))
	bigMaxM := new(big.Int).SetUint64(maxM)
	bigMaxRep := new(big.Int).SetUint64(xrplNumMaxRep)
	ten := big.NewInt(10)
	rem := new(big.Int)

	var g xrplGuard
	if zn {
		g.setNegative()
	}
	for zm.Cmp(bigMaxM) > 0 || zm.Cmp(bigMaxRep) > 0 {
		zm.DivMod(zm, ten, rem)
		g.push(uint(rem.Uint64()))
		ze++
	}
	xm := zm.Uint64()
	g.doRoundUp(&zn, &xm, &ze, minM, maxM, n.scale.cuspRoundingFixEnabled(), mode, "XRPLNumber::multiplication overflow")

	r := XRPLNumber{negative: zn, mantissa: xm, exponent: ze, scale: n.scale}
	r.normalize(mode)
	return r
}

// Div returns n / y using banker's rounding.
func (n XRPLNumber) Div(y XRPLNumber) XRPLNumber { return n.DivRounded(y, RoundToNearest) }

// DivRounded returns n / y rounded under mode (rippled operator/=).
func (n XRPLNumber) DivRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	if y.IsZero() {
		panic("XRPLNumber: divide by zero")
	}
	if n.IsZero() {
		return n
	}
	zn := n.negative != y.negative

	// The first stage always uses 10^17. Large scales then retain five more
	// decimal places from the remainder before normalization.
	f := new(big.Int).SetUint64(100_000_000_000_000_000)
	dmu := new(big.Int).SetUint64(y.mantissa)
	numerator := new(big.Int).Mul(new(big.Int).SetUint64(n.mantissa), f)

	zm := new(big.Int)
	remainder := new(big.Int)
	zm.QuoRem(numerator, dmu, remainder)
	ze := n.exponent - y.exponent - 17
	dropped := false

	if n.scale != MantissaScaleSmall && remainder.Sign() != 0 {
		correctionFactor := big.NewInt(100_000)
		partialNumerator := new(big.Int).Mul(remainder, correctionFactor)
		correction := new(big.Int).Quo(new(big.Int).Set(partialNumerator), dmu)
		if correction.Sign() != 0 {
			zm.Mul(zm, correctionFactor)
			zm.Add(zm, correction)
			ze -= 5
		}
		if n.scale.cuspRoundingFixEnabled() {
			dropped = new(big.Int).Rem(partialNumerator, dmu).Sign() != 0
		}
	}

	return normalizeFromBigDropped(zn, zm, ze, n.scale, mode, dropped)
}

// normalizeFromBig normalizes a big.Int mantissa (the Div intermediate can
// exceed 64 bits) into an XRPLNumber (rippled doNormalize for uint128).
func normalizeFromBig(negative bool, m *big.Int, e int, scale MantissaScale, mode RoundingMode) XRPLNumber {
	return normalizeFromBigDropped(negative, m, e, scale, mode, false)
}

func normalizeFromBigDropped(negative bool, m *big.Int, e int, scale MantissaScale, mode RoundingMode, dropped bool) XRPLNumber {
	z := XRPLNumber{scale: scale}
	if m.Sign() == 0 {
		return z.zero()
	}
	minM, maxM, _ := scale.params()
	bigMinM := new(big.Int).SetUint64(minM)
	bigMaxM := new(big.Int).SetUint64(maxM)
	bigMaxRep := new(big.Int).SetUint64(xrplNumMaxRep)
	ten := big.NewInt(10)
	rem := new(big.Int)
	mm := new(big.Int).Set(m)

	for mm.Cmp(bigMinM) < 0 && e > xrplNumMinExponent {
		mm.Mul(mm, ten)
		e--
	}
	var g xrplGuard
	if negative {
		g.setNegative()
	}
	if dropped {
		g.setDropped()
	}
	for mm.Cmp(bigMaxM) > 0 {
		if e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		mm.DivMod(mm, ten, rem)
		g.push(uint(rem.Uint64()))
		e++
	}
	if e < xrplNumMinExponent || mm.Cmp(bigMinM) < 0 {
		return z.zero()
	}
	if mm.Cmp(bigMaxRep) > 0 {
		if e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		mm.DivMod(mm, ten, rem)
		g.push(uint(rem.Uint64()))
		e++
	}
	mu := mm.Uint64()
	g.doRoundUp(&negative, &mu, &e, minM, maxM, scale.cuspRoundingFixEnabled(), mode, "XRPLNumber::normalize overflow")
	return XRPLNumber{negative: negative, mantissa: mu, exponent: e, scale: scale}
}

// NormalizeToRange returns the value's mantissa and exponent renormalized to an
// arbitrary [minMantissa, maxMantissa] range (rippled Number::normalizeToRange).
// The returned mantissa is signed. It is used by STAmount's IOU conversion to
// snap a Number into the IOU mantissa range.
func (n XRPLNumber) NormalizeToRange(minMantissa, maxMantissa uint64) (mantissa int64, exponent int) {
	neg := n.negative
	m := n.mantissa
	e := n.exponent
	normalizeInRange(&neg, &m, &e, minMantissa, maxMantissa)
	if neg {
		return -int64(m), e
	}
	return int64(m), e
}

// normalizeInRange is doNormalize parameterized by an explicit mantissa range,
// under the ambient (to_nearest) rounding used by normalizeToRange.
func normalizeInRange(negative *bool, m *uint64, e *int, minM, maxM uint64) {
	if *m == 0 {
		*negative = false
		*e = xrplNumZeroExponent
		return
	}
	for *m < minM && *e > xrplNumMinExponent {
		*m *= 10
		*e--
	}
	var g xrplGuard
	if *negative {
		g.setNegative()
	}
	for *m > maxM {
		if *e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		g.push(uint(*m % 10))
		*m /= 10
		*e++
	}
	if *e < xrplNumMinExponent || *m < minM {
		*negative = false
		*m = 0
		*e = xrplNumZeroExponent
		return
	}
	if *m > xrplNumMaxRep {
		if *e >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		g.push(uint(*m % 10))
		*m /= 10
		*e++
	}
	g.doRoundUp(negative, m, e, minM, maxM, false, RoundToNearest, "XRPLNumber::normalize overflow")
}

// ToIOUAmountValue converts to IOUAmountValue using banker's rounding.
func (n XRPLNumber) ToIOUAmountValue() IOUAmountValue {
	return n.ToIOUAmountValueRounded(RoundToNearest)
}

// ToIOUAmountValueRounded converts to the IOU mantissa range under mode, then
// clamps the wider Number exponent range to IOUAmount's [-96, 80].
func (n XRPLNumber) ToIOUAmountValueRounded(mode RoundingMode) IOUAmountValue {
	if n.IsZero() {
		return ZeroIOUValue()
	}
	if n.scale != MantissaScaleSmall {
		n = newNumber(n.Mantissa(), n.Exponent(), MantissaScaleSmall, mode)
	}
	e := n.Exponent()
	if e > MaxExponent {
		panic("XRPLNumber→IOUAmountValue overflow")
	}
	if e < MinExponent {
		return ZeroIOUValue()
	}
	return IOUAmountValue{mantissa: n.Mantissa(), exponent: e}
}

// ToInt64WithMode converts to int64 with Guard rounding under mode (rippled
// Number::operator rep()). It panics on overflow.
func (n XRPLNumber) ToInt64WithMode(mode RoundingMode) int64 {
	drops := n.Mantissa()
	offset := n.Exponent()
	var g xrplGuard
	if drops == 0 {
		return 0
	}
	if drops < 0 {
		g.setNegative()
		drops = -drops
	}
	for offset < 0 {
		g.push(uint(drops % 10))
		drops /= 10
		offset++
	}
	for offset > 0 {
		if uint64(drops) > xrplNumMaxRep/10 {
			panic("XRPLNumber::operator rep() overflow")
		}
		drops *= 10
		offset--
	}
	if r := g.round(mode); r == 1 || (r == 0 && (drops&1) == 1) {
		if uint64(drops) >= xrplNumMaxRep {
			panic("XRPLNumber::operator rep() rounding overflow")
		}
		drops++
	}
	if g.sbit {
		drops = -drops
	}
	return drops
}

// shiftExponent returns the number with its exponent shifted by delta, keeping
// the mantissa (rippled Number::shiftExponent).
func (n XRPLNumber) shiftExponent(delta int) XRPLNumber {
	newE := n.exponent + delta
	if newE >= xrplNumMaxExponent {
		panic("XRPLNumber::shiftExponent overflow")
	}
	if newE < xrplNumMinExponent {
		return n.zero()
	}
	return XRPLNumber{negative: n.negative, mantissa: n.mantissa, exponent: newE, scale: n.scale}
}

// root2 computes the square root using banker's rounding.
func (n XRPLNumber) root2() XRPLNumber { return n.root2Rounded(RoundToNearest) }

// Root2 computes the square root using banker's rounding.
func (n XRPLNumber) Root2() XRPLNumber { return n.Root2Rounded(RoundToNearest) }

// root2Rounded computes the square root via Newton-Raphson iteration, rounding
// every intermediate under mode (rippled root2).
func (n XRPLNumber) root2Rounded(mode RoundingMode) XRPLNumber {
	return n.Root2Rounded(mode)
}

// Root2Rounded computes the square root via Newton-Raphson iteration, rounding
// every intermediate under mode.
func (n XRPLNumber) Root2Rounded(mode RoundingMode) XRPLNumber {
	one := n.oneVal()
	if n.Equal(one) {
		return n
	}
	if n.negative {
		panic("XRPLNumber::root2 nan")
	}
	if n.IsZero() {
		return n
	}

	_, _, rangeLog := n.scale.params()
	e := n.exponent + rangeLog + 1
	if e%2 != 0 {
		e++
	}
	f := n.shiftExponent(-e)

	a0 := n.intConst(18)
	a1 := n.intConst(144)
	a2 := n.intConst(-60)
	den := n.intConst(105)
	r := a2.MulRounded(f, mode).AddRounded(a1, mode).MulRounded(f, mode).AddRounded(a0, mode).DivRounded(den, mode)

	two := n.intConst(2)
	var rm1, rm2 XRPLNumber
	for {
		rm2 = rm1
		rm1 = r
		r = r.AddRounded(f.DivRounded(r, mode), mode).DivRounded(two, mode)
		if r.Equal(rm1) || r.Equal(rm2) {
			break
		}
	}
	return r.shiftExponent(e / 2)
}

// String renders the number as rippled's to_string(Number) does, using the
// receiver's scale to choose thresholds and padding. This is the text form of
// every NUMBER sfield in JSON.
func (n XRPLNumber) String() string {
	if n.mantissa == 0 {
		return "0"
	}
	_, _, rangeLog := n.scale.params()
	exponent := n.exponent
	mantissa := n.mantissa

	if exponent != 0 && (exponent < -(rangeLog+10) || exponent > -(rangeLog-10)) {
		for mantissa != 0 && mantissa%10 == 0 && exponent < xrplNumMaxExponent {
			mantissa /= 10
			exponent++
		}
		var b strings.Builder
		if n.negative {
			b.WriteByte('-')
		}
		b.WriteString(strconv.FormatUint(mantissa, 10))
		b.WriteByte('e')
		b.WriteString(strconv.Itoa(exponent))
		return b.String()
	}

	padPrefix := rangeLog + 12
	padSuffix := rangeLog + 8
	raw := strconv.FormatUint(mantissa, 10)
	val := strings.Repeat("0", padPrefix) + raw + strings.Repeat("0", padSuffix)
	offset := exponent + padPrefix + rangeLog + 1

	preFrom, preTo := 0, offset
	postFrom, postTo := offset, len(val)

	if preTo-preFrom > padPrefix {
		preFrom += padPrefix
	}
	for preFrom < preTo && val[preFrom] == '0' {
		preFrom++
	}
	if postTo-postFrom > padSuffix {
		postTo -= padSuffix
	}
	for postTo > postFrom && val[postTo-1] == '0' {
		postTo--
	}

	var b strings.Builder
	if n.negative {
		b.WriteByte('-')
	}
	if preFrom == preTo {
		b.WriteByte('0')
	} else {
		b.WriteString(val[preFrom:preTo])
	}
	if postTo != postFrom {
		b.WriteByte('.')
		b.WriteString(val[postFrom:postTo])
	}
	return b.String()
}
