package offer

// Offer direct and bridged crossing tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testXRPDirectCross   (lines 2510-2584)
//   - testDirectCross      (lines 2587-2702)
//   - testBridgedCross     (lines 2705-2800)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_XRPDirectCross tests XRP direct crossing.
// alice has USD wants XRP, bob has XRP wants USD. They cross offers directly,
// then a partial cross is tested where one offer has a remainder.
// Reference: rippled Offer_test.cpp testXRPDirectCross (lines 2510-2584)
func TestOffer_XRPDirectCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testXRPDirectCross(t, fs.disabled)
		})
	}
}

func testXRPDirectCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	usdOffer := USD(1000)
	xrpOffer := jtx.XRPTxAmountFromXRP(1000)

	fee := env.BaseFee()

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(bob, uint64(jtx.XRP(1000000)))
	env.Close()

	// alice's account has enough for the reserve, one trust line plus two
	// offers, and two fees.
	env.FundAmount(alice, Reserve(env, 2)+fee*2)
	env.Close()

	env.Trust(alice, usdOffer)
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)

	// The scenario:
	//   o alice has USD but wants XRP.
	//   o bob has XRP but wants USD.
	alicesXRP := env.Balance(alice)
	bobsXRP := env.Balance(bob)

	// alice: takerPays=XRP(1000) (wants XRP), takerGets=USD(1000) (gives USD)
	result = env.Submit(OfferCreate(alice, xrpOffer, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob: takerPays=USD(1000) (wants USD), takerGets=XRP(1000) (gives XRP)
	result = env.Submit(OfferCreate(bob, usdOffer, xrpOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After crossing: alice should have no USD left, bob should have usdOffer.
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1000)

	// alice gains XRP(1000) minus fee for the offer tx
	jtx.RequireBalance(t, env, alice, alicesXRP+uint64(jtx.XRP(1000))-fee)
	// bob loses XRP(1000) plus fee for the offer tx
	jtx.RequireBalance(t, env, bob, bobsXRP-uint64(jtx.XRP(1000))-fee)

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)

	// Make two more offers that leave one of the offers non-dry.
	// alice: wants USD(999), gives XRP(999)
	result = env.Submit(OfferCreate(alice, USD(999), jtx.XRPTxAmountFromXRP(999)).Build())
	jtx.RequireTxSuccess(t, result)

	// bob: wants XRP(1000), gives USD(1000) -- bigger than alice's offer
	result = env.Submit(OfferCreate(bob, xrpOffer, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice crossed 999 of bob's 1000: alice gets USD(999)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 999)
	// bob had 1000 USD, paid 999 to alice, keeps 1
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1)

	// alice's offer should be fully consumed
	RequireOfferCount(t, env, alice, 0)

	// bob should have one remaining offer for the unfilled portion
	bobsOffers := OffersOnAccount(env, bob)
	require.Equal(t, 1, len(bobsOffers), "bob should have 1 remaining offer")

	bobOffer := bobsOffers[0]
	require.True(t, amountsEqual(bobOffer.TakerGets, USD(1)),
		"bob's remaining offer TakerGets should be USD(1), got %v", bobOffer.TakerGets)
	require.True(t, amountsEqual(bobOffer.TakerPays, jtx.XRPTxAmountFromXRP(1)),
		"bob's remaining offer TakerPays should be XRP(1), got %v", bobOffer.TakerPays)
}

// TestOffer_DirectCross tests IOU direct crossing.
// alice has USD wants EUR, bob has EUR wants USD. Full cross, partial cross,
// and cleanup cross scenarios.
// Reference: rippled Offer_test.cpp testDirectCross (lines 2587-2702)
func TestOffer_DirectCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testDirectCross(t, fs.disabled)
		})
	}
}

func testDirectCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	usdOffer := USD(1000)
	eurOffer := EUR(1000)

	fee := env.BaseFee()

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.Close()

	// Each account has enough for the reserve, two trust lines, one
	// offer, and some fees.
	env.FundAmount(alice, Reserve(env, 3)+fee*3)
	env.FundAmount(bob, Reserve(env, 3)+fee*2)
	env.Close()

	env.Trust(alice, usdOffer)
	env.Trust(bob, eurOffer)
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, bob, eurOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 1000)

	// The scenario:
	//   o alice has USD but wants EUR.
	//   o bob has EUR but wants USD.
	// alice: takerPays=EUR(1000), takerGets=USD(1000)
	result = env.Submit(OfferCreate(alice, eurOffer, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)

	// bob: takerPays=USD(1000), takerGets=EUR(1000)
	result = env.Submit(OfferCreate(bob, usdOffer, eurOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After full crossing: alice has EUR, bob has USD, no offers remain.
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 1000)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)

	// Make two more offers that leave one of the offers non-dry.
	// Guarantee the order of application by putting a close() between them.
	// bob: takerPays=EUR(1000), takerGets=USD(1000)
	result = env.Submit(OfferCreate(bob, eurOffer, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice: takerPays=USD(999), takerGets=EUR(1000)
	result = env.Submit(OfferCreate(alice, USD(999), eurOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 1)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 999)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 999)

	// Verify bob's remaining offer
	bobsOffers := OffersOnAccount(env, bob)
	require.Equal(t, 1, len(bobsOffers), "bob should have 1 remaining offer")

	bobOffer := bobsOffers[0]
	require.True(t, amountsEqual(bobOffer.TakerGets, USD(1)),
		"bob's remaining offer TakerGets should be USD(1), got %v", bobOffer.TakerGets)
	require.True(t, amountsEqual(bobOffer.TakerPays, EUR(1)),
		"bob's remaining offer TakerPays should be EUR(1), got %v", bobOffer.TakerPays)

	// alice makes one more offer that cleans out bob's offer.
	// alice: takerPays=USD(1), takerGets=EUR(1)
	result = env.Submit(OfferCreate(alice, USD(1), EUR(1)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 1000)
	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)

	// The two trustlines that were generated by offers should be gone.
	// alice should not have an EUR trust line, bob should not have a USD trust line.
	require.False(t, env.TrustLineExists(alice, gw, "EUR"),
		"alice's offer-created EUR trust line should be deleted")
	require.False(t, env.TrustLineExists(bob, gw, "USD"),
		"bob's offer-created USD trust line should be deleted")

	// Make two more offers that leave one of the offers non-dry. We
	// need to properly sequence the transactions.
	// alice: takerPays=EUR(999), takerGets=USD(1000)
	result = env.Submit(OfferCreate(alice, EUR(999), usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob: takerPays=USD(1000), takerGets=EUR(1000)
	result = env.Submit(OfferCreate(bob, usdOffer, eurOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 999)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1000)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 1)
}

// TestOffer_BridgedCross tests bridged crossing through XRP.
// alice has USD wants XRP, bob has XRP wants EUR, carol has EUR wants USD.
// Carol's offer triggers auto-bridge, first partial then full consumption.
// Reference: rippled Offer_test.cpp testBridgedCross (lines 2705-2800)
func TestOffer_BridgedCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testBridgedCross(t, fs.disabled)
		})
	}
}

func testBridgedCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	usdOffer := USD(1000)
	eurOffer := EUR(1000)

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, uint64(jtx.XRP(1000000)))
	env.FundAmount(bob, uint64(jtx.XRP(1000000)))
	env.FundAmount(carol, uint64(jtx.XRP(1000000)))
	env.Close()

	env.Trust(alice, usdOffer)
	env.Trust(carol, eurOffer)
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, eurOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// The scenario:
	//   o alice has USD but wants XRP.
	//   o bob has XRP but wants EUR.
	//   o carol has EUR but wants USD.
	// Note that carol's offer must come last. If carol's offer is placed
	// before bob's or alice's, then autobridging will not occur.

	// alice: takerPays=XRP(1000), takerGets=USD(1000)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), usdOffer).Build())
	jtx.RequireTxSuccess(t, result)

	// bob: takerPays=EUR(1000), takerGets=XRP(1000)
	result = env.Submit(OfferCreate(bob, eurOffer, jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	bobXrpBalance := env.Balance(bob)
	env.Close()

	// carol makes an offer that partially consumes alice and bob's offers.
	// carol: takerPays=USD(400), takerGets=EUR(400)
	result = env.Submit(OfferCreate(carol, USD(400), EUR(400)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After partial bridged crossing:
	// alice started with USD(1000), sold USD(400) for XRP(400) via the bridge
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 600)
	// bob received EUR(400) from carol via the bridge
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 400)
	// carol received USD(400) from alice via the bridge
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 400)
	// bob's XRP decreased by XRP(400) -- he paid XRP to facilitate the bridge
	jtx.RequireBalance(t, env, bob, bobXrpBalance-uint64(jtx.XRP(400)))
	// carol's offers should be fully consumed
	RequireOfferCount(t, env, carol, 0)

	// Verify alice's remaining offer: USD(600) for XRP(600)
	alicesOffers := OffersOnAccount(env, alice)
	require.Equal(t, 1, len(alicesOffers), "alice should have 1 remaining offer")

	aliceOffer := alicesOffers[0]
	require.True(t, amountsEqual(aliceOffer.TakerGets, USD(600)),
		"alice's remaining offer TakerGets should be USD(600), got %v", aliceOffer.TakerGets)
	require.True(t, amountsEqual(aliceOffer.TakerPays, jtx.XRPTxAmountFromXRP(600)),
		"alice's remaining offer TakerPays should be XRP(600), got %v", aliceOffer.TakerPays)

	// Verify bob's remaining offer: XRP(600) for EUR(600)
	bobsOffers := OffersOnAccount(env, bob)
	require.Equal(t, 1, len(bobsOffers), "bob should have 1 remaining offer")

	bobOffer := bobsOffers[0]
	require.True(t, amountsEqual(bobOffer.TakerGets, jtx.XRPTxAmountFromXRP(600)),
		"bob's remaining offer TakerGets should be XRP(600), got %v", bobOffer.TakerGets)
	require.True(t, amountsEqual(bobOffer.TakerPays, EUR(600)),
		"bob's remaining offer TakerPays should be EUR(600), got %v", bobOffer.TakerPays)

	// carol makes an offer that exactly consumes alice and bob's remaining offers.
	// carol: takerPays=USD(600), takerGets=EUR(600)
	result = env.Submit(OfferCreate(carol, USD(600), EUR(600)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// After full bridged crossing:
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 1000)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 1000)
	jtx.RequireBalance(t, env, bob, bobXrpBalance-uint64(jtx.XRP(1000)))
	RequireOfferCount(t, env, bob, 0)
	RequireOfferCount(t, env, carol, 0)

	// In pre-flow code alice's offer is left empty in the ledger.
	// With flow, alice's offer should be cleaned up. Either way is acceptable.
	alicesOffers = OffersOnAccount(env, alice)
	if len(alicesOffers) != 0 {
		require.Equal(t, 1, len(alicesOffers),
			"if alice has leftover offers, there should be exactly 1")

		aliceOffer = alicesOffers[0]
		require.True(t, amountsEqual(aliceOffer.TakerGets, USD(0)),
			"alice's leftover offer TakerGets should be USD(0), got %v", aliceOffer.TakerGets)
		require.True(t, amountsEqual(aliceOffer.TakerPays, jtx.XRPTxAmountFromXRP(0)),
			"alice's leftover offer TakerPays should be XRP(0), got %v", aliceOffer.TakerPays)
	}
}
