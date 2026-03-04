package offer

// Miscellaneous offer tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testFalseAssert (lines 5095-5110)
//   - testRmFundedOffer (lines 74-133)
//   - testDirectToDirectPath (lines 3692-3742)
//   - testBadPathAssert (lines 3621-3689)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestOffer_FalseAssert tests that computing rates for offers does not trigger
// a false assert. An assert was falsely triggering when computing rates for
// offers with very large amounts.
// This test does NOT iterate over feature sets -- it runs once.
// Reference: rippled Offer_test.cpp testFalseAssert (lines 5095-5110)
func TestOffer_FalseAssert(t *testing.T) {
	env := jtx.NewTestEnvBacked(t)

	alice := jtx.NewAccount("alice")
	// alice is the issuer of USD
	USD := func(amount float64) tx.Amount { return jtx.IssuedCurrency(alice, "USD", amount) }

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// XRP(100000000000) in rippled = 100 billion XRP = 1e17 drops.
	// The offer is: takerPays = XRP(100000000000), takerGets = USD(100000000).
	// alice is the issuer of USD, so she can offer any amount.
	// This just verifies that no panic/assert happens.
	result := env.Submit(OfferCreate(alice,
		tx.NewXRPAmount(100000000000000000), // 100 billion XRP in drops (1e17)
		USD(100000000),                      // 100 million USD
	).Build())
	jtx.RequireTxSuccess(t, result)
}

// TestOffer_RmFundedOffer tests incorrect removal of funded offers.
// Uses path payments with multiple offer books to verify that funded offers
// are not incorrectly removed during payment processing.
// Reference: rippled Offer_test.cpp testRmFundedOffer (lines 74-133)
func TestOffer_RmFundedOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testRmFundedOffer(t, fs.disabled)
		})
	}
}

func testRmFundedOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(bob, USD(1000))
	env.Trust(carol, USD(1000))
	env.Trust(alice, BTC(1000))
	env.Trust(bob, BTC(1000))
	env.Trust(carol, BTC(1000))

	result := env.Submit(payment.PayIssued(gw, alice, BTC(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(payment.PayIssued(gw, carol, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gw, carol, BTC(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	// Must be two offers at the same quality, taker gets must be XRP.
	// carol offers: takerPays = BTC(49), takerGets = XRP(49)
	// carol receives BTC and gives XRP.
	result = env.Submit(OfferCreate(carol, BTC(49), jtx.XRPTxAmountFromXRP(49)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(OfferCreate(carol, BTC(51), jtx.XRPTxAmountFromXRP(51)).Build())
	jtx.RequireTxSuccess(t, result)

	// Offers for the poor quality path.
	// carol offers: takerPays = XRP(50), takerGets = USD(50)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(50), USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(50), USD(50)).Build())
	jtx.RequireTxSuccess(t, result)

	// Offers for the good quality path.
	// carol offers: takerPays = BTC(1), takerGets = USD(100)
	result = env.Submit(OfferCreate(carol, BTC(1), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// PathSet paths(Path(XRP, USD), Path(USD));
	// Path(XRP, USD): step through XRP (currency=XRP, issuer=zero) then USD (currency=USD, issuer=gw)
	// Path(USD): step through USD directly (currency=USD, issuer=gw)
	paths := [][]paymentPkg.PathStep{
		// Path through XRP then USD order book
		{
			{Currency: "XRP"},
			{Currency: "USD", Issuer: gw.Address},
		},
		// Direct path through USD order book
		{
			{Currency: "USD", Issuer: gw.Address},
		},
	}

	// alice pays bob USD(100) with sendmax BTC(1000), partial payment allowed
	result = env.Submit(payment.PayIssued(alice, bob, USD(100)).
		SendMax(BTC(1000)).
		Paths(paths).
		PartialPayment().
		Build())
	jtx.RequireTxSuccess(t, result)

	// bob should have received USD(100)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 100)

	// The good quality path offer (BTC(1) -> USD(100)) should have been consumed.
	// The first BTC->XRP offer (BTC(49) -> XRP(49)) should still exist.
	require.False(t, IsOffer(env, carol, BTC(1), USD(100)),
		"carol's BTC(1)->USD(100) offer should have been consumed")
	require.True(t, IsOffer(env, carol, BTC(49), jtx.XRPTxAmountFromXRP(49)),
		"carol's BTC(49)->XRP(49) offer should still exist")
}

// TestOffer_DirectToDirectPath tests that the offer crossing code handles
// the case where a DirectStep is preceded by another DirectStep (not a
// BookStep). This was a bug where the default path was not matching the
// assumption that DirectStep is always preceded by BookStep.
// Reference: rippled Offer_test.cpp testDirectToDirectPath (lines 3692-3742)
func TestOffer_DirectToDirectPath(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testDirectToDirectPath(t, fs.disabled)
		})
	}
}

func testDirectToDirectPath(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	ann := jtx.NewAccount("ann")
	bob := jtx.NewAccount("bob")
	cam := jtx.NewAccount("cam")

	// A_BUX is ann's BUX, B_BUX is bob's BUX - different issuers
	A_BUX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(ann, "BUX", amount) }
	B_BUX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(bob, "BUX", amount) }

	fee := env.BaseFee()
	fundAmount := Reserve(env, 4) + fee*5
	env.FundAmount(ann, fundAmount)
	env.FundAmount(bob, fundAmount)
	env.FundAmount(cam, fundAmount)
	env.Close()

	// ann trusts bob's BUX
	env.Trust(ann, B_BUX(40))
	// cam trusts ann's BUX and bob's BUX
	env.Trust(cam, A_BUX(40))
	env.Trust(cam, B_BUX(40))
	env.Close()

	// ann pays cam A_BUX(35) -- ann is issuer, so this is direct issuance
	result := env.Submit(payment.PayIssued(ann, cam, A_BUX(35)).Build())
	jtx.RequireTxSuccess(t, result)
	// bob pays cam B_BUX(35) -- bob is issuer
	result = env.Submit(payment.PayIssued(bob, cam, B_BUX(35)).Build())
	jtx.RequireTxSuccess(t, result)

	// bob places offer: wants A_BUX(30), pays B_BUX(30)
	result = env.Submit(OfferCreate(bob, A_BUX(30), B_BUX(30)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// cam puts a passive offer that her upcoming offer could cross.
	// But this offer should be deleted, not crossed, by her upcoming offer.
	// cam offers: wants A_BUX(29), pays B_BUX(30), passive
	result = env.Submit(OfferCreate(cam, A_BUX(29), B_BUX(30)).Passive().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, cam, ann, "BUX", 35)
	jtx.RequireIOUBalance(t, env, cam, bob, "BUX", 35)
	RequireOfferCount(t, env, cam, 1)

	// This offer caused the assert in rippled.
	// cam offers: wants B_BUX(30), pays A_BUX(30)
	result = env.Submit(OfferCreate(cam, B_BUX(30), A_BUX(30)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob should have received A_BUX(30) from crossing with cam's offer
	jtx.RequireIOUBalance(t, env, bob, ann, "BUX", 30)
	// cam: started with A_BUX(35), gave A_BUX(30) to bob, left with A_BUX(5)
	jtx.RequireIOUBalance(t, env, cam, ann, "BUX", 5)
	// cam: started with B_BUX(35), received B_BUX(30) from bob, total B_BUX(65)
	jtx.RequireIOUBalance(t, env, cam, bob, "BUX", 65)
	// All of cam's offers should be consumed
	RequireOfferCount(t, env, cam, 0)
}

// TestOffer_BadPathAssert tests that an invalid path does not cause an assert.
// A trust line's QualityOut affects payments, and a specific combination of
// QualityOut, offers, and self-payment paths previously caused an assert.
// Reference: rippled Offer_test.cpp testBadPathAssert (lines 3621-3689)
func TestOffer_BadPathAssert(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testBadPathAssert(t, fs.disabled)
		})
	}
}

func testBadPathAssert(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	ann := jtx.NewAccount("ann")
	bob := jtx.NewAccount("bob")
	cam := jtx.NewAccount("cam")
	dan := jtx.NewAccount("dan")

	A_BUX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(ann, "BUX", amount) }
	D_BUX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(dan, "BUX", amount) }

	fee := env.BaseFee()
	fundAmount := Reserve(env, 4) + fee*4
	env.FundAmount(ann, fundAmount)
	env.FundAmount(bob, fundAmount)
	env.FundAmount(cam, fundAmount)
	env.FundAmount(dan, fundAmount)
	env.Close()

	// bob trusts ann's BUX (limit 400)
	env.Trust(bob, A_BUX(400))

	// bob trusts dan's BUX (limit 200) with qualityOutPercent(120)
	// qualityOutPercent(120) = (120/100) * 1e9 = 1,200,000,000
	result := env.Submit(trustset.TrustSet(bob, D_BUX(200)).
		QualityOut(trustset.QualityFromPercentage(120)).
		Build())
	jtx.RequireTxSuccess(t, result)

	// cam trusts dan's BUX (limit 100)
	env.Trust(cam, D_BUX(100))
	env.Close()

	// dan pays bob D_BUX(100) -- dan is issuer
	result = env.Submit(payment.PayIssued(dan, bob, D_BUX(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify bob has D_BUX(100)
	jtx.RequireIOUBalance(t, env, bob, dan, "BUX", 100)

	// Payment with path through bob and dan:
	// ann pays cam D_BUX(60) with path(bob, dan) and sendmax A_BUX(200).
	// path(bob, dan) = account steps through bob then dan.
	// The QualityOut of 120% on bob's D_BUX trust line means that when
	// D_BUX flows out of bob, it costs 1.2x in A_BUX from ann.
	// So 60 D_BUX out of bob costs 72 A_BUX from ann.
	result = env.Submit(payment.PayIssued(ann, cam, D_BUX(60)).
		SendMax(A_BUX(200)).
		Paths([][]paymentPkg.PathStep{
			{
				{Account: bob.Address},
				{Account: dan.Address},
			},
		}).
		Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Check balances after the first payment
	// bob received A_BUX(72) from ann (60 * 1.2 QualityOut adjustment)
	jtx.RequireIOUBalance(t, env, bob, ann, "BUX", 72)
	// bob's D_BUX: started with 100, paid 60 out, left with 40
	jtx.RequireIOUBalance(t, env, bob, dan, "BUX", 40)
	// cam received D_BUX(60)
	jtx.RequireIOUBalance(t, env, cam, dan, "BUX", 60)

	// bob places offer: wants A_BUX(30), pays D_BUX(30)
	result = env.Submit(OfferCreate(bob, A_BUX(30), D_BUX(30)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// ann trusts dan's BUX (limit 100)
	env.Trust(ann, D_BUX(100))
	env.Close()

	// This payment caused the assert in rippled.
	// ann pays herself D_BUX(30) with path(A_BUX, D_BUX) and sendmax A_BUX(30).
	// path(A_BUX, D_BUX) = Issue steps: {Currency: "BUX", Issuer: ann} then {Currency: "BUX", Issuer: dan}.
	// This should fail with temBAD_PATH because the path is invalid (self-payment
	// with this path structure is not allowed).
	result = env.Submit(payment.PayIssued(ann, ann, D_BUX(30)).
		SendMax(A_BUX(30)).
		Paths([][]paymentPkg.PathStep{
			{
				{Currency: "BUX", Issuer: ann.Address},
				{Currency: "BUX", Issuer: dan.Address},
			},
		}).
		Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_PATH)
	env.Close()

	// After the failed payment, balances should not have changed.
	// ann's D_BUX balance should be 0 (she just set up the trust line, never received any)
	jtx.RequireIOUBalance(t, env, ann, dan, "BUX", 0)
	// bob still has A_BUX(72)
	jtx.RequireIOUBalance(t, env, bob, ann, "BUX", 72)
	// bob still has D_BUX(40)
	jtx.RequireIOUBalance(t, env, bob, dan, "BUX", 40)
	// cam still has D_BUX(60)
	jtx.RequireIOUBalance(t, env, cam, dan, "BUX", 60)
}
