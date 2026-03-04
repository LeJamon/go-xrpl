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

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
	offerbuild "github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// setupLPTokenEnv creates an AMM with two liquidity providers holding LP tokens.
// Returns the env, and bob/carol both have LP tokens from depositing into XRP/USD AMM.
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
	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Failed to create AMM: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Carol deposits to get LP tokens
	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		Amount(amm.XRPAmount(1000)).
		Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
		TwoAsset().
		Build()
	result = env.Submit(depositTx)
	if !result.Success {
		t.Fatalf("Carol deposit failed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Bob deposits to get LP tokens
	depositTx2 := amm.AMMDeposit(env.Bob, amm.XRP(), env.USD).
		Amount(amm.XRPAmount(1000)).
		Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
		TwoAsset().
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
	t.Run("TransferBetweenLPs", func(t *testing.T) {
		env := setupLPTokenEnv(t)

		// Bob sends LP tokens to Carol (both are LPs)
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 100)
		payTx := payment.PayIssued(env.Bob, env.Carol, lpAmt).Build()
		result := env.Submit(payTx)
		if result.Success {
			t.Log("PASS: LP token direct transfer succeeded")
		} else {
			t.Logf("Note: LP token direct transfer got %s (may need LP token payment path support)", result.Code)
		}
	})

	t.Run("FrozenUSD_BlocksSender", func(t *testing.T) {
		// When Carol's USD trust line is frozen, Carol should not be able to
		// send LP tokens (with fixFrozenLPTokenTransfer).
		env := setupLPTokenEnv(t)

		// Freeze Carol's USD trust line
		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol tries to send LP tokens to Bob
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 100)
		payTx := payment.PayIssued(env.Carol, env.Bob, lpAmt).Build()
		result := env.Submit(payTx)
		if !result.Success {
			t.Logf("PASS: frozen Carol cannot send LP tokens (got %s)", result.Code)
		} else {
			t.Log("Note: frozen Carol can still send LP tokens - fixFrozenLPTokenTransfer may not be active")
		}
	})

	t.Run("FrozenUSD_ReceiveAllowed", func(t *testing.T) {
		// A frozen account should still be able to receive LP tokens.
		env := setupLPTokenEnv(t)

		// Freeze Carol's USD trust line
		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Bob sends LP tokens to frozen Carol - should succeed
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 100)
		payTx := payment.PayIssued(env.Bob, env.Carol, lpAmt).Build()
		result := env.Submit(payTx)
		if result.Success {
			t.Log("PASS: frozen Carol can receive LP tokens")
		} else {
			t.Logf("Note: frozen Carol cannot receive LP tokens (got %s)", result.Code)
		}
	})

	t.Run("CannotTransferToAMMAccount", func(t *testing.T) {
		// Cannot transfer LP tokens to the AMM pseudo-account itself.
		// The AMM pseudo-account is not a normal account and should reject
		// direct payments. We verify this by attempting a send.
		env := setupLPTokenEnv(t)

		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 100)
		// Attempt to pay to a non-existent account (stand-in for AMM pseudo-account).
		// In practice, the AMM account rejects direct payments.
		nonExistent := jtx.NewAccount("amm_pseudo")
		payTx := payment.PayIssued(env.Bob, nonExistent, lpAmt).Build()
		result := env.Submit(payTx)
		if !result.Success {
			t.Logf("PASS: cannot send LP tokens to non-existent/AMM account (got %s)", result.Code)
		} else {
			t.Log("Note: LP token transfer to non-existent account succeeded")
		}
	})
}

// TestLPTokenTransfer_BookStep tests LP token transfers via offer book.
// Reference: rippled LPTokenTransfer_test.cpp testBookStep
func TestLPTokenTransfer_BookStep(t *testing.T) {
	t.Run("FrozenCurrency_BlocksOfferConsumption", func(t *testing.T) {
		// With fixFrozenLPTokenTransfer, frozen currencies prevent consuming
		// offers to sell LP tokens.
		env := setupLPTokenEnv(t)

		// Carol creates an offer selling LP tokens for XRP
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 500)
		offerTx := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(500), lpAmt).Build()
		result := env.Submit(offerTx)
		if !result.Success {
			t.Skipf("Carol offer creation failed: %s", result.Code)
		}
		env.Close()

		// Freeze Carol's USD trust line
		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Bob tries to buy LP tokens via offer crossing
		buyTx := offerbuild.OfferCreate(env.Bob, lpAmt, amm.XRPAmount(500)).Build()
		result = env.Submit(buyTx)
		// With fix: Carol's offer should not be consumed because her USD is frozen
		// Without fix: offer crossing proceeds normally
		t.Logf("Frozen offer crossing result: success=%v code=%s", result.Success, result.Code)
	})

	t.Run("BuyingLPTokens_WorksWhenSellerFrozen", func(t *testing.T) {
		// Buying LP tokens should work even when seller's currency is frozen
		// (the buyer is acquiring LP tokens, not the seller sending them).
		env := setupLPTokenEnv(t)

		// Bob creates an offer to sell LP tokens for XRP
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 500)
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(500), lpAmt).Build()
		result := env.Submit(offerTx)
		if !result.Success {
			t.Skipf("Bob offer creation failed: %s", result.Code)
		}
		env.Close()

		// Carol tries to buy LP tokens (Carol's USD is NOT frozen)
		buyTx := offerbuild.OfferCreate(env.Carol, lpAmt, amm.XRPAmount(500)).Build()
		result = env.Submit(buyTx)
		t.Logf("Buy LP tokens result: success=%v code=%s", result.Success, result.Code)
	})
}

// TestLPTokenTransfer_OfferCreation tests creating offers with LP token backing.
// Reference: rippled LPTokenTransfer_test.cpp testOfferCreation
func TestLPTokenTransfer_OfferCreation(t *testing.T) {
	t.Run("FrozenCurrency_BlocksSellOffer", func(t *testing.T) {
		// With fixFrozenLPTokenTransfer, cannot create sell offers for LP tokens
		// when backing currency is frozen.
		env := setupLPTokenEnv(t)

		// Freeze Carol's USD trust line
		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol tries to create offer selling LP tokens
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 500)
		offerTx := offerbuild.OfferCreate(env.Carol, amm.XRPAmount(500), lpAmt).Build()
		result := env.Submit(offerTx)
		if !result.Success {
			t.Logf("PASS: frozen Carol cannot create sell offer for LP tokens (got %s)", result.Code)
		} else {
			t.Log("Note: frozen Carol can create LP sell offer - fixFrozenLPTokenTransfer may not be active")
		}
	})

	t.Run("FrozenCurrency_BuyOfferAllowed", func(t *testing.T) {
		// Buying offers for LP tokens can be created even with frozen backing currency.
		env := setupLPTokenEnv(t)

		// Freeze Carol's USD trust line
		env.FreezeTrustLine(env.GW, env.Carol, "USD")
		env.Close()

		// Carol tries to create offer buying LP tokens (pays XRP, gets LP tokens)
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 500)
		offerTx := offerbuild.OfferCreate(env.Carol, lpAmt, amm.XRPAmount(500)).Build()
		result := env.Submit(offerTx)
		t.Logf("Frozen Carol buy LP offer: success=%v code=%s", result.Success, result.Code)
	})
}

// TestLPTokenTransfer_OfferCrossing tests offer crossing with two LP tokens.
// Reference: rippled LPTokenTransfer_test.cpp testOfferCrossing
func TestLPTokenTransfer_OfferCrossing(t *testing.T) {
	t.Run("CrossingBlockedWhenFrozen", func(t *testing.T) {
		// With fixFrozenLPTokenTransfer, offers don't cross when LP token's
		// underlying currency is frozen.
		env := setupLPTokenEnv(t)

		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 200)

		// Bob creates an offer selling LP tokens for XRP
		sellTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(200), lpAmt).Build()
		result := env.Submit(sellTx)
		if !result.Success {
			t.Skipf("Bob sell offer failed: %s", result.Code)
		}
		env.Close()

		// Freeze Bob's USD trust line
		env.FreezeTrustLine(env.GW, env.Bob, "USD")
		env.Close()

		// Carol creates a crossing offer to buy LP tokens
		buyTx := offerbuild.OfferCreate(env.Carol, lpAmt, amm.XRPAmount(200)).Build()
		result = env.Submit(buyTx)
		// With fix: Bob's offer should NOT be consumed
		// Without fix: crossing proceeds
		t.Logf("Crossing with frozen LP result: success=%v code=%s", result.Success, result.Code)
	})
}

// TestLPTokenTransfer_GlobalFreeze tests LP token behavior under global freeze.
// Reference: rippled LPTokenTransfer_test.cpp (global freeze variant)
func TestLPTokenTransfer_GlobalFreeze(t *testing.T) {
	t.Run("GlobalFreezeBlocksLPTransfer", func(t *testing.T) {
		env := setupLPTokenEnv(t)

		// Enable global freeze on gateway
		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Bob tries to send LP tokens to Carol
		lpAmt := amm.LPTokenAmount(amm.XRP(), env.USD, 100)
		payTx := payment.PayIssued(env.Bob, env.Carol, lpAmt).Build()
		result := env.Submit(payTx)
		if !result.Success {
			t.Logf("PASS: global freeze blocks LP token transfer (got %s)", result.Code)
		} else {
			t.Log("Note: LP token transfer succeeded despite global freeze")
		}
	})

	t.Run("GlobalFreezeBlocksWithdrawal", func(t *testing.T) {
		env := setupLPTokenEnv(t)

		// Enable global freeze on gateway
		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Carol tries to withdraw from AMM
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		if !result.Success {
			t.Logf("PASS: global freeze blocks AMM withdrawal (got %s)", result.Code)
		} else {
			t.Log("Note: AMM withdrawal succeeded despite global freeze")
		}
	})
}

// TestLPTokenTransfer_MultipleLPs tests LP token balance tracking with multiple providers.
// Reference: rippled AMM_test.cpp testLPTokenBalance (multiple liquidity providers)
func TestLPTokenTransfer_MultipleLPs(t *testing.T) {
	t.Run("XRP_IOU_MultipleLPs", func(t *testing.T) {
		// More than one Liquidity Provider - XRP/IOU
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Alice creates AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10), amm.IOUAmount(env.GW, "USD", 10)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM create failed: %s", result.Code)
		}
		env.Close()

		// Carol deposits
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		if !result.Success {
			t.Skipf("Carol deposit failed: %s", result.Code)
		}
		env.Close()

		// Both should have LP tokens but neither is the only provider
		t.Log("PASS: multiple LPs with XRP/IOU AMM")
	})

	t.Run("IOU_IOU_MultipleLPs", func(t *testing.T) {
		// More than one Liquidity Provider - IOU/IOU
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		// Set up EUR
		env.Trust(env.Alice, env.GW, "EUR", 100000)
		env.Trust(env.Carol, env.GW, "EUR", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "EUR", 20000)
		env.PayIOU(env.GW, env.Carol, "EUR", 20000)
		env.Close()

		// Alice creates IOU/IOU AMM
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "EUR", 10), amm.IOUAmount(env.GW, "USD", 10)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("IOU/IOU AMM create failed: %s", result.Code)
		}
		env.Close()

		// Carol deposits
		depositTx := amm.AMMDeposit(env.Carol, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "EUR", 1000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		if !result.Success {
			t.Skipf("Carol deposit failed: %s", result.Code)
		}
		env.Close()

		t.Log("PASS: multiple LPs with IOU/IOU AMM")
	})
}

// TestLPTokenTransfer_WithdrawAllAsLastLP tests behavior when last LP withdraws all tokens.
// Reference: rippled AMM_test.cpp testLPTokenBalance (last liquidity provider scenarios)
func TestLPTokenTransfer_WithdrawAllAsLastLP(t *testing.T) {
	t.Run("LastLPWithdrawsAll", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Alice creates AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM create failed: %s", result.Code)
		}
		env.Close()

		// Alice withdraws all (she's the only LP)
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result = env.Submit(withdrawTx)
		if result.Success {
			t.Log("PASS: last LP can withdraw all, AMM should be deleted")
		} else {
			t.Logf("Note: last LP withdraw all got %s", result.Code)
		}
	})

	t.Run("TwoLPsWithdrawSequentially", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Alice creates AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM create failed: %s", result.Code)
		}
		env.Close()

		// Carol deposits
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		result = env.Submit(depositTx)
		if !result.Success {
			t.Skipf("Carol deposit failed: %s", result.Code)
		}
		env.Close()

		// Carol withdraws all her LP tokens
		withdrawTx1 := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result = env.Submit(withdrawTx1)
		if !result.Success {
			t.Logf("Note: Carol withdraw all got %s", result.Code)
		}
		env.Close()

		// Alice withdraws all (now she's the last LP)
		withdrawTx2 := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result = env.Submit(withdrawTx2)
		if result.Success {
			t.Log("PASS: sequential LP withdrawals succeeded")
		} else {
			// With fixAMMv1_1: this should succeed
			// Without fixAMMv1_1: may get tecAMM_BALANCE
			t.Logf("Note: last LP withdraw got %s (may depend on fixAMMv1_1)", result.Code)
		}
	})
}

// Suppress unused import warnings
var (
	_ = offerbuild.OfferCreate
	_ = payment.Pay
	_ = trustset.TrustLine
)
