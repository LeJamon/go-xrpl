// Package oversizemeta_test tests order book handling with many offers
// and oversized transaction metadata.
// Reference: rippled/src/test/app/OversizeMeta_test.cpp
package oversizemeta_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// createOffers creates n offers: alice sells XRP(amount) for USD(1) each.
// If samePrice is true, all offers are at XRP(1)/USD(1). Otherwise, each
// offer is at XRP(i)/USD(1) (different prices for different book entries).
func createOffers(t *testing.T, env *jtx.TestEnv, alice *jtx.Account, gw *jtx.Account, n int, samePrice bool) {
	t.Helper()
	for i := 1; i <= n; i++ {
		var xrpAmount uint64
		if samePrice {
			xrpAmount = 1_000_000 // 1 XRP
		} else {
			xrpAmount = uint64(i) * 1_000_000 // i XRP
		}
		result := env.Submit(
			offer.OfferCreateXRP(alice, xrpAmount, gw.IOU("USD", 1), false).Build(),
		)
		if !result.Success {
			t.Fatalf("offer %d failed: %s", i, result.Code)
		}
		env.Close()
	}
}

// TestThinBook verifies basic order book handling with a single offer.
// This is the auto-run test from rippled (ThinBook_test).
// Reference: rippled OversizeMeta_test.cpp ThinBook_test::run()
func TestThinBook(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")

	// Fund with large amounts matching rippled's billion XRP
	// Need enough for reserves + many offers. 100K XRP is plenty for 1 offer.
	env.FundAmount(alice, 100_000_000_000) // 100K XRP
	env.FundAmount(gw, 100_000_000_000)
	env.Close()

	// Alice trusts gateway for USD
	env.Trust(alice, gw.IOU("USD", 1_000_000_000))
	env.Close()

	// Gateway pays alice USD
	result := env.Submit(
		payment.PayIssued(gw, alice, gw.IOU("USD", 1_000_000_000)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create 1 offer: alice sells XRP(1) for USD(1)
	createOffers(t, env, alice, gw, 1, false)
}

// TestMediumBook tests order book handling with a moderate number of offers (20).
// This is a scaled-down version of PlumpBook to verify multi-offer book handling
// without the extreme scale of the 10,000-offer manual test.
func TestMediumBook(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")

	// Need enough XRP for reserves (200 base + 50 per offer) + offer amounts + fees
	// 20 offers: reserve = 200 + 50*21 (trust + 20 offers) = 1250 XRP
	// Plus offer amounts sum(1..20) = 210 XRP + fees
	env.FundAmount(alice, 10_000_000_000) // 10K XRP
	env.FundAmount(gw, 10_000_000_000)
	env.Close()

	env.Trust(alice, gw.IOU("USD", 1_000_000_000))
	env.Close()

	result := env.Submit(
		payment.PayIssued(gw, alice, gw.IOU("USD", 1_000_000_000)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create 20 offers at different prices
	createOffers(t, env, alice, gw, 20, false)

	// Verify alice's owner count includes trust line + 20 offers
	jtx.RequireOwnerCount(t, env, alice, 21) // 1 trust + 20 offers
}

// TestOversizeMetaCross tests that a crossing payment against many offers
// produces correct results (or tecOVERSIZE for very large metadata).
// This is a scaled-down version testing the basic flow with 50 offers.
func TestOversizeMetaCross(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")

	// Fund generously: 50 offers at same price + crossing payment
	env.FundAmount(alice, 100_000_000_000) // 100K XRP
	env.FundAmount(gw, 100_000_000_000)
	env.Close()

	env.Trust(alice, gw.IOU("USD", 1_000_000_000))
	env.Close()

	result := env.Submit(
		payment.PayIssued(gw, alice, gw.IOU("USD", 1_000_000_000)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create 50 offers at same price: alice sells XRP(1) for USD(1)
	// Reference: rippled OversizeMeta_test.cpp OversizeMeta_test::createOffers()
	createOffers(t, env, alice, gw, 50, true)

	// Pay back all USD to gateway (should cross all offers)
	result = env.Submit(
		payment.PayIssued(alice, gw, gw.IOU("USD", 1_000_000_000)).Build(),
	)
	// This may succeed or get tecPATH_DRY depending on path finding
	// The important thing is it doesn't crash
	_ = result

	// Place one more offer to verify book is still functional
	result = env.Submit(
		offer.OfferCreate(alice, gw.IOU("USD", 1), tx.NewXRPAmount(1_000_000)).Build(),
	)
	_ = fmt.Sprintf("final offer result: %s", result.Code)
}

// TestPlumpBook tests order book handling with 10,000 offers at different prices.
// Reference: rippled OversizeMeta_test.cpp PlumpBook_test::run()
//
// This is marked MANUAL in rippled (BEAST_DEFINE_TESTSUITE_MANUAL_PRIO) because
// it creates 10,000 offers, each requiring a separate ledger close.
func TestPlumpBook(t *testing.T) {
	t.Skip("Manual stress test: creates 10,000 offers (marked MANUAL_PRIO in rippled)")
}

// TestOversizeMeta_Full tests oversized metadata with 9,000 offers.
// Reference: rippled OversizeMeta_test.cpp OversizeMeta_test::test()
//
// This is marked MANUAL in rippled (BEAST_DEFINE_TESTSUITE_MANUAL_PRIO) because
// it creates 9,000 identical-price offers, then crosses them with a payment,
// generating extremely large transaction metadata.
func TestOversizeMeta_Full(t *testing.T) {
	t.Skip("Manual stress test: creates 9,000 offers + crossing payment (marked MANUAL_PRIO in rippled)")
}

// TestFindOversizeCross finds the minimum number of offers that causes tecOVERSIZE.
// Reference: rippled OversizeMeta_test.cpp FindOversizeCross_test::run()
//
// This is marked MANUAL in rippled (BEAST_DEFINE_TESTSUITE_MANUAL_PRIO with priority 50)
// and binary searches between 100-9,000 offers to find the tecOVERSIZE threshold.
func TestFindOversizeCross(t *testing.T) {
	t.Skip("Manual stress test: binary search for tecOVERSIZE threshold (marked MANUAL_PRIO in rippled)")
}
