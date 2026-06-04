package state

// XRPLNumber implements rippled's Number class with Guard-based precision.
// Reference: rippled/src/libxrpl/basics/Number.cpp
//
// The Number class uses wider exponent range [-32768, 32768] than IOUAmount
// [-96, 80] and employs a Guard mechanism that preserves digits discarded
// during scale-down, enabling banker's rounding (round-half-to-even).
// When fixUniversalNumber is enabled, IOUAmount arithmetic delegates here.
//
// Panic contract: Add / Mul / Div / normalize / root2 / ToIOUAmountValue
// panic on overflow, divide-by-zero, and NaN inputs — matching rippled's
// `Throw<std::overflow_error>`. ParseIOUAmountBinary / ParseMPTAmountBinary
// enforce codec-boundary bounds; arithmetic overflow during evaluation is
// caught by recover() points in the tx engine and surfaced as a TER (the
// node never crashes from a peer-fed amount overflow).

import (
	"math/big"
	"sync/atomic"
)

// XRPLNumber constants matching rippled's Number.h
const (
	xrplNumMinMantissa int64 = 1_000_000_000_000_000 // 10^15
	xrplNumMaxMantissa int64 = 9_999_999_999_999_999 // 10^16 - 1
	xrplNumMinExponent       = -32768
	xrplNumMaxExponent       = 32768
	// Zero exponent for Number (different from IOUAmount's zeroExponent)
	xrplNumZeroExponent = -2147483648 // math.MinInt32, matching Number{} default
)

// Package-level switchover flag, the Go equivalent of rippled's per-thread
// getSTNumberSwitchover() / setSTNumberSwitchover() (LocalValue<bool>).
//
// Unlike the rounding mode, the switchover is written only by the single
// transaction-apply goroutine (do_apply.go, from rules().Enabled(
// fixUniversalNumber)) and is constant for the duration of a ledger, since
// amendments do not flip mid-ledger. Concurrent readers are the RPC/path-find
// goroutines, which only ever read it. An atomic.Bool therefore removes the
// data race while preserving exact behavior: every reader observes the value
// the apply goroutine established for the current ledger.
//
// The rounding mode, by contrast, is mutated mid-computation by both the apply
// and RPC goroutines, so it cannot be a shared global — it is threaded
// explicitly through the arithmetic call path (see RoundingMode below).
var numberSwitchoverEnabled atomic.Bool

// SetNumberSwitchover enables or disables the XRPLNumber switchover.
// When enabled, IOUAmount arithmetic uses Guard-based precision.
func SetNumberSwitchover(enabled bool) {
	numberSwitchoverEnabled.Store(enabled)
}

// GetNumberSwitchover returns whether the XRPLNumber switchover is enabled.
func GetNumberSwitchover() bool {
	return numberSwitchoverEnabled.Load()
}

// RoundingMode controls how XRPLNumber rounds during normalization.
// Reference: Number::rounding_mode in Number.h line 196.
//
// rippled stores the active mode in a thread_local (Number::mode_) and mutates
// it via the NumberRoundModeGuard RAII helper. go-xrpl has no thread-local
// storage and a live node performs amount arithmetic concurrently (apply vs.
// RPC/path-find goroutines), so the mode is instead threaded explicitly as an
// argument: every operation defaults to RoundToNearest, and the mode-sensitive
// strict-rounding and AMM paths call the matching *Rounded variant with the
// desired mode. This keeps the arithmetic race-free and deterministic without
// any shared mutable rounding state.
type RoundingMode int

const (
	RoundToNearest   RoundingMode = iota // banker's rounding (default)
	RoundTowardsZero                     // always truncate towards zero
	RoundDownward                        // round towards negative infinity
	RoundUpward                          // round towards positive infinity
)

// XRPLNumber represents a decimal floating-point number with Guard-based rounding.
// Reference: rippled Number class in Number.h / Number.cpp
type XRPLNumber struct {
	mantissa int64
	exponent int
}

// xrplGuard preserves discarded digits during scale-down operations.
// Uses BCD (Binary Coded Decimal) storage in a uint64 for 16 guard digits.
// Reference: rippled Number::Guard class (Number.cpp lines 64-171)
type xrplGuard struct {
	digits uint64 // 16 BCD guard digits
	xbit   bool   // non-zero digit shifted off end
	sbit   bool   // sign bit (true = negative)
}

func (g *xrplGuard) setNegative() { g.sbit = true }

// push adds a digit to the guard, shifting existing digits right.
// Reference: Number.cpp lines 117-122
func (g *xrplGuard) push(d uint) {
	g.xbit = g.xbit || (g.digits&0x000000000000000F) != 0
	g.digits >>= 4
	g.digits |= uint64(d&0x0F) << 60
}

// pop removes and returns the most significant guard digit.
// Reference: Number.cpp lines 125-130
func (g *xrplGuard) pop() uint {
	d := uint((g.digits & 0xF000000000000000) >> 60)
	g.digits <<= 4
	return d
}

// round returns the rounding direction for the given mode.
// Returns: 1 if round up, -1 if round down, 0 if exactly half.
// Reference: Number.cpp lines 137-171
func (g *xrplGuard) round(mode RoundingMode) int {
	if mode == RoundTowardsZero {
		return -1
	}

	if mode == RoundDownward {
		if g.sbit {
			// Negative number, rounding down = more negative = round up magnitude
			if g.digits > 0 || g.xbit {
				return 1
			}
		}
		return -1
	}

	if mode == RoundUpward {
		if g.sbit {
			// Negative number, rounding up = less negative = round down magnitude
			return -1
		}
		if g.digits > 0 || g.xbit {
			return 1
		}
		return -1
	}

	// to_nearest mode (default, banker's rounding)
	if g.digits > 0x5000000000000000 {
		return 1
	}
	if g.digits < 0x5000000000000000 {
		return -1
	}
	// Exactly 0x5000000000000000
	if g.xbit {
		return 1
	}
	return 0
}

// NewXRPLNumber creates a new XRPLNumber and normalizes it using banker's
// rounding (RoundToNearest). Use NewXRPLNumberRounded to normalize under a
// different mode.
// Reference: Number::Number(rep mantissa, int exponent) in Number.h line 219-223
func NewXRPLNumber(mantissa int64, exponent int) XRPLNumber {
	return NewXRPLNumberRounded(mantissa, exponent, RoundToNearest)
}

// NewXRPLNumberRounded creates a new XRPLNumber and normalizes it under mode.
func NewXRPLNumberRounded(mantissa int64, exponent int, mode RoundingMode) XRPLNumber {
	n := XRPLNumber{mantissa: mantissa, exponent: exponent}
	n.normalize(mode)
	return n
}

// NewXRPLNumberFromInt creates a Number from a plain integer.
// Reference: Number::Number(rep mantissa) → Number{mantissa, 0}
func NewXRPLNumberFromInt(mantissa int64) XRPLNumber {
	return NewXRPLNumber(mantissa, 0)
}

// xrplNumberZero returns the zero Number.
func xrplNumberZero() XRPLNumber {
	return XRPLNumber{mantissa: 0, exponent: xrplNumZeroExponent}
}

// IsZero returns true if this number is zero.
func (n XRPLNumber) IsZero() bool {
	return n.mantissa == 0
}

// Equal returns true if two Numbers are identical.
func (n XRPLNumber) Equal(other XRPLNumber) bool {
	return n.mantissa == other.mantissa && n.exponent == other.exponent
}

// Negate returns the negated number.
func (n XRPLNumber) Negate() XRPLNumber {
	return XRPLNumber{mantissa: -n.mantissa, exponent: n.exponent}
}

// normalize adjusts mantissa and exponent to the proper range using Guard-based
// rounding under mode.
// Reference: Number.cpp lines 177-227
func (n *XRPLNumber) normalize(mode RoundingMode) {
	if n.mantissa == 0 {
		*n = xrplNumberZero()
		return
	}

	negative := n.mantissa < 0
	var m uint64
	if negative {
		m = uint64(-n.mantissa)
	} else {
		m = uint64(n.mantissa)
	}

	// Scale up if mantissa is too small
	for m < uint64(xrplNumMinMantissa) && n.exponent > xrplNumMinExponent {
		m *= 10
		n.exponent--
	}

	// Scale down with guard if mantissa is too large
	var g xrplGuard
	if negative {
		g.setNegative()
	}
	for m > uint64(xrplNumMaxMantissa) {
		if n.exponent >= xrplNumMaxExponent {
			panic("XRPLNumber::normalize overflow")
		}
		g.push(uint(m % 10))
		m /= 10
		n.exponent++
	}

	n.mantissa = int64(m)

	// Underflow to zero
	if n.exponent < xrplNumMinExponent || n.mantissa < xrplNumMinMantissa {
		*n = xrplNumberZero()
		return
	}

	// Apply guard rounding (round-half-to-even)
	r := g.round(mode)
	if r == 1 || (r == 0 && (n.mantissa&1) == 1) {
		n.mantissa++
		if n.mantissa > xrplNumMaxMantissa {
			n.mantissa /= 10
			n.exponent++
		}
	}

	if n.exponent > xrplNumMaxExponent {
		panic("XRPLNumber::normalize overflow")
	}

	if negative {
		n.mantissa = -n.mantissa
	}
}

// Add returns the sum of two XRPLNumbers using banker's rounding.
func (n XRPLNumber) Add(y XRPLNumber) XRPLNumber {
	return n.AddRounded(y, RoundToNearest)
}

// AddRounded returns the sum of two XRPLNumbers rounded under mode.
// Reference: Number::operator+= in Number.cpp lines 229-345
func (n XRPLNumber) AddRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	// Handle zero operands
	if y.IsZero() {
		return n
	}
	if n.IsZero() {
		return y
	}
	// Exact cancellation
	if n.Equal(y.Negate()) {
		return xrplNumberZero()
	}

	xm := n.mantissa
	xe := n.exponent
	xn := int64(1)
	if xm < 0 {
		xm = -xm
		xn = -1
	}

	ym := y.mantissa
	ye := y.exponent
	yn := int64(1)
	if ym < 0 {
		ym = -ym
		yn = -1
	}

	var g xrplGuard

	// Align exponents by shifting the smaller-exponent operand's digits into guard
	if xe < ye {
		if xn == -1 {
			g.setNegative()
		}
		for xe < ye {
			g.push(uint(xm % 10))
			xm /= 10
			xe++
		}
	} else if xe > ye {
		if yn == -1 {
			g.setNegative()
		}
		for xe > ye {
			g.push(uint(ym % 10))
			ym /= 10
			ye++
		}
	}

	if xn == yn {
		// Same sign: add magnitudes
		xm += ym
		if xm > xrplNumMaxMantissa {
			g.push(uint(xm % 10))
			xm /= 10
			xe++
		}
		r := g.round(mode)
		if r == 1 || (r == 0 && (xm&1) == 1) {
			xm++
			if xm > xrplNumMaxMantissa {
				xm /= 10
				xe++
			}
		}
		if xe > xrplNumMaxExponent {
			panic("XRPLNumber::addition overflow")
		}
	} else {
		// Different sign: subtract magnitudes
		if xm > ym {
			xm = xm - ym
		} else {
			xm = ym - xm
			xe = ye
			xn = yn
		}
		// Restore precision from guard digits
		for xm < xrplNumMinMantissa {
			xm *= 10
			xm -= int64(g.pop())
			xe--
		}
		r := g.round(mode)
		if r == 1 || (r == 0 && (xm&1) == 1) {
			xm--
			if xm < xrplNumMinMantissa {
				xm *= 10
				xe--
			}
		}
		if xe < xrplNumMinExponent {
			return xrplNumberZero()
		}
	}

	return XRPLNumber{mantissa: xm * xn, exponent: xe}
}

// Sub returns n - y.
func (n XRPLNumber) Sub(y XRPLNumber) XRPLNumber {
	return n.Add(y.Negate())
}

// Mul returns the product of two XRPLNumbers using banker's rounding.
func (n XRPLNumber) Mul(y XRPLNumber) XRPLNumber {
	return n.MulRounded(y, RoundToNearest)
}

// MulRounded returns the product of two XRPLNumbers rounded under mode.
// Reference: Number::operator*= in Number.cpp lines 375-445
func (n XRPLNumber) MulRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	if n.IsZero() {
		return n
	}
	if y.IsZero() {
		return y
	}

	xm := n.mantissa
	xe := n.exponent
	xn := int64(1)
	if xm < 0 {
		xm = -xm
		xn = -1
	}

	ym := y.mantissa
	ye := y.exponent
	yn := int64(1)
	if ym < 0 {
		ym = -ym
		yn = -1
	}

	// Use big.Int for multiplication (equivalent to uint128_t)
	zm := new(big.Int).Mul(big.NewInt(xm), big.NewInt(ym))
	ze := xe + ye
	zn := xn * yn

	// Scale down with guard
	var g xrplGuard
	if zn == -1 {
		g.setNegative()
	}
	bigMaxMant := big.NewInt(xrplNumMaxMantissa)
	bigTen := big.NewInt(10)
	bigRem := new(big.Int)
	for zm.Cmp(bigMaxMant) > 0 {
		zm.DivMod(zm, bigTen, bigRem)
		g.push(uint(bigRem.Int64()))
		ze++
	}

	xm = zm.Int64()
	xe = ze

	// Apply guard rounding
	r := g.round(mode)
	if r == 1 || (r == 0 && (xm&1) == 1) {
		xm++
		if xm > xrplNumMaxMantissa {
			xm /= 10
			xe++
		}
	}

	// Handle underflow/overflow
	if xe < xrplNumMinExponent {
		return xrplNumberZero()
	}
	if xe > xrplNumMaxExponent {
		panic("XRPLNumber::multiplication overflow")
	}

	return XRPLNumber{mantissa: xm * zn, exponent: xe}
}

// Div returns n / y using banker's rounding.
func (n XRPLNumber) Div(y XRPLNumber) XRPLNumber {
	return n.DivRounded(y, RoundToNearest)
}

// DivRounded returns n / y rounded under mode.
// Reference: Number::operator/= in Number.cpp lines 447-478
func (n XRPLNumber) DivRounded(y XRPLNumber, mode RoundingMode) XRPLNumber {
	if y.IsZero() {
		panic("XRPLNumber: divide by zero")
	}
	if n.IsZero() {
		return n
	}

	np := int64(1)
	nm := n.mantissa
	ne := n.exponent
	if nm < 0 {
		nm = -nm
		np = -1
	}

	dp := int64(1)
	dm := y.mantissa
	de := y.exponent
	if dm < 0 {
		dm = -dm
		dp = -1
	}

	// Scale by 10^17 for maximum precision without overflowing
	// uint128_t equivalent: big.Int
	f := new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
	bigNm := new(big.Int).SetInt64(nm)
	bigDm := new(big.Int).SetInt64(dm)
	bigNm.Mul(bigNm, f)
	quotient := new(big.Int).Div(bigNm, bigDm)

	result := XRPLNumber{
		mantissa: quotient.Int64() * np * dp,
		exponent: ne - de - 17,
	}
	result.normalize(mode)
	return result
}

// ToIOUAmountValue converts an XRPLNumber back to IOUAmountValue,
// clamping the wider exponent range to IOUAmount's [-96, 80].
func (n XRPLNumber) ToIOUAmountValue() IOUAmountValue {
	if n.IsZero() {
		return ZeroIOUValue()
	}
	if n.exponent > MaxExponent {
		panic("XRPLNumber→IOUAmountValue overflow")
	}
	if n.exponent < MinExponent {
		return ZeroIOUValue()
	}
	return IOUAmountValue{mantissa: n.mantissa, exponent: n.exponent}
}

// ToInt64WithMode converts this Number to an int64 using Guard-based rounding
// under mode, matching rippled's Number::operator rep() (Number.cpp lines
// 480-512).
func (n XRPLNumber) ToInt64WithMode(mode RoundingMode) int64 {
	drops := n.mantissa
	offset := n.exponent
	var g xrplGuard

	if drops != 0 {
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
			drops *= 10
			offset--
		}

		// Apply rounding with the specified mode
		r := g.round(mode)

		if r == 1 || (r == 0 && (drops&1) == 1) {
			drops++
		}
		if g.sbit {
			drops = -drops
		}
	}
	return drops
}

// root2 computes the square root of n using banker's rounding.
func (n XRPLNumber) root2() XRPLNumber {
	return n.root2Rounded(RoundToNearest)
}

// root2Rounded computes the square root of n using Newton-Raphson iteration,
// rounding every intermediate operation under mode.
// Reference: root2() in Number.cpp lines 700-736
func (n XRPLNumber) root2Rounded(mode RoundingMode) XRPLNumber {
	one := NewXRPLNumber(xrplNumMinMantissa, -15) // Number{1}
	if n.Equal(one) {
		return n
	}
	if n.mantissa < 0 {
		panic("XRPLNumber::root2 nan")
	}
	if n.IsZero() {
		return n
	}

	// Scale f into range (0, 1) such that f's exponent is even
	f := n
	e := f.exponent + 16
	if e%2 != 0 {
		e++
	}
	f = XRPLNumber{mantissa: f.mantissa, exponent: f.exponent - e}
	f.normalize(mode)

	// Quadratic least squares curve fit: r = ((a2*f + a1)*f + a0) / D
	// where D=105, a0=18, a1=144, a2=-60
	a0 := NewXRPLNumberFromInt(18)
	a1 := NewXRPLNumberFromInt(144)
	a2 := NewXRPLNumberFromInt(-60)
	D := NewXRPLNumberFromInt(105)
	r := a2.MulRounded(f, mode).AddRounded(a1, mode).MulRounded(f, mode).AddRounded(a0, mode).DivRounded(D, mode)

	// Newton-Raphson iteration: r = (r + f/r) / 2
	two := NewXRPLNumberFromInt(2)
	var rm1, rm2 XRPLNumber
	for {
		rm2 = rm1
		rm1 = r
		r = r.AddRounded(f.DivRounded(r, mode), mode).DivRounded(two, mode)
		if r.Equal(rm1) || r.Equal(rm2) {
			break
		}
	}

	// Return r * 10^(e/2) to reverse scaling
	return XRPLNumber{mantissa: r.mantissa, exponent: r.exponent + e/2}
}
