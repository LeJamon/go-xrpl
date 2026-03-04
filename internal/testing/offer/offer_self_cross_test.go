package offer

// Offer self-crossing tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSelfCross (lines 1283-1391)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_SelfCross tests self-crossing with and without a partner account.
// Part 1: Auto-bridge through XRP: create BTC->XRP offer, XRP->USD offer,
// then USD->BTC offer. All offers get consumed by self-crossing.
// Part 2: Direct crossing: create BTC->USD offer, then USD->BTC offer.
// Second offer replaces first.
// Reference: rippled Offer_test.cpp testSelfCross (lines 1283-1391)
func TestOffer_SelfCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name+"/WithoutPartner", func(t *testing.T) {
			testSelfCross(t, false, fs.disabled)
		})
		t.Run(fs.name+"/WithPartner", func(t *testing.T) {
			testSelfCross(t, true, fs.disabled)
		})
	}
}

func testSelfCross(t *testing.T, usePartner bool, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	partner := jtx.NewAccount("partner")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BTC := func(amount float64) tx.Amount { return jtx.BTC(gw, amount) }

	env.Close()
	env.FundAmount(gw, uint64(jtx.XRP(10000)))

	if usePartner {
		env.FundAmount(partner, uint64(jtx.XRP(10000)))
		env.Close()
		env.Trust(partner, USD(100))
		env.Trust(partner, BTC(500))
		env.Close()
		result := env.Submit(payment.PayIssued(gw, partner, USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw, partner, BTC(500)).Build())
		jtx.RequireTxSuccess(t, result)
	}

	var accountToTest *jtx.Account
	if usePartner {
		accountToTest = partner
	} else {
		accountToTest = gw
	}

	env.Close()
	RequireOfferCount(t, env, accountToTest, 0)

	// ========================================================================
	// PART 1: auto-bridge BTC->USD through XRP
	// ========================================================================

	// Create offer: account wants BTC(250), offers XRP(1000)
	result := env.Submit(OfferCreate(accountToTest, BTC(250), jtx.XRPTxAmountFromXRP(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, accountToTest, 1)
	RequireIsOffer(t, env, accountToTest, BTC(250), jtx.XRPTxAmountFromXRP(1000))

	// Record the sequence for the second leg offer so we can cancel it later
	secondLegSeq := env.Seq(accountToTest)

	// Create offer: account wants XRP(1000), offers USD(50)
	result = env.Submit(OfferCreate(accountToTest, jtx.XRPTxAmountFromXRP(1000), USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, accountToTest, 2)
	RequireIsOffer(t, env, accountToTest, jtx.XRPTxAmountFromXRP(1000), USD(50))

	// This crosses via auto-bridge, consuming outstanding offers.
	// Create offer: account wants USD(50), offers BTC(250)
	result = env.Submit(OfferCreate(accountToTest, USD(50), BTC(250)).Build())
	jtx.RequireTxSuccess(t, result)

	// All offers should be consumed
	acctOffers := OffersOnAccount(env, accountToTest)
	require.Equal(t, 0, len(acctOffers),
		"Expected all offers to be consumed by self-crossing, but found %d", len(acctOffers))

	// Cancel lingering second offer (in case it survived)
	result = env.Submit(OfferCancel(accountToTest, secondLegSeq).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, accountToTest, 0)

	// ========================================================================
	// PART 2: direct crossing
	// ========================================================================

	// Create offer: account wants BTC(250), offers USD(50)
	result = env.Submit(OfferCreate(accountToTest, BTC(250), USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, accountToTest, 1)
	RequireIsOffer(t, env, accountToTest, BTC(250), USD(50))

	// Create offer: account wants USD(50), offers BTC(250)
	// This should replace the first offer via self-crossing
	result = env.Submit(OfferCreate(accountToTest, USD(50), BTC(250)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, accountToTest, 1)
	RequireIsOffer(t, env, accountToTest, USD(50), BTC(250))
}
