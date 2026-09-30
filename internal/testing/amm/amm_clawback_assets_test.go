// Behavioral vectors from rippled's AMMClawback_test.cpp.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAMMClawback_SameIssuerAssets(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1000000)))
	env.Close()

	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw issues USD and EUR (both from same issuer)
	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 10000)
	env.Trust(env.Bob, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Bob, "USD", 9000)
	env.Trust(env.Carol, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Carol, "USD", 8000)
	env.Close()

	env.Trust(env.Alice, env.GW, "EUR", 100000)
	env.PayIOU(env.GW, env.Alice, "EUR", 10000)
	env.Trust(env.Bob, env.GW, "EUR", 100000)
	env.PayIOU(env.GW, env.Bob, "EUR", 9000)
	env.Trust(env.Carol, env.GW, "EUR", 100000)
	env.PayIOU(env.GW, env.Carol, "EUR", 8000)
	env.Close()

	// Alice creates AMM pool of EUR(2000)/USD(8000)
	createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "EUR", 2000), amm.IOUAmount(env.GW, "USD", 8000)).Build()
	result = env.Submit(createTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob deposits USD(4000) + EUR(1000)
	depositTx := amm.AMMDeposit(env.Bob, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 4000)).
		Amount2(amm.IOUAmount(env.GW, "EUR", 1000)).
		TwoAsset().
		Build()
	result = env.Submit(depositTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Carol deposits USD(2000.25) + EUR(500)
	// With fixAMMv1_3 upward rounding, the exact USD(2000) amount causes the
	// equalDepositLimit check to fail (rounding makes deposit exceed limit).
	// rippled's test uses USD(2000.25) with fixAMMv1_3 enabled.
	// Reference: rippled AMMClawback_test.cpp line 1375-1377
	depositTx = amm.AMMDeposit(env.Carol, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 2000.25)).
		Amount2(amm.IOUAmount(env.GW, "EUR", 500)).
		TwoAsset().
		Build()
	result = env.Submit(depositTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw clawback 1000 USD from carol (without tfClawTwoAssets)
	// The proportional EUR should be returned to carol
	clawbackTx := amm.AMMClawback(env.GW, env.Carol.Address, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 1000)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// rippled expects: carol EUR = 7750 (8000 - 500 + 250 returned proportionally)
	requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "EUR", 7750)

	// gw clawback 1000 USD from bob WITH tfClawTwoAssets
	// EUR is NOT returned to bob (both assets clawed back)
	clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 1000)).
		ClawTwoAssets().
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// rippled expects: bob EUR = 8000 (no EUR returned because tfClawTwoAssets)
	requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "EUR", 8000)

	// gw clawback all USD from alice with tfClawTwoAssets
	clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).
		ClawTwoAssets().
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// rippled expects: alice USD = 2000, alice EUR = 8000 (no EUR returned)
	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)
	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "EUR", 8000)
}

func TestAMMClawback_SameCurrency(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	gw2 := jtx.NewAccount("gw2")

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000000)))
	env.Close()

	// Both gateways set asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(accountset.AccountSet(gw2).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	gwUSD := env.USD // gw["USD"]
	gw2USD := tx.Asset{Currency: "USD", Issuer: gw2.Address}

	// gw issues gw["USD"] to alice(8000) and bob(7000)
	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 8000)
	env.Trust(env.Bob, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Bob, "USD", 7000)
	env.Close()

	// gw2 issues gw2["USD"] to alice(6000) and bob(5000)
	env.Trust(env.Alice, gw2, "USD", 100000)
	env.PayIOU(gw2, env.Alice, "USD", 6000)
	env.Trust(env.Bob, gw2, "USD", 100000)
	env.PayIOU(gw2, env.Bob, "USD", 5000)
	env.Close()

	// Alice creates AMM pool of gw["USD"](1000) / gw2["USD"](1500)
	createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 1000), amm.IOUAmount(gw2, "USD", 1500)).Build()
	result = env.Submit(createTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob deposits gw["USD"](2000) + gw2["USD"](3000)
	depositTx := amm.AMMDeposit(env.Bob, gwUSD, gw2USD).
		Amount(amm.IOUAmount(env.GW, "USD", 2000)).
		Amount2(amm.IOUAmount(gw2, "USD", 3000)).
		TwoAsset().
		Build()
	result = env.Submit(depositTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Issuer does not match with asset: gw trying to clawback gw2["USD"] => temMALFORMED
	clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, gw2USD, gwUSD).
		Amount(amm.IOUAmount(gw2, "USD", 500)).
		Build()
	result = env.Submit(clawbackTx)
	amm.ExpectTER(t, result, ter.TemMALFORMED.String())

	// gw2 clawback 500 gw2["USD"] from alice
	clawbackTx = amm.AMMClawback(gw2, env.Alice.Address, gw2USD, gwUSD).
		Amount(amm.IOUAmount(gw2, "USD", 500)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw clawback all gw["USD"] from bob
	clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, gwUSD, gw2USD).Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// rippled expects: bob gw["USD"] = 5000 (7000 - 2000 deposited), bob gw2["USD"] = 5000 (2000 + 3000 returned)
	requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 5000)
	requireAMMIOUBalance(t, env.TestEnv, env.Bob, gw2, "USD", 5000)
}

func TestAMMClawback_IssuesEachOther(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	gw2 := jtx.NewAccount("gw2")

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
	env.Close()

	// Both gateways set asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(accountset.AccountSet(gw2).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

	// gw issues USD to gw2(5000) and alice(5000)
	env.Trust(gw2, env.GW, "USD", 100000)
	env.PayIOU(env.GW, gw2, "USD", 5000)
	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 5000)
	env.Close()

	// gw2 issues EUR to gw(6000) and alice(6000)
	env.Trust(env.GW, gw2, "EUR", 100000)
	env.PayIOU(gw2, env.GW, "EUR", 6000)
	env.Trust(env.Alice, gw2, "EUR", 100000)
	env.PayIOU(gw2, env.Alice, "EUR", 6000)
	env.Close()

	// gw creates AMM pool of USD(1000)/EUR(2000)
	// Note: gw is the issuer of USD, so USD(1000) is issued directly.
	// For EUR(2000), gw needs to have EUR from gw2 (which it does: 6000).
	createTx := amm.AMMCreate(env.GW, amm.IOUAmount(env.GW, "USD", 1000), amm.IOUAmount(gw2, "EUR", 2000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// gw2 deposits USD(2000) + EUR(4000)
	// gw2 is the issuer of EUR — issuer deposits issue from thin air.
	depositTx := amm.AMMDeposit(gw2, env.USD, EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 2000)).
		Amount2(amm.IOUAmount(gw2, "EUR", 4000)).
		TwoAsset().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(depositTx))
	env.Close()

	// alice deposits USD(3000) + EUR(6000)
	depositTx = amm.AMMDeposit(env.Alice, env.USD, EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 3000)).
		Amount2(amm.IOUAmount(gw2, "EUR", 6000)).
		TwoAsset().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(depositTx))
	env.Close()

	// gw claws back 1000 USD from gw2
	clawbackTx := amm.AMMClawback(env.GW, gw2.Address, env.USD, EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 1000)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)
	requireAMMIOUBalance(t, env.TestEnv, env.GW, gw2, "EUR", 4000)
	requireAMMIOUBalance(t, env.TestEnv, gw2, env.GW, "USD", 3000)

	// gw2 claws back 1000 EUR from gw
	clawbackTx = amm.AMMClawback(gw2, env.GW.Address, EUR, env.USD).
		Amount(amm.IOUAmount(gw2, "EUR", 1000)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw2 claws back 4000 EUR from alice
	clawbackTx = amm.AMMClawback(gw2, env.Alice.Address, EUR, env.USD).
		Amount(amm.IOUAmount(gw2, "EUR", 4000)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 4000)
}

func TestAMMClawback_NotHoldingLPToken(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
	env.Close()

	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 5000)
	env.Close()

	// gw creates AMM pool of USD(1000)/XRP(2000) -- Alice did NOT deposit
	createTx := amm.AMMCreate(env.GW, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(2000)).Build()
	result = env.Submit(createTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice did not deposit, so the issuer cannot claw back an LP balance that
	// does not exist for her.
	clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
		Amount(amm.IOUAmount(env.GW, "USD", 1000)).
		Build()
	result = env.Submit(clawbackTx)
	amm.ExpectTER(t, result, ter.TecAMM_BALANCE.String())
}

func TestAMMClawback_AssetFrozen(t *testing.T) {
	// Sub-test 1: Individually frozen USD trust line
	t.Run("IndividualFreezeOneAsset", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		gw2 := jtx.NewAccount("gw2")

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 3000)
		env.Close()
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 3000)
		env.Close()

		// Alice creates AMM pool of EUR(1000)/USD(2000)
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 1000), amm.IOUAmount(env.GW, "USD", 2000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze gw-alice USD trust line
		env.FreezeTrustLine(env.GW, env.Alice, "USD")
		env.Close()

		// gw clawback 1000 USD -- should succeed despite freeze
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 2500)

		// gw clawback another 1000 USD -- AMM gets deleted
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 3000)
		require.True(t, ammIsDeleted(t, env, env.USD, EUR))
	})

	// Sub-test 2: Both trust lines frozen
	t.Run("IndividualFreezeBothAssets", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		gw2 := jtx.NewAccount("gw2")

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 3000)
		env.Close()
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 3000)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 1000), amm.IOUAmount(env.GW, "USD", 2000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Freeze both trust lines
		env.FreezeTrustLine(env.GW, env.Alice, "USD")
		env.FreezeTrustLine(gw2, env.Alice, "EUR")
		env.Close()

		// gw clawback 1000 USD -- should succeed despite both frozen
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
	})

	// Sub-test 3: Global freeze
	t.Run("GlobalFreeze", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		gw2 := jtx.NewAccount("gw2")

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 3000)
		env.Close()
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 3000)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 1000), amm.IOUAmount(env.GW, "USD", 2000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Global freeze gw
		result = env.Submit(accountset.AccountSet(env.GW).GlobalFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 1000 USD -- should succeed despite global freeze
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
	})

	// Sub-test 4: Same issuer assets with global freeze and tfClawTwoAssets
	t.Run("SameIssuerGlobalFreeze", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1000000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 10000)
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Bob, "USD", 9000)
		env.Trust(env.Carol, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Carol, "USD", 8000)
		env.Close()

		env.Trust(env.Alice, env.GW, "EUR", 100000)
		env.PayIOU(env.GW, env.Alice, "EUR", 10000)
		env.Trust(env.Bob, env.GW, "EUR", 100000)
		env.PayIOU(env.GW, env.Bob, "EUR", 9000)
		env.Trust(env.Carol, env.GW, "EUR", 100000)
		env.PayIOU(env.GW, env.Carol, "EUR", 8000)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "EUR", 2000), amm.IOUAmount(env.GW, "USD", 8000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob and Carol deposit
		depositTx := amm.AMMDeposit(env.Bob, env.USD, env.EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 4000)).
			Amount2(amm.IOUAmount(env.GW, "EUR", 1000)).
			TwoAsset().Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// With fixAMMv1_3, use USD(2000.25) — matching rippled's test
		// Reference: rippled AMMClawback_test.cpp line 1975-1978
		depositTx = amm.AMMDeposit(env.Carol, env.USD, env.EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 2000.25)).
			Amount2(amm.IOUAmount(env.GW, "EUR", 500)).
			TwoAsset().Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Global freeze
		result = env.Submit(accountset.AccountSet(env.GW).GlobalFreeze().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 1000 USD from carol -- succeeds despite global freeze
		clawbackTx := amm.AMMClawback(env.GW, env.Carol.Address, env.USD, env.EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 1000 USD from bob with tfClawTwoAssets
		clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, env.USD, env.EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			ClawTwoAssets().
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback all from alice with tfClawTwoAssets
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).
			ClawTwoAssets().
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "EUR", 8000)
	})
}

func TestClawback_AMMAccountHolder(t *testing.T) {
	run := func(t *testing.T, singleAssetVault bool, wantCode string) {
		env := amm.NewAMMTestEnv(t)
		if !singleAssetVault {
			env.DisableFeature("SingleAssetVault")
		}
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000)))
		env.Close()

		// Issuer enables clawback before issuing into the AMM.
		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
		env.Close()

		// A clawback-enabled issuer may create an AMM only with featureAMMClawback,
		// which PresetAllSupported enables by default.
		createTx := amm.AMMCreate(env.GW, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := env.ReadAMMAccount(amm.XRP(), env.USD)
		require.NotNil(t, ammAcc, "AMM pseudo-account must exist")

		// Setting the Amount's issuer subfield to the AMM account makes the AMM
		// pseudo-account the clawback holder.
		clawTx := clawback.Claw(env.GW, ammAcc, "USD", 10).Build()
		jtx.RequireTxFail(t, env.Submit(clawTx), wantCode)
	}

	t.Run("AMMAccount", func(t *testing.T) {
		run(t, false, "tecAMM_ACCOUNT")
	})
	t.Run("PseudoAccount_SingleAssetVault", func(t *testing.T) {
		run(t, true, "tecPSEUDO_ACCOUNT")
	})
}
