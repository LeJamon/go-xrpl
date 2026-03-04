package offer

// Offer fee tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testOfferFeesConsumeFunds (lines 2043-2096)
//   - testTransferRateOffer (lines 3078-3384)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_FeesConsumeFunds tests that offer fees consume available funds.
// Reference: rippled Offer_test.cpp testOfferFeesConsumeFunds (lines 2043-2096)
func TestOffer_FeesConsumeFunds(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testOfferFeesConsumeFunds(t, fs.disabled)
		})
	}
}

func testOfferFeesConsumeFunds(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw1 := jtx.NewAccount("gateway_1")
	gw2 := jtx.NewAccount("gateway_2")
	gw3 := jtx.NewAccount("gateway_3")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD1 := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }

	f := env.BaseFee()

	// Reserve: Alice has 3 entries in the ledger, via trust lines
	// Fees: 1 for each trust limit == 3 + 1 for payment == 4
	startingXRP := uint64(jtx.XRP(100)) + Reserve(env, 3) + f*4

	env.FundAmount(gw1, startingXRP)
	env.FundAmount(gw2, startingXRP)
	env.FundAmount(gw3, startingXRP)
	env.FundAmount(alice, startingXRP)
	env.FundAmount(bob, startingXRP)
	env.Close()

	env.Trust(alice, USD1(1000))
	env.Trust(alice, jtx.USD(gw2, 1000))
	env.Trust(alice, jtx.USD(gw3, 1000))
	env.Trust(bob, USD1(1000))
	env.Trust(bob, jtx.USD(gw2, 1000))

	result := env.Submit(payment.PayIssued(gw1, bob, USD1(500)).Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(200), USD1(200)).Build())
	jtx.RequireTxSuccess(t, result)

	// Alice has 350 fees - a reserve of 50 = 250 reserve = 100 available.
	// Ask for more than available to prove reserve works.
	result = env.Submit(OfferCreate(alice, USD1(200), jtx.XRPTxAmountFromXRP(200)).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireIOUBalance(t, env, alice, gw1, "USD", 100)
	jtx.RequireBalance(t, env, alice, Reserve(env, 3))

	jtx.RequireIOUBalance(t, env, bob, gw1, "USD", 400)
}

// TestOffer_TransferRateOffer tests offer behavior with transfer rates.
// Reference: rippled Offer_test.cpp testTransferRateOffer (lines 3078-3384)
func TestOffer_TransferRateOffer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testTransferRateOffer(t, fs.disabled)
		})
	}
}

func testTransferRateOffer(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw1 := jtx.NewAccount("gateway1")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }

	f := env.BaseFee()

	env.FundAmount(gw1, uint64(jtx.XRP(100000)))
	env.Close()

	env.SetTransferRate(gw1, 1250000000) // rate(gw1, 1.25)

	// Section 1: ann and bob with transfer rate
	t.Run("AnnBob", func(t *testing.T) {
		ann := jtx.NewAccount("ann")
		bob := jtx.NewAccount("bob")
		env.FundAmount(ann, uint64(jtx.XRP(100))+Reserve(env, 2)+f*2)
		env.FundAmount(bob, uint64(jtx.XRP(100))+Reserve(env, 2)+f*2)
		env.Close()

		env.Trust(ann, USD(200))
		env.Trust(bob, USD(200))
		env.Close()

		result := env.Submit(payment.PayIssued(gw1, bob, USD(125)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob offers to sell USD(100) for XRP. Due to 25% transfer fee,
		// USD(125) is removed from bob's account.
		result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(1), USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(ann, USD(100), jtx.XRPTxAmountFromXRP(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, ann, gw1, "USD", 100)
		jtx.RequireBalance(t, env, ann, uint64(jtx.XRP(99))+Reserve(env, 2))
		RequireOfferCount(t, env, ann, 0)

		jtx.RequireIOUBalance(t, env, bob, gw1, "USD", 0)
		jtx.RequireBalance(t, env, bob, uint64(jtx.XRP(101))+Reserve(env, 2))
		RequireOfferCount(t, env, bob, 0)
	})

	// Section 2: Reverse order - offer in book sells XRP for USD
	t.Run("CheDeb", func(t *testing.T) {
		che := jtx.NewAccount("che")
		deb := jtx.NewAccount("deb")
		env.FundAmount(che, uint64(jtx.XRP(100))+Reserve(env, 2)+f*2)
		env.FundAmount(deb, uint64(jtx.XRP(100))+Reserve(env, 2)+f*2)
		env.Close()

		env.Trust(che, USD(200))
		env.Trust(deb, USD(200))
		env.Close()

		result := env.Submit(payment.PayIssued(gw1, deb, USD(125)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(che, USD(100), jtx.XRPTxAmountFromXRP(1)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(deb, jtx.XRPTxAmountFromXRP(1), USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, che, gw1, "USD", 100)
		jtx.RequireBalance(t, env, che, uint64(jtx.XRP(99))+Reserve(env, 2))
		RequireOfferCount(t, env, che, 0)

		jtx.RequireIOUBalance(t, env, deb, gw1, "USD", 0)
		jtx.RequireBalance(t, env, deb, uint64(jtx.XRP(101))+Reserve(env, 2))
		RequireOfferCount(t, env, deb, 0)
	})

	// Section 3: Transfer rate affects offer amounts
	t.Run("EveFyn", func(t *testing.T) {
		eve := jtx.NewAccount("eve")
		fyn := jtx.NewAccount("fyn")

		env.FundAmount(eve, uint64(jtx.XRP(20000))+f*2)
		env.FundAmount(fyn, uint64(jtx.XRP(20000))+f*2)
		env.Close()

		env.Trust(eve, USD(1000))
		env.Trust(fyn, USD(1000))
		env.Close()

		result := env.Submit(payment.PayIssued(gw1, eve, USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw1, fyn, USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		eveOfferSeq := env.Seq(eve)
		result = env.Submit(OfferCreate(eve, USD(10), jtx.XRPTxAmountFromXRP(4000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(fyn, jtx.XRPTxAmountFromXRP(2000), USD(5)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireIOUBalance(t, env, eve, gw1, "USD", 105)
		jtx.RequireBalance(t, env, eve, uint64(jtx.XRP(18000)))

		eveOffers := OffersOnAccount(env, eve)
		require.Equal(t, 1, len(eveOffers))
		if len(eveOffers) > 0 {
			require.True(t, amountsEqual(eveOffers[0].TakerGets, jtx.XRPTxAmountFromXRP(2000)),
				"Expected TakerGets = XRP(2000)")
			require.True(t, amountsEqual(eveOffers[0].TakerPays, USD(5)),
				"Expected TakerPays = USD(5)")
		}
		// Cancel eve's remaining offer for later tests
		result = env.Submit(OfferCancel(eve, eveOfferSeq).Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireIOUBalance(t, env, fyn, gw1, "USD", 93.75)
		jtx.RequireBalance(t, env, fyn, uint64(jtx.XRP(22000)))
		RequireOfferCount(t, env, fyn, 0)
	})
}
