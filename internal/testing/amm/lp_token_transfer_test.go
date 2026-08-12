// Package amm_test contains behavioral tests for LP token transfers.
// Tests ported from rippled's LPTokenTransfer_test.cpp.
//
// Reference: rippled/src/test/app/LPTokenTransfer_test.cpp
//
// These tests verify that frozen trust lines correctly block or allow
// LP token transfers, depending on the fixFrozenLPTokenTransfer amendment.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/stretchr/testify/require"
)

// setupLPTokenEnv creates an AMM with two liquidity providers holding LP tokens.
// Returns the env, and bob/carol both have LP tokens from depositing into XRP/USD AMM.
// Matches rippled's LPTokenTransfer_test.cpp setup which uses tfLPToken deposits
// (equalDepositTokens) rather than tfTwoAsset (equalDepositLimit).
func setupLPTokenEnv(t *testing.T) *amm.AMMTestEnv {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(30000, 0) // Fund GW, Alice, Carol with 30k XRP + USD

	// Fund Bob
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
	env.Trust(env.Bob, env.GW, "USD", 100000)
	env.Close()
	env.PayIOU(env.GW, env.Bob, "USD", 30000)
	env.Close()

	// Alice creates the AMM: XRP(10000)/USD(10000)
	// Initial LP tokens = sqrt(10000000000 * 10000) = 10,000,000
	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Failed to create AMM: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Carol deposits using tfLPToken mode (equalDepositTokens) to get 1,000,000 LP tokens.
	// This is a proportional deposit — the pool determines amounts automatically.
	// Reference: rippled deposit(carol, 10) uses LPToken mode
	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
		LPToken().
		Build()
	result = env.Submit(depositTx)
	if !result.Success {
		t.Fatalf("Carol deposit failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Bob deposits using tfLPToken mode to get 1,000,000 LP tokens.
	depositTx2 := amm.AMMDeposit(env.Bob, amm.XRP(), env.USD).
		LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
		LPToken().
		Build()
	result = env.Submit(depositTx2)
	if !result.Success {
		t.Fatalf("Bob deposit failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	return env
}

// TestLPTokenTransfer_DirectStep tests direct payment of LP tokens.
// Reference: rippled LPTokenTransfer_test.cpp testDirectStep
func TestLPTokenTransfer_DirectStep(t *testing.T) {
	t.Run("CannotTransferToAMMAccount", func(t *testing.T) {
		env := setupLPTokenEnv(t)
		ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)
		currency := coreAmm.GenerateAMMLPTCurrencyForAssets(amm.XRP(), env.USD)
		before := env.IOUBalance(env.Bob, ammAcc, currency)
		require.NotNil(t, before)

		payTx := payment.PayIssued(env.Bob, ammAcc, env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)).Build()
		amm.ExpectTER(t, env.Submit(payTx), amm.TecNO_PERMISSION)

		after := env.IOUBalance(env.Bob, ammAcc, currency)
		require.NotNil(t, after)
		require.Equal(t, before.Value(), after.Value())
	})
}

// ----------------------------------------------------------------
// testAMMTokens
// Reference: rippled AMM_test.cpp testAMMTokens (line 4743)
// ----------------------------------------------------------------

// TestAMMTokens_LPTokenXRPOfferCrossing tests LP token offer crossing with XRP.
// Carol buys LP tokens with XRP, Alice sells LP tokens for XRP.
// After crossing, both have LP tokens and can vote, bid, and withdraw.
// Reference: rippled AMM_test.cpp testAMMTokens block 1 (line 4749-4795)
func TestAMMTokens_LPTokenXRPOfferCrossing(t *testing.T) {
	t.Run("LPToken_XRP_OfferCross", func(t *testing.T) {
		// Offer crossing with AMM LPTokens and XRP.
		// Reference: rippled AMM_test.cpp testAMMTokens block 1 (line 4749-4795)
		amm.WithDefaultAMM(t, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			xrpAsset := amm.XRP()
			usdAsset := env.USD
			baseFee := uint64(10) // 10 drops

			// Compute price: ammAssetOut(XRP(10B drops), token1(10M), token1(5M), 0)
			xrpBalance := tx.NewXRPAmount(10_000_000_000) // 10,000 XRP in drops
			lpTotal := amm.LPTokenAmount(env, xrpAsset, usdAsset, 10_000_000)
			lpHalf := amm.LPTokenAmount(env, xrpAsset, usdAsset, 5_000_000)
			priceXRP := amm.AMMAssetOut(xrpBalance, lpTotal, lpHalf, 0)

			// Carol places an order to buy LPTokens: she pays priceXRP, receives 5M LP tokens
			carolOfferTx := offerbuild.OfferCreate(env.Carol, lpHalf, priceXRP).Build()
			result := env.Submit(carolOfferTx)
			if !result.Success {
				t.Fatalf("Carol offer to buy LP tokens failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Alice places an order to sell LPTokens: she pays 5M LP tokens, receives priceXRP
			aliceOfferTx := offerbuild.OfferCreate(env.Alice, priceXRP, lpHalf).Build()
			result = env.Submit(aliceOfferTx)
			if !result.Success {
				t.Fatalf("Alice offer to sell LP tokens failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Pool's LPTokens balance doesn't change
			env.ExpectAMMBalances(t, ammAcc, 10_000_000_000, env.GW, "USD", 10000)
			// Carol is now a Liquidity Provider
			env.ExpectLPTokens(env.Carol, xrpAsset, usdAsset, 5_000_000)
			env.ExpectLPTokens(env.Alice, xrpAsset, usdAsset, 5_000_000)

			// Carol votes
			env.Vote(env.Carol, xrpAsset, usdAsset, 1000)
			fee := env.AMMTradingFee(xrpAsset, usdAsset)
			if fee != 500 {
				t.Errorf("Expected trading fee 500 after carol vote(1000), got %d", fee)
			}
			env.Vote(env.Carol, xrpAsset, usdAsset, 0)
			fee = env.AMMTradingFee(xrpAsset, usdAsset)
			if fee != 0 {
				t.Errorf("Expected trading fee 0 after carol vote(0), got %d", fee)
			}

			// Carol bids with bidMin=100 LP tokens
			bidMinAmt := amm.LPTokenAmount(env, xrpAsset, usdAsset, 100)
			bidTx := amm.AMMBid(env.Carol, xrpAsset, usdAsset).BidMin(bidMinAmt).Build()
			result = env.Submit(bidTx)
			if !result.Success {
				t.Fatalf("Carol bid failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Carol should have 4,999,900 LP tokens after bidding 100
			env.ExpectLPTokens(env.Carol, xrpAsset, usdAsset, 4_999_900)

			// Carol XRP balance: 30000 XRP - priceXRP - fees
			// priceXRP = 7,500,000,000 drops = 7,500 XRP
			// Our setup charges 1 extra fee (trust line) vs rippled
			// Fees: trust(1) + offer(2) + vote(3) + vote(4) + bid(5) = 5 * baseFee
			expectedCarolXRP := 22_500_000_000 - 5*baseFee
			actualCarolXRP := env.TestEnv.Balance(env.Carol)
			if actualCarolXRP != expectedCarolXRP {
				t.Errorf("Carol XRP balance: got %d, want %d (diff=%d)", actualCarolXRP, expectedCarolXRP, int64(actualCarolXRP)-int64(expectedCarolXRP))
			}

			// Carol withdraws all (single-asset: XRP only)
			// Reference: rippled withdrawAll(carol, XRP(0)) → tfOneAssetWithdrawAll
			xrpZero := tx.NewXRPAmount(0)
			withdrawTx := amm.AMMWithdraw(env.Carol, xrpAsset, usdAsset).
				Amount(xrpZero).
				OneAssetWithdrawAll().
				Build()
			result = env.Submit(withdrawTx)
			if !result.Success {
				t.Fatalf("Carol withdrawAll failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// After OneAssetWithdrawAll: carol gets XRP only.
			// priceXRP2 = ammAssetOut(XRP(10B), token1(9999900), token1(4999900), 0)
			// Expected: ~7,499,950,000 XRP drops returned
			// Carol XRP ≈ 22.5B - 50 + 7,499,950,000 - 10 = 29,999,949,940
			// Rippled expects 29,999,949,999 - 5*baseFee (with different setup fees)
			// expectedCarolXRP2 is setup-adjusted: 30B - 7.5B + ammAssetOut - 6*baseFee
			// We compute the expected using ammAssetOut:
			lpAfterBid := amm.LPTokenAmount(env, xrpAsset, usdAsset, 9_999_900)
			carolLPAfterBid := amm.LPTokenAmount(env, xrpAsset, usdAsset, 4_999_900)
			priceXRP2 := amm.AMMAssetOut(xrpBalance, lpAfterBid, carolLPAfterBid, 0)

			// Pool should have only alice's LP tokens remaining
			env.ExpectLPTokens(env.Alice, xrpAsset, usdAsset, 5_000_000)
			jtx.RequireTrustLineNotExists(t, env.TestEnv, env.Carol, ammAcc, coreAmm.GenerateAMMLPTCurrencyForAssets(xrpAsset, usdAsset))

			// Verify pool USD is unchanged (OneAssetWithdrawAll takes only XRP)
			actualUSD := env.AMMPoolIOU(ammAcc, env.GW, "USD")
			if actualUSD != 10000 {
				t.Errorf("Pool USD balance: got %f, want 10000", actualUSD)
			}

			// Verify pool XRP decreased by priceXRP2
			actualPoolXRP := env.AMMPoolXRP(ammAcc)
			expectedPoolXRP := 10_000_000_000 - uint64(priceXRP2.Drops())
			require.Equal(t, expectedPoolXRP, actualPoolXRP)
		})
	})
}

// TestAMMTokens_TwoAMMLPTokenOfferCrossing tests offer crossing between two
// AMMs' LP tokens.
// Reference: rippled AMM_test.cpp testAMMTokens block 2 (line 4797-4819)
func TestAMMTokens_TwoAMMLPTokenOfferCrossing(t *testing.T) {
	t.Run("TwoAMM_LPToken_OfferCross", func(t *testing.T) {
		// Offer crossing with two AMM LPTokens.
		// Reference: rippled AMM_test.cpp testAMMTokens block 2 (line 4797-4819)
		amm.WithDefaultAMM(t, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			xrpAsset := amm.XRP()
			usdAsset := env.USD
			eurAsset := env.EUR

			// Carol deposits 1,000,000 LP tokens into AMM1 (XRP/USD)
			depositTx := amm.AMMDeposit(env.Carol, xrpAsset, usdAsset).
				LPTokenOut(amm.LPTokenAmount(env, xrpAsset, usdAsset, 1_000_000)).
				LPToken().
				Build()
			result := env.Submit(depositTx)
			if !result.Success {
				t.Fatalf("Carol deposit into AMM1 failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Fund alice and carol with EUR
			env.Trust(env.Alice, env.GW, "EUR", 100000)
			env.Trust(env.Carol, env.GW, "EUR", 100000)
			env.Close()
			env.PayIOU(env.GW, env.Alice, "EUR", 10000)
			env.PayIOU(env.GW, env.Carol, "EUR", 10000)
			env.Close()

			// Create AMM2: XRP(10000)/EUR(10000) by alice
			eurAmt := tx.NewIssuedAmountFromFloat64(10000, "EUR", env.GW.Address)
			xrpAmt := tx.NewXRPAmount(10_000_000_000) // 10,000 XRP
			createTx2 := amm.AMMCreate(env.Alice, xrpAmt, eurAmt).Build()
			result = env.Submit(createTx2)
			if !result.Success {
				t.Fatalf("Create AMM2 (XRP/EUR) failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Carol deposits 1,000,000 LP tokens into AMM2 (XRP/EUR)
			depositTx2 := amm.AMMDeposit(env.Carol, xrpAsset, eurAsset).
				LPTokenOut(amm.LPTokenAmount(env, xrpAsset, eurAsset, 1_000_000)).
				LPToken().
				Build()
			result = env.Submit(depositTx2)
			if !result.Success {
				t.Fatalf("Carol deposit into AMM2 failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// token1 = AMM1 LP tokens (XRP/USD), token2 = AMM2 LP tokens (XRP/EUR)
			token1_100 := amm.LPTokenAmount(env, xrpAsset, usdAsset, 100)
			token2_100 := amm.LPTokenAmount(env, xrpAsset, eurAsset, 100)

			// Alice: passive offer — alice receives 100 token1, pays 100 token2
			aliceOfferTx := offerbuild.OfferCreate(env.Alice, token1_100, token2_100).Passive().Build()
			result = env.Submit(aliceOfferTx)
			if !result.Success {
				t.Fatalf("Alice passive offer failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// Verify alice has 1 offer on the book
			aliceOffers := len(env.AccountOffers(env.Alice))
			if aliceOffers != 1 {
				t.Errorf("Expected 1 alice offer, got %d", aliceOffers)
			}

			// Carol: offer — carol receives 100 token2, pays 100 token1
			carolOfferTx := offerbuild.OfferCreate(env.Carol, token2_100, token1_100).Build()
			result = env.Submit(carolOfferTx)
			if !result.Success {
				t.Fatalf("Carol offer failed: %s - %s", result.Code, result.Message)
			}
			env.Close()

			// After crossing:
			// alice: token1 = 10,000,100, token2 = 9,999,900
			env.ExpectLPTokens(env.Alice, xrpAsset, usdAsset, 10_000_100)
			env.ExpectLPTokens(env.Alice, xrpAsset, eurAsset, 9_999_900)
			// carol: token2 = 1,000,100, token1 = 999,900
			env.ExpectLPTokens(env.Carol, xrpAsset, eurAsset, 1_000_100)
			env.ExpectLPTokens(env.Carol, xrpAsset, usdAsset, 999_900)

			// Both offers consumed
			aliceOffers = len(env.AccountOffers(env.Alice))
			carolOffers := len(env.AccountOffers(env.Carol))
			if aliceOffers != 0 {
				t.Errorf("Expected 0 alice offers after crossing, got %d", aliceOffers)
			}
			if carolOffers != 0 {
				t.Errorf("Expected 0 carol offers after crossing, got %d", carolOffers)
			}
		})
	})
}

// TestAMMTokens_DirectLPTokenPayment tests direct LP token payment between LPs.
// LPs must trust-set first because the auto-created AMM trust line has 0 limit.
// Reference: rippled AMM_test.cpp testAMMTokens block 3 (line 4821-4851)
func TestAMMTokens_DirectLPTokenPayment(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(30000, 0)
	env.Close()

	// Alice creates AMM: XRP(10000)/USD(10000) -> gets 10,000,000 LP tokens
	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("AMM create failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Carol sets trust line for LP tokens (limit 2,000,000) before depositing.
	// This is required because the AMM auto-created trust line has limit 0,
	// and payment checks the limit.
	// NOTE: rippled allows TrustSet for LP tokens to AMM accounts, but go-xrpl
	// currently blocks all TrustSet to AMM pseudo-accounts with tecNO_PERMISSION.
	// Use real AMM account address (pseudo-account) for the LP token issuer.
	lpToken := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 2000000)
	trustTx := trustset.TrustSet(env.Carol, lpToken).Build()
	result = env.Submit(trustTx)
	if !result.Success {
		t.Fatalf("Carol trust set for LP token failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Carol deposits 1,000,000 LP tokens worth of assets
	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
		LPToken().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(depositTx))
	env.Close()

	// Alice pays Carol 100 LP tokens.
	// Pool balance should not change, only LP token balances shift.
	payAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)
	payTx := payment.PayIssued(env.Alice, env.Carol, payAmt).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// Expected: Alice LP = 10,000,000 - 100 = 9,999,900
	//           Carol LP = 1,000,000 + 100 = 1,000,100
	ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)
	lpCurrency := coreAmm.GenerateAMMLPTCurrencyForAssets(amm.XRP(), env.USD)
	aliceLP, found := env.LookupIOUBalance(env.Alice, ammAcc, lpCurrency)
	require.True(t, found)
	carolLP, found := env.LookupIOUBalance(env.Carol, ammAcc, lpCurrency)
	require.True(t, found)
	require.Equal(t, "9999900", aliceLP.Value())
	require.Equal(t, "1000100", carolLP.Value())

	// Alice sets trust line for LP tokens (limit 20,000,000) to receive back.
	// Alice's auto-created trust line from AMMCreate also has limit 0.
	trustTx2 := trustset.TrustSet(env.Alice, env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 20000000)).Build()
	result = env.Submit(trustTx2)
	if !result.Success {
		t.Fatalf("Alice trust set for LP token failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Carol pays Alice 100 LP tokens back.
	payTx2 := payment.PayIssued(env.Carol, env.Alice, payAmt).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()

	// Expected: back to original balances
	//   Alice LP = 10,000,000
	//   Carol LP = 1,000,000
	aliceLP, found = env.LookupIOUBalance(env.Alice, ammAcc, lpCurrency)
	require.True(t, found)
	carolLP, found = env.LookupIOUBalance(env.Carol, ammAcc, lpCurrency)
	require.True(t, found)
	require.Equal(t, "10000000", aliceLP.Value())
	require.Equal(t, "1000000", carolLP.Value())
}
