// Behavioral vectors from rippled's AMMClawback_test.cpp.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMClawbackTerminalIOUPoolMatrix(t *testing.T) {
	for _, tc := range []struct {
		name             string
		fixAMMv13        bool
		fixClawbackRound bool
	}{
		{name: "Legacy"},
		{name: "AMMv1_3", fixAMMv13: true},
		{name: "ClawbackRounding", fixClawbackRound: true},
		{name: "Both", fixAMMv13: true, fixClawbackRound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			if !tc.fixAMMv13 {
				env.DisableFeature("fixAMMv1_3")
			}
			if !tc.fixClawbackRound {
				env.DisableFeature("fixAMMClawbackRounding")
			}

			gw2 := jtx.NewAccount("gw2")
			env.FundAmount(env.GW, uint64(jtx.XRP(1_000_000)))
			env.FundAmount(gw2, uint64(jtx.XRP(1_000_000)))
			env.FundAmount(env.Alice, uint64(jtx.XRP(1_000_000)))
			env.Close()

			jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
			env.Close()

			eur := tx.Asset{Currency: "EUR", Issuer: gw2.Address}
			env.Trust(env.Alice, env.GW, "USD", 100_000)
			env.PayIOU(env.GW, env.Alice, "USD", 3_000)
			env.Trust(env.Alice, gw2, "EUR", 100_000)
			env.PayIOU(gw2, env.Alice, "EUR", 3_000)
			env.Close()

			jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
				env.Alice,
				amm.IOUAmount(gw2, "EUR", 1_000),
				amm.IOUAmount(env.GW, "USD", 2_000),
			).Build()))
			env.Close()

			ammAccount := amm.AMMAccount(t, env, env.USD, eur)
			lpCurrency := coreAmm.GenerateAMMLPTCurrencyForAssets(env.USD, eur)
			claw := func() jtx.TxResult {
				result := env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, eur).
					Amount(amm.IOUAmount(env.GW, "USD", 1_000)).
					Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				return result
			}

			claw()
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1_000)
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 2_500)

			result := claw()
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1_000)
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 3_000)

			ammKey := coreAmm.ComputeAMMKeylet(env.USD, eur)
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, ammKey)
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, keylet.Account(ammAccount.ID))
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, keylet.OwnerDir(ammAccount.ID))
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, keylet.Line(ammAccount.ID, env.GW.ID, "USD"))
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, keylet.Line(ammAccount.ID, gw2.ID, "EUR"))
			jtx.RequireLedgerEntryNotExists(t, env.TestEnv, keylet.Line(env.Alice.ID, ammAccount.ID, lpCurrency))

			require.NotNil(t, result.Metadata)
			requireDeletedNode(t, result.Metadata, ammKey)
			requireDeletedNode(t, result.Metadata, keylet.Account(ammAccount.ID))
			requireDeletedNode(t, result.Metadata, keylet.Line(ammAccount.ID, env.GW.ID, "USD"))
			requireDeletedNode(t, result.Metadata, keylet.Line(ammAccount.ID, gw2.ID, "EUR"))
			requireDeletedNode(t, result.Metadata, keylet.Line(env.Alice.ID, ammAccount.ID, lpCurrency))
		})
	}
}

func TestAMMClawback_ExceedBalance(t *testing.T) {
	// EUR/USD pool: multiple clawbacks, last one exceeds balance
	t.Run("EUR_USD_Pool", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		gw2 := jtx.NewAccount("gw2")

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.Close()

		// gw sets asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		// gw issues 6000 USD to Alice
		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 6000)
		env.Close()

		// gw2 issues 6000 EUR to Alice
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 6000)
		env.Close()

		// Alice creates AMM pool of EUR(5000)/USD(4000)
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 5000), amm.IOUAmount(env.GW, "USD", 4000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 1000 USD
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// rippled expects: alice USD = 2000 (6000-4000 deposited), EUR = 2250 (1000 + 1250 returned)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)

		// gw clawback 500 USD
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 500)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)

		// gw clawback 1 USD
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 4000 USD (exceeds remaining balance in pool)
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 4000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// rippled expects: USD still 2000, EUR = 6000 (all returned), AMM deleted
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 2000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 6000)
		require.True(t, ammIsDeleted(t, env, env.USD, EUR))
	})

	// USD/XRP pool with multiple depositors
	t.Run("USD_XRP_Pool_MultiDepositors", func(t *testing.T) {
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

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		// gw issues USD to alice(6000) and bob(5000)
		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 6000)
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Bob, "USD", 5000)
		env.Close()

		// gw2 issues EUR to alice(5000) and bob(4000)
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 5000)
		env.Trust(env.Bob, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Bob, "EUR", 4000)
		env.Close()

		// gw creates AMM pool of XRP(2000)/USD(1000)
		createTx := amm.AMMCreate(env.GW, amm.XRPAmount(2000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice deposits USD(1000) + XRP(2000) into XRP/USD AMM
		depositTx := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Amount2(amm.XRPAmount(2000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob deposits USD(1000) + XRP(2000) into XRP/USD AMM
		depositTx = amm.AMMDeposit(env.Bob, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Amount2(amm.XRPAmount(2000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 500 USD from alice in XRP/USD amm
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 500)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// rippled expects: alice USD = 5000, bob USD = 4000
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 5000)
		requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 4000)

		// gw clawback 10 USD from bob in amm
		clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 10)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw2 clawback 200 EUR from alice in EUR/XRP amm2
		// First create EUR/XRP AMM
		createTx2 := amm.AMMCreate(gw2, amm.XRPAmount(3000), amm.IOUAmount(gw2, "EUR", 1000)).Build()
		result = env.Submit(createTx2)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice deposits EUR(1000) + XRP(3000)
		depositTx = amm.AMMDeposit(env.Alice, amm.XRP(), EUR).
			Amount(amm.IOUAmount(gw2, "EUR", 1000)).
			Amount2(amm.XRPAmount(3000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw2 clawback 200 EUR from alice
		clawbackTx = amm.AMMClawback(gw2, env.Alice.Address, EUR, amm.XRP()).
			Amount(amm.IOUAmount(gw2, "EUR", 200)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 4000)

		// Exceed: gw clawback 1000 USD from alice (exceeds remaining in pool)
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Exceed: gw clawback 1000 USD from bob (exceeds remaining in pool)
		clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

func TestAMMClawback_All(t *testing.T) {
	// EUR/USD pool with three depositors, clawback all from each
	t.Run("EUR_USD_Pool_ThreeDepositors", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		gw2 := jtx.NewAccount("gw2")

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1000000)))
		env.Close()

		// Both gateways set asfAllowTrustLineClawback
		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(accountset.AccountSet(gw2).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		// gw issues USD: alice=6000, bob=5000, carol=4000
		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 6000)
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Bob, "USD", 5000)
		env.Trust(env.Carol, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Carol, "USD", 4000)
		env.Close()

		// gw2 issues EUR: alice=6000, bob=5000, carol=4000
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 6000)
		env.Trust(env.Bob, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Bob, "EUR", 5000)
		env.Trust(env.Carol, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Carol, "EUR", 4000)
		env.Close()

		// Alice creates AMM pool of EUR(5000)/USD(4000)
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 5000), amm.IOUAmount(env.GW, "USD", 4000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob deposits USD(2000) + EUR(2500)
		depositTx := amm.AMMDeposit(env.Bob, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 2000)).
			Amount2(amm.IOUAmount(gw2, "EUR", 2500)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Carol deposits USD(1000) + EUR(1250)
		depositTx = amm.AMMDeposit(env.Carol, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Amount2(amm.IOUAmount(gw2, "EUR", 1250)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback ALL bob's USD in amm (no Amount field)
		clawbackTx := amm.AMMClawback(env.GW, env.Bob.Address, env.USD, EUR).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 3000)
		requireAMMAmount(t, ammHolding(t, env, env.Bob, EUR), "4999.999999999999")

		// gw2 clawback ALL carol's EUR in amm
		clawbackTx = amm.AMMClawback(gw2, env.Carol.Address, EUR, env.USD).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 4000)

		// gw2 clawback ALL alice's EUR in amm (should delete AMM)
		clawbackTx = amm.AMMClawback(gw2, env.Alice.Address, EUR, env.USD).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.True(t, ammIsDeleted(t, env, env.USD, EUR))
	})

	// XRP/USD pool: clawback all from alice and bob
	t.Run("XRP_USD_Pool", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 1000000)
		env.PayIOU(env.GW, env.Alice, "USD", 600000)
		env.Trust(env.Bob, env.GW, "USD", 1000000)
		env.PayIOU(env.GW, env.Bob, "USD", 500000)
		env.Close()

		// gw creates AMM pool of XRP(2000)/USD(10000)
		createTx := amm.AMMCreate(env.GW, amm.XRPAmount(2000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice deposits USD(1000) + XRP(200)
		depositTx := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Amount2(amm.XRPAmount(200)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob deposits USD(2000) + XRP(400)
		depositTx = amm.AMMDeposit(env.Bob, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 2000)).
			Amount2(amm.XRPAmount(400)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceXrpBefore := env.TestEnv.Balance(env.Alice)

		// gw clawback all alice's USD in amm (no Amount)
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceXrpAfter := env.TestEnv.Balance(env.Alice)
		require.Equal(t, jtx.XRP(200)-1, int64(aliceXrpAfter)-int64(aliceXrpBefore))

		// gw clawback all bob's USD in amm
		bobXrpBefore := env.TestEnv.Balance(env.Bob)
		clawbackTx = amm.AMMClawback(env.GW, env.Bob.Address, env.USD, amm.XRP()).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bobXrpAfter := env.TestEnv.Balance(env.Bob)
		require.Equal(t, int64(jtx.XRP(400)), int64(bobXrpAfter)-int64(bobXrpBefore))
	})
}

func TestAMMClawback_SingleDepositAndClawback(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000000)))
	env.Close()

	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 1000)
	env.Close()

	// gw creates AMM pool of XRP(100)/USD(400)
	createTx := amm.AMMCreate(env.GW, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 400)).Build()
	result = env.Submit(createTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice deposits USD(400) as single-asset deposit
	depositTx := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
		Amount(amm.IOUAmount(env.GW, "USD", 400)).
		SingleAsset().
		Build()
	result = env.Submit(depositTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	aliceXrpBefore := env.TestEnv.Balance(env.Alice)

	// gw clawback 400 USD from alice
	clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
		Amount(amm.IOUAmount(env.GW, "USD", 400)).
		Build()
	result = env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Alice should have received some XRP back (proportional to LP tokens burned)
	// With fixAMMv1_3 enabled, rippled returns exactly 29.289321 XRP.
	aliceXrpAfter := env.TestEnv.Balance(env.Alice)
	xrpDelta := int64(aliceXrpAfter) - int64(aliceXrpBefore)
	require.Equal(t, int64(29_289_321), xrpDelta)
}

func TestAMMClawback_LastHolderLPTokenBalance(t *testing.T) {
	// Helper: setupAccounts creates gw, alice, bob with clawback enabled and USD funded
	setupAccounts := func(t *testing.T) (*amm.AMMTestEnv, *jtx.Account, *jtx.Account) {
		t.Helper()

		env := amm.NewAMMTestEnv(t)

		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(100000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(100000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(100000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 50000)
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Bob, "USD", 40000)
		env.Close()

		return env, env.Alice, env.Bob
	}

	// Sub-test 1: IOU/XRP pool - clawback part of last holder's balance
	t.Run("IOU_XRP_PartialClawback", func(t *testing.T) {
		env, alice, bob := setupAccounts(t)

		createTx := amm.AMMCreate(alice, amm.XRPAmount(2), amm.IOUAmount(env.GW, "USD", 1)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob deposits, then withdraws to make alice sole LP holder
		depositTx := amm.AMMDeposit(bob, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		withdrawTx := amm.AMMWithdraw(bob, amm.XRP(), env.USD).WithdrawAll().Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
		env.Close()

		// Clawback 0.5 USD from alice
		clawbackTx := amm.AMMClawback(env.GW, alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 0.5)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.False(t, ammIsDeleted(t, env, env.USD, amm.XRP()))
	})

	// Sub-test 2: IOU/XRP pool - clawback all of last holder's balance
	t.Run("IOU_XRP_ClawbackAll", func(t *testing.T) {
		env, alice, bob := setupAccounts(t)

		createTx := amm.AMMCreate(alice, amm.XRPAmount(2), amm.IOUAmount(env.GW, "USD", 1)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		depositTx := amm.AMMDeposit(bob, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		withdrawTx := amm.AMMWithdraw(bob, amm.XRP(), env.USD).WithdrawAll().Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
		env.Close()

		// Clawback all from alice
		clawbackTx := amm.AMMClawback(env.GW, alice.Address, env.USD, amm.XRP()).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, ammIsDeleted(t, env, env.USD, amm.XRP()))
	})

	// Sub-test 3: IOU/IOU pool (different issuers)
	t.Run("IOU_IOU_DifferentIssuers", func(t *testing.T) {
		env, alice, bob := setupAccounts(t)
		gw2 := jtx.NewAccount("gw2")
		env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(100000)))
		env.Close()

		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}

		env.Trust(alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, alice, "EUR", 50000)
		env.Trust(bob, gw2, "EUR", 100000)
		env.PayIOU(gw2, bob, "EUR", 50000)
		env.Close()

		createTx := amm.AMMCreate(alice, amm.IOUAmount(env.GW, "USD", 2), amm.IOUAmount(gw2, "EUR", 1)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		depositTx := amm.AMMDeposit(bob, env.USD, EUR).
			LPTokenOut(amm.LPTokenAmount(env, env.USD, EUR, 1000)).
			LPToken().Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		withdrawTx := amm.AMMWithdraw(bob, env.USD, EUR).WithdrawAll().Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
		env.Close()

		// Clawback all from alice
		clawbackTx := amm.AMMClawback(env.GW, alice.Address, env.USD, EUR).Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, ammIsDeleted(t, env, env.USD, EUR))
	})

	// Sub-test 4: IOU/IOU pool (same issuer) with tfClawTwoAssets
	t.Run("IOU_IOU_SameIssuer", func(t *testing.T) {
		env, alice, bob := setupAccounts(t)

		env.Trust(alice, env.GW, "EUR", 100000)
		env.PayIOU(env.GW, alice, "EUR", 50000)
		env.Trust(bob, env.GW, "EUR", 100000)
		env.PayIOU(env.GW, bob, "EUR", 50000)
		env.Close()

		createTx := amm.AMMCreate(alice, amm.IOUAmount(env.GW, "USD", 1), amm.IOUAmount(env.GW, "EUR", 2)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		depositTx := amm.AMMDeposit(bob, env.USD, env.EUR).
			LPTokenOut(amm.LPTokenAmount(env, env.USD, env.EUR, 1000)).
			LPToken().Build()
		result = env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		withdrawTx := amm.AMMWithdraw(bob, env.USD, env.EUR).WithdrawAll().Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
		env.Close()

		// Clawback all with tfClawTwoAssets
		clawbackTx := amm.AMMClawback(env.GW, alice.Address, env.USD, env.EUR).
			ClawTwoAssets().Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, ammIsDeleted(t, env, env.USD, env.EUR))
	})
}
