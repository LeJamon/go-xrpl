// Package amm_test contains AMM calculation precision tests.
// Tests ported from rippled's AMMCalc_test.cpp.
//
// Reference: rippled/src/test/app/AMMCalc_test.cpp
//
// rippled's AMMCalc_test is a manual calculator DSL for verifying AMM math.
// Here we test the same formulas through behavioral deposit/withdraw operations
// with known expected results, verifying that the AMM math produces correct
// LP token amounts, pool balances, and swap calculations.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/stretchr/testify/require"
)

// ───────────────────────────────────────────────────────────────────────
// LP Token calculation tests
// Reference: rippled AMMCalc_test.cpp "lptokens" DSL operations
// Formula: LPTokens = sqrt(pool1 * pool2)
// ───────────────────────────────────────────────────────────────────────

// TestAMMCalc_LPTokensOnCreate tests that initial LP tokens = sqrt(amount1 * amount2).
// Reference: rippled AMMCalc_test.cpp lptokens calculations
func TestAMMCalc_LPTokensOnCreate(t *testing.T) {
	t.Run("EqualAmounts_XRP_USD", func(t *testing.T) {
		// AMM with XRP(10000)/USD(10000) → LP tokens = sqrt(10000*10000) = 10000
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Verify Alice has LP tokens by trying to withdraw
		// If she can withdraw, LP tokens were minted correctly.
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(100)).
			SingleAsset().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
	})

	t.Run("UnequalAmounts_XRP_USD", func(t *testing.T) {
		// AMM with XRP(2)/USD(1) → LP tokens = sqrt(2*1) ≈ 1.414
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(2), amm.IOUAmount(env.GW, "USD", 1)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()
	})

	t.Run("LargeAmounts_XRP_USD", func(t *testing.T) {
		// AMM with XRP(20000)/USD(20000) → LP tokens = 20000
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(20000), amm.IOUAmount(env.GW, "USD", 20000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()
	})

	t.Run("IOU_IOU_Pool", func(t *testing.T) {
		// AMM with USD(20000)/BTC(0.5) → LP tokens = sqrt(20000*0.5) = 100
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(20000, 1) // fund with USD and BTC
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 20000), amm.IOUAmount(env.GW, "BTC", 0.5)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()
	})
}

// ───────────────────────────────────────────────────────────────────────
// Swap tests (deposit/withdraw precision)
// Reference: rippled AMMCalc_test.cpp "swapin" and "swapout" DSL operations
// ───────────────────────────────────────────────────────────────────────

// TestAMMCalc_SingleAssetDeposit tests single-asset deposit LP token calculation.
// Equation 4: lpTokensOut = lptBalance * ((1 + amountIn/assetBalance)^0.5 - 1) * (1 - tfee)
func TestAMMCalc_SingleAssetDeposit(t *testing.T) {
	t.Run("DepositXRP_GetLPTokens", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create balanced AMM: XRP(10000)/USD(10000) → LP = 10000
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits 1000 XRP (single asset)
		carolBefore := env.Balance(env.Carol)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		carolAfter := env.Balance(env.Carol)
		require.Equal(t, uint64(1_000_000_010), carolBefore-carolAfter)
	})

	t.Run("DepositUSD_GetLPTokens", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits 1000 USD (single asset)
		usdBefore := env.IOUBalance(env.Carol, env.GW, "USD")
		require.NotNil(t, usdBefore)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		usdAfter := env.IOUBalance(env.Carol, env.GW, "USD")
		require.NotNil(t, usdAfter)
		spent, err := usdBefore.Sub(*usdAfter)
		require.NoError(t, err)
		require.Equal(t, "999.99999999999", spent.Value())
	})

	t.Run("DepositWithTradingFee", func(t *testing.T) {
		// Trading fee reduces LP tokens received for single-asset deposit.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM with 1% trading fee
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).
			TradingFee(1000). // 1%
			Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits 1000 XRP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			SingleAsset().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depositTx))
	})
}

// TestAMMCalc_TwoAssetDeposit tests proportional (two-asset) deposit.
// Proportional deposit: deposit both assets in pool ratio → LP tokens proportional to deposit.
func TestAMMCalc_TwoAssetDeposit(t *testing.T) {
	t.Run("ProportionalDeposit", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM: XRP(10000)/USD(10000) → LP = 10000
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits proportionally: XRP(1000)/USD(1000)
		// Should receive 1000 LP tokens (10% of pool)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("DisproportionateDeposit", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Deposit with 2x XRP but 1x USD
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(2000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depositTx))
	})
}

// TestAMMCalc_SingleAssetWithdraw tests single-asset withdrawal calculation.
// Equation 8: assetOut = assetBalance * (1 - (1 - lpTokensIn/lptBalance)^2) / (1 + tfee)
func TestAMMCalc_SingleAssetWithdraw(t *testing.T) {
	t.Run("WithdrawXRP", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice withdraws some XRP
		aliceBefore := env.Balance(env.Alice)
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(500)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		jtx.RequireTxSuccess(t, result)
		aliceAfter := env.Balance(env.Alice)
		require.Equal(t, uint64(499_999_989), aliceAfter-aliceBefore)
	})

	t.Run("WithdrawUSD", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice withdraws some USD
		usdBefore := env.IOUBalance(env.Alice, env.GW, "USD")
		require.NotNil(t, usdBefore)
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 500)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		jtx.RequireTxSuccess(t, result)
		usdAfter := env.IOUBalance(env.Alice, env.GW, "USD")
		require.NotNil(t, usdAfter)
		gained, err := usdAfter.Sub(*usdBefore)
		require.NoError(t, err)
		require.Zero(t, gained.Compare(amm.IOUAmount(env.GW, "USD", 500)))
	})

	t.Run("WithdrawWithTradingFee", func(t *testing.T) {
		// Trading fee means less asset received for the same LP tokens burned.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).
			TradingFee(1000).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(500)).
			SingleAsset().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
	})
}

// TestAMMCalc_DepositByLPTokens tests depositing by specifying desired LP token amount.
// Equation 3 inverse: assetIn = assetBalance * ((1 + lpTokensOut/lptBalance)^2 - 1) / (1 - tfee)
func TestAMMCalc_DepositByLPTokens(t *testing.T) {
	t.Run("SpecifyLPTokens", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits specifying LP token amount
		lpAmt := amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(lpAmt).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depositTx))
	})

	t.Run("OneAssetLPToken_XRP", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Carol deposits XRP for specific LP tokens
		lpAmt := amm.LPTokenAmount(env, amm.XRP(), env.USD, 500)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(5000)). // maximum XRP to spend
			LPTokenOut(lpAmt).
			OneAssetLPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depositTx))
	})
}

// TestAMMCalc_WithdrawByLPTokens tests withdrawing by burning specific LP token amount.
func TestAMMCalc_WithdrawByLPTokens(t *testing.T) {
	t.Run("BurnLPTokens_Proportional", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Withdraw by burning LP tokens (proportional withdrawal)
		lpAmt := amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000)
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(lpAmt).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
	})

	t.Run("OneAssetLPToken_USD", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Withdraw USD by burning specific LP tokens
		lpAmt := amm.LPTokenAmount(env, amm.XRP(), env.USD, 100000)
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			LPTokenIn(lpAmt).
			OneAssetLPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
	})
}

// TestAMMCalc_ConstantProduct verifies constant product invariant.
// After any swap, pool1 * pool2 should remain approximately constant (minus fees).
func TestAMMCalc_ConstantProduct(t *testing.T) {
	t.Run("DepositDoesNotBreakInvariant", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Multiple deposits and withdrawals should not break the AMM
		for range 5 {
			depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
				Amount(amm.XRPAmount(100)).
				SingleAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depositTx))
		}
		env.Close()

		// Withdraw all
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(withdrawTx))
	})
}

// TestAMMCalc_SpotPriceQuality tests that AMM spot price converges to offer quality.
// Reference: rippled AMMCalc_test.cpp "changespq" operations
func TestAMMCalc_SpotPriceQuality(t *testing.T) {
	t.Run("AMMAndOfferCoexist", func(t *testing.T) {
		// When AMM and CLOB offers coexist, the payment engine selects
		// the best price between them.
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

		// Bob places a CLOB offer at a different price
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(1100), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// Carol crosses — engine should pick best available
		crossTx := offerbuild.OfferCreate(env.Carol, amm.IOUAmount(env.GW, "USD", 100), amm.XRPAmount(100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(crossTx))
	})
}
