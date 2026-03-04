package offer

// Table-driven sell offer tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testSellOffer (lines 2803-2989)
//
// Tests a number of different corner cases regarding offer crossing
// when the tfSell flag is set. The test is table driven so it should
// be easy to add or remove tests.

import (
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// sellOfferTestData holds the parameters and expected results for a single
// sell offer test case.  Mirrors the TestData struct in rippled's
// testSellOffer (Offer_test.cpp lines 2825-2906).
type sellOfferTestData struct {
	account  string     // Account name
	fundXrp  uint64     // XRP acct funded with (drops)
	fundUSD  float64    // USD acct funded with (0 means no pre-trust)
	gwGets   tx.Amount  // gw's offer: what gw gets
	gwPays   tx.Amount  // gw's offer: what gw pays
	acctGets tx.Amount  // acct's offer: what acct gets
	acctPays tx.Amount  // acct's offer: what acct pays
	tec      string     // Expected result code
	spentXrp int64      // XRP removed from fundXrp (drops, can be negative)
	finalUSD float64    // Final USD balance on acct
	offers   uint32     // Expected offer count on acct
	owners   uint32     // Expected owner count on acct
	// Remainder of acct's offer (only checked when offers > 0)
	takerGets tx.Amount
	takerPays tx.Amount
}

// TestOffer_SellOfferTable tests table-driven sell offer crossing.
// Reference: rippled Offer_test.cpp testSellOffer (lines 2803-2989)
func TestOffer_SellOfferTable(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSellOfferTable(t, fs.disabled)
		})
	}
}

func testSellOfferTable(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	XRP := func(n int64) tx.Amount { return jtx.XRPTxAmountFromXRP(float64(n)) }

	env.FundAmount(gw, uint64(jtx.XRP(10000000)))
	env.Close()

	// The fee charged for transactions.
	f := env.BaseFee()

	// Build the test table.
	// To keep things simple all offers are 1:1 for XRP:USD.
	// Reference: Offer_test.cpp lines 2909-2928
	tests := []sellOfferTestData{
		// acct pays XRP
		//                account  fundXrp                               fundUSD  gwGets   gwPays    acctGets  acctPays  tec                         spentXrp                   finalUSD  offers  owners  takerGets  takerPays
		{account: "ann", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 0) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(5), acctGets: USD(10), acctPays: XRP(10), tec: jtx.TecINSUF_RESERVE_OFFER, spentXrp: jtx.XRP(0) + int64(1*f), finalUSD: 0, offers: 0, owners: 0},
		{account: "bev", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 1) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(5), acctGets: USD(10), acctPays: XRP(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(0) + int64(1*f), finalUSD: 0, offers: 1, owners: 1, takerGets: XRP(10), takerPays: USD(10)},
		{account: "cam", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 0) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(10), acctGets: USD(10), acctPays: XRP(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(10) + int64(1*f), finalUSD: 10, offers: 0, owners: 1},
		{account: "deb", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 0) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(20), acctGets: USD(10), acctPays: XRP(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(10) + int64(1*f), finalUSD: 20, offers: 0, owners: 1},
		{account: "eve", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 0) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(20), acctGets: USD(5), acctPays: XRP(5), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(5) + int64(1*f), finalUSD: 10, offers: 0, owners: 1},
		{account: "flo", fundXrp: uint64(jtx.XRP(10)) + Reserve(env, 0) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(20), acctGets: USD(20), acctPays: XRP(20), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(10) + int64(1*f), finalUSD: 20, offers: 0, owners: 1},
		{account: "gay", fundXrp: uint64(jtx.XRP(20)) + Reserve(env, 1) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(20), acctGets: USD(20), acctPays: XRP(20), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(10) + int64(1*f), finalUSD: 20, offers: 0, owners: 1},
		{account: "hye", fundXrp: uint64(jtx.XRP(20)) + Reserve(env, 2) + 1*f, fundUSD: 0, gwGets: XRP(10), gwPays: USD(20), acctGets: USD(20), acctPays: XRP(20), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(10) + int64(1*f), finalUSD: 20, offers: 1, owners: 2, takerGets: XRP(10), takerPays: USD(10)},
		// acct pays USD
		{account: "meg", fundXrp: Reserve(env, 1) + 2*f, fundUSD: 10, gwGets: USD(10), gwPays: XRP(5), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TecINSUF_RESERVE_OFFER, spentXrp: jtx.XRP(0) + int64(2*f), finalUSD: 10, offers: 0, owners: 1},
		{account: "nia", fundXrp: Reserve(env, 2) + 2*f, fundUSD: 10, gwGets: USD(10), gwPays: XRP(5), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(0) + int64(2*f), finalUSD: 10, offers: 1, owners: 2, takerGets: USD(10), takerPays: XRP(10)},
		{account: "ova", fundXrp: Reserve(env, 1) + 2*f, fundUSD: 10, gwGets: USD(10), gwPays: XRP(10), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(-10) + int64(2*f), finalUSD: 0, offers: 0, owners: 1},
		{account: "pam", fundXrp: Reserve(env, 1) + 2*f, fundUSD: 10, gwGets: USD(10), gwPays: XRP(20), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(-20) + int64(2*f), finalUSD: 0, offers: 0, owners: 1},
		{account: "qui", fundXrp: Reserve(env, 1) + 2*f, fundUSD: 10, gwGets: USD(20), gwPays: XRP(40), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(-20) + int64(2*f), finalUSD: 0, offers: 0, owners: 1},
		{account: "rae", fundXrp: Reserve(env, 2) + 2*f, fundUSD: 10, gwGets: USD(5), gwPays: XRP(5), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(-5) + int64(2*f), finalUSD: 5, offers: 1, owners: 2, takerGets: USD(5), takerPays: XRP(5)},
		{account: "sue", fundXrp: Reserve(env, 2) + 2*f, fundUSD: 10, gwGets: USD(5), gwPays: XRP(10), acctGets: XRP(10), acctPays: USD(10), tec: jtx.TesSUCCESS, spentXrp: jtx.XRP(-10) + int64(2*f), finalUSD: 5, offers: 1, owners: 2, takerGets: USD(5), takerPays: XRP(5)},
	}

	for _, tc := range tests {
		t.Run(tc.account, func(t *testing.T) {
			// Make sure gateway has no current offers.
			RequireOfferCount(t, env, gw, 0)

			acct := jtx.NewAccount(tc.account)

			env.FundAmount(acct, tc.fundXrp)
			env.Close()

			// Optionally give acct some USD. This is not part of the test,
			// so we assume that acct has sufficient USD to cover the reserve
			// on the trust line.
			if tc.fundUSD > 0 {
				env.Trust(acct, USD(tc.fundUSD))
				env.Close()
				result := env.Submit(payment.PayIssued(gw, acct, USD(tc.fundUSD)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
			}

			// Gateway creates its offer.
			result := env.Submit(OfferCreate(gw, tc.gwGets, tc.gwPays).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			gwOfferSeq := env.Seq(gw) - 1

			// Acct creates a tfSell offer. This is the heart of the test.
			acctResult := env.Submit(OfferCreate(acct, tc.acctGets, tc.acctPays).Sell().Build())
			env.Close()
			acctOfferSeq := env.Seq(acct) - 1

			// Check the result code.
			if tc.tec == jtx.TesSUCCESS {
				jtx.RequireTxSuccess(t, acctResult)
			} else {
				jtx.RequireTxClaimed(t, acctResult, tc.tec)
			}

			// Check USD balance.
			jtx.RequireIOUBalance(t, env, acct, gw, "USD", tc.finalUSD)

			// Check XRP balance: fundXrp - spentXrp
			expectedXrpBalance := int64(tc.fundXrp) - tc.spentXrp
			require.Equal(t, uint64(expectedXrpBalance), env.Balance(acct),
				fmt.Sprintf("Account %s XRP balance mismatch: expected %d, got %d",
					tc.account, expectedXrpBalance, env.Balance(acct)))

			// Check offer count and owner count.
			RequireOfferCount(t, env, acct, tc.offers)
			jtx.RequireOwnerCount(t, env, acct, tc.owners)

			// If the account has remaining offers, verify the amounts.
			if tc.offers > 0 {
				acctOffers := OffersOnAccount(env, acct)
				require.Greater(t, len(acctOffers), 0,
					"Expected at least one offer on account %s", tc.account)
				if len(acctOffers) > 0 {
					require.Equal(t, 1, len(acctOffers),
						"Expected exactly 1 offer on account %s, got %d",
						tc.account, len(acctOffers))
					offer := acctOffers[0]
					require.True(t, amountsEqual(offer.TakerGets, tc.takerGets),
						"Account %s TakerGets mismatch: expected %v, got %v",
						tc.account, tc.takerGets, offer.TakerGets)
					require.True(t, amountsEqual(offer.TakerPays, tc.takerPays),
						"Account %s TakerPays mismatch: expected %v, got %v",
						tc.account, tc.takerPays, offer.TakerPays)
				}
			}

			// Clean up by canceling any left-over offers.
			env.Submit(OfferCancel(acct, acctOfferSeq).Build())
			env.Submit(OfferCancel(gw, gwOfferSeq).Build())
			env.Close()
		})
	}
}
