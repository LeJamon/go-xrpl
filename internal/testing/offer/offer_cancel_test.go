package offer

// Offer cancellation tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testCanceledOffer (lines 135-214)
//   - testOfferAcceptThenCancel (lines 1599-1621)
//   - testOfferCancelPastAndFuture (lines 1623-1647)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_CanceledOffer tests offer replacement via OfferSequence and OfferCancel.
// Reference: rippled Offer_test.cpp testCanceledOffer (lines 135-214)
func TestOffer_CanceledOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testCanceledOffer(t, fs.disabled)
		})
	}
}

func testCanceledOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.Trust(alice, USD(100))
	env.Close()

	result := env.Submit(payment.PayIssued(gw, alice, USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	offer1Seq := env.Seq(alice)

	// Create first offer
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(500), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, alice, 1)
	env.Close()

	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(500), USD(100))

	// Cancel the offer above and replace it with a new offer
	offer2Seq := env.Seq(alice)

	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(300), USD(100)).
		OfferSequence(offer1Seq).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, alice, 1)
	env.Close()

	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(300), USD(100))
	RequireNoOffer(t, env, alice, jtx.XRPTxAmountFromXRP(500), USD(100))

	// Test canceling non-existent offer.
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(400), USD(200)).
		OfferSequence(offer1Seq).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, alice, 2)
	env.Close()

	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(300), USD(100))
	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(400), USD(200))

	// Test cancellation now with OfferCancel tx
	offer4Seq := env.Seq(alice)
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(222), USD(111)).Build())
	jtx.RequireTxSuccess(t, result)
	RequireOfferCount(t, env, alice, 3)
	env.Close()

	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(222), USD(111))

	result = env.Submit(OfferCancel(alice, offer4Seq).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	require.Equal(t, offer4Seq+2, env.Seq(alice))
	RequireNoOffer(t, env, alice, jtx.XRPTxAmountFromXRP(222), USD(111))

	// Create an offer that both fails with a tecEXPIRED code and removes
	// an offer. Show that the attempt to remove the offer fails.
	RequireOfferCount(t, env, alice, 2)

	// featureDepositPreauth changes the return code on an expired Offer.
	featPreauth := featureEnabled(disabledFeatures, "DepositPreauth")
	expiredOffer := OfferCreate(alice, jtx.XRPTxAmountFromXRP(5), USD(2)).
		Expiration(LastClose(env)).
		OfferSequence(offer2Seq).Build()
	result = env.Submit(expiredOffer)
	if featPreauth {
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
	} else {
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	RequireOfferCount(t, env, alice, 2)
	RequireIsOffer(t, env, alice, jtx.XRPTxAmountFromXRP(300), USD(100)) // offer2
	RequireNoOffer(t, env, alice, jtx.XRPTxAmountFromXRP(5), USD(2))    // expired
}

// TestOffer_AcceptThenCancel tests basic offer creation followed by cancellation.
// Reference: rippled Offer_test.cpp testOfferAcceptThenCancel (lines 1599-1621)
func TestOffer_AcceptThenCancel(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferAcceptThenCancel(t, fs.disabled)
		})
	}
}

func testOfferAcceptThenCancel(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	master := jtx.MasterAccount()
	USD := func(amount float64) tx.Amount { return jtx.USD(master, amount) }

	nextOfferSeq := env.Seq(master)
	result := env.Submit(OfferCreate(master, jtx.XRPTxAmountFromXRP(500), USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(OfferCancel(master, nextOfferSeq).Build())
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, nextOfferSeq+2, env.Seq(master))

	// ledger_accept, call twice and verify no odd behavior
	env.Close()
	env.Close()
	require.Equal(t, nextOfferSeq+2, env.Seq(master))
}

// TestOffer_CancelPastAndFuture tests OfferCancel with past, current, and future sequence numbers.
// Reference: rippled Offer_test.cpp testOfferCancelPastAndFuture (lines 1623-1647)
func TestOffer_CancelPastAndFuture(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferCancelPastAndFuture(t, fs.disabled)
		})
	}
}

func testOfferCancelPastAndFuture(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	nextOfferSeq := env.Seq(master)
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Cancel past sequence - should succeed (offer may or may not exist)
	result := env.Submit(OfferCancel(master, nextOfferSeq).Build())
	jtx.RequireTxSuccess(t, result)

	// Cancel current sequence - should fail
	result = env.Submit(OfferCancel(master, env.Seq(master)).Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_SEQUENCE)

	// Cancel future sequence - should fail
	result = env.Submit(OfferCancel(master, env.Seq(master)+1).Build())
	jtx.RequireTxFail(t, result, jtx.TemBAD_SEQUENCE)

	env.Close()
}
