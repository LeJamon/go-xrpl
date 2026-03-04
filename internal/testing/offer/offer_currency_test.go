package offer

// Currency conversion and cross-currency payment tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testCurrencyConversionEntire (lines 1649-1701)
//   - testCurrencyConversionIntoDebt (lines 1703-1731)
//   - testCurrencyConversionInParts (lines 1733-1817)
//   - testCrossCurrencyStartXRP (lines 1820-1859)
//   - testCrossCurrencyEndXRP (lines 1861-1907)
//   - testCrossCurrencyBridged (lines 1909-1973)
//   - testBridgedSecondLegDry (lines 1976-2040)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// ---------------------------------------------------------------------------
// testCurrencyConversionEntire
// ---------------------------------------------------------------------------

// TestOffer_CurrencyConversionEntire tests that alice can convert all her USD
// to XRP via bob's offer using a self-payment with sendmax.
// Reference: rippled Offer_test.cpp testCurrencyConversionEntire (lines 1649-1701)
func TestOffer_CurrencyConversionEntire(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCurrencyConversionEntire(t, fs.disabled)
		})
	}
}

func testCurrencyConversionEntire(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	jtx.RequireOwnerCount(t, env, bob, 0)

	env.Trust(alice, USD(100))
	env.Trust(bob, USD(1000))

	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, bob, 1)

	// Pay gw -> alice USD(100)
	result := env.Submit(payment.PayIssued(gw, alice, USD(100)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob creates offer: wants USD(100), offers XRP(500)
	// offer(bob, USD(100), XRP(500)) means bob wants to buy USD(100) and sell XRP(500)
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, USD(100), jtx.XRPTxAmountFromXRP(500)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, bob, 2)

	// Verify bob's offer exists with correct amounts
	offer := GetOffer(env, bob, bobOfferSeq)
	if offer == nil {
		t.Fatal("Bob's offer should exist")
	}

	// Alice self-pays: pay(alice, alice, XRP(500)), sendmax(USD(100))
	// This converts alice's USD to XRP through bob's offer
	result = env.Submit(
		payment.Pay(alice, alice, uint64(jtx.XRP(500))).
			SendMax(USD(100)).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Alice should have 0 USD (spent all 100 USD)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)

	// Alice's XRP balance: started with 10000 XRP, gained 500 XRP,
	// paid 2 fees (trust + self-pay)
	jtx.RequireBalance(t, env, alice,
		uint64(jtx.XRP(10000))+uint64(jtx.XRP(500))-env.BaseFee()*2)

	// Bob should have 100 USD (received from alice via the offer)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 100)

	// Bob's offer should be consumed (no longer exists)
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)

	jtx.RequireOwnerCount(t, env, alice, 1) // trust line only
	jtx.RequireOwnerCount(t, env, bob, 1)   // trust line only (offer consumed)
}

// ---------------------------------------------------------------------------
// testCurrencyConversionIntoDebt
// ---------------------------------------------------------------------------

// TestOffer_CurrencyConversionIntoDebt tests that bob's unfunded offer
// fails with tecUNFUNDED_OFFER and alice's matching offer is placed.
// Reference: rippled Offer_test.cpp testCurrencyConversionIntoDebt (lines 1703-1731)
func TestOffer_CurrencyConversionIntoDebt(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCurrencyConversionIntoDebt(t, fs.disabled)
		})
	}
}

func testCurrencyConversionIntoDebt(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	// Set up trust lines:
	// alice trusts carol for EUR(2000)
	// bob trusts alice for USD(100)
	// carol trusts bob for EUR(1000)
	env.Trust(alice, jtx.EUR(carol, 2000))
	env.Trust(bob, jtx.USD(alice, 100))
	env.Trust(carol, jtx.EUR(bob, 1000))

	// Bob tries to create offer: wants alice["USD"](50), offers carol["EUR"](200)
	// Bob has no EUR from carol, so this should fail as tecUNFUNDED_OFFER
	bobOfferSeq := env.Seq(bob)
	result := env.Submit(
		OfferCreate(bob, jtx.USD(alice, 50), jtx.EUR(carol, 200)).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecUNFUNDED_OFFER")

	// Alice places offer: wants carol["EUR"](200), offers alice["USD"](50)
	result = env.Submit(
		OfferCreate(alice, jtx.EUR(carol, 200), jtx.USD(alice, 50)).Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Verify bob's offer does not exist (it was not created due to tecUNFUNDED_OFFER)
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)
}

// ---------------------------------------------------------------------------
// testCurrencyConversionInParts
// ---------------------------------------------------------------------------

// TestOffer_CurrencyConversionInParts tests that alice can convert USD to XRP
// in parts, partially consuming bob's offer, and then using partial payment
// on the second attempt.
// Reference: rippled Offer_test.cpp testCurrencyConversionInParts (lines 1733-1817)
func TestOffer_CurrencyConversionInParts(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCurrencyConversionInParts(t, fs.disabled)
		})
	}
}

func testCurrencyConversionInParts(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(200))
	env.Trust(bob, USD(1000))

	// Pay gw -> alice USD(200)
	result := env.Submit(payment.PayIssued(gw, alice, USD(200)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob creates offer: wants USD(100), offers XRP(500)
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, USD(100), jtx.XRPTxAmountFromXRP(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice self-pays: pay(alice, alice, XRP(200)), sendmax(USD(100))
	// This partially consumes bob's offer (200 XRP worth = 40 USD at 5:1 rate)
	result = env.Submit(
		payment.Pay(alice, alice, uint64(jtx.XRP(200))).
			SendMax(USD(100)).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Bob's offer should be partially filled: XRP(300) / USD(60) remaining
	offer := GetOffer(env, bob, bobOfferSeq)
	if offer == nil {
		t.Fatal("Bob's offer should still exist (partially filled)")
	}

	// Verify remaining offer amounts
	RequireIsOffer(t, env, bob, USD(60), jtx.XRPTxAmountFromXRP(300))

	// Alice should have 160 USD (200 - 40 sent to bob)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 160)

	// Alice should have 10000 + 200 - 2*fee XRP
	jtx.RequireBalance(t, env, alice,
		uint64(jtx.XRP(10000))+uint64(jtx.XRP(200))-env.BaseFee()*2)

	// Bob should have 40 USD from the partial consumption
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 40)

	// Alice tries to convert more USD to XRP without partial payment - should fail
	result = env.Submit(
		payment.Pay(alice, alice, uint64(jtx.XRP(600))).
			SendMax(USD(100)).
			Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")

	// Alice converts USD to XRP with partial payment - should succeed
	result = env.Submit(
		payment.Pay(alice, alice, uint64(jtx.XRP(600))).
			SendMax(USD(100)).
			PartialPayment().
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Bob's offer should be consumed
	RequireNoOfferInLedger(t, env, bob, bobOfferSeq)

	// Alice should have 100 USD (160 - 60 more sent to bob in second payment)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 100)

	// Alice's XRP: 10000 + 200 (first) + 300 (second, partial) - 4*fee
	// 4 fees: trust, first self-pay, failed pay (tec - fee claimed), partial pay
	jtx.RequireBalance(t, env, alice,
		uint64(jtx.XRP(10000))+uint64(jtx.XRP(200))+uint64(jtx.XRP(300))-env.BaseFee()*4)

	// Bob should have 100 USD (40 from first + 60 from second)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 100)
}

// ---------------------------------------------------------------------------
// testCrossCurrencyStartXRP
// ---------------------------------------------------------------------------

// TestOffer_CrossCurrencyStartXRP tests a cross-currency payment starting
// with XRP. Alice pays bob USD(25) using XRP, going through carol's offer.
// Reference: rippled Offer_test.cpp testCrossCurrencyStartXRP (lines 1820-1859)
func TestOffer_CrossCurrencyStartXRP(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCrossCurrencyStartXRP(t, fs.disabled)
		})
	}
}

func testCrossCurrencyStartXRP(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(carol, USD(1000))
	env.Trust(bob, USD(2000))

	// Pay gw -> carol USD(500)
	result := env.Submit(payment.PayIssued(gw, carol, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Carol creates offer: wants XRP(500), offers USD(50)
	// carol sells USD for XRP: offer(carol, XRP(500), USD(50))
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(500), USD(50)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice pays bob USD(25) with sendmax(XRP(333))
	// Alice sends XRP which gets converted to USD through carol's offer
	result = env.Submit(
		payment.PayIssued(alice, bob, USD(25)).
			SendMax(jtx.XRPTxAmountFromXRP(333)).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Bob should have 25 USD
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 25)

	// Carol should have 475 USD (500 - 25 sold via offer)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 475)

	// Carol's offer should be partially consumed: USD(25)/XRP(250) remaining
	offer := GetOffer(env, carol, carolOfferSeq)
	if offer == nil {
		t.Fatal("Carol's offer should still exist (partially filled)")
	}
	RequireIsOffer(t, env, carol, jtx.XRPTxAmountFromXRP(250), USD(25))
}

// ---------------------------------------------------------------------------
// testCrossCurrencyEndXRP
// ---------------------------------------------------------------------------

// TestOffer_CrossCurrencyEndXRP tests a cross-currency payment ending with XRP.
// Alice pays bob XRP(250) using USD, going through carol's offer.
// Reference: rippled Offer_test.cpp testCrossCurrencyEndXRP (lines 1861-1907)
func TestOffer_CrossCurrencyEndXRP(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCrossCurrencyEndXRP(t, fs.disabled)
		})
	}
}

func testCrossCurrencyEndXRP(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(carol, USD(2000))

	// Pay gw -> alice USD(500)
	result := env.Submit(payment.PayIssued(gw, alice, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Carol creates offer: wants USD(50), offers XRP(500)
	// carol sells XRP for USD: offer(carol, USD(50), XRP(500))
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, USD(50), jtx.XRPTxAmountFromXRP(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice pays bob XRP(250) with sendmax(USD(333))
	// Alice sends USD which gets converted to XRP through carol's offer
	result = env.Submit(
		payment.Pay(alice, bob, uint64(jtx.XRP(250))).
			SendMax(USD(333)).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Alice should have 475 USD (500 - 25 spent via offer)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 475)

	// Carol should have 25 USD (received from alice via offer)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 25)

	// Bob should have 10000 + 250 XRP
	jtx.RequireBalance(t, env, bob,
		uint64(jtx.XRP(10000))+uint64(jtx.XRP(250)))

	// Carol's offer should be partially consumed: XRP(250)/USD(25) remaining
	offer := GetOffer(env, carol, carolOfferSeq)
	if offer == nil {
		t.Fatal("Carol's offer should still exist (partially filled)")
	}
	RequireIsOffer(t, env, carol, USD(25), jtx.XRPTxAmountFromXRP(250))
}

// ---------------------------------------------------------------------------
// testCrossCurrencyBridged
// ---------------------------------------------------------------------------

// TestOffer_CrossCurrencyBridged tests a bridged cross-currency payment
// USD -> XRP -> EUR going through two offers.
// Reference: rippled Offer_test.cpp testCrossCurrencyBridged (lines 1909-1973)
func TestOffer_CrossCurrencyBridged(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCrossCurrencyBridged(t, fs.disabled)
		})
	}
}

func testCrossCurrencyBridged(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw1 := jtx.NewAccount("gateway_1")
	gw2 := jtx.NewAccount("gateway_2")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dan := jtx.NewAccount("dan")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw2, amount) }

	env.FundAmount(gw1, uint64(jtx.XRP(10000)))
	env.FundAmount(gw2, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(dan, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(alice, USD(1000))
	env.Trust(bob, EUR(1000))
	env.Trust(carol, USD(1000))
	env.Trust(dan, EUR(1000))

	// Pay gw1 -> alice USD(500)
	result := env.Submit(payment.PayIssued(gw1, alice, USD(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Pay gw2 -> dan EUR(400)
	result = env.Submit(payment.PayIssued(gw2, dan, EUR(400)).Build())
	jtx.RequireTxSuccess(t, result)

	// Carol creates offer: wants USD(50), offers XRP(500)
	// carol buys USD with XRP
	carolOfferSeq := env.Seq(carol)
	result = env.Submit(OfferCreate(carol, USD(50), jtx.XRPTxAmountFromXRP(500)).Build())
	jtx.RequireTxSuccess(t, result)

	// Dan creates offer: wants XRP(500), offers EUR(50)
	// dan buys XRP with EUR
	danOfferSeq := env.Seq(dan)
	result = env.Submit(OfferCreate(dan, jtx.XRPTxAmountFromXRP(500), EUR(50)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice pays bob EUR(30) via path through XRP, sendmax USD(333)
	// Path: alice USD -> [carol's offer: USD->XRP] -> [dan's offer: XRP->EUR] -> bob EUR
	result = env.Submit(
		payment.PayIssued(alice, bob, EUR(30)).
			SendMax(USD(333)).
			PathsXRP().
			Build(),
	)
	jtx.RequireTxSuccess(t, result)

	// Verify balances:
	// Alice spent 30 USD (at 10:1 ratio through both offers: 30 EUR = 300 XRP = 30 USD)
	jtx.RequireIOUBalance(t, env, alice, gw1, "USD", 470)

	// Bob received 30 EUR
	jtx.RequireIOUBalance(t, env, bob, gw2, "EUR", 30)

	// Carol received 30 USD from the first leg
	jtx.RequireIOUBalance(t, env, carol, gw1, "USD", 30)

	// Dan spent 30 EUR in the second leg, leaving 370
	jtx.RequireIOUBalance(t, env, dan, gw2, "EUR", 370)

	// Carol's offer should have remaining: USD(20)/XRP(200)
	carolOffer := GetOffer(env, carol, carolOfferSeq)
	if carolOffer == nil {
		t.Fatal("Carol's offer should still exist (partially filled)")
	}
	RequireIsOffer(t, env, carol, USD(20), jtx.XRPTxAmountFromXRP(200))

	// Dan's offer should have remaining: XRP(200)/EUR(20)
	danOffer := GetOffer(env, dan, danOfferSeq)
	if danOffer == nil {
		t.Fatal("Dan's offer should still exist (partially filled)")
	}
	RequireIsOffer(t, env, dan, jtx.XRPTxAmountFromXRP(200), EUR(20))
}

// ---------------------------------------------------------------------------
// testBridgedSecondLegDry
// ---------------------------------------------------------------------------

// TestOffer_BridgedSecondLegDry tests that auto-bridged offers work correctly
// when the second leg goes dry before the first one.
// Reference: rippled Offer_test.cpp testBridgedSecondLegDry (lines 1976-2040)
func TestOffer_BridgedSecondLegDry(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testBridgedSecondLegDry(t, fs.disabled)
		})
	}
}

func testBridgedSecondLegDry(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	gw := jtx.NewAccount("gateway")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000000)))
	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.Close()

	// Set up alice's USD trust + balance
	env.Trust(alice, USD(10))
	env.Close()
	result := env.Submit(payment.PayIssued(gw, alice, USD(10)).Build())
	jtx.RequireTxSuccess(t, result)

	// Set up carol's USD trust + balance
	env.Trust(carol, USD(10))
	env.Close()
	result = env.Submit(payment.PayIssued(gw, carol, USD(3)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice creates two EUR->XRP offers and one XRP->USD offer
	// offer(alice, EUR(2), XRP(1)): alice wants EUR(2), offers XRP(1)
	result = env.Submit(OfferCreate(alice, EUR(2), jtx.XRPTxAmountFromXRP(1)).Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(OfferCreate(alice, EUR(2), jtx.XRPTxAmountFromXRP(1)).Build())
	jtx.RequireTxSuccess(t, result)

	// offer(alice, XRP(1), USD(4)): alice wants XRP(1), offers USD(4)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(1), USD(4)).Build())
	jtx.RequireTxSuccess(t, result)

	// offer(carol, XRP(1), USD(3)): carol wants XRP(1), offers USD(3)
	result = env.Submit(OfferCreate(carol, jtx.XRPTxAmountFromXRP(1), USD(3)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob offers to buy 10 USD for 10 EUR:
	//  1. He spends 2 EUR taking Alice's auto-bridged offers and gets 4 USD
	//  2. He spends another 2 EUR taking Alice's last EUR->XRP offer and
	//     Carol's XRP->USD offer. He gets 3 USD for that.
	// Key: Alice's XRP->USD leg goes dry before Alice's EUR->XRP.
	env.Trust(bob, EUR(10))
	env.Close()
	result = env.Submit(payment.PayIssued(gw, bob, EUR(10)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(OfferCreate(bob, USD(10), EUR(10)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify bob's balances
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 7)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 6)
	RequireOfferCount(t, env, bob, 1)
	jtx.RequireOwnerCount(t, env, bob, 3) // 1 USD trust + 1 EUR trust + 1 remaining offer

	// Verify alice's balances
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 6)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 4)
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, alice, 2) // 1 USD trust + 1 EUR trust

	// Verify carol's balances
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	// carol has no EUR trust line (EUR(none) in rippled)
	jtx.RequireTrustLineNotExists(t, env, carol, gw, "EUR")
	RequireOfferCount(t, env, carol, 0)
	jtx.RequireOwnerCount(t, env, carol, 1) // 1 USD trust only
}
