package payment

import (
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// EitherAmount holds either an XRP amount (in drops) or an IOU amount
// This allows unified handling in the flow algorithm regardless of currency type
type EitherAmount struct {
	// IsNative is true if this is an XRP amount, false for IOU
	IsNative bool

	// XRP holds the amount in drops (only valid if IsNative is true)
	XRP int64

	// IOU holds the IOU amount (only valid if IsNative is false)
	IOU tx.Amount
}

// NewXRPEitherAmount creates an EitherAmount for XRP
func NewXRPEitherAmount(drops int64) EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      drops,
	}
}

// NewIOUEitherAmount creates an EitherAmount for IOU
func NewIOUEitherAmount(amount tx.Amount) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      amount,
	}
}

// ZeroXRPEitherAmount creates a zero XRP EitherAmount
func ZeroXRPEitherAmount() EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      0,
	}
}

// ZeroIOUEitherAmount creates a zero IOU EitherAmount with the given currency/issuer
func ZeroIOUEitherAmount(currency, issuer string) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      tx.NewIssuedAmount(0, -100, currency, issuer),
	}
}

// IsZero returns true if the amount is zero
func (e EitherAmount) IsZero() bool {
	if e.IsNative {
		return e.XRP == 0
	}
	return e.IOU.IsZero()
}

// IsNegative returns true if the amount is negative
func (e EitherAmount) IsNegative() bool {
	if e.IsNative {
		return e.XRP < 0
	}
	return e.IOU.IsNegative()
}

// Add adds two EitherAmounts (must be same type - both XRP or both IOU)
func (e EitherAmount) Add(other EitherAmount) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP + other.XRP)
	}
	result, _ := e.IOU.Add(other.IOU)
	return NewIOUEitherAmount(result)
}

// Sub subtracts other from e (must be same type)
func (e EitherAmount) Sub(other EitherAmount) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP - other.XRP)
	}
	result, _ := e.IOU.Sub(other.IOU)
	return NewIOUEitherAmount(result)
}

// Compare compares two EitherAmounts
// Returns -1 if e < other, 0 if equal, 1 if e > other
func (e EitherAmount) Compare(other EitherAmount) int {
	if e.IsNative {
		if e.XRP < other.XRP {
			return -1
		}
		if e.XRP > other.XRP {
			return 1
		}
		return 0
	}
	return e.IOU.Compare(other.IOU)
}

// Min returns the minimum of two EitherAmounts
func (e EitherAmount) Min(other EitherAmount) EitherAmount {
	if e.Compare(other) <= 0 {
		return e
	}
	return other
}

// Max returns the maximum of two EitherAmounts
func (e EitherAmount) Max(other EitherAmount) EitherAmount {
	if e.Compare(other) >= 0 {
		return e
	}
	return other
}

// ToEitherAmount converts a tx.Amount to EitherAmount
func ToEitherAmount(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return NewXRPEitherAmount(amt.Drops())
	}
	return NewIOUEitherAmount(amt)
}

// FromEitherAmount converts EitherAmount back to tx.Amount
func FromEitherAmount(e EitherAmount) tx.Amount {
	if e.IsNative {
		return tx.NewXRPAmount(e.XRP)
	}
	return e.IOU
}

// MulRatio multiplies an amount by a ratio (num/den)
func MulRatio(amt EitherAmount, num, den uint32, roundUp bool) EitherAmount {
	if den == 0 {
		return amt
	}

	if amt.IsNative {
		xrpAmt := tx.NewXRPAmount(amt.XRP)
		result := xrpAmt.MulRatio(num, den, roundUp)
		return NewXRPEitherAmount(result.Drops())
	}

	return NewIOUEitherAmount(amt.IOU.MulRatio(num, den, roundUp))
}

// CanonicalizeDrops converts an IOU-style mantissa/exponent to XRP drops,
// matching rippled's canonicalizeRound (non-strict) for native amounts.
// Uses loop count (not actual remainder) to decide rounding: adds 10 when
// only 1 division loop occurred, 9 when 2+ loops.
// Reference: rippled STAmount.cpp canonicalizeRound lines 1432-1464
func CanonicalizeDrops(mantissa int64, exponent int) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}

	// Scale up if exponent > 0
	for exponent > 0 {
		value *= 10
		exponent--
	}

	// Scale down if exponent < 0
	if exponent < 0 {
		loops := 0
		for exponent < -1 {
			value /= 10
			exponent++
			loops++
		}
		// Non-strict: add 10 when loops < 2, add 9 when loops >= 2
		// Reference: rippled "value += (loops >= 2) ? 9 : 10;"
		var adder int64 = 10
		if loops >= 2 {
			adder = 9
		}
		value = (value + adder) / 10
	}

	if mantissa < 0 {
		return -value
	}
	return value
}

// canonicalizeDropsFloor converts an IOU-style mantissa/exponent to XRP drops
// using plain floor (truncation toward zero).
// This matches rippled's STAmount::canonicalize() for native amounts when
// canonicalizeRoundStrict is NOT called (i.e., when roundUp=false for positive values).
// Reference: rippled STAmount.cpp canonicalize() lines 914-918:
//
//	while (mOffset < 0) { mValue /= 10; ++mOffset; }
func canonicalizeDropsFloor(mantissa int64, exponent int) int64 {
	if mantissa == 0 || exponent <= -20 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}
	for exponent > 0 {
		value *= 10
		exponent--
	}
	for exponent < 0 {
		value /= 10
		exponent++
	}
	if mantissa < 0 {
		return -value
	}
	return value
}

// canonicalizeDropsRound converts an IOU-style mantissa/exponent to XRP drops
// using round-to-nearest with ties going to even (banker's rounding).
// This matches rippled's Number::operator rep() which is used by
// XRPAmount{Number} constructor (e.g., in limitOut for XRP output).
// Reference: rippled Number.cpp operator rep() lines 480-512
func canonicalizeDropsRound(mantissa int64, exponent int) int64 {
	if mantissa == 0 || exponent <= -20 {
		return 0
	}
	value := mantissa
	negative := false
	if value < 0 {
		negative = true
		value = -value
	}
	for exponent > 0 {
		value *= 10
		exponent--
	}
	// Track remainder digits for rounding
	var lastDigit int64
	var hasRemainder bool
	for exponent < 0 {
		d := value % 10
		if exponent == -1 {
			// This is the digit we'll round on
			lastDigit = d
		} else if d != 0 {
			hasRemainder = true
		}
		value /= 10
		exponent++
	}
	// Round to nearest, even on tie
	// lastDigit > 5: round up
	// lastDigit == 5 && hasRemainder: round up (more than 0.5)
	// lastDigit == 5 && !hasRemainder: round to even (banker's rounding)
	// lastDigit < 5: round down (already done by truncation)
	if lastDigit > 5 || (lastDigit == 5 && hasRemainder) || (lastDigit == 5 && !hasRemainder && (value%2) == 1) {
		value++
	}
	if negative {
		return -value
	}
	return value
}

// CanonicalizeDropsStrict converts an IOU-style mantissa/exponent to XRP drops,
// matching rippled's canonicalizeRoundStrict for native amounts.
// Reference: rippled STAmount.cpp canonicalizeRoundStrict lines 1471-1497
func CanonicalizeDropsStrict(mantissa int64, exponent int, roundUp bool) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}

	// Scale up if exponent > 0
	for exponent > 0 {
		value *= 10
		exponent--
	}

	// Scale down if exponent < 0
	// Track whether any bits were lost during intermediate divisions
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
		// Final division with proper rounding
		// When roundUp=true and there was a remainder, add 10 to force round-up
		// Otherwise add 9 (rounds to nearest, up on 5)
		var adder int64 = 9
		if hadRemainder && roundUp {
			adder = 10
		}
		value = (value + adder) / 10
	}

	if mantissa < 0 {
		return -value
	}
	return value
}
