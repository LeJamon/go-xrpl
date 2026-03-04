package offer

// Offer tfSell flag tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSellFlagBasic (lines 2154-2194)
//   - testSellFlagExceedLimit (lines 2196-2239)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestOffer_SellFlagBasic tests basic tfSell offer crossing behavior.
// When both sides use tfSell, the offer crosses at the book rate.
// Alice ends up with 100 USD (half of what she offered for 200 XRP because
// bob's offer was only for 200 USD at 1:1 rate and alice only has 100 XRP to sell).
// Reference: rippled Offer_test.cpp testSellFlagBasic (lines 2154-2194)
func TestOffer_SellFlagBasic(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSellFlagBasic(t, fs.disabled)
		})
	}
}

func testSellFlagBasic(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	// starting_xrp = XRP(100) + reserve(env, 1) + env.current()->fees().base * 2
	startingXRP := uint64(jtx.XRP(100)) + Reserve(env, 1) + env.BaseFee()*2

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, startingXRP)
	env.FundAmount(bob, startingXRP)
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(bob, USD(1000))
	result := env.Submit(payment.PayIssued(gw, bob, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob places a sell offer: wants XRP(200), offers USD(200), with tfSell
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(200), USD(200)).Sell().Build())
	jtx.RequireTxSuccess(t, result)

	// Alice places a sell offer: wants USD(200), offers XRP(200), with tfSell
	result = env.Submit(OfferCreate(alice, USD(200), jtx.XRPTxAmountFromXRP(200)).Sell().Build())
	jtx.RequireTxSuccess(t, result)

	// Alice should hold 100 USD (rippled shows -100 from issuer perspective)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 100)

	// Alice's XRP balance should be exactly the reserve for 1 owner object
	jtx.RequireBalance(t, env, alice, Reserve(env, 1))

	// Bob should hold 400 USD (started with 500, paid 100 to alice)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 400)
}

// TestOffer_SellFlagExceedLimit tests tfSell offer crossing where the taker
// would exceed their trust line limit. With tfSell, the taker gets more than
// they asked for if the book rate is better, even exceeding the trust line limit.
// Reference: rippled Offer_test.cpp testSellFlagExceedLimit (lines 2196-2239)
func TestOffer_SellFlagExceedLimit(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSellFlagExceedLimit(t, fs.disabled)
		})
	}
}

func testSellFlagExceedLimit(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	// starting_xrp = XRP(100) + reserve(env, 1) + env.current()->fees().base * 2
	startingXRP := uint64(jtx.XRP(100)) + Reserve(env, 1) + env.BaseFee()*2

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.FundAmount(alice, startingXRP)
	env.FundAmount(bob, startingXRP)
	env.Close()

	// Alice's trust line limit is only 150 USD
	env.Trust(alice, USD(150))
	env.Trust(bob, USD(1000))
	result := env.Submit(payment.PayIssued(gw, bob, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob places offer: wants XRP(100), offers USD(200) (no tfSell)
	// This means bob is willing to pay up to 2 USD per XRP
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(100), USD(200)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice places sell offer: wants USD(100), offers XRP(100), with tfSell
	// With tfSell, alice will sell all 100 XRP at the book rate (2 USD per XRP),
	// receiving 200 USD even though she only asked for 100 USD and her trust
	// line limit is 150 USD.
	result = env.Submit(OfferCreate(alice, USD(100), jtx.XRPTxAmountFromXRP(100)).Sell().Build())
	jtx.RequireTxSuccess(t, result)

	// Alice should hold 200 USD (exceeds her 150 trust line limit due to tfSell)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 200)

	// Alice's XRP balance should be exactly the reserve for 1 owner object
	jtx.RequireBalance(t, env, alice, Reserve(env, 1))

	// Bob should hold 300 USD (started with 500, paid 200 to alice)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 300)
}
