// Behavioral vectors from rippled's AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

func TestAMMExtended_DeliverMin(t *testing.T) {
	t.Run("DeliverMinEqualsAmount_NoPartialPay", func(t *testing.T) {
		// DeliverMin equal to amount without partial payment flag → temBAD_AMOUNT
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 10)).
			DeliverMin(amm.IOUAmount(env.GW, "USD", 10)).
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_AMOUNT")
	})

	t.Run("DeliverMinNegative_Rejected", func(t *testing.T) {
		// Negative DeliverMin with partial payment → temBAD_AMOUNT
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 10)).
			DeliverMin(amm.IOUAmount(env.GW, "USD", -1)).
			PartialPayment().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_AMOUNT")
	})

	t.Run("DeliverMinWrongCurrency_Rejected", func(t *testing.T) {
		// DeliverMin with wrong currency → temBAD_AMOUNT
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 10)).
			DeliverMin(amm.XRPAmount(7)). // wrong currency
			PartialPayment().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_AMOUNT")
	})

	t.Run("DeliverMinExceedsAmount_Rejected", func(t *testing.T) {
		// DeliverMin > amount → temBAD_AMOUNT
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 10)).
			DeliverMin(amm.IOUAmount(env.GW, "USD", 20)).
			PartialPayment().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_AMOUNT")
	})
}

func TestAMMExtended_CrossingLimits(t *testing.T) {
	t.Run("StepLimit_ManyOffers", func(t *testing.T) {
		// When there are many offers, the step limit controls how many are processed.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob creates multiple offers (simulating a book with many entries)
		for i := range 20 {
			offerTx := offerbuild.OfferCreate(env.Bob, amm.IOUAmount(env.GW, "USD", 10), amm.XRPAmount(10)).Build()
			result := env.Submit(offerTx)
			if !result.Success {
				t.Fatalf("offer %d: expected tesSUCCESS, got %s (%s)", i, result.Code, result.Message)
			}
		}
		env.Close()

		// Carol creates a crossing offer that should consume some of Bob's offers
		offerTx := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(offerTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_OfferCrossWithXRP(t *testing.T) {
	t.Run("BasicXRPOfferCross", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob creates offer to sell USD for XRP
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// Carol creates a crossing offer to buy USD with XRP
		carolBefore := env.Balance(env.Carol)
		crossTx := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 500), amm.XRPAmount(500)).Build()
		result := env.Submit(crossTx)
		carolAfter := env.Balance(env.Carol)
		jtx.RequireTxSuccess(t, result)
		require.Equal(t, uint64(jtx.XRP(500))+env.BaseFee(), carolBefore-carolAfter)
	})
}

func TestAMMExtended_CurrencyConversion(t *testing.T) {
	t.Run("EntireConversion", func(t *testing.T) {
		// Convert entire amount in single currency pair through AMM
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob creates offer selling 1000 USD for 1000 XRP
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// Carol consumes the entire offer
		crossTx := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(1000)).Build()
		result := env.Submit(crossTx)
		jtx.RequireTxSuccess(t, result)
		require.Empty(t, env.AccountOffers(env.Bob))
	})

	t.Run("InPartsConversion", func(t *testing.T) {
		// Convert in multiple parts
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob creates offer
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// Carol consumes half
		crossTx1 := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 500), amm.XRPAmount(500)).Build()
		result := env.Submit(crossTx1)
		jtx.RequireTxSuccess(t, result)
		require.Len(t, env.AccountOffers(env.Bob), 1)
	})
}

func TestAMMExtended_OfferWithTransferRate(t *testing.T) {
	t.Run("TransferRateOnOffer", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Set transfer rate on gateway (1.25 = 125%)
		env.SetTransferRate(env.GW, 1250000000)
		env.Close()

		// Create AMM (creator is charged transfer fee on IOU)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob creates offer
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		result = env.Submit(offerTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_FillModes(t *testing.T) {
	t.Run("FillOrKill_Succeeds", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob creates a large offer
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(5000), amm.IOUAmount(env.GW, "USD", 5000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// Carol creates a FillOrKill offer that should fully fill
		fokTx := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 100), amm.XRPAmount(100)).
			FillOrKill().Build()
		result := env.Submit(fokTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("FillOrKill_Killed", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM with small pool
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol creates FillOrKill for more than available - should be killed
		fokTx := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 10000), amm.XRPAmount(10000)).
			FillOrKill().Build()
		result := env.Submit(fokTx)
		amm.ExpectTER(t, result, "tecKILLED")
	})
}

func TestAMMExtended_PayStrand(t *testing.T) {
	t.Run("CrossCurrencyStartWithXRP", func(t *testing.T) {
		// Cross-currency payment starting with XRP, routing through AMM
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()

		// Create AMM with XRP/USD
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob sends XRP, Carol receives USD (cross-currency via AMM)
		payTx := payment.PayIssued(env.Bob, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
			SendMax(amm.XRPAmount(200)).
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("CrossCurrencyEndWithXRP", func(t *testing.T) {
		// Cross-currency payment ending with XRP, routing through AMM
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Create AMM with XRP/USD
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob sends USD, Carol receives XRP (cross-currency via AMM)
		payTx := payment.Pay(env.Bob, env.Carol, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 200)).
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_RmFundedOffer(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")

	// Fund accounts with XRP(10000), USD(200000), BTC(2000)
	for _, acc := range []*jtx.Account{env.GW, env.Alice, env.Bob, env.Carol} {
		env.TestEnv.FundAmount(acc, uint64(jtx.XRP(10000)))
	}
	env.Close()

	// Trust lines for USD and BTC
	for _, acc := range []*jtx.Account{env.Alice, env.Bob, env.Carol} {
		env.Trust(acc, env.GW, "USD", 300000)
		env.Trust(acc, env.GW, "BTC", 3000)
	}
	env.Close()

	// Fund IOUs
	for _, acc := range []*jtx.Account{env.Alice, env.Bob, env.Carol} {
		env.PayIOU(env.GW, acc, "USD", 200000)
		env.PayIOU(env.GW, acc, "BTC", 2000)
	}
	env.Close()

	// Carol creates offers: BTC→XRP (funded, should NOT be removed)
	offer1 := offerbuild.OfferCreate(env.Carol,
		amm.IOUAmount(env.GW, "BTC", 49),
		amm.XRPAmount(49)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offer1))
	offer2 := offerbuild.OfferCreate(env.Carol,
		amm.IOUAmount(env.GW, "BTC", 51),
		amm.XRPAmount(51)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offer2))

	// Carol creates offers for poor quality path: XRP→USD
	offer3 := offerbuild.OfferCreate(env.Carol,
		amm.XRPAmount(50),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offer3))
	offer4 := offerbuild.OfferCreate(env.Carol,
		amm.XRPAmount(50),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offer4))
	env.Close()

	// Carol creates AMM: BTC(1000)/USD(100100) — good quality path
	createTx := amm.AMMCreate(env.Carol,
		amm.IOUAmount(env.GW, "BTC", 1000),
		amm.IOUAmount(env.GW, "USD", 100100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// Alice pays bob USD(100), two paths, sendmax BTC(1000), partial payment
	// Path 1: BTC→XRP→USD (via CLOB offers) — poor quality
	// Path 2: BTC→USD (via AMM) — good quality
	payTx := payment.PayIssued(env.Alice, env.Bob,
		amm.IOUAmount(env.GW, "USD", 100)).
		SendMax(amm.IOUAmount(env.GW, "BTC", 1000)).
		Paths([][]paymenttx.PathStep{
			// Path(XRP, USD): BTC→XRP book, then XRP→USD book
			{
				{Currency: "XRP"},
				{Currency: "USD", Issuer: env.GW.Address},
			},
			// Path(USD): BTC→USD book (through AMM)
			{
				{Currency: "USD", Issuer: env.GW.Address},
			},
		}).
		PartialPayment().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// Bob should have received USD(100) more → 200100
	bobUSD := env.TestEnv.BalanceIOU(env.Bob, "USD", env.GW)
	require.Equal(t, float64(200100), bobUSD)

	// Carol's first BTC→XRP offer should still exist (funded but unused)
	carolOffers := env.AccountOffers(env.Carol)
	foundOffer := false
	for _, o := range carolOffers {
		// XRP amounts have empty currency; BTC is IOU
		if o.TakerGets.IsNative() && o.TakerPays.Currency == "BTC" {
			foundOffer = true
			break
		}
	}
	require.True(t, foundOffer, "Carol's funded BTC/XRP offer should still exist")
}
