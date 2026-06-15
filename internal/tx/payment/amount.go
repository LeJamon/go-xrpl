package payment

import (
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// EitherAmount holds either an XRP amount (in drops) or an IOU amount, allowing
// unified handling in the flow algorithm regardless of currency type.
type EitherAmount struct {
	IsNative bool

	// XRP holds the amount in drops (only valid if IsNative is true)
	XRP int64

	// IOU holds the IOU amount (only valid if IsNative is false)
	IOU tx.Amount
}

func NewXRPEitherAmount(drops int64) EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      drops,
	}
}

func NewIOUEitherAmount(amount tx.Amount) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      amount,
	}
}

func ZeroXRPEitherAmount() EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      0,
	}
}

func ZeroIOUEitherAmount(currency, issuer string) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      tx.NewIssuedAmount(0, -100, currency, issuer),
	}
}

func (e EitherAmount) IsZero() bool {
	if e.IsNative {
		return e.XRP == 0
	}
	return e.IOU.IsZero()
}

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

func (e EitherAmount) Min(other EitherAmount) EitherAmount {
	if e.Compare(other) <= 0 {
		return e
	}
	return other
}

func (e EitherAmount) Max(other EitherAmount) EitherAmount {
	if e.Compare(other) >= 0 {
		return e
	}
	return other
}

func ToEitherAmount(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return NewXRPEitherAmount(amt.Drops())
	}
	return NewIOUEitherAmount(amt)
}

func FromEitherAmount(e EitherAmount) tx.Amount {
	if e.IsNative {
		return tx.NewXRPAmount(e.XRP)
	}
	return e.IOU
}

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
