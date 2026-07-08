package offer

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// maxNativeDrops is rippled STAmount::cMaxNativeN — the isLegalNet ceiling on a
// native (XRP) amount's magnitude.
const maxNativeDrops uint64 = 100_000_000_000_000_000

// isLegalNetAmount mirrors rippled STAmount isLegalNet: a native amount's
// magnitude may not exceed cMaxNativeN; non-native amounts are always legal.
// Zero amounts are legal here and are rejected later by the temBAD_OFFER check.
func isLegalNetAmount(amt tx.Amount) bool {
	if !amt.IsNative() {
		return true
	}
	d := amt.Drops()
	if d < 0 {
		d = -d
	}
	return uint64(d) <= maxNativeDrops
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
