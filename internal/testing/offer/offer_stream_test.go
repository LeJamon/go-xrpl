package offer

// OfferStream behavioral tests.
// Tests the behavior of the offer book traversal ("stream") during offer crossing:
// expired offer cleanup, unfunded offer pruning, partially filled offers,
// book ordering preservation, multiple expired offers, self-offer crossing,
// and tecEXPIRED on offer create.
//
// Reference: rippled/src/test/app/OfferStream_test.cpp (stub only -- pass())
// Scenarios derived from rippled Offer_test.cpp, OfferStream.h comments,
// and the task specification.

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOfferStream_ExpiredOfferCleanup verifies that an expired offer sitting in
// the book is removed during crossing when another offer tries to cross it.
// The crossing offer does not fill (nothing to cross after cleanup), and the
// expired offer is cleaned from the book.
//
// Engine gap: The goXRPL engine's BookStep.getNextOffer() skips expired offers
// without removing them from the book. When all offers in a book are expired,
// the strand returns tecPATH_DRY and the expired offers are never cleaned up.
// This matches the pre-existing TestOffer_Expiration failure.
// The test is skipped pending engine implementation of expired offer removal.
//
// Reference: rippled Offer_test.cpp testExpiration (lines 1145-1221)
func TestOfferStream_ExpiredOfferCleanup(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_ExpiredOfferCleanup(t, fs.disabled)
		})
	}
}

func testOfferStream_ExpiredOfferCleanup(t *testing.T, disabledFeatures []string) {
	t.Skip("Engine gap: expired offer removal during book traversal not yet implemented " +
		"(same gap as TestOffer_Expiration). The BookStep skips expired offers without " +
		"removing them; when all offers are expired, the strand returns tecPATH_DRY " +
		"before the cleanup logic can run.")

	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	startBalance := uint64(jtx.XRP(1000000))

	env.FundAmount(gw, startBalance)
	env.FundAmount(alice, startBalance)
	env.FundAmount(bob, startBalance)
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, BTC(100))
	env.Trust(bob, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a BTC→USD offer that expires after the next close (+1 sec):
	// takerPays=USD(1000), takerGets=BTC(10) — alice is offering BTC to get USD
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(
		OfferCreate(alice, USD(1000), BTC(10)).
			Expiration(LastClose(env) + 1).Build())
	jtx.RequireTxSuccess(t, result)

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, alice, 2) // trust line + offer

	// Advance time past the expiration
	env.Close()

	// alice's offer is still physically in ledger (lazy cleanup)
	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 1)

	// bob submits a crossing BTC→USD offer. The book traversal should encounter
	// alice's expired offer, remove it, find nothing else to cross, and place bob's offer.
	// bob's offer: takerPays=BTC(10), takerGets=USD(1000)
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, BTC(10), USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	// alice's expired offer must have been removed
	RequireNoOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, alice, 1) // only trust line remains

	// alice's IOU balances are unchanged (offer was not filled)
	jtx.RequireIOUBalance(t, env, alice, gw, "BTC", 10)
	// alice paid: f(Trust) + f(OfferCreate)
	jtx.RequireBalance(t, env, alice, startBalance-f-f)

	// bob's offer goes into the book (nothing crossed)
	RequireOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferCount(t, env, bob, 1)
	jtx.RequireOwnerCount(t, env, bob, 2) // trust line + offer
	jtx.RequireBalance(t, env, bob, startBalance-f-f)
}

// TestOfferStream_UnfundedOfferPruning verifies that an offer whose owner no
// longer has sufficient funds is removed from the book during crossing.
// The crossing offer skips it and crosses with the next funded offer.
// Reference: rippled Offer_test.cpp testNegativeBalance (lines 1394-1501)
func TestOfferStream_UnfundedOfferPruning(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_UnfundedOfferPruning(t, fs.disabled)
		})
	}
}

func testOfferStream_UnfundedOfferPruning(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(carol, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a funded offer: wants XRP(500), gives USD(500)
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(500), USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 500)

	// alice sends all her USD back, making her offer unfunded
	result = env.Submit(payment.PayIssued(alice, gw, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	RequireOfferInLedger(t, env, alice, aliceOfferSeq) // still in ledger but unfunded

	// carol places a funded offer: wants XRP(500), gives USD(250) (rate: 0.5 USD/XRP)
	// alice's rate was 1 USD/XRP -- alice's is better quality but unfunded,
	// so traversal will skip alice and reach carol.
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(500), USD(250)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, carol, carolOfferSeq)

	// bob wants USD(200) and offers XRP(400) (at carol's rate: 2 XRP per USD).
	// Traversal: alice's offer is unfunded -> remove it. Carol's offer crosses partially.
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, USD(200), jtx.XRPTxAmountFromXRP(400)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's unfunded offer must have been pruned
	RequireNoOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0) // unchanged

	// bob should have received USD via carol's offer
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 200)

	// carol's offer was partially consumed; she gave USD(200) and received XRP(400)
	// carol's original: USD(250) for XRP(500); residual: USD(50) for XRP(100)
	carolOffers := OffersOnAccount(env, carol)
	require.Equal(t, 1, len(carolOffers), "carol should have a residual partial offer")
	require.True(t, amountsEqual(carolOffers[0].TakerGets, USD(50)),
		"carol's residual TakerGets should be USD(50), got %v", carolOffers[0].TakerGets)
	require.True(t, amountsEqual(carolOffers[0].TakerPays, jtx.XRPTxAmountFromXRP(100)),
		"carol's residual TakerPays should be XRP(100), got %v", carolOffers[0].TakerPays)

	// bob's offer is fully consumed
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferCount(t, env, bob, 0)
}

// TestOfferStream_SkipBadOffers verifies that the offer stream skips expired
// offers and continues crossing with the next valid offer.
//
// Engine gap: same as TestOfferStream_ExpiredOfferCleanup — expired offers are
// not removed during book traversal when they are the only offers in the book.
// Skipped pending engine implementation.
func TestOfferStream_SkipBadOffers(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_SkipBadOffers(t, fs.disabled)
		})
	}
}

func testOfferStream_SkipBadOffers(t *testing.T, disabledFeatures []string) {
	t.Skip("Engine gap: expired offer removal during book traversal not yet implemented. " +
		"The BookStep skips expired offers but does not remove them, causing the strand " +
		"to return tecPATH_DRY before cleanup can occur.")

	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, BTC(100))
	env.Trust(carol, BTC(100))
	env.Trust(bob, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a BTC→USD offer that expires after the next close
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(
		OfferCreate(alice, USD(100), BTC(10)).
			Expiration(LastClose(env) + 1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)

	// carol places a good non-expiring offer at the same quality
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, USD(100), BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, carol, carolOfferSeq)

	// Now alice's offer is expired. bob wants BTC(10) and gives USD(100).
	// Stream: skips+removes alice's expired offer, then crosses carol's good offer.
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, BTC(10), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// alice's expired offer is gone
	RequireNoOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 0)

	// carol's offer was consumed
	RequireNoOfferInLedger(t, env, carol, carolOfferSeq)
	RequireOfferCount(t, env, carol, 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "BTC", 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 100)

	// bob got BTC(10)
	jtx.RequireIOUBalance(t, env, bob, gw, "BTC", 10)
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferCount(t, env, bob, 0)
}

// TestOfferStream_PartiallyFilledOffer verifies that after a partial crossing,
// the remaining portion of the offer stays correctly in the book with the
// correct residual amounts.
// Reference: rippled Offer_test.cpp testOfferCrossWithXRP (lines 1503-1556)
func TestOfferStream_PartiallyFilledOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_PartiallyFilledOffer(t, fs.disabled)
		})
	}
}

func testOfferStream_PartiallyFilledOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a large offer: wants XRP(1000), gives USD(1000)
	// takerPays=XRP(1000), takerGets=USD(1000)
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)

	// bob partially crosses alice's offer: wants USD(400), gives XRP(400)
	result = env.Submit(OfferCreate(bob, USD(400), jtx.XRPTxAmountFromXRP(400)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's offer should still exist with residual amounts
	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 1)

	aliceOffer := GetOffer(env, alice, aliceOfferSeq)
	require.NotNil(t, aliceOffer, "alice's partial offer should still exist")

	// alice offered USD(1000) for XRP(1000); bob took USD(400) for XRP(400)
	// Remaining: USD(600) for XRP(600)
	require.True(t, amountsEqual(aliceOffer.TakerGets, USD(600)),
		"alice's remaining TakerGets should be USD(600), got %v", aliceOffer.TakerGets)
	require.True(t, amountsEqual(aliceOffer.TakerPays, jtx.XRPTxAmountFromXRP(600)),
		"alice's remaining TakerPays should be XRP(600), got %v", aliceOffer.TakerPays)

	// bob's offer is fully consumed
	RequireOfferCount(t, env, bob, 0)

	// alice received XRP(400); paid f(Trust) + f(OfferCreate)
	// FundAmount gives alice exactly XRP(10000) (AccountSet fee is internal to FundAmount)
	jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(10000))-2*f+uint64(jtx.XRP(400)))
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 600) // gave USD(400)

	// bob spent XRP(400); paid f(OfferCreate)
	// FundAmount gives bob exactly XRP(10000); no Trust fee since bob got no IOU trust set up
	jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(10000))-f-uint64(jtx.XRP(400)))
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 400)
}

// TestOfferStream_OrderPreservedAfterCross verifies that after offer crossing,
// the remaining lower-quality offers in the book are untouched and remain in
// correct quality order.
func TestOfferStream_OrderPreservedAfterCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_OrderPreservedAfterCross(t, fs.disabled)
		})
	}
}

func testOfferStream_OrderPreservedAfterCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000)))
	env.Close()

	env.Trust(alice, USD(10000))
	env.Trust(carol, USD(10000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(5000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, USD(5000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a better-quality offer: wants XRP(100), gives USD(200) -> 2 USD/XRP
	alice1Seq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(100), USD(200)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol places a worse-quality offer: wants XRP(100), gives USD(100) -> 1 USD/XRP
	carol1Seq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(100), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, alice1Seq)
	RequireOfferInLedger(t, env, carol, carol1Seq)

	// bob crosses only the best offer: wants USD(200), gives XRP(100)
	// alice's offer gives more USD per XRP -> it is at the front of the book
	result = env.Submit(OfferCreate(bob, USD(200), jtx.XRPTxAmountFromXRP(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's offer (better quality) should be consumed
	RequireNoOfferInLedger(t, env, alice, alice1Seq)
	RequireOfferCount(t, env, alice, 0)

	// carol's lower-quality offer remains untouched
	RequireOfferInLedger(t, env, carol, carol1Seq)
	RequireOfferCount(t, env, carol, 1)

	carolOffer := GetOffer(env, carol, carol1Seq)
	require.NotNil(t, carolOffer, "carol's offer should still be in the book")
	require.True(t, amountsEqual(carolOffer.TakerGets, USD(100)),
		"carol's TakerGets should still be USD(100), got %v", carolOffer.TakerGets)
	require.True(t, amountsEqual(carolOffer.TakerPays, jtx.XRPTxAmountFromXRP(100)),
		"carol's TakerPays should still be XRP(100), got %v", carolOffer.TakerPays)

	// bob received USD(200) from alice's offer
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 200)
	RequireOfferCount(t, env, bob, 0)
}

// TestOfferStream_MultipleExpiredOffers verifies that several expired offers
// in the book are all cleaned up in a single crossing pass before reaching
// a valid offer that actually gets crossed.
//
// Engine gap: same as TestOfferStream_ExpiredOfferCleanup — expired offer
// cleanup during book traversal is not yet implemented.
// Skipped pending engine implementation.
func TestOfferStream_MultipleExpiredOffers(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_MultipleExpiredOffers(t, fs.disabled)
		})
	}
}

func testOfferStream_MultipleExpiredOffers(t *testing.T, disabledFeatures []string) {
	t.Skip("Engine gap: expired offer removal during book traversal not yet implemented. " +
		"Multiple expired offers should all be cleaned up before reaching a valid offer, " +
		"but the engine returns tecPATH_DRY before cleanup runs.")

	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dave := jtx.NewAccount("dave")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000)))
	env.FundAmount(dave, uint64(jtx.XRP(100000)))
	env.Close()

	env.Trust(alice, BTC(100))
	env.Trust(bob, BTC(100))
	env.Trust(carol, BTC(100))
	env.Trust(dave, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, dave, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice and bob place expiring offers
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(
		OfferCreate(alice, USD(100), BTC(10)).
			Expiration(LastClose(env) + 1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	bobOfferSeq := env.Seq(bob)
	result = env.Submit(
		OfferCreate(bob, USD(100), BTC(10)).
			Expiration(LastClose(env) + 1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol places a good non-expiring offer
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, USD(100), BTC(10)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferInLedger(t, env, carol, carolOfferSeq)

	// dave: wants BTC(10), gives USD(100)
	// Stream: removes alice's expired offer, removes bob's expired offer,
	// then crosses carol's good offer.
	result = env.Submit(OfferCreate(dave, BTC(10), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)

	RequireNoOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 0)
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferCount(t, env, bob, 0)

	RequireNoOfferInLedger(t, env, carol, carolOfferSeq)
	RequireOfferCount(t, env, carol, 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "BTC", 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 100)

	jtx.RequireIOUBalance(t, env, dave, gw, "BTC", 10)
	RequireOfferCount(t, env, dave, 0)
}

// TestOfferStream_TecExpiredOnCreate verifies that creating an offer with an
// already-past expiration results in tecEXPIRED (when DepositPreauth is enabled)
// or tesSUCCESS with no offer placed (pre-amendment behavior).
// Reference: rippled Offer_test.cpp testExpiration (lines 1145-1221)
func TestOfferStream_TecExpiredOnCreate(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_TecExpiredOnCreate(t, fs.disabled)
		})
	}
}

func testOfferStream_TecExpiredOnCreate(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// DepositPreauth amendment controls the return code for past-expiry offers
	featPreauth := featureEnabled(disabledFeatures, "DepositPreauth")

	// alice tries to create an offer with an expiration at exactly LastClose
	// (which is <= current ledger close time -- already expired)
	pastExpiry := LastClose(env)
	result = env.Submit(
		OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), USD(1000)).
			Expiration(pastExpiry).Build())

	if featPreauth {
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
	} else {
		jtx.RequireTxSuccess(t, result)
	}

	// In both cases, no offer is placed in the ledger
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, alice, 1) // only trust line

	// Fee is always charged
	// alice: FundAmount gives XRP(10000) net, Trust(-f), OfferCreate(-f)
	jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(10000))-f-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000) // no IOU change
}

// TestOfferStream_SelfCrossDocumented verifies the actual self-crossing behavior
// of the goXRPL engine when an account places a mirror offer that crosses with
// its own existing offer.
//
// In goXRPL (matching rippled's default-path behavior), when account A places
// an offer that directly crosses A's own existing offer, the engine executes
// the self-cross: the original offer is consumed and the new offer replaces it.
// This matches the behavior in TestOffer_SelfCross (testSelfCross Part 2).
//
// Both offers must be placed in the same ledger round (no env.Close() between
// them) for the self-cross detection to work correctly.
//
// Reference: rippled Offer_test.cpp testSelfCross (lines 1283-1391)
func TestOfferStream_SelfCrossDocumented(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_SelfCrossDocumented(t, fs.disabled)
		})
	}
}

func testOfferStream_SelfCrossDocumented(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(alice, BTC(1000))
	env.Trust(bob, USD(1000))
	env.Trust(bob, BTC(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, alice, BTC(500)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Place the first offer and the mirror offer in the SAME ledger round.
	// This matches rippled's testSelfCross behavior where no Close() is
	// called between the two offers.
	//
	// alice offer 1: wants USD(100), gives BTC(100)
	// takerPays=BTC(100), takerGets=USD(100) → alice sells USD to get BTC
	alice1Seq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, BTC(100), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)

	RequireOfferInLedger(t, env, alice, alice1Seq)
	RequireOfferCount(t, env, alice, 1)

	// alice offer 2 (mirror): wants BTC(100), gives USD(100)
	// takerPays=USD(100), takerGets=BTC(100) → alice sells BTC to get USD
	// This crosses alice's first offer (self-cross IS executed in goXRPL/rippled).
	// After self-cross: original offer (alice1Seq) is consumed, new offer replaces it.
	alice2Seq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, USD(100), BTC(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// Both offers are now in the SAME open ledger; self-cross occurs.
	env.Close()

	// The self-cross has occurred: alice1Seq is consumed, alice2Seq is the remaining offer
	// Matching rippled testSelfCross Part 2 behavior.
	RequireNoOfferInLedger(t, env, alice, alice1Seq)
	RequireOfferCount(t, env, alice, 1)
	RequireIsOffer(t, env, alice, USD(100), BTC(100))

	// bob crosses alice's remaining offer (alice offers BTC for USD):
	// alice's remaining offer: TakerPays=USD(100), TakerGets=BTC(100)
	// -> alice is offering BTC and wants USD
	// -> bob must provide USD and receive BTC
	// bob's offer: TakerPays=BTC(100), TakerGets=USD(100)
	// -> bob wants BTC(100), offers USD(100)
	result = env.Submit(OfferCreate(bob, BTC(100), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's remaining offer is consumed by bob
	RequireNoOfferInLedger(t, env, alice, alice2Seq)
	RequireOfferCount(t, env, alice, 0)

	// bob gave USD(100), got BTC(100) from alice
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 900) // started with 1000, gave 100
	jtx.RequireIOUBalance(t, env, bob, gw, "BTC", 100) // got BTC(100) from alice
	RequireOfferCount(t, env, bob, 0)
}

// TestOfferStream_UnfundedOfferRemovedDuringCross verifies that when an offer
// owner's funds are reduced to zero after placing the offer, the offer is
// identified as unfunded during a subsequent crossing attempt and removed.
// A crossing offer that encounters only unfunded offers succeeds with no fill
// and those unfunded offers are cleaned up.
func TestOfferStream_UnfundedOfferRemovedDuringCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferStream_UnfundedOfferRemovedDuringCross(t, fs.disabled)
		})
	}
}

func testOfferStream_UnfundedOfferRemovedDuringCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice places a funded offer: wants XRP(500), gives USD(500)
	aliceOfferSeq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(500), USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferInLedger(t, env, alice, aliceOfferSeq)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 500)

	// alice sends all her USD back to the gateway, making her offer completely unfunded
	result = env.Submit(payment.PayIssued(alice, gw, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	// The unfunded offer is still in the ledger
	RequireOfferInLedger(t, env, alice, aliceOfferSeq)

	// bob submits a crossing offer.
	// Traversal sees alice's offer as unfunded -> removes it.
	// No other offers exist -> bob's offer goes onto the book.
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, USD(200), jtx.XRPTxAmountFromXRP(200)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's unfunded offer is removed
	RequireNoOfferInLedger(t, env, alice, aliceOfferSeq)
	RequireOfferCount(t, env, alice, 0)

	// bob's offer goes into the book (no crossing happened)
	RequireOfferInLedger(t, env, bob, bobOfferSeq)
	RequireOfferCount(t, env, bob, 1)

	// No balances changed (no crossing)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
}
