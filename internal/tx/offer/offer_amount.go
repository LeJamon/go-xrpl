package offer

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// isLegalNetAmount checks if an amount is a valid net amount.
// Reference: rippled protocol/STAmount.h isLegalNet()
func isLegalNetAmount(amt tx.Amount) bool {
	// A legal net amount is non-zero
	return !amt.IsZero()
}

// isAmountZeroOrNegative checks if an amount is zero or negative.
func isAmountZeroOrNegative(amt tx.Amount) bool {
	return amt.IsZero() || amt.IsNegative()
}

// isAmountNegative checks if an amount is strictly negative.
func isAmountNegative(amt tx.Amount) bool {
	return amt.IsNegative()
}

// zeroAmount returns a zero amount matching the type/issue of the given amount.
func zeroAmount(amt tx.Amount) tx.Amount {
	if amt.IsNative() {
		return tx.NewXRPAmount(0)
	}
	return tx.NewIssuedAmount(0, -100, amt.Currency, amt.Issuer)
}

// subtractAmounts subtracts b from a.
// a - b = result
func subtractAmounts(a, b tx.Amount) tx.Amount {
	result, err := a.Sub(b)
	if err != nil {
		// Type mismatch - return zero amount of a's type
		if a.IsNative() {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, a.Currency, a.Issuer)
	}

	// Clamp negative results to zero
	if result.IsNegative() {
		if result.IsNative() {
			return tx.NewXRPAmount(0)
		}
		return tx.NewIssuedAmount(0, -100, a.Currency, a.Issuer)
	}

	return result
}
