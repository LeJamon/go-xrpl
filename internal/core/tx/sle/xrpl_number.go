package sle

// XRPLNumber implements rippled's Number class with Guard-based precision.
// Reference: rippled/src/libxrpl/basics/Number.cpp
//
// The Number class uses wider exponent range [-32768, 32768] than IOUAmount [-96, 80]
// and employs a Guard mechanism that preserves digits discarded during scale-down,
// enabling banker's rounding (round-half-to-even) for correct precision.
//
// When fixUniversalNumber is enabled, IOUAmount arithmetic delegates to this type.

import (
	"math/big"
)

// XRPLNumber constants matching rippled's Number.h
const (
	xrplNumMinMantissa int64 = 1_000_000_000_000_000  // 10^15
	xrplNumMaxMantissa int64 = 9_999_999_999_999_999  // 10^16 - 1
	xrplNumMinExponent       = -32768
	xrplNumMaxExponent       = 32768
	// Zero exponent for Number (different from IOUAmount's zeroExponent)
	xrplNumZeroExponent = -2147483648 // math.MinInt32, matching Number{} default
)

// Package-level switchover flag.
// Matches rippled's thread-local getSTNumberSwitchover() / setSTNumberSwitchover().
// Safe as package-level var because Go transaction processing is single-threaded.
var numberSwitchoverEnabled bool

// SetNumberSwitchover enables or disables the XRPLNumber switchover.
// When enabled, IOUAmount arithmetic uses Guard-based precision.
func SetNumberSwitchover(enabled bool) {
	numberSwitchoverEnabled = enabled
}

// GetNumberSwitchover returns whether the XRPLNumber switchover is enabled.
func GetNumberSwitchover() bool {
	return numberSwitchoverEnabled
}

// RoundingMode controls how XRPLNumber rounds during normalization.
// Reference: Number::rounding_mode in Number.h line 196
type RoundingMode int

const (
	RoundToNearest   RoundingMode = iota // banker's rounding (default)
	RoundTowardsZero                     // always truncate towards zero
	RoundDownward                        // round towards negative infinity
	RoundUpward                          // round towards positive infinity
)

// Package-level rounding mode, matching rippled's thread_local Number::mode_.
var numberRoundingMode RoundingMode = RoundToNearest

// GetNumberRound returns the current rounding mode.
func GetNumberRound() RoundingMode {
	return numberRoundingMode
}

// SetNumberRound sets the rounding mode and returns the previous mode.
func SetNumberRound(mode RoundingMode) RoundingMode {
	prev := numberRoundingMode
	numberRoundingMode = mode
	return prev
}

// NumberRoundModeGuard sets the rounding mode and restores it on Release().
// Matches rippled's NumberRoundModeGuard RAII class.
type NumberRoundModeGuard struct {
	saved RoundingMode
}

// NewNumberRoundModeGuard sets the rounding mode and returns a guard.
func NewNumberRoundModeGuard(mode RoundingMode) NumberRoundModeGuard {
	return NumberRoundModeGuard{saved: SetNumberRound(mode)}
}

// Release restores the previous rounding mode.
func (g NumberRoundModeGuard) Release() {
	SetNumberRound(g.saved)
}

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

func (g *xrplGuard) setPositive() { g.sbit = false }
func (g *xrplGuard) setNegative() { g.sbit = true }
func (g *xrplGuard) isNegative() bool { return g.sbit }

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

// round returns the rounding direction based on the current rounding mode.
// Returns: 1 if round up, -1 if round down, 0 if exactly half.
// Reference: Number.cpp lines 137-171
func (g *xrplGuard) round() int {
	mode := GetNumberRound()

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

// NewXRPLNumber creates a new XRPLNumber and normalizes it.
// Reference: Number::Number(rep mantissa, int exponent) in Number.h line 219-223
func NewXRPLNumber(mantissa int64, exponent int) XRPLNumber {
	n := XRPLNumber{mantissa: mantissa, exponent: exponent}
	n.normalize()
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

// normalize adjusts mantissa and exponent to the proper range using Guard-based rounding.
// Reference: Number.cpp lines 177-227
func (n *XRPLNumber) normalize() {
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
	r := g.round()
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

// Add returns the sum of two XRPLNumbers.
// Reference: Number::operator+= in Number.cpp lines 229-345
func (n XRPLNumber) Add(y XRPLNumber) XRPLNumber {
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
		r := g.round()
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
		r := g.round()
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

// Mul returns the product of two XRPLNumbers.
// Reference: Number::operator*= in Number.cpp lines 375-445
func (n XRPLNumber) Mul(y XRPLNumber) XRPLNumber {
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
	r := g.round()
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

// Div returns n / y.
// Reference: Number::operator/= in Number.cpp lines 447-478
func (n XRPLNumber) Div(y XRPLNumber) XRPLNumber {
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
	result.normalize()
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

// root2 computes the square root of n using Newton-Raphson iteration.
// Reference: root2() in Number.cpp lines 700-736
func (n XRPLNumber) root2() XRPLNumber {
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
	f.normalize()

	// Quadratic least squares curve fit: r = ((a2*f + a1)*f + a0) / D
	// where D=105, a0=18, a1=144, a2=-60
	a0 := NewXRPLNumberFromInt(18)
	a1 := NewXRPLNumberFromInt(144)
	a2 := NewXRPLNumberFromInt(-60)
	D := NewXRPLNumberFromInt(105)
	r := a2.Mul(f).Add(a1).Mul(f).Add(a0).Div(D)

	// Newton-Raphson iteration: r = (r + f/r) / 2
	two := NewXRPLNumberFromInt(2)
	var rm1, rm2 XRPLNumber
	for {
		rm2 = rm1
		rm1 = r
		r = r.Add(f.Div(r)).Div(two)
		if r.Equal(rm1) || r.Equal(rm2) {
			break
		}
	}

	// Return r * 10^(e/2) to reverse scaling
	return XRPLNumber{mantissa: r.mantissa, exponent: r.exponent + e/2}
}
