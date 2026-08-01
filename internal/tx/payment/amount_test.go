package payment

import (
	"testing"

	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestToNumberAmountStripsIOUIssue(t *testing.T) {
	usd := NewIOUEitherAmount(tx.NewIssuedAmount(1_000_000_000_000_000, -15, "USD", "issuer-a"))
	eur := NewIOUEitherAmount(tx.NewIssuedAmount(2_000_000_000_000_000, -15, "EUR", "issuer-b"))

	usdNumber := toNumberAmount(usd)
	eurNumber := toNumberAmount(eur)

	if usdNumber.Currency != "" || usdNumber.Issuer != "" {
		t.Fatalf("number amount retained issue: currency=%q issuer=%q", usdNumber.Currency, usdNumber.Issuer)
	}
	if usdNumber.Compare(eurNumber) >= 0 {
		t.Fatalf("numeric comparison = %d, want negative", usdNumber.Compare(eurNumber))
	}
}
