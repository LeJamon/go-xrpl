package offer

// Crossing limits tests.
// Reference: rippled/src/test/app/CrossingLimits_test.cpp
//   - testStepLimit (lines 30-67)
//   - testCrossingLimit (lines 70-105)
//   - testStepAndCrossingLimit (lines 108-161)
//   - testAutoBridgedLimitsTaker (lines 164-257)
//   - testAutoBridgedLimits (lines 260-441)
//   - testOfferOverflow (lines 444-512)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// crossingLimitsFeatureSets matches the 4 feature combinations from
// CrossingLimits_test.cpp run() method (lines 514-530).
//
//	testAll(sa);
//	testAll(sa - featureFlowSortStrands);
//	testAll(sa - featurePermissionedDEX);
//	testAll(sa - featureFlowSortStrands - featurePermissionedDEX);
var crossingLimitsFeatureSets = []featureSet{
	{
		name:     "all",
		disabled: []string{},
	},
	{
		name:     "noFlowSortStrands",
		disabled: []string{"FlowSortStrands"},
	},
	{
		name:     "noPermDEX",
		disabled: []string{"PermissionedDEX"},
	},
	{
		name:     "noFlowSortStrands_noPermDEX",
		disabled: []string{"FlowSortStrands", "PermissionedDEX"},
	},
}

// nOffers creates n identical offers for an account.
// After creating the offers, verifies the owner count increased by n.
// Equivalent to rippled's n_offers() helper in TestHelpers.cpp (lines 311-325).
func nOffers(t *testing.T, env *jtx.TestEnv, n int, acc *jtx.Account, takerPays, takerGets tx.Amount) {
	t.Helper()
	startOwnerCount := env.OwnerCount(acc)
	for i := 0; i < n; i++ {
		result := env.Submit(OfferCreate(acc, takerPays, takerGets).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
	jtx.RequireOwnerCount(t, env, acc, startOwnerCount+uint32(n))
}

// TestCrossingLimits_StepLimit tests that the payment engine step limit
// causes offer crossing to stop after consuming 1000 offers.
// Reference: CrossingLimits_test.cpp testStepLimit (lines 30-67)
func TestCrossingLimits_StepLimit(t *testing.T) {
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testStepLimit(t, fs.disabled)
		})
	}
}

func testStepLimit(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dan := jtx.NewAccount("dan")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000000)))
	env.FundAmount(dan, uint64(jtx.XRP(100000000)))

	env.Trust(bob, USD(1))
	result := env.Submit(payment.PayIssued(gw, bob, USD(1)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(dan, USD(1))
	result = env.Submit(payment.PayIssued(gw, dan, USD(1)).Build())
	jtx.RequireTxSuccess(t, result)

	nOffers(t, env, 2000, bob, jtx.XRPTxAmountFromXRP(1), USD(1))
	nOffers(t, env, 1, dan, jtx.XRPTxAmountFromXRP(1), USD(1))

	// Alice offers to buy 1000 XRP for 1000 USD. She takes Bob's first
	// offer, removes 999 more as unfunded, then hits the step limit.
	result = env.Submit(OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1)
	jtx.RequireOwnerCount(t, env, alice, 2)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, bob, 1001)
	jtx.RequireIOUBalance(t, env, dan, gw, "USD", 1)
	jtx.RequireOwnerCount(t, env, dan, 2)

	// Carol offers to buy 1000 XRP for 1000 USD. She removes Bob's next
	// 1000 offers as unfunded and hits the step limit.
	result = env.Submit(OfferCreate(carol, USD(1000), jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	// Carol has no USD trust line → balance is 0
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, carol, 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, bob, 1)
	jtx.RequireIOUBalance(t, env, dan, gw, "USD", 1)
	jtx.RequireOwnerCount(t, env, dan, 2)
}

// TestCrossingLimits_CrossingLimit tests the 1000 offer crossing limit.
// Reference: CrossingLimits_test.cpp testCrossingLimit (lines 70-105)
func TestCrossingLimits_CrossingLimit(t *testing.T) {
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCrossingLimit(t, fs.disabled)
		})
	}
}

func testCrossingLimit(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	// The payment engine allows 1000 offers to cross.
	const maxConsumed = 1000

	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000000)))

	bobsOfferCount := maxConsumed + 150
	env.Trust(bob, USD(float64(bobsOfferCount)))
	result := env.Submit(payment.PayIssued(gw, bob, USD(float64(bobsOfferCount))).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	nOffers(t, env, bobsOfferCount, bob, jtx.XRPTxAmountFromXRP(1), USD(1))

	// Alice offers to buy Bob's offers. However she hits the offer
	// crossing limit, so she can't buy them all at once.
	result = env.Submit(OfferCreate(alice, USD(float64(bobsOfferCount)), jtx.XRPTxAmountFromXRP(float64(bobsOfferCount))).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", float64(maxConsumed))
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 150)
	jtx.RequireOwnerCount(t, env, bob, 150+1)

	// Carol offers to buy 1000 XRP for 1000 USD. She takes Bob's
	// remaining 150 offers without hitting a limit.
	result = env.Submit(OfferCreate(carol, USD(1000), jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 150)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, bob, 1)
}

// TestCrossingLimits_StepAndCrossingLimit tests both step and crossing limits together.
// Reference: CrossingLimits_test.cpp testStepAndCrossingLimit (lines 108-161)
func TestCrossingLimits_StepAndCrossingLimit(t *testing.T) {
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testStepAndCrossingLimit(t, fs.disabled)
		})
	}
}

func testStepAndCrossingLimit(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dan := jtx.NewAccount("dan")
	evita := jtx.NewAccount("evita")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000000)))
	env.FundAmount(dan, uint64(jtx.XRP(100000000)))
	env.FundAmount(evita, uint64(jtx.XRP(100000000)))

	// The payment engine allows 1000 offers to cross.
	const maxConsumed = 1000

	evitasOfferCount := maxConsumed + 49
	env.Trust(alice, USD(1000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(carol, USD(1000))
	result = env.Submit(payment.PayIssued(gw, carol, USD(1)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(evita, USD(float64(evitasOfferCount+1)))
	result = env.Submit(payment.PayIssued(gw, evita, USD(float64(evitasOfferCount+1))).Build())
	jtx.RequireTxSuccess(t, result)

	// The payment engine has a limit of 1000 funded or unfunded offers.
	carolsOfferCount := 700
	nOffers(t, env, 400, alice, jtx.XRPTxAmountFromXRP(1), USD(1))
	nOffers(t, env, carolsOfferCount, carol, jtx.XRPTxAmountFromXRP(1), USD(1))
	nOffers(t, env, evitasOfferCount, evita, jtx.XRPTxAmountFromXRP(1), USD(1))

	// Bob offers to buy 1000 XRP for 1000 USD. He takes all 400 USD from
	// Alice's offers, 1 USD from Carol's and then removes 599 of Carol's
	// offers as unfunded, before hitting the step limit.
	result = env.Submit(OfferCreate(bob, USD(1000), jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 401)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 600)
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, carol, uint32(carolsOfferCount-599))
	jtx.RequireIOUBalance(t, env, evita, gw, "USD", float64(evitasOfferCount+1))
	jtx.RequireOwnerCount(t, env, evita, uint32(evitasOfferCount+1))

	// Dan offers to buy maxConsumed + 50 XRP USD. He removes all of
	// Carol's remaining offers as unfunded, then takes
	// (maxConsumed - 100) USD from Evita's, hitting the crossing limit.
	result = env.Submit(OfferCreate(dan, USD(float64(maxConsumed+50)), jtx.XRPTxAmountFromXRP(float64(maxConsumed+50))).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, dan, gw, "USD", float64(maxConsumed-100))
	jtx.RequireOwnerCount(t, env, dan, 2)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 600)
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireOwnerCount(t, env, carol, 1)
	jtx.RequireIOUBalance(t, env, evita, gw, "USD", 150)
	jtx.RequireOwnerCount(t, env, evita, 150)
}

// TestCrossingLimits_AutoBridgedLimitsTaker tests auto-bridging limits
// with taker consuming bridged offers across EUR->XRP->USD.
// NOTE: This test exists in rippled (lines 164-257) but is NOT called by run().
// It is dead code in rippled. The expected values describe pre-strand flow behavior
// and do not match the modern strand-based engine used by both rippled and goXRPL.
// Reference: CrossingLimits_test.cpp testAutoBridgedLimitsTaker (lines 164-257)
func TestCrossingLimits_AutoBridgedLimitsTaker(t *testing.T) {
	t.Skip("Dead code in rippled: testAutoBridgedLimitsTaker is not called by run(). Expected values describe obsolete pre-strand flow behavior.")
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testAutoBridgedLimitsTaker(t, fs.disabled)
		})
	}
}

func testAutoBridgedLimitsTaker(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dan := jtx.NewAccount("dan")
	evita := jtx.NewAccount("evita")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000000)))
	env.FundAmount(dan, uint64(jtx.XRP(100000000)))
	env.FundAmount(evita, uint64(jtx.XRP(100000000)))

	env.Trust(alice, USD(2000))
	result := env.Submit(payment.PayIssued(gw, alice, USD(2000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(carol, USD(1000))
	result = env.Submit(payment.PayIssued(gw, carol, USD(3)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(evita, USD(1000))
	result = env.Submit(payment.PayIssued(gw, evita, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)

	nOffers(t, env, 302, alice, EUR(2), jtx.XRPTxAmountFromXRP(1))
	nOffers(t, env, 300, alice, jtx.XRPTxAmountFromXRP(1), USD(4))
	nOffers(t, env, 497, carol, jtx.XRPTxAmountFromXRP(1), USD(3))
	nOffers(t, env, 1001, evita, EUR(1), USD(1))

	// Bob offers to buy 2000 USD for 2000 EUR, even though he only has
	// 1000 EUR.
	//  1. He spends 600 EUR taking Alice's auto-bridged offers and
	//     gets 1200 USD for that.
	//  2. He spends another 2 EUR taking one of Alice's EUR->XRP and
	//     one of Carol's XRP-USD offers. He gets 3 USD for that.
	//  3. The remainder of Carol's offers are now unfunded. We've
	//     consumed 602 offers so far. We now chew through 398 more
	//     of Carol's unfunded offers until we hit the 1000 offer limit.
	//     This sets have_bridge to false -- we will handle no more
	//     bridged offers.
	//  4. However, have_direct is still true. So we go around one more
	//     time and take one of Evita's offers.
	//  5. After taking one of Evita's offers we notice (again) that our
	//     offer count was exceeded. So we completely stop after taking
	//     one of Evita's offers.
	env.Trust(bob, EUR(10000))
	env.Close()
	result = env.Submit(payment.PayIssued(gw, bob, EUR(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(OfferCreate(bob, USD(2000), EUR(2000)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1204)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 397)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 800)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 602)
	RequireOfferCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, alice, 3)

	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "EUR", 0) // EUR(none) - no trust line
	RequireOfferCount(t, env, carol, 100)
	jtx.RequireOwnerCount(t, env, carol, 101)

	jtx.RequireIOUBalance(t, env, evita, gw, "USD", 999)
	jtx.RequireIOUBalance(t, env, evita, gw, "EUR", 1)
	RequireOfferCount(t, env, evita, 1000)
	jtx.RequireOwnerCount(t, env, evita, 1002)

	// Dan offers to buy 900 EUR for 900 USD.
	//  1. He removes all 100 of Carol's remaining unfunded offers.
	//  2. Then takes 850 USD from Evita's offers.
	//  3. Consuming 850 of Evita's funded offers hits the crossing
	//     limit. So Dan's offer crossing stops even though he would
	//     be willing to take another 50 of Evita's offers.
	env.Trust(dan, EUR(10000))
	env.Close()
	result = env.Submit(payment.PayIssued(gw, dan, EUR(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(OfferCreate(dan, USD(900), EUR(900)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, dan, gw, "USD", 850)
	jtx.RequireIOUBalance(t, env, dan, gw, "EUR", 150)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 800)
	jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 602)
	RequireOfferCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, alice, 3)

	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "EUR", 0) // EUR(none) - no trust line
	RequireOfferCount(t, env, carol, 0)
	jtx.RequireOwnerCount(t, env, carol, 1)

	jtx.RequireIOUBalance(t, env, evita, gw, "USD", 149)
	jtx.RequireIOUBalance(t, env, evita, gw, "EUR", 851)
	RequireOfferCount(t, env, evita, 150)
	jtx.RequireOwnerCount(t, env, evita, 152)
}

// TestCrossingLimits_AutoBridgedLimits tests auto-bridging limits with
// strands that have many unfunded offers causing the strand to be marked dry.
// Two sub-tests: one where the dry strand has best initial quality, one where it doesn't.
// Reference: CrossingLimits_test.cpp testAutoBridgedLimits (lines 260-441)
func TestCrossingLimits_AutoBridgedLimits(t *testing.T) {
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testAutoBridgedLimits(t, fs.disabled)
		})
	}
}

func testAutoBridgedLimits(t *testing.T, disabledFeatures []string) {
	USD := func(gw *jtx.Account, amount float64) tx.Amount { return jtx.USD(gw, amount) }
	EUR := func(gw *jtx.Account, amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	// Sub-test 1: The strand with 800 unfunded offers has the initial best quality.
	// Reference: CrossingLimits_test.cpp lines 290-368
	t.Run("BestQualityDryStrand", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		env.FundAmount(gw, uint64(jtx.XRP(100000000)))
		env.FundAmount(alice, uint64(jtx.XRP(100000000)))
		env.FundAmount(bob, uint64(jtx.XRP(100000000)))
		env.FundAmount(carol, uint64(jtx.XRP(100000000)))

		env.Trust(alice, USD(gw, 4000))
		result := env.Submit(payment.PayIssued(gw, alice, USD(gw, 4000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Trust(carol, USD(gw, 1000))
		result = env.Submit(payment.PayIssued(gw, carol, USD(gw, 3)).Build())
		jtx.RequireTxSuccess(t, result)

		// Notice the strand with the 800 unfunded offers has the initial
		// best quality
		nOffers(t, env, 2000, alice, EUR(gw, 2), jtx.XRPTxAmountFromXRP(1))
		nOffers(t, env, 100, alice, jtx.XRPTxAmountFromXRP(1), USD(gw, 4))
		nOffers(t, env, 801, carol, jtx.XRPTxAmountFromXRP(1), USD(gw, 3)) // only one funded
		nOffers(t, env, 1000, alice, jtx.XRPTxAmountFromXRP(1), USD(gw, 3))

		nOffers(t, env, 1, alice, EUR(gw, 500), USD(gw, 500))

		// Bob offers to buy 2000 USD for 2000 EUR; He starts with 2000 EUR
		//  1. Best quality: autobridged 2 EUR → 4 USD.
		//     Bob spends 200 EUR, receives 400 USD. (200 offers consumed)
		//  2. Best quality: autobridged 2 EUR → 3 USD.
		//     Carol's 1 funded + 800 unfunded + Alice's 199 funded = 1000 step limit
		//     Bob spends 400 EUR, receives 600 USD. (1200 total consumed)
		//  3. Non-autobridged: 500 EUR → 500 USD.
		//     Bob spends 500 EUR, receives 500 USD.
		// Total: Bob spent 1100 EUR, has 900 remaining, received 1500 USD.
		env.Trust(bob, EUR(gw, 10000))
		env.Close()
		result = env.Submit(payment.PayIssued(gw, bob, EUR(gw, 2000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(OfferCreate(bob, USD(gw, 4000), EUR(gw, 4000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1500)
		jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 900)
		RequireOfferCount(t, env, bob, 1)
		jtx.RequireOwnerCount(t, env, bob, 3)

		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 2503)
		jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 1100)
		// numAOffers = 2000 + 100 + 1000 + 1 - (2*100 + 2*199 + 1 + 1) = 2501
		numAOffers := 2000 + 100 + 1000 + 1 - (2*100 + 2*199 + 1 + 1)
		RequireOfferCount(t, env, alice, uint32(numAOffers))
		jtx.RequireOwnerCount(t, env, alice, uint32(numAOffers+2))

		RequireOfferCount(t, env, carol, 0)
	})

	// Sub-test 2: The strand with 800 unfunded offers does NOT have the
	// initial best quality.
	// Reference: CrossingLimits_test.cpp lines 369-441
	t.Run("NonBestQualityDryStrand", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		env.FundAmount(gw, uint64(jtx.XRP(100000000)))
		env.FundAmount(alice, uint64(jtx.XRP(100000000)))
		env.FundAmount(bob, uint64(jtx.XRP(100000000)))
		env.FundAmount(carol, uint64(jtx.XRP(100000000)))

		env.Trust(alice, USD(gw, 4000))
		result := env.Submit(payment.PayIssued(gw, alice, USD(gw, 4000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Trust(carol, USD(gw, 1000))
		result = env.Submit(payment.PayIssued(gw, carol, USD(gw, 3)).Build())
		jtx.RequireTxSuccess(t, result)

		// Notice the strand with the 800 unfunded offers does NOT have the
		// initial best quality
		nOffers(t, env, 1, alice, EUR(gw, 1), USD(gw, 10))
		nOffers(t, env, 2000, alice, EUR(gw, 2), jtx.XRPTxAmountFromXRP(1))
		nOffers(t, env, 100, alice, jtx.XRPTxAmountFromXRP(1), USD(gw, 4))
		nOffers(t, env, 801, carol, jtx.XRPTxAmountFromXRP(1), USD(gw, 3)) // only one funded
		nOffers(t, env, 1000, alice, jtx.XRPTxAmountFromXRP(1), USD(gw, 3))

		nOffers(t, env, 1, alice, EUR(gw, 499), USD(gw, 499))

		// Bob offers to buy 2000 USD for 2000 EUR; He starts with 2000 EUR
		//  1. Best quality: 1 EUR → 10 USD. Bob spends 1 EUR, receives 10 USD.
		//  2. Best quality: autobridged 2 EUR → 4 USD.
		//     Bob spends 200 EUR, receives 400 USD.
		//  3. Best quality: autobridged 2 EUR → 3 USD.
		//     Same as sub-test 1. Bob spends 400 EUR, receives 600 USD.
		//  4. Non-autobridged: 499 EUR → 499 USD.
		//     Bob spends 499 EUR, receives 499 USD.
		// Total: Bob spent 1100 EUR, has 900 remaining, received 1509 USD.
		env.Trust(bob, EUR(gw, 10000))
		env.Close()
		result = env.Submit(payment.PayIssued(gw, bob, EUR(gw, 2000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(OfferCreate(bob, USD(gw, 4000), EUR(gw, 4000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1509)
		jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 900)
		RequireOfferCount(t, env, bob, 1)
		jtx.RequireOwnerCount(t, env, bob, 3)

		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 2494)
		jtx.RequireIOUBalance(t, env, alice, gw, "EUR", 1100)
		// numAOffers = 1 + 2000 + 100 + 1000 + 1 - (1 + 2*100 + 2*199 + 1 + 1) = 2501
		numAOffers := 1 + 2000 + 100 + 1000 + 1 - (1 + 2*100 + 2*199 + 1 + 1)
		RequireOfferCount(t, env, alice, uint32(numAOffers))
		jtx.RequireOwnerCount(t, env, alice, uint32(numAOffers+2))

		RequireOfferCount(t, env, carol, 0)
	})
}

// TestCrossingLimits_OfferOverflow tests offer overflow behavior when
// consuming excessive offers across multiple quality levels.
// The behavior differs based on featureFlowSortStrands:
//   - Without FlowSortStrands: results in tecOVERSIZE
//   - With FlowSortStrands: stops after consuming 1996 offers, tesSUCCESS
//
// Reference: CrossingLimits_test.cpp testOfferOverflow (lines 444-512)
func TestCrossingLimits_OfferOverflow(t *testing.T) {
	for _, fs := range crossingLimitsFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferOverflow(t, fs.disabled)
		})
	}
}

func testOfferOverflow(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(100000000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000000)))

	env.Trust(alice, USD(8000))
	env.Trust(bob, USD(8000))
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, USD(8000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Set up a book with many offers. At each quality keep the number of
	// offers below the limit. However, if all the offers are consumed it
	// would create a tecOVERSIZE error without FlowSortStrands.
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(1.00), USD(1))
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(0.99), USD(1))
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(0.98), USD(1))
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(0.97), USD(1))
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(0.96), USD(1))
	nOffers(t, env, 998, alice, jtx.XRPTxAmountFromXRP(0.95), USD(1))

	withSortStrands := featureEnabled(disabledFeatures, "FlowSortStrands")

	result = env.Submit(OfferCreate(bob, USD(8000), jtx.XRPTxAmountFromXRP(8000)).Build())

	if withSortStrands {
		jtx.RequireTxSuccess(t, result)
	} else {
		jtx.RequireTxClaimed(t, result, jtx.TecOVERSIZE)
	}
	env.Close()

	if withSortStrands {
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1996)
	} else {
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	}
}

