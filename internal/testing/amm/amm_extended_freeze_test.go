// Behavioral vectors from rippled's AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestAMMExtended_RippleStateFreeze(t *testing.T) {
	t.Run("FrozenCannotSellViaOffer", func(t *testing.T) {
		// When a trust line is frozen, the holder cannot sell that asset
		// via offers (should get tecUNFUNDED_OFFER).
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol tries to sell USD via offer — should fail
		offerTx := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(offerTx)
		amm.ExpectTER(t, result, "tecUNFUNDED_OFFER")
	})

	t.Run("FrozenCanReceivePayment", func(t *testing.T) {
		// A frozen trust line should still allow receiving payments.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// GW pays USD to frozen Carol — should succeed (receiving is allowed)
		payTx := payment.PayIssued(env.GW, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("FrozenCannotMakePayment", func(t *testing.T) {
		// A frozen trust line blocks the holder from making payments from that line.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 10000)
		env.Close()

		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol tries to pay Bob USD — should fail (sending from frozen line)
		payTx := payment.PayIssued(env.Carol, env.Bob, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_DRY")
	})

	t.Run("UnfreezeRestoresAbility", func(t *testing.T) {
		// After unfreezing, the account can transact normally.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()

		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()
		env.UnfreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol should be able to pay Bob
		payTx := payment.PayIssued(env.Carol, env.Bob, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_GlobalFreeze(t *testing.T) {
	t.Run("GlobalFreezeBlocksAMMCreation", func(t *testing.T) {
		// Creating an AMM with a globally frozen asset should fail.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Alice tries to create AMM with frozen USD
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, ter.TecFROZEN.String())
	})

	t.Run("GlobalFreezeBlocksViaRippling", func(t *testing.T) {
		// Global freeze should block via-rippling payments.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 10000)
		env.Close()

		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Alice tries to pay Bob USD via rippling
		payTx := payment.PayIssued(env.Carol, env.Bob, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_DRY")
	})

	t.Run("DirectIssueStillWorks", func(t *testing.T) {
		// Gateway can still issue directly even with global freeze.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// GW can still issue USD to Carol directly
		payTx := payment.PayIssued(env.GW, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("DirectRedemptionStillWorks", func(t *testing.T) {
		// Direct redemptions (paying back to issuer) still work under global freeze.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Carol pays USD back to GW (redemption)
		payTx := payment.PayIssued(env.Carol, env.GW, amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_EnforceNoRipple(t *testing.T) {
	t.Run("NoRippleBlocksAMMPath", func(t *testing.T) {
		// bob has NoRipple on trust lines, blocks rippling USD1->USD2
		env := amm.NewAMMTestEnv(t)
		dan := jtx.NewAccount("dan")
		gw1 := jtx.NewAccount("gw1")
		gw2 := jtx.NewAccount("gw2")

		for _, acc := range []*jtx.Account{env.Alice, env.Bob, env.Carol, dan, gw1, gw2} {
			env.TestEnv.FundAmount(acc, uint64(jtx.XRP(20000)))
		}
		env.Close()

		usd1Amt := amm.IOUAmount(gw1, "USD", 20000)
		usd2Amt := amm.IOUAmount(gw2, "USD", 1000)
		for _, acc := range []*jtx.Account{env.Alice, env.Carol, dan} {
			tsTx := trustset.TrustSet(acc, usd1Amt).Build()
			jtx.RequireTxSuccess(t, env.Submit(tsTx))
			tsTx2 := trustset.TrustSet(acc, usd2Amt).Build()
			jtx.RequireTxSuccess(t, env.Submit(tsTx2))
		}
		tsBob1 := trustset.TrustSet(env.Bob, amm.IOUAmount(gw1, "USD", 1000)).NoRipple().Build()
		jtx.RequireTxSuccess(t, env.Submit(tsBob1))
		tsBob2 := trustset.TrustSet(env.Bob, amm.IOUAmount(gw2, "USD", 1000)).NoRipple().Build()
		jtx.RequireTxSuccess(t, env.Submit(tsBob2))
		env.Close()

		pay1 := payment.PayIssued(gw1, dan, amm.IOUAmount(gw1, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay1))
		pay2 := payment.PayIssued(gw1, env.Bob, amm.IOUAmount(gw1, "USD", 50)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay2))
		pay3 := payment.PayIssued(gw2, env.Bob, amm.IOUAmount(gw2, "USD", 50)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay3))
		env.Close()

		createTx := amm.AMMCreate(dan,
			amm.XRPAmount(10000),
			amm.IOUAmount(gw1, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice pays carol USD2(50), path(~USD1, bob), sendmax XRP(50)
		// Should fail: bob has NoRipple → tecPATH_DRY
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(gw2, "USD", 50)).
			SendMax(amm.XRPAmount(50)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: gw1.Address},
					{Account: env.Bob.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_DRY")
	})

	t.Run("DefaultFlagsAllowAMMPath", func(t *testing.T) {
		// Same as above but bob does NOT have NoRipple
		env := amm.NewAMMTestEnv(t)
		dan := jtx.NewAccount("dan")
		gw1 := jtx.NewAccount("gw1")
		gw2 := jtx.NewAccount("gw2")

		for _, acc := range []*jtx.Account{env.Alice, env.Bob, env.Carol, dan, gw1, gw2} {
			env.TestEnv.FundAmount(acc, uint64(jtx.XRP(20000)))
		}
		env.Close()

		// Trust lines — no NoRipple for bob
		for _, acc := range []*jtx.Account{env.Alice, env.Bob, env.Carol, dan} {
			tsTx := trustset.TrustSet(acc, amm.IOUAmount(gw1, "USD", 20000)).Build()
			jtx.RequireTxSuccess(t, env.Submit(tsTx))
			tsTx2 := trustset.TrustSet(acc, amm.IOUAmount(gw2, "USD", 1000)).Build()
			jtx.RequireTxSuccess(t, env.Submit(tsTx2))
		}
		env.Close()

		pay1 := payment.PayIssued(gw1, dan, amm.IOUAmount(gw1, "USD", 10050)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay1))
		pay2 := payment.PayIssued(gw1, env.Bob, amm.IOUAmount(gw1, "USD", 50)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay2))
		pay3 := payment.PayIssued(gw2, env.Bob, amm.IOUAmount(gw2, "USD", 50)).Build()
		jtx.RequireTxSuccess(t, env.Submit(pay3))
		env.Close()

		createTx := amm.AMMCreate(dan,
			amm.XRPAmount(10000),
			amm.IOUAmount(gw1, "USD", 10050)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice pays carol USD2(50), path(~USD1, bob), sendmax XRP(50)
		// Should succeed: bob allows rippling
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(gw2, "USD", 50)).
			SendMax(amm.XRPAmount(50)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: gw1.Address},
					{Account: env.Bob.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})
}
