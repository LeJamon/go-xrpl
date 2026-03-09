package offer

// Offer scaling and tiny offer tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSelfCrossLowQualityOffer (lines 3745-3778)
//   - testOfferInScaling (lines 3781-3825)
//   - testOfferInScalingWithXferRate (lines 3828-3877)
//   - testTinyOffer (lines 3947-3992)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_SelfCrossLowQualityOffer tests that a self-crossing low quality
// offer does not assert.
// The Flow offer crossing code used to assert if an offer was made for more
// XRP than the offering account held. This unit test reproduces that failing case.
// Reference: rippled Offer_test.cpp testSelfCrossLowQualityOffer (lines 3745-3778)
func TestOffer_SelfCrossLowQualityOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfCrossLowQualityOffer(t, fs.disabled)
		})
	}
}

func testSelfCrossLowQualityOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	ann := jtx.NewAccount("ann")
	gw := jtx.NewAccount("gateway")

	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	f := env.BaseFee()

	// ann: reserve(env, 2) + extra + fee, where extra < reserve increment.
	// This ensures ann can hold 2 items but cannot afford reserve for a 3rd.
	// In rippled this was 9999640 (with 50M increment); scale proportionally.
	increment := Reserve(env, 3) - Reserve(env, 2)
	extra := increment - 360
	env.FundAmount(ann, Reserve(env, 2)+extra+f)
	// gw: reserve(env, 2) + fee * 4
	env.FundAmount(gw, Reserve(env, 2)+f*4)
	env.Close()

	// rate(gw, 1.002) => 1.002 * 1e9 = 1002000000
	env.SetTransferRate(gw, 1002000000)
	env.Trust(ann, BTC(10))
	env.Close()

	result := env.Submit(payment.PayIssued(gw, ann, BTC(2.856)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// ann offers: drops(365611702030) for BTC(5.713)
	result = env.Submit(
		OfferCreate(ann, tx.NewXRPAmount(365611702030), BTC(5.713)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// This offer caused the assert in the original code.
	// ann offers: BTC(0.687) for drops(20000000000) -> tecINSUF_RESERVE_OFFER
	result = env.Submit(
		OfferCreate(ann, BTC(0.687), tx.NewXRPAmount(20000000000)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecINSUF_RESERVE_OFFER)
}

// TestOffer_InScaling tests offer in scaling after partial crossing.
// The Flow offer crossing code had a case where it was not rounding
// the offer crossing correctly after a partial crossing. The
// failing case was found on the network.
// Reference: rippled Offer_test.cpp testOfferInScaling (lines 3781-3825)
func TestOffer_InScaling(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferInScaling(t, fs.disabled)
		})
	}
}

func testOfferInScaling(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	CNY := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "CNY", amount) }

	f := env.BaseFee()

	// Fund alice and bob: reserve(env, 2) + drops(400000000000) + fee
	env.FundAmount(alice, Reserve(env, 2)+400000000000+f)
	env.FundAmount(bob, Reserve(env, 2)+400000000000+f)
	// Fund gw: reserve(env, 2) + fee * 4
	env.FundAmount(gw, Reserve(env, 2)+f*4)
	env.Close()

	env.Trust(bob, CNY(500))
	env.Close()

	result := env.Submit(payment.PayIssued(gw, bob, CNY(300)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob offers: drops(5400000000) for CNY(216.054)
	result = env.Submit(
		OfferCreate(bob, tx.NewXRPAmount(5400000000), CNY(216.054)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// This offer did not round result of partial crossing correctly.
	// alice offers: CNY(13562.0001) for drops(339000000000)
	result = env.Submit(
		OfferCreate(alice, CNY(13562.0001), tx.NewXRPAmount(339000000000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice's remaining offer after partial crossing.
	aliceOffers := OffersOnAccount(env, alice)
	require.Equal(t, 1, len(aliceOffers),
		"Expected alice to have 1 remaining offer after partial crossing")

	for _, offer := range aliceOffers {
		// TakerGets should be drops(333599446582)
		require.True(t, amountsEqual(offer.TakerGets, tx.NewXRPAmount(333599446582)),
			"Expected TakerGets = drops(333599446582), got %v", offer.TakerGets)
		// TakerPays should be CNY(13345.9461)
		require.True(t, amountsEqual(offer.TakerPays, CNY(13345.9461)),
			"Expected TakerPays = CNY(13345.9461), got %v", offer.TakerPays)
	}
}

// TestOffer_InScalingWithXferRate tests offer in scaling with transfer rate.
// After adding the previous case, there were still failing rounding
// cases in Flow offer crossing. This one was because the gateway
// transfer rate was not being correctly handled.
// Reference: rippled Offer_test.cpp testOfferInScalingWithXferRate (lines 3828-3877)
func TestOffer_InScalingWithXferRate(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferInScalingWithXferRate(t, fs.disabled)
		})
	}
}

func testOfferInScalingWithXferRate(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }
	JPY := func(amount float64) tx.Amount { return jtx.IssuedCurrency(gw, "JPY", amount) }

	f := env.BaseFee()

	// Fund alice and bob: reserve(env, 2) + drops(400000000000) + fee
	env.FundAmount(alice, Reserve(env, 2)+400000000000+f)
	env.FundAmount(bob, Reserve(env, 2)+400000000000+f)
	// Fund gw: reserve(env, 2) + fee * 4
	env.FundAmount(gw, Reserve(env, 2)+f*4)
	env.Close()

	// rate(gw, 1.002) => 1.002 * 1e9 = 1002000000
	env.SetTransferRate(gw, 1002000000)
	env.Trust(alice, JPY(4000))
	env.Trust(bob, BTC(2))
	env.Close()

	// Pay precise amounts using mantissa/exponent.
	// pay(gw, alice, JPY(3699.034802280317))
	result := env.Submit(payment.PayIssued(gw, alice, JPY(3699.034802280317)).Build())
	jtx.RequireTxSuccess(t, result)
	// pay(gw, bob, BTC(1.156722559140311))
	result = env.Submit(payment.PayIssued(gw, bob, BTC(1.156722559140311)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob offers: JPY(1241.913390770747) for BTC(0.01969825690469254)
	result = env.Submit(
		OfferCreate(bob, JPY(1241.913390770747), BTC(0.01969825690469254)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// This offer did not round result of partial crossing correctly.
	// alice offers: BTC(0.05507568706427876) for JPY(3472.696773391072)
	result = env.Submit(
		OfferCreate(alice, BTC(0.05507568706427876), JPY(3472.696773391072)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify alice's remaining offer after partial crossing.
	// The exact expected values use mantissa/exponent:
	//   TakerGets = STAmount(JPY.issue(), 2230682446713524, -12)
	//   TakerPays = BTC(0.035378)
	aliceOffers := OffersOnAccount(env, alice)
	require.Equal(t, 1, len(aliceOffers),
		"Expected alice to have 1 remaining offer after partial crossing")

	expectedTakerGets := jtx.IssuedCurrencyFromMantissa(gw, "JPY", 2230682446713524, -12)
	expectedTakerPays := BTC(0.035378)

	for _, offer := range aliceOffers {
		// Compare TakerGets with exact mantissa/exponent value
		require.True(t, amountsEqual(offer.TakerGets, expectedTakerGets),
			"Expected TakerGets = JPY{mantissa=2230682446713524, exp=-12}, got mantissa=%d exp=%d",
			offer.TakerGets.Mantissa(), offer.TakerGets.Exponent())
		// Compare TakerPays
		require.True(t, amountsEqual(offer.TakerPays, expectedTakerPays),
			"Expected TakerPays = BTC(0.035378), got mantissa=%d exp=%d",
			offer.TakerPays.Mantissa(), offer.TakerPays.Exponent())
	}
}

// TestOffer_TinyOffer tests tiny amount offer crossing.
// Reference: rippled Offer_test.cpp testTinyOffer (lines 3947-3992)
func TestOffer_TinyOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTinyOffer(t, fs.disabled)
		})
	}
}

func testTinyOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	f := env.BaseFee()

	// startXrpBalance = drops(400000000000) + fee * 2
	startXrpBalance := uint64(400000000000) + f*2

	env.FundAmount(gw, startXrpBalance)
	env.FundAmount(alice, startXrpBalance)
	env.FundAmount(bob, startXrpBalance)
	env.Close()

	// trust(bob, CNY(100000))
	CNY100000 := jtx.IssuedCurrency(gw, "CNY", 100000)
	env.Trust(bob, CNY100000)
	env.Close()

	// Place alice's tiny offer in the book first.
	// alicesCnyOffer = STAmount{CNY.issue(), 4926000000000000, -23}
	alicesCnyOffer := jtx.IssuedCurrencyFromMantissa(gw, "CNY", 4926000000000000, -23)

	// alice creates a passive offer: alicesCnyOffer for drops(1)
	result := env.Submit(
		OfferCreate(alice, alicesCnyOffer, tx.NewXRPAmount(1)).Passive().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob's CNY start balance = STAmount{CNY.issue(), 3767479960090235, -15}
	bobsCnyStartBalance := jtx.IssuedCurrencyFromMantissa(gw, "CNY", 3767479960090235, -15)
	result = env.Submit(payment.PayIssued(gw, bob, bobsCnyStartBalance).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob offers: drops(203) for STAmount{CNY.issue(), 1000000000000000, -20}
	bobsCnyOffer := jtx.IssuedCurrencyFromMantissa(gw, "CNY", 1000000000000000, -20)
	result = env.Submit(
		OfferCreate(bob, tx.NewXRPAmount(203), bobsCnyOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify balances:
	// alice should have received alicesCnyOffer in CNY
	aliceCNYBalance := env.IOUBalance(alice, gw, "CNY")
	require.NotNil(t, aliceCNYBalance, "Alice should have a CNY balance")
	require.Equal(t, 0, aliceCNYBalance.Compare(alicesCnyOffer),
		"Expected alice CNY balance = alicesCnyOffer, got mantissa=%d exp=%d",
		aliceCNYBalance.Mantissa(), aliceCNYBalance.Exponent())

	// alice XRP: startXrpBalance - fee - drops(1)
	// (alice paid 1 fee for the passive offer, and sold 1 drop)
	jtx.RequireBalance(t, env, alice, startXrpBalance-f-1)

	// bob CNY: bobsCnyStartBalance - alicesCnyOffer
	bobExpectedCNY, err := bobsCnyStartBalance.Sub(alicesCnyOffer)
	require.NoError(t, err, "Failed to compute expected bob CNY balance")
	bobCNYBalance := env.IOUBalance(bob, gw, "CNY")
	require.NotNil(t, bobCNYBalance, "Bob should have a CNY balance")
	require.Equal(t, 0, bobCNYBalance.Compare(bobExpectedCNY),
		"Expected bob CNY balance = bobsCnyStartBalance - alicesCnyOffer, got mantissa=%d exp=%d",
		bobCNYBalance.Mantissa(), bobCNYBalance.Exponent())

	// bob XRP: startXrpBalance - fee*2 + drops(1)
	// Bob paid 2 fees: 1 for trust(CNY) + 1 for OfferCreate.
	// Bob received 1 drop from alice's crossed passive offer.
	jtx.RequireBalance(t, env, bob, startXrpBalance-2*f+1)
}
