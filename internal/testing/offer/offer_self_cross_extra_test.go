package offer

// Self-crossing offer extra tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSelfCrossOffer1 (lines 3386-3453)
//   - testSelfCrossOffer2 (lines 3456-3565)
//   - testSelfIssueOffer (lines 3575-3618)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_SelfCrossOffer1 verifies that when an account creates an offer
// that can be directly crossed by its previous offers, the old offers are
// deleted and the new one goes on the book.
// Reference: rippled Offer_test.cpp testSelfCrossOffer1 (lines 3386-3453)
func TestOffer_SelfCrossOffer1(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfCrossOffer1(t, fs.disabled)
		})
	}
}

func testSelfCrossOffer1(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	f := env.BaseFee()
	startBalance := uint64(jtx.XRP(1000000))

	env.FundAmount(gw, startBalance+f*4)
	env.Close()

	// Create 3 offers that will be self-crossed
	result := env.Submit(OfferCreate(gw, USD(60), jtx.XRPTxAmountFromXRP(600)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(OfferCreate(gw, USD(60), jtx.XRPTxAmountFromXRP(600)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(OfferCreate(gw, USD(60), jtx.XRPTxAmountFromXRP(600)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, gw, 3)
	jtx.RequireBalance(t, env, gw, startBalance+f)

	gwOffers := OffersOnAccount(env, gw)
	require.Equal(t, 3, len(gwOffers))
	for _, offer := range gwOffers {
		require.True(t, amountsEqual(offer.TakerGets, jtx.XRPTxAmountFromXRP(600)),
			"Expected TakerGets = XRP(600)")
		require.True(t, amountsEqual(offer.TakerPays, USD(60)),
			"Expected TakerPays = USD(60)")
	}

	// This offer crosses the first three, so they get deleted and this one
	// goes on the book.
	result = env.Submit(OfferCreate(gw, jtx.XRPTxAmountFromXRP(1000), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireOwnerCount(t, env, gw, 1)
	RequireOfferCount(t, env, gw, 1)
	jtx.RequireBalance(t, env, gw, startBalance)

	gwOffers = OffersOnAccount(env, gw)
	require.Equal(t, 1, len(gwOffers))
	for _, offer := range gwOffers {
		require.True(t, amountsEqual(offer.TakerGets, USD(100)),
			"Expected TakerGets = USD(100)")
		require.True(t, amountsEqual(offer.TakerPays, jtx.XRPTxAmountFromXRP(1000)),
			"Expected TakerPays = XRP(1000)")
	}
}

// TestOffer_SelfCrossOffer2 is table-driven and tests various funding
// scenarios for self-crossing IOU-to-IOU offers.
// Reference: rippled Offer_test.cpp testSelfCrossOffer2 (lines 3456-3565)
func TestOffer_SelfCrossOffer2(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfCrossOffer2(t, fs.disabled)
		})
	}
}

func testSelfCrossOffer2(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw1 := jtx.NewAccount("gateway1")
	gw2 := jtx.NewAccount("gateway2")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw2, amount) }

	env.FundAmount(gw1, uint64(jtx.XRP(1000000)))
	env.FundAmount(gw2, uint64(jtx.XRP(1000000)))
	env.Close()

	f := env.BaseFee()

	type testData struct {
		acct           string
		fundXRP        uint64
		fundUSD        float64
		fundEUR        float64
		firstOfferTec  string
		secondOfferTec string
	}

	tests := []testData{
		{"ann", Reserve(env, 3) + f*4, 1000, 1000, "", ""},
		{"bev", Reserve(env, 3) + f*4, 1, 1000, "", ""},
		{"cam", Reserve(env, 3) + f*4, 1000, 1, "", ""},
		{"deb", Reserve(env, 3) + f*4, 0, 1, "", string(jtx.TecUNFUNDED_OFFER)},
		{"eve", Reserve(env, 3) + f*4, 1, 0, string(jtx.TecUNFUNDED_OFFER), ""},
		{"flo", Reserve(env, 3) + 0, 1000, 1000, string(jtx.TecINSUF_RESERVE_OFFER), string(jtx.TecINSUF_RESERVE_OFFER)},
	}

	for _, tt := range tests {
		t.Run(tt.acct, func(t *testing.T) {
			acct := jtx.NewAccount(tt.acct)
			env.FundAmount(acct, tt.fundXRP)
			env.Close()

			env.Trust(acct, USD(1000))
			env.Trust(acct, EUR(1000))
			env.Close()

			if tt.fundUSD > 0 {
				result := env.Submit(payment.PayIssued(gw1, acct, USD(tt.fundUSD)).Build())
				jtx.RequireTxSuccess(t, result)
			}
			if tt.fundEUR > 0 {
				result := env.Submit(payment.PayIssued(gw2, acct, EUR(tt.fundEUR)).Build())
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()

			// First offer: USD(500) for EUR(600)
			firstOfferSeq := env.Seq(acct)
			result := env.Submit(OfferCreate(acct, USD(500), EUR(600)).Build())
			if tt.firstOfferTec == "" {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TxResultCode(tt.firstOfferTec))
			}
			env.Close()

			offerCount := 0
			if tt.firstOfferTec == "" {
				offerCount = 1
			}
			jtx.RequireOwnerCount(t, env, acct, uint32(2+offerCount))
			if tt.fundUSD > 0 {
				jtx.RequireIOUBalance(t, env, acct, gw1, "USD", tt.fundUSD)
			}
			if tt.fundEUR > 0 {
				jtx.RequireIOUBalance(t, env, acct, gw2, "EUR", tt.fundEUR)
			}

			acctOffers := OffersOnAccount(env, acct)
			require.Equal(t, offerCount, len(acctOffers))
			for _, offer := range acctOffers {
				require.True(t, amountsEqual(offer.TakerGets, EUR(600)))
				require.True(t, amountsEqual(offer.TakerPays, USD(500)))
			}

			// Second offer: EUR(600) for USD(500)
			secondOfferSeq := env.Seq(acct)
			result = env.Submit(OfferCreate(acct, EUR(600), USD(500)).Build())
			if tt.secondOfferTec == "" {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TxResultCode(tt.secondOfferTec))
			}
			env.Close()

			if tt.secondOfferTec == "" {
				offerCount = 1
			}
			jtx.RequireOwnerCount(t, env, acct, uint32(2+offerCount))
			if tt.fundUSD > 0 {
				jtx.RequireIOUBalance(t, env, acct, gw1, "USD", tt.fundUSD)
			}
			if tt.fundEUR > 0 {
				jtx.RequireIOUBalance(t, env, acct, gw2, "EUR", tt.fundEUR)
			}

			acctOffers = OffersOnAccount(env, acct)
			require.Equal(t, offerCount, len(acctOffers))
			for _, offer := range acctOffers {
				if offer.Sequence == firstOfferSeq {
					require.True(t, amountsEqual(offer.TakerGets, EUR(600)))
					require.True(t, amountsEqual(offer.TakerPays, USD(500)))
				} else {
					require.True(t, amountsEqual(offer.TakerGets, USD(500)))
					require.True(t, amountsEqual(offer.TakerPays, EUR(600)))
				}
			}

			// Cleanup
			env.Submit(OfferCancel(acct, firstOfferSeq).Build())
			env.Close()
			env.Submit(OfferCancel(acct, secondOfferSeq).Build())
			env.Close()
		})
	}
}

// TestOffer_SelfIssueOffer tests that self-issuing works correctly.
// An issuer can create offers for their own currency.
// Reference: rippled Offer_test.cpp testSelfIssueOffer (lines 3575-3618)
func TestOffer_SelfIssueOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfIssueOffer(t, fs.disabled)
		})
	}
}

func testSelfIssueOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	// bob is the issuer of USD
	USD := func(amount float64) tx.Amount { return jtx.USD(bob, amount) }

	f := env.BaseFee()

	env.FundAmount(alice, uint64(jtx.XRP(50000))+f)
	env.FundAmount(bob, uint64(jtx.XRP(50000))+f)
	env.Close()

	// alice creates offer to buy bob's USD with XRP
	result := env.Submit(OfferCreate(alice, USD(5000), jtx.XRPTxAmountFromXRP(50000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob crosses with his own USD → takes alice's offer up to reserve
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(50000), USD(5000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's offer should be removed since she's at her reserve
	// In rippled this was XRP(250) = 200 base + 50 owner; use dynamic reserve.
	jtx.RequireBalance(t, env, alice, Reserve(env, 1))
	jtx.RequireOwnerCount(t, env, alice, 1) // just the trust line
	// alice should have 1 line (auto-created trust line)

	// bob should have a remaining offer
	bobOffers := OffersOnAccount(env, bob)
	require.Equal(t, 1, len(bobOffers))
	if len(bobOffers) > 0 {
		require.True(t, amountsEqual(bobOffers[0].TakerGets, USD(25)),
			"Expected TakerGets = USD(25)")
		require.True(t, amountsEqual(bobOffers[0].TakerPays, jtx.XRPTxAmountFromXRP(250)),
			"Expected TakerPays = XRP(250)")
	}
}
