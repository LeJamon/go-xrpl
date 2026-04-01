package state

import (
	"math/big"
)

// Sqrt returns the square root of this Amount.
// Reference: rippled's root2() function in Number.cpp
// Uses Newton-Raphson iteration for precision.
// Panics if the amount is negative.
// When fixUniversalNumber is enabled, delegates to XRPLNumber.root2().
func (a Amount) Sqrt() Amount {
	// Handle special cases
	if a.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, zeroExponent, a.Currency, a.Issuer)
	}
	if a.IsNegative() {
		panic("cannot take square root of negative amount")
	}

	// For XRP (native), compute sqrt of drops
	if a.IsNative() {
		drops := a.Drops()
		// Use integer square root for XRP
		result := intSqrt(uint64(drops))
		return NewXRPAmountFromInt(int64(result))
	}

	// When switchover is on, delegate to XRPLNumber.root2()
	if GetNumberSwitchover() {
		n := NewXRPLNumber(a.iou.Mantissa(), a.iou.Exponent())
		result := n.root2()
		iou := result.ToIOUAmountValue()
		return NewIssuedAmountFromValue(iou.mantissa, iou.exponent, a.Currency, a.Issuer)
	}

	// For IOU amounts, use Newton-Raphson iteration
	// Scale f into a range where exponent is even
	mantissa := a.iou.Mantissa()
	exponent := a.iou.Exponent()

	// Adjust exponent to be even (required for sqrt)
	e := exponent + 16 // shift to positive range
	if e%2 != 0 {
		e++
	}
	// Create scaled value: f = mantissa * 10^(exponent - e)
	scaledExp := exponent - e

	// Convert to a working Number-like representation
	// We need to do Newton-Raphson: r = (r + f/r) / 2
	// Start with initial guess using quadratic approximation
	// Coefficients from rippled: a0=18, a1=144, a2=-60, D=105
	// r = ((a2*f + a1)*f + a0) / D

	// For simplicity, use a good initial guess based on the mantissa
	// sqrt(m * 10^e) = sqrt(m) * 10^(e/2)
	// Initial guess: sqrt of mantissa scaled appropriately
	fVal := IOUAmountValue{mantissa: mantissa, exponent: scaledExp}

	// Initial guess using the quadratic fit from rippled
	// r = ((a2*f + a1)*f + a0) / D where a0=18, a1=144, a2=-60, D=105
	// Simplified: start with a reasonable approximation
	rVal := initialSqrtGuess(fVal)

	// Newton-Raphson iteration: r = (r + f/r) / 2
	// Continue until convergence (r stops changing)
	var rm1, rm2 IOUAmountValue
	for i := 0; i < 100; i++ { // max iterations for safety
		rm2 = rm1
		rm1 = rVal

		// r = (r + f/r) / 2
		fDivR := divIOUValues(fVal, rVal)
		rPlusFDivR := addIOUValues(rVal, fDivR)
		rVal = divIOUValuesByInt(rPlusFDivR, 2)

		// Check for convergence
		if rVal.mantissa == rm1.mantissa && rVal.exponent == rm1.exponent {
			break
		}
		if rVal.mantissa == rm2.mantissa && rVal.exponent == rm2.exponent {
			break
		}
	}

	// Reverse the scaling: multiply by 10^(e/2)
	resultExp := rVal.exponent + e/2
	result := NewIOUAmountValue(rVal.mantissa, resultExp)

	return NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(), a.Currency, a.Issuer)
}

// intSqrt computes integer square root using Newton's method
func intSqrt(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}

// initialSqrtGuess returns an initial guess for Newton-Raphson sqrt iteration
func initialSqrtGuess(f IOUAmountValue) IOUAmountValue {
	if f.mantissa == 0 {
		return ZeroIOUValue()
	}
	// Use the quadratic fit from rippled: r = ((a2*f + a1)*f + a0) / D
	// where a0=18, a1=144, a2=-60, D=105
	// This gives a good initial approximation in the range [0, 1]

	// Simplified approach: sqrt(m * 10^e) ≈ sqrt(m) * 10^(e/2)
	// For normalized mantissa in [10^15, 10^16), sqrt is in [~3.16*10^7, 10^8)
	mantissa := f.mantissa
	if mantissa < 0 {
		mantissa = -mantissa
	}

	// Approximate sqrt of mantissa
	sqrtMant := intSqrt(uint64(mantissa))

	// Adjust exponent: if original exp is e, result exp is e/2
	// But mantissa sqrt reduces magnitude by half in log scale
	// sqrt(m * 10^e) = sqrt(m) * 10^(e/2)
	// If m is ~10^15, sqrt(m) is ~3*10^7, so we need to normalize
	resultExp := f.exponent / 2

	// Normalize the result
	return NewIOUAmountValue(int64(sqrtMant), resultExp)
}

// divIOUValues divides two IOU values
func divIOUValues(a, b IOUAmountValue) IOUAmountValue {
	if b.mantissa == 0 {
		return ZeroIOUValue()
	}
	if a.mantissa == 0 {
		return ZeroIOUValue()
	}

	// a / b = (a.mantissa / b.mantissa) * 10^(a.exponent - b.exponent)
	// Scale up a.mantissa to preserve precision
	aMant := a.mantissa
	bMant := b.mantissa

	negative := (aMant < 0) != (bMant < 0)
	if aMant < 0 {
		aMant = -aMant
	}
	if bMant < 0 {
		bMant = -bMant
	}

	// Scale aMant by 10^16 for precision
	// Use big.Int to avoid overflow
	bigA := new(big.Int).SetInt64(aMant)
	bigB := new(big.Int).SetInt64(bMant)
	scale := new(big.Int).SetInt64(1e16)
	bigA.Mul(bigA, scale)

	bigResult := new(big.Int).Div(bigA, bigB)
	resultMant := bigResult.Int64()
	resultExp := a.exponent - b.exponent - 16

	if negative {
		resultMant = -resultMant
	}

	return NewIOUAmountValue(resultMant, resultExp)
}

// divIOUValuesByInt divides an IOU value by an integer
func divIOUValuesByInt(a IOUAmountValue, d int64) IOUAmountValue {
	if d == 0 || a.mantissa == 0 {
		return ZeroIOUValue()
	}

	mantissa := a.mantissa
	negative := (mantissa < 0) != (d < 0)
	if mantissa < 0 {
		mantissa = -mantissa
	}
	if d < 0 {
		d = -d
	}

	// Simple integer division for small divisors
	// For divisors like 2, we can just divide directly
	resultMant := mantissa / d
	remainder := mantissa % d
	resultExp := a.exponent

	// Handle case where result is too small (below normalized range)
	// by scaling up and adjusting exponent
	if resultMant < MinMantissa && resultMant > 0 {
		// Multiply mantissa by 10 and check remainder contribution
		for resultMant < MinMantissa && resultExp > MinExponent {
			resultMant = resultMant*10 + (remainder*10)/d
			remainder = (remainder * 10) % d
			resultExp--
		}
	}

	if negative {
		resultMant = -resultMant
	}

	return NewIOUAmountValue(resultMant, resultExp)
}
