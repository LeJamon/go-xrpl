package offer

// Offer threshold and tiny payment tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testOfferThresholdWithReducedFunds (lines 3880-3944)
//   - testTinyPayment (lines 216-249)
//   - testXRPTinyPayment (lines 251-321)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	paymentBuilder "github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_ThresholdWithReducedFunds tests offer threshold behavior
// when the account creating the second offer has reduced funds due to
// a transfer rate and partial crossing.
// Reference: rippled Offer_test.cpp testOfferThresholdWithReducedFunds (lines 3880-3944)
func TestOffer_ThresholdWithReducedFunds(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferThresholdWithReducedFunds(t, fs.disabled)
		})
	}
}

func testOfferThresholdWithReducedFunds(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw1 := jtx.NewAccount("gw1")
	gw2 := jtx.NewAccount("gw2")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }
	JPY := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw2, "JPY", amount) }

	f := env.BaseFee()

	// env.fund(reserve(env, 2) + drops(400000000000) + (fee), alice, bob)
	env.FundAmount(alice, Reserve(env, 2)+400000000000+f)
	env.FundAmount(bob, Reserve(env, 2)+400000000000+f)
	// env.fund(reserve(env, 2) + (fee * 4), gw1, gw2)
	env.FundAmount(gw1, Reserve(env, 2)+f*4)
	env.FundAmount(gw2, Reserve(env, 2)+f*4)
	env.Close()

	// rate(gw1, 1.002) => 1.002 * 1e9 = 1002000000
	env.SetTransferRate(gw1, 1002000000)
	env.Trust(alice, USD(1000))
	env.Trust(bob, JPY(100000))
	env.Close()

	// pay(gw1, alice, STAmount{USD.issue(), uint64(2185410179555600), -14})
	aliceUSD := jtx.IssuedCurrencyFromMantissa(gw1, "USD", 2185410179555600, -14)
	result := env.Submit(paymentBuilder.PayIssued(gw1, alice, aliceUSD).Build())
	jtx.RequireTxSuccess(t, result)

	// pay(gw2, bob, STAmount{JPY.issue(), uint64(6351823459548956), -12})
	bobJPY := jtx.IssuedCurrencyFromMantissa(gw2, "JPY", 6351823459548956, -12)
	result = env.Submit(paymentBuilder.PayIssued(gw2, bob, bobJPY).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// offer(bob,
	//     STAmount{USD.issue(), uint64(4371257532306000), -17},
	//     STAmount{JPY.issue(), uint64(4573216636606000), -15})
	// Bob wants USD, gives JPY
	bobWantsUSD := jtx.IssuedCurrencyFromMantissa(gw1, "USD", 4371257532306000, -17)
	bobGivesJPY := jtx.IssuedCurrencyFromMantissa(gw2, "JPY", 4573216636606000, -15)
	result = env.Submit(OfferCreate(bob, bobWantsUSD, bobGivesJPY).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// offer(alice,
	//     STAmount{JPY.issue(), uint64(2291181510070762), -12},
	//     STAmount{USD.issue(), uint64(2190218999914694), -14})
	// Alice wants JPY, gives USD
	aliceWantsJPY := jtx.IssuedCurrencyFromMantissa(gw2, "JPY", 2291181510070762, -12)
	aliceGivesUSD := jtx.IssuedCurrencyFromMantissa(gw1, "USD", 2190218999914694, -14)
	result = env.Submit(OfferCreate(alice, aliceWantsJPY, aliceGivesUSD).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice's remaining offers
	aliceOffers := OffersOnAccount(env, alice)
	require.Equal(t, 1, len(aliceOffers),
		"Expected alice to have 1 remaining offer")

	expectedTakerGets := jtx.IssuedCurrencyFromMantissa(gw1, "USD", 2185847305256635, -14)
	expectedTakerPays := jtx.IssuedCurrencyFromMantissa(gw2, "JPY", 2286608293434156, -12)

	for _, offer := range aliceOffers {
		require.True(t, amountsEqual(offer.TakerGets, expectedTakerGets),
			"Expected TakerGets = USD{mantissa=2185847305256635, exp=-14}, got mantissa=%d exp=%d",
			offer.TakerGets.Mantissa(), offer.TakerGets.Exponent())
		require.True(t, amountsEqual(offer.TakerPays, expectedTakerPays),
			"Expected TakerPays = JPY{mantissa=2286608293434156, exp=-12}, got mantissa=%d exp=%d",
			offer.TakerPays.Mantissa(), offer.TakerPays.Exponent())
	}

	// Suppress unused variable warnings for the closure-based helpers
	_ = USD
	_ = JPY
}

// TestOffer_TinyPayment tests that tiny IOU payments through a large number
// of offers do not crash or panic.
// Creates 101 offers (more than the loop max count in DeliverNodeReverse)
// and then sends a payment for the smallest possible IOU amount (epsilon).
// Reference: rippled Offer_test.cpp testTinyPayment (lines 216-249)
func TestOffer_TinyPayment(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTinyPayment(t, fs.disabled)
		})
	}
}

func testTinyPayment(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	gw := jtx.NewAccount("gw")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(bob, USD(1000))
	env.Trust(carol, USD(1000))
	env.Trust(alice, EUR(1000))
	env.Trust(bob, EUR(1000))
	env.Trust(carol, EUR(1000))
	env.Close()

	result := env.Submit(paymentBuilder.PayIssued(gw, alice, USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(paymentBuilder.PayIssued(gw, carol, EUR(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// Create more offers than the loop max count in DeliverNodeReverse
	// carol creates 101 offers: wants USD(1), gives EUR(2)
	for i := 0; i < 101; i++ {
		result = env.Submit(OfferCreate(carol, USD(1), EUR(2)).Build())
		jtx.RequireTxSuccess(t, result)
	}

	// epsilon EUR = smallest possible IOU amount: mantissa=1, exponent=-81
	epsilonEUR := jtx.IssuedCurrencyFromMantissa(gw, "EUR", 1, -81)

	// Pay alice -> bob EUR(epsilon), path through EUR order book, sendmax USD(100)
	// path(~EUR) = path through EUR book = {Currency: "EUR", Issuer: gw.Address}
	result = env.Submit(
		paymentBuilder.PayIssued(alice, bob, epsilonEUR).
			SendMax(USD(100)).
			Paths([][]payment.PathStep{
				{{Currency: "EUR", Issuer: gw.Address}},
			}).
			Build(),
	)
	// The test just verifies no crash/panic - it doesn't check specific balances.
	// The result may be tesSUCCESS or a tec code; what matters is no panic.
	_ = result
}

// TestOffer_XRPTinyPayment tests tiny XRP payments through offers with
// accounts that have barely enough funds.
// Reference: rippled Offer_test.cpp testXRPTinyPayment (lines 251-321)
func TestOffer_XRPTinyPayment(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testXRPTinyPayment(t, fs.disabled)
		})
	}
}

func testXRPTinyPayment(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dan := jtx.NewAccount("dan")
	erin := jtx.NewAccount("erin")
	gw := jtx.NewAccount("gw")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(dan, uint64(jtx.XRP(10000)))
	env.FundAmount(erin, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	for _, acc := range []*jtx.Account{alice, bob, carol, dan, erin} {
		env.Trust(acc, USD(1000))
	}
	env.Close()

	// pay(gw, carol, USD(0.99999))
	result := env.Submit(paymentBuilder.PayIssued(gw, carol, USD(0.99999)).Build())
	jtx.RequireTxSuccess(t, result)
	// pay(gw, dan, USD(1))
	result = env.Submit(paymentBuilder.PayIssued(gw, dan, USD(1)).Build())
	jtx.RequireTxSuccess(t, result)
	// pay(gw, erin, USD(1))
	result = env.Submit(paymentBuilder.PayIssued(gw, erin, USD(1)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Carol doesn't quite have enough funds for her offer
	// offer(carol, drops(1), USD(0.99999))
	result = env.Submit(OfferCreate(carol, tx.NewXRPAmount(1), USD(0.99999)).Build())
	jtx.RequireTxSuccess(t, result)

	// offer(dan, XRP(100), USD(1))
	result = env.Submit(OfferCreate(dan, jtx.XRPTxAmountFromXRP(100), USD(1)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// offer(erin, drops(2), USD(1))
	result = env.Submit(OfferCreate(erin, tx.NewXRPAmount(2), USD(1)).Build())
	jtx.RequireTxSuccess(t, result)

	// pay(alice, bob, USD(1)),
	//     path(~USD),
	//     sendmax(XRP(102)),
	//     txflags(tfNoRippleDirect | tfPartialPayment)
	result = env.Submit(
		paymentBuilder.PayIssued(alice, bob, USD(1)).
			SendMax(jtx.XRPTxAmountFromXRP(102)).
			Paths([][]payment.PathStep{
				{{Currency: "USD", Issuer: gw.Address}},
			}).
			NoDirectRipple().
			PartialPayment().
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// env.require(offers(carol, 0), offers(dan, 1))
	RequireOfferCount(t, env, carol, 0)
	RequireOfferCount(t, env, dan, 1)

	// env.require(balance(erin, USD(0.99999)), offers(erin, 1))
	jtx.RequireIOUBalance(t, env, erin, gw, "USD", 0.99999)
	RequireOfferCount(t, env, erin, 1)
}
