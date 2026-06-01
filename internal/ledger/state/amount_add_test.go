package state

import (
	"strings"
	"testing"
)

// TestAmount_Add_NativeMismatch asserts that adding XRP + IOU produces a
// temBAD_AMOUNT-prefixed error matching rippled's STAmount::operator+ contract
// (which throws on !areComparable).
func TestAmount_Add_NativeMismatch(t *testing.T) {
	xrp := NewXRPAmountFromInt(100)
	iou := NewIssuedAmountFromValue(1, 0, "USD", "rIssuer")

	_, err := xrp.Add(iou)
	if err == nil {
		t.Fatal("expected error adding XRP + IOU, got nil")
	}
	if !strings.HasPrefix(err.Error(), "temBAD_AMOUNT:") {
		t.Errorf("expected temBAD_AMOUNT prefix, got %q", err.Error())
	}
}

// TestAmount_Add_CurrencyMismatch asserts that adding two IOUs with
// different (non-empty) currencies produces a temBAD_AMOUNT-prefixed
// error, mirroring rippled's areComparable (STAmount.cpp:132-141), which
// requires matching currency for two Issue amounts to be addable.
func TestAmount_Add_CurrencyMismatch(t *testing.T) {
	usd := NewIssuedAmountFromValue(1, 0, "USD", "rIssuer")
	eur := NewIssuedAmountFromValue(1, 0, "EUR", "rIssuer")

	_, err := usd.Add(eur)
	if err == nil {
		t.Fatal("expected error adding USD + EUR, got nil")
	}
	if !strings.HasPrefix(err.Error(), "temBAD_AMOUNT:") {
		t.Errorf("expected temBAD_AMOUNT prefix, got %q", err.Error())
	}
}

// TestAmount_Add_CurrencylessNumber asserts that amounts carrying an empty
// currency — go-xrpl's representation of rippled's unitless Number, used
// throughout the AMM math — add freely regardless of the other operand's
// tag. An empty currency marks the Number namespace, which has no
// areComparable gate.
func TestAmount_Add_CurrencylessNumber(t *testing.T) {
	number := NewIssuedAmountFromValue(2_000_000_000_000_000, -15, "", "")
	lpToken := NewIssuedAmountFromValue(3_000_000_000_000_000, -15, "03ABC", "rAMM")

	// Number + tagged amount.
	sum, err := number.Add(lpToken)
	if err != nil {
		t.Fatalf("unexpected error adding currency-less Number: %v", err)
	}
	if sum.IsZero() {
		t.Error("sum unexpectedly zero")
	}

	// Tagged amount + Number (commuted operands).
	if _, err := lpToken.Add(number); err != nil {
		t.Fatalf("unexpected error adding tagged amount + Number: %v", err)
	}

	// Number + Number.
	if _, err := number.Add(number); err != nil {
		t.Fatalf("unexpected error adding Number + Number: %v", err)
	}
}

// TestAmount_Add_IssuerMismatch_Tolerated documents that an issuer
// mismatch is deliberately tolerated, matching rippled's areComparable
// (STAmount.cpp:132-141), which compares currency but NOT issuer for two
// Issue amounts. The result inherits a.Issuer, mirroring operator+'s
// v1-tagged result (STAmount.cpp:395-401). Same-currency call sites such
// as DirectStepI's creditLimit (which does not normalise the issuer the
// way rippled View.cpp:469-484 does) rely on this.
func TestAmount_Add_IssuerMismatch_Tolerated(t *testing.T) {
	a := NewIssuedAmountFromValue(1, 0, "USD", "rIssuerA")
	b := NewIssuedAmountFromValue(1, 0, "USD", "rIssuerB")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v (issuer mismatch must stay tolerated to match areComparable)", err)
	}
	if sum.Issuer != "rIssuerA" {
		t.Errorf("expected result tagged with a.Issuer=rIssuerA, got %q", sum.Issuer)
	}
}

// TestAmount_Add_MatchingIOU sanity-checks that matched currency+issuer IOUs
// still add successfully after the new guard rails.
func TestAmount_Add_MatchingIOU(t *testing.T) {
	a := NewIssuedAmountFromValue(1_000_000_000_000_000, -15, "USD", "rIssuer")
	b := NewIssuedAmountFromValue(2_000_000_000_000_000, -15, "USD", "rIssuer")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.Currency != "USD" || sum.Issuer != "rIssuer" {
		t.Errorf("sum metadata not preserved: currency=%q issuer=%q", sum.Currency, sum.Issuer)
	}
	if sum.IsZero() {
		t.Error("sum unexpectedly zero")
	}
}
