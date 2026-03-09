// Package testing provides test infrastructure for XRPL transaction testing.
package testing

import (
	"math"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// DropsPerXRP is the number of drops in one XRP.
const DropsPerXRP int64 = 1_000_000

// XRP converts an XRP amount to drops.
// For example, XRP(100) returns 100,000,000 drops.
func XRP(n int64) int64 {
	return n * DropsPerXRP
}

// Drops returns the drop amount unchanged.
// This is a convenience function for clarity when specifying amounts in drops.
func Drops(n int64) int64 {
	return n
}

// XRPTxAmount creates an XRP tx.Amount from drops.
// This returns a tx.Amount suitable for use in transactions.
func XRPTxAmount(drops int64) tx.Amount {
	return tx.NewXRPAmount(drops)
}

// XRPTxAmountFromXRP creates an XRP tx.Amount from whole XRP units.
// For example, XRPTxAmountFromXRP(100) creates an amount of 100 XRP.
func XRPTxAmountFromXRP(xrp float64) tx.Amount {
	drops := int64(xrp * float64(DropsPerXRP))
	return tx.NewXRPAmount(drops)
}

// floatToMantissaExponent converts a float64 to mantissa and exponent.
// Returns (mantissa, exponent) where value = mantissa * 10^exponent.
//
// Matches rippled's jtx behavior: amounts are formatted via std::to_string(double)
// which uses %.6f (6 decimal places), then parsed via amountFromString.
// This means only 6 decimal digits of precision are preserved, matching C++ test behavior.
func floatToMantissaExponent(value float64) (int64, int) {
	if value == 0 {
		return 0, -100 // XRPL zero exponent
	}

	negative := value < 0
	if negative {
		value = -value
	}

	// Round to 6 decimal places, matching rippled's std::to_string(double) = sprintf("%.6f", v)
	value = math.Round(value*1e6) / 1e6

	// Find the exponent
	exponent := 0
	if value >= 1 {
		for value >= 10 {
			value /= 10
			exponent++
		}
	} else {
		for value < 1 {
			value *= 10
			exponent--
		}
	}

	// Scale to get ~15 significant digits in mantissa
	// Mantissa should be in range [10^15, 10^16)
	targetMantissa := value * math.Pow10(15)
	mantissa := int64(math.Round(targetMantissa))
	exponent = exponent - 15

	if negative {
		mantissa = -mantissa
	}

	return mantissa, exponent
}

// USD creates a USD issued currency amount with the specified gateway.
// The amount is specified as a float (e.g., 100.50 for $100.50).
func USD(gw *Account, amount float64) tx.Amount {
	mantissa, exponent := floatToMantissaExponent(amount)
	return tx.NewIssuedAmount(mantissa, exponent, "USD", gw.Address)
}

// EUR creates a EUR issued currency amount with the specified gateway.
func EUR(gw *Account, amount float64) tx.Amount {
	mantissa, exponent := floatToMantissaExponent(amount)
	return tx.NewIssuedAmount(mantissa, exponent, "EUR", gw.Address)
}

// BTC creates a BTC issued currency amount with the specified gateway.
func BTC(gw *Account, amount float64) tx.Amount {
	mantissa, exponent := floatToMantissaExponent(amount)
	return tx.NewIssuedAmount(mantissa, exponent, "BTC", gw.Address)
}

// IssuedCurrency creates an issued currency amount with custom currency code.
// The currency code must be 3 characters (e.g., "JPY", "GBP", "CNY").
func IssuedCurrency(gw *Account, currency string, amount float64) tx.Amount {
	mantissa, exponent := floatToMantissaExponent(amount)
	return tx.NewIssuedAmount(mantissa, exponent, currency, gw.Address)
}

// IssuedCurrencyFromMantissa creates an issued currency amount from mantissa/exponent.
// Use this when you need precise control over the amount representation.
func IssuedCurrencyFromMantissa(gw *Account, currency string, mantissa int64, exponent int) tx.Amount {
	return tx.NewIssuedAmount(mantissa, exponent, currency, gw.Address)
}
