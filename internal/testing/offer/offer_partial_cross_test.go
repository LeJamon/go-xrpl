package offer

// Offer partial crossing tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testPartialCross (lines 2351-2508)
//   - testOfferCrossWithXRP (lines 1503-1556)
//   - testOfferCrossWithLimitOverride (lines 1558-1597)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_PartialCross tests a number of different corner cases regarding
// adding a possibly crossable offer to an account. Table driven.
// Reference: rippled Offer_test.cpp testPartialCross (lines 2351-2508)
func TestOffer_PartialCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testPartialCross(t, fs.disabled)
		})
	}
}

func testPartialCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000000)))
	env.Close()

	f := env.BaseFee()

	const (
		noPreTrust   = 0
		gwPreTrust   = 1
		acctPreTrust = 2
	)

	type testData struct {
		account    string
		fundXrp    uint64
		bookAmount int     // gw offers this much USD for XRP
		preTrust   int     // noPreTrust, gwPreTrust, acctPreTrust
		offerAmt   int     // account offers XRP for this much USD
		tec        string  // expected result code ("" for success)
		spentXrp   uint64  // XRP removed from fundXrp
		balanceUsd float64 // final USD balance
		offers     int     // offers on account
		owners     int     // owners on account
	}

	// Compute reserve-dependent expected values.
	// During offer crossing with noPreTrust, reserveReduction=-1 reduces
	// the effective owner count to 0, so xrpLiquid uses R(0) as reserve.
	// Available XRP = balance_after_fee - R(0).
	ownerRes := env.ReserveIncrement()

	// gay: available = R(1) - R(0) = ownerReserve
	gayXrp := ownerRes

	// hye: available = XRP(1000) - R(0)
	hyeXrp := uint64(jtx.XRP(1000)) - Reserve(env, 0)

	// kim: available = XRP(900) + R(2) - R(0) = XRP(900) + 2*ownerRes, capped by bookAmount
	kimXrp := uint64(jtx.XRP(900)) + 2*ownerRes
	if kimXrp > uint64(jtx.XRP(999)) {
		kimXrp = uint64(jtx.XRP(999))
	}

	tests := []testData{
		// No pre-established trust lines
		{"ann", Reserve(env, 0) + 0*f, 1, noPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), f, 0, 0, 0},
		{"bev", Reserve(env, 0) + 1*f, 1, noPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), f, 0, 0, 0},
		{"cam", Reserve(env, 0) + 2*f, 0, noPreTrust, 1000, string(jtx.TecINSUF_RESERVE_OFFER), f, 0, 0, 0},
		{"deb", 10 + Reserve(env, 0) + 1*f, 1, noPreTrust, 1000, "", 10 + f, 0.00001, 0, 1},
		{"eve", Reserve(env, 1) + 0*f, 0, noPreTrust, 1000, "", f, 0, 1, 1},
		{"flo", Reserve(env, 1) + 0*f, 1, noPreTrust, 1000, "", uint64(jtx.XRP(1)) + f, 1, 0, 1},
		{"gay", Reserve(env, 1) + 1*f, 1000, noPreTrust, 1000, "", gayXrp + f, float64(gayXrp) / 1e6, 0, 1},
		{"hye", uint64(jtx.XRP(1000)) + 1*f, 1000, noPreTrust, 1000, "", hyeXrp + f, float64(hyeXrp) / 1e6, 0, 1},
		{"ivy", uint64(jtx.XRP(1)) + Reserve(env, 1) + 1*f, 1, noPreTrust, 1000, "", uint64(jtx.XRP(1)) + f, 1, 0, 1},
		{"joy", uint64(jtx.XRP(1)) + Reserve(env, 2) + 1*f, 1, noPreTrust, 1000, "", uint64(jtx.XRP(1)) + f, 1, 1, 2},
		{"kim", uint64(jtx.XRP(900)) + Reserve(env, 2) + 1*f, 999, noPreTrust, 1000, "", kimXrp + f, float64(kimXrp) / 1e6, 0, 1},
		{"liz", uint64(jtx.XRP(998)) + Reserve(env, 0) + 1*f, 999, noPreTrust, 1000, "", uint64(jtx.XRP(998)) + f, 998, 0, 1},
		{"meg", uint64(jtx.XRP(998)) + Reserve(env, 1) + 1*f, 999, noPreTrust, 1000, "", uint64(jtx.XRP(999)) + f, 999, 0, 1},
		{"nia", uint64(jtx.XRP(998)) + Reserve(env, 2) + 1*f, 999, noPreTrust, 1000, "", uint64(jtx.XRP(999)) + f, 999, 1, 2},
		{"ova", uint64(jtx.XRP(999)) + Reserve(env, 0) + 1*f, 1000, noPreTrust, 1000, "", uint64(jtx.XRP(999)) + f, 999, 0, 1},
		{"pam", uint64(jtx.XRP(999)) + Reserve(env, 1) + 1*f, 1000, noPreTrust, 1000, "", uint64(jtx.XRP(1000)) + f, 1000, 0, 1},
		{"rae", uint64(jtx.XRP(999)) + Reserve(env, 2) + 1*f, 1000, noPreTrust, 1000, "", uint64(jtx.XRP(1000)) + f, 1000, 0, 1},
		{"sue", uint64(jtx.XRP(1000)) + Reserve(env, 2) + 1*f, 0, noPreTrust, 1000, "", f, 0, 1, 1},
		// Pre-established trust lines: gateway pre-trusts
		{"abe", Reserve(env, 0) + 0*f, 1, gwPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), f, 0, 0, 0},
		{"bud", Reserve(env, 0) + 1*f, 1, gwPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), f, 0, 0, 0},
		{"che", Reserve(env, 0) + 2*f, 0, gwPreTrust, 1000, string(jtx.TecINSUF_RESERVE_OFFER), f, 0, 0, 0},
		{"dan2", 10 + Reserve(env, 0) + 1*f, 1, gwPreTrust, 1000, "", 10 + f, 0.00001, 0, 0},
		{"eli", uint64(jtx.XRP(20)) + Reserve(env, 0) + 1*f, 1000, gwPreTrust, 1000, "", uint64(jtx.XRP(20)) + 1*f, 20, 0, 0},
		{"fyn", Reserve(env, 1) + 0*f, 0, gwPreTrust, 1000, "", f, 0, 1, 1},
		{"gar", Reserve(env, 1) + 0*f, 1, gwPreTrust, 1000, "", uint64(jtx.XRP(1)) + f, 1, 1, 1},
		{"hal", Reserve(env, 1) + 1*f, 1, gwPreTrust, 1000, "", uint64(jtx.XRP(1)) + f, 1, 1, 1},
		// Pre-established trust lines: account pre-trusts
		{"ned", Reserve(env, 1) + 0*f, 1, acctPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), 2 * f, 0, 0, 1},
		{"ole", Reserve(env, 1) + 1*f, 1, acctPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), 2 * f, 0, 0, 1},
		{"pat", Reserve(env, 1) + 2*f, 0, acctPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), 2 * f, 0, 0, 1},
		{"quy", Reserve(env, 1) + 2*f, 1, acctPreTrust, 1000, string(jtx.TecUNFUNDED_OFFER), 2 * f, 0, 0, 1},
		{"ron", Reserve(env, 1) + 3*f, 0, acctPreTrust, 1000, string(jtx.TecINSUF_RESERVE_OFFER), 2 * f, 0, 0, 1},
		{"syd", 10 + Reserve(env, 1) + 2*f, 1, acctPreTrust, 1000, "", 10 + 2*f, 0.00001, 0, 1},
		{"ted", uint64(jtx.XRP(20)) + Reserve(env, 1) + 2*f, 1000, acctPreTrust, 1000, "", uint64(jtx.XRP(20)) + 2*f, 20, 0, 1},
		{"uli", Reserve(env, 2) + 0*f, 0, acctPreTrust, 1000, string(jtx.TecINSUF_RESERVE_OFFER), 2 * f, 0, 0, 1},
		{"vic", Reserve(env, 2) + 0*f, 1, acctPreTrust, 1000, "", uint64(jtx.XRP(1)) + 2*f, 1, 0, 1},
		{"wes", Reserve(env, 2) + 1*f, 0, acctPreTrust, 1000, "", 2 * f, 0, 1, 2},
		{"xan", Reserve(env, 2) + 1*f, 1, acctPreTrust, 1000, "", uint64(jtx.XRP(1)) + 2*f, 1, 1, 2},
	}

	for _, tt := range tests {
		t.Run(tt.account, func(t *testing.T) {
			acct := jtx.NewAccount(tt.account)
			env.FundAmount(acct, tt.fundXrp)
			env.Close()

			// Make sure gateway has no current offers
			RequireOfferCount(t, env, gw, 0)

			// Gateway optionally creates an offer that would be crossed
			gwOfferSeq := env.Seq(gw)
			if tt.bookAmount > 0 {
				result := env.Submit(OfferCreate(gw, jtx.XRPTxAmountFromXRP(float64(tt.bookAmount)), USD(float64(tt.bookAmount))).Build())
				jtx.RequireTxSuccess(t, result)
			}
			env.Close()
			gwOfferSeq = env.Seq(gw) - 1

			// Optionally pre-establish a trust line
			if tt.preTrust == gwPreTrust {
				env.Trust(gw, jtx.IssuedCurrency(acct, "USD", 1))
			}
			env.Close()

			if tt.preTrust == acctPreTrust {
				env.Trust(acct, USD(1))
			}
			env.Close()

			// Account creates an offer - the heart of the test
			acctOfferSeq := env.Seq(acct)

			// Defer cleanup BEFORE assertions to prevent cascading failures.
			// If assertions fail (FailNow), the test goroutine exits but
			// defers still run, ensuring offers are cleaned up for the next test.
			defer func() {
				env.Submit(OfferCancel(acct, acctOfferSeq).Build())
				env.Submit(OfferCancel(gw, gwOfferSeq).Build())
				env.Close()
			}()

			result := env.Submit(OfferCreate(acct, USD(float64(tt.offerAmt)), jtx.XRPTxAmountFromXRP(float64(tt.offerAmt))).Build())
			if tt.tec == "" {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TxResultCode(tt.tec))
			}
			env.Close()

			// Verify balances
			if tt.balanceUsd != 0 {
				jtx.RequireIOUBalance(t, env, acct, gw, "USD", tt.balanceUsd)
			}
			jtx.RequireBalance(t, env, acct, tt.fundXrp-tt.spentXrp)
			RequireOfferCount(t, env, acct, uint32(tt.offers))
			jtx.RequireOwnerCount(t, env, acct, uint32(tt.owners))

			// Verify remaining offer amounts if any
			acctOffers := OffersOnAccount(env, acct)
			require.Equal(t, tt.offers, len(acctOffers))
			if len(acctOffers) > 0 && tt.offers > 0 {
				leftover := tt.offerAmt - tt.bookAmount
				require.True(t, amountsEqual(acctOffers[0].TakerGets, jtx.XRPTxAmountFromXRP(float64(leftover))),
					"Expected TakerGets = XRP(%d), got %v", leftover, acctOffers[0].TakerGets)
				require.True(t, amountsEqual(acctOffers[0].TakerPays, USD(float64(leftover))),
					"Expected TakerPays = USD(%d), got %v", leftover, acctOffers[0].TakerPays)
			}
		})
	}
}

// TestOffer_CrossWithXRP tests offer crossing with XRP in normal and reverse order.
// Reference: rippled Offer_test.cpp testOfferCrossWithXRP (lines 1503-1556)
func TestOffer_CrossWithXRP(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name+"/Normal", func(t *testing.T) {
			testOfferCrossWithXRP(t, false, fs.disabled)
		})
		t.Run(fs.name+"/Reverse", func(t *testing.T) {
			testOfferCrossWithXRP(t, true, fs.disabled)
		})
	}
}

func testOfferCrossWithXRP(t *testing.T, reverseOrder bool, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, jtx.USD(gw, 1000))
	env.Trust(bob, jtx.USD(gw, 1000))

	result := env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 500)).Build())
	jtx.RequireTxSuccess(t, result)

	if reverseOrder {
		result = env.Submit(OfferCreate(bob, jtx.USD(gw, 1), jtx.XRPTxAmountFromXRP(4000)).Build())
		jtx.RequireTxSuccess(t, result)
	}

	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(150000), jtx.USD(gw, 50)).Build())
	jtx.RequireTxSuccess(t, result)

	if !reverseOrder {
		result = env.Submit(OfferCreate(bob, jtx.USD(gw, 1), jtx.XRPTxAmountFromXRP(4000)).Build())
		jtx.RequireTxSuccess(t, result)
	}

	// Verify bob got 1 USD
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1)

	// Bob's XRP depends on order
	if reverseOrder {
		jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(10000-4000))-f*2)
	} else {
		jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(10000-3000))-f*2)
	}

	// Alice gave 1 USD
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 499)
	if reverseOrder {
		jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(10000+4000))-f*2)
	} else {
		jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(10000+3000))-f*2)
	}
}

// TestOffer_CrossWithLimitOverride tests offer crossing where bob has no trust
// line but gets USD via crossing.
// Reference: rippled Offer_test.cpp testOfferCrossWithLimitOverride (lines 1558-1597)
func TestOffer_CrossWithLimitOverride(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferCrossWithLimitOverride(t, fs.disabled)
		})
	}
}

func testOfferCrossWithLimitOverride(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000)))
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, jtx.USD(gw, 1000))

	result := env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 500)).Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(150000), jtx.USD(gw, 50)).Build())
	jtx.RequireTxSuccess(t, result)

	// Bob has no trust line but crosses via offer
	result = env.Submit(OfferCreate(bob, jtx.USD(gw, 1), jtx.XRPTxAmountFromXRP(3000)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1)
	jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(100000-3000))-f*1)

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 499)
	jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(100000+3000))-f*2)
}
