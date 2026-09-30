// Behavioral vectors from rippled's AMMClawback_test.cpp.
package amm_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func setupClawbackEnv(t *testing.T, gwFund, aliceFund int64) *amm.AMMTestEnv {
	t.Helper()

	env := amm.NewAMMTestEnv(t)

	// Fund accounts
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(gwFund)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(aliceFund)))
	env.Close()

	// Enable clawback on gateway BEFORE trust lines
	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	return env
}

func setupClawbackEnvWithUSD(t *testing.T, gwFund, aliceFund int64, usdFund float64) *amm.AMMTestEnv {
	t.Helper()

	env := setupClawbackEnv(t, gwFund, aliceFund)

	// Set up USD trust line and fund
	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", usdFund)
	env.Close()

	return env
}

func ammIsDeleted(t *testing.T, env *amm.AMMTestEnv, asset, asset2 tx.Asset) bool {
	t.Helper()
	return !env.LedgerEntryExists(coreAmm.ComputeAMMKeylet(asset, asset2))
}

func requireDeletedNode(t *testing.T, metadata *tx.Metadata, key keylet.Keylet) {
	t.Helper()
	want := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for _, node := range metadata.AffectedNodes {
		if node.LedgerIndex == want {
			require.Equal(t, "DeletedNode", node.NodeType)
			return
		}
	}
	t.Fatalf("deleted node %s not found", want)
}

func TestAMMClawback(t *testing.T) {
	// Test basic clawback functionality
	t.Run("BasicClawback", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM creation should succeed: %s", result.Code)
		}
		env.Close()

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result = env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TecNO_PERMISSION.String())
	})

	// Non-issuer cannot clawback
	t.Run("NonIssuerCannotClawback", func(t *testing.T) {
		env := setupAMM(t)

		clawbackTx := amm.AMMClawback(env.Alice, env.Carol.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Invalid holder account
	t.Run("InvalidHolderAccount", func(t *testing.T) {
		env := setupAMM(t)

		bad := jtx.NewAccount("bad")
		clawbackTx := amm.AMMClawback(env.GW, bad.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TerNO_ACCOUNT.String())
	})

	// Clawback from non-existent AMM
	t.Run("NonExistentAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.GBP).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TerNO_AMM.String())
	})

	// Invalid flags
	t.Run("InvalidFlags", func(t *testing.T) {
		env := setupAMM(t)

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Flags(amm.TfWithdrawAll).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TemINVALID_FLAG.String())
	})

	// Zero amount clawback
	t.Run("ZeroAmount", func(t *testing.T) {
		env := setupAMM(t)

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 0)).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMOUNT.String())
	})

	// Negative amount clawback
	t.Run("NegativeAmount", func(t *testing.T) {
		env := setupAMM(t)

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", -100)).
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMOUNT.String())
	})

	// Clawback with tfClawTwoAssets flag when assets have different issuers
	// Reference: tfClawTwoAssets requires both assets from same issuer
	t.Run("ClawTwoAssetsRequiresSameIssuer", func(t *testing.T) {
		env := setupAMM(t)

		// XRP/USD pool: XRP has no issuer, so tfClawTwoAssets should fail
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			ClawTwoAssets().
			Build()
		result := env.Submit(clawbackTx)
		amm.ExpectTER(t, result, ter.TemINVALID_FLAG.String())
	})
}

func TestClawbackBasic(t *testing.T) {
	t.Run("CannotEnableClawbackAfterTrustLines", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM creation should succeed: %s", result.Code)
		}
		env.Close()

		// Try to enable clawback on gateway - should fail because gw already has trust lines
		result = env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
		amm.ExpectTER(t, result, "tecOWNERS")
	})
}

func TestAMMClawback_FeatureDisabled(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
	env.Close()

	// gw sets asfAllowTrustLineClawback
	result := env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// gw issues 3000 USD to Alice
	env.Trust(env.Alice, env.GW, "USD", 100000)
	env.PayIOU(env.GW, env.Alice, "USD", 3000)
	env.Close()

	// Disable the AMMClawback amendment
	env.DisableFeature("AMMClawback")
	env.Close()

	// When featureAMMClawback is not enabled, AMMClawback is disabled.
	clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
	result = env.Submit(clawbackTx)
	amm.ExpectTER(t, result, ter.TemDISABLED.String())
}

func TestAMMClawback_SpecificAmount(t *testing.T) {
	// Sub-test 1: USD/EUR pool (different issuers)
	// Reference: gw claws back 1000 USD twice from EUR/USD pool.
	// After first clawback: alice gets 500 EUR back proportionally.
	// After second clawback: AMM is deleted, alice gets remaining EUR.
	t.Run("USD_EUR_Pool", func(t *testing.T) {
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

		// gw issues 3000 USD to Alice
		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.PayIOU(env.GW, env.Alice, "USD", 3000)
		env.Close()

		// gw2 issues 3000 EUR to Alice
		EUR := tx.Asset{Currency: "EUR", Issuer: gw2.Address}
		env.Trust(env.Alice, gw2, "EUR", 100000)
		env.PayIOU(gw2, env.Alice, "EUR", 3000)
		env.Close()

		// Alice creates AMM pool of EUR(1000)/USD(2000)
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(gw2, "EUR", 1000), amm.IOUAmount(env.GW, "USD", 2000)).Build()
		result = env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// gw clawback 1000 USD from the AMM pool
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// rippled expected: alice USD = 1000 (3000 - 2000 deposited), EUR = 2500 (2000 + 500 returned)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 2500)

		// gw clawback another 1000 USD from the AMM pool
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, EUR).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// rippled expected: alice USD still 1000, EUR = 3000 (all returned), AMM deleted
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, gw2, "EUR", 3000)
		require.True(t, ammIsDeleted(t, env, env.USD, EUR))
	})

	// Sub-test 2: USD/XRP pool
	// Reference: gw claws back 1000 USD twice, alice gets 500 XRP each time.
	t.Run("USD_XRP_Pool", func(t *testing.T) {
		env := setupClawbackEnvWithUSD(t, 1000000, 1000000, 3000)

		// Alice creates AMM pool of XRP(1000)/USD(2000)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 2000)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceXrpBefore := env.TestEnv.Balance(env.Alice)

		// gw clawback 1000 USD from the AMM pool
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice should still have 1000 USD (3000 - 2000 deposited)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)

		// Alice should get ~500 XRP back
		aliceXrpAfter := env.TestEnv.Balance(env.Alice)
		xrpDelta := int64(aliceXrpAfter) - int64(aliceXrpBefore)
		require.Equal(t, jtx.XRP(500), xrpDelta)

		// gw clawback another 1000 USD
		aliceXrpBefore = env.TestEnv.Balance(env.Alice)
		clawbackTx = amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			Build()
		result = env.Submit(clawbackTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 1000)

		aliceXrpAfter = env.TestEnv.Balance(env.Alice)
		xrpDelta = int64(aliceXrpAfter) - int64(aliceXrpBefore)
		require.Equal(t, jtx.XRP(500), xrpDelta)
		require.True(t, ammIsDeleted(t, env, env.USD, amm.XRP()))
	})
}
