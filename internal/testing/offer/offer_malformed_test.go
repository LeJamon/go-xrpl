package offer

// Offer malformed detection tests.
// Reference: rippled/src/test/app/Offer_test.cpp - testMalformed (lines 1067-1143)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	offertx "github.com/LeJamon/goXRPLd/internal/core/tx/offer"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestOffer_Malformed tests validation of malformed offer transactions.
// Reference: rippled Offer_test.cpp testMalformed (lines 1067-1143)
func TestOffer_Malformed(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testMalformed(t, fs.disabled)
		})
	}
}

func testMalformed(t *testing.T, disabledFeatures []string) {
	env := jtx.NewTestEnvBacked(t)
	for _, f := range disabledFeatures {
		env.DisableFeature(f)
	}

	startBalance := uint64(jtx.XRP(1000000))
	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")

	env.FundAmount(gw, startBalance)
	env.FundAmount(alice, startBalance)
	env.Close()

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	// Order that has invalid flags
	offerTx := OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(1000)).
		Flags(offertx.OfferCreateFlagImmediateOrCancel + 1).Build()
	result := env.Submit(offerTx)
	jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	jtx.RequireBalance(t, env, alice, startBalance)
	jtx.RequireOwnerCount(t, env, alice, 0)
	RequireOfferCount(t, env, alice, 0)

	// Order with incompatible flags
	offerTx = OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(1000)).
		Flags(offertx.OfferCreateFlagImmediateOrCancel | offertx.OfferCreateFlagFillOrKill).Build()
	result = env.Submit(offerTx)
	jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	jtx.RequireBalance(t, env, alice, startBalance)
	jtx.RequireOwnerCount(t, env, alice, 0)
	RequireOfferCount(t, env, alice, 0)

	// Sell and buy the same asset
	{
		// Alice tries an XRP to XRP order:
		offerTx = OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), jtx.XRPTxAmountFromXRP(1000)).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_OFFER)
		jtx.RequireOwnerCount(t, env, alice, 0)
		RequireOfferCount(t, env, alice, 0)

		// Alice tries an IOU to IOU order:
		env.Trust(alice, USD(1000))
		result = env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)

		offerTx = OfferCreate(alice, USD(1000), USD(1000)).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemREDUNDANT)
		jtx.RequireOwnerCount(t, env, alice, 1) // just the trust line
		RequireOfferCount(t, env, alice, 0)
	}

	// Offers with negative amounts
	{
		negUSD := jtx.IssuedCurrency(gw, "USD", -1000)
		negXRP := tx.NewXRPAmount(-int64(jtx.XRP(1000)))

		offerTx = OfferCreate(alice, negUSD, jtx.XRPTxAmountFromXRP(1000)).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_OFFER)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)

		offerTx = OfferCreate(alice, USD(1000), negXRP).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_OFFER)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)
	}

	// Offer with a bad expiration
	{
		exp := uint32(0)
		offerTx = OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(1000)).
			Expiration(exp).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_EXPIRATION)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)
	}

	// Offer with a bad offer sequence
	{
		offerTx = OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(1000)).
			OfferSequence(0).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_SEQUENCE)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)
	}

	// Use XRP as a currency code
	{
		// "XRP" is badCurrency() in rippled - cannot be used as IOU currency code
		badCurrencyAmt := jtx.IssuedCurrency(gw, "XRP", 1000)
		offerTx = OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), badCurrencyAmt).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxFail(t, result, jtx.TemBAD_CURRENCY)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)
	}
}
