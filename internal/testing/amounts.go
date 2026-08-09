// Package jtx provides test infrastructure for XRPL transaction testing.
package jtx

import (
	"fmt"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// DropsPerXRP is the number of drops in one XRP.
const DropsPerXRP int64 = 1_000_000

// XRP converts an XRP amount to drops.
// For example, XRP(100) returns 100,000,000 drops.
func XRP(n int64) int64 {
	if n > math.MaxInt64/DropsPerXRP || n < math.MinInt64/DropsPerXRP {
		panic(fmt.Sprintf("XRP amount overflows drops: %d", n))
	}
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
	if math.IsNaN(xrp) || math.IsInf(xrp, 0) {
		panic(fmt.Sprintf("XRP amount must be finite: %v", xrp))
	}
	scaled := xrp * float64(DropsPerXRP)
	if scaled >= float64(math.MaxInt64) || scaled < float64(math.MinInt64) {
		panic(fmt.Sprintf("XRP amount overflows drops: %v", xrp))
	}
	rounded := math.Round(scaled)
	if rounded != scaled {
		panic(fmt.Sprintf("XRP amount is not an exact number of drops: %v", xrp))
	}
	drops := int64(rounded)
	if float64(drops)/float64(DropsPerXRP) != xrp {
		panic(fmt.Sprintf("XRP amount cannot be represented exactly in drops: %v", xrp))
	}
	return tx.NewXRPAmount(drops)
}

// floatToMantissaExponent converts a float64 to mantissa and exponent.
// Returns (mantissa, exponent) where value = mantissa * 10^exponent.
//
// Matches rippled's jtx behavior: amounts are formatted via std::to_string(double)
// which uses %.6f (6 decimal places), then parsed via amountFromString.
// This means only 6 decimal digits of precision are preserved, matching C++ test behavior.
func floatToMantissaExponent(value float64) (int64, int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, 0, fmt.Errorf("issued amount must be finite: %v", value)
	}

	formatted := strconv.FormatFloat(value, 'f', 6, 64)
	number, err := state.ParseXRPLNumber(formatted, state.MantissaScaleSmall, state.RoundToNearest)
	if err != nil {
		return 0, 0, fmt.Errorf("parse issued amount %q: %w", formatted, err)
	}
	if number.IsZero() {
		return 0, -100, nil
	}
	exponent := number.Exponent()
	if exponent > state.MaxExponent {
		return 0, 0, fmt.Errorf("issued amount %q exceeds maximum exponent %d", formatted, state.MaxExponent)
	}
	if exponent < state.MinExponent {
		return 0, -100, nil
	}
	return number.Mantissa(), exponent, nil
}

func issuedCurrency(gw *Account, currency string, amount float64) tx.Amount {
	mantissa, exponent, err := floatToMantissaExponent(amount)
	if err != nil {
		panic(err)
	}
	return tx.NewIssuedAmount(mantissa, exponent, currency, gw.Address)
}

// USD creates a USD issued currency amount with the specified gateway.
// The amount is specified as a float (e.g., 100.50 for $100.50).
func USD(gw *Account, amount float64) tx.Amount {
	return issuedCurrency(gw, "USD", amount)
}

// EUR creates a EUR issued currency amount with the specified gateway.
func EUR(gw *Account, amount float64) tx.Amount {
	return issuedCurrency(gw, "EUR", amount)
}

// BTC creates a BTC issued currency amount with the specified gateway.
func BTC(gw *Account, amount float64) tx.Amount {
	return issuedCurrency(gw, "BTC", amount)
}

// IssuedCurrency creates an issued currency amount with custom currency code.
// The currency code must be 3 characters (e.g., "JPY", "GBP", "CNY").
func IssuedCurrency(gw *Account, currency string, amount float64) tx.Amount {
	return issuedCurrency(gw, currency, amount)
}

// IssuedCurrencyFromMantissa creates an issued currency amount from mantissa/exponent.
// Use this when you need precise control over the amount representation.
func IssuedCurrencyFromMantissa(gw *Account, currency string, mantissa int64, exponent int) tx.Amount {
	return tx.NewIssuedAmount(mantissa, exponent, currency, gw.Address)
}
