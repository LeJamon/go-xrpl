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

// TestAmount_Add_CurrencyMismatch_NotYetEnforced documents the deliberate
// gap relative to rippled: currency mismatch is currently permitted (Add
// inherits a.Currency for the result). AMM helpers compare amounts with
// disjoint currency tags (e.g. an empty-currency tolerance against an
// LP-token balance) and treat the result as "close enough"; tightening
// this into a hard error regresses those flows until each site is
// audited. Once callers are cleaned up, this test should be flipped to
// assert a temBAD_CURRENCY prefix.
func TestAmount_Add_CurrencyMismatch_NotYetEnforced(t *testing.T) {
	usd := NewIssuedAmountFromValue(1, 0, "USD", "rIssuer")
	eur := NewIssuedAmountFromValue(1, 0, "EUR", "rIssuer")

	sum, err := usd.Add(eur)
	if err != nil {
		t.Fatalf("unexpected error: %v (rippled would assert here; goXRPL Add tolerates currency mismatch pending caller cleanup)", err)
	}
	if sum.Currency != "USD" {
		t.Errorf("expected result tagged with a.Currency=USD, got %q", sum.Currency)
	}
}

// TestAmount_Add_IssuerMismatch_NotYetEnforced documents the deliberate
// gap relative to rippled: issuer mismatch is currently permitted (Add
// inherits a.Issuer for the result) until callers like
// DirectStepI.creditLimit normalize the issuer field to match accountHolds.
// Once those sites are fixed, this test should be flipped to assert a
// temBAD_ISSUER prefix.
func TestAmount_Add_IssuerMismatch_NotYetEnforced(t *testing.T) {
	a := NewIssuedAmountFromValue(1, 0, "USD", "rIssuerA")
	b := NewIssuedAmountFromValue(1, 0, "USD", "rIssuerB")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v (rippled would assert here; goXRPL Add tolerates issuer mismatch pending caller cleanup)", err)
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
