// Package amm_test contains tests for AMM withdraw transactions.
// Reference: rippled/src/test/app/AMM_test.cpp testInvalidWithdraw and testWithdraw
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestInvalidWithdraw tests invalid withdrawal scenarios.
// Reference: rippled AMM_test.cpp testInvalidWithdraw
func TestInvalidWithdraw(t *testing.T) {
	// Invalid flags - tfBurnable
	// Reference: ammAlice.withdraw(alice, 1'000'000, ..., tfBurnable, ..., ter(temINVALID_FLAG));
	t.Run("InvalidFlags_Burnable", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 1000000)).
			Flags(0x00000001). // tfBurnable - invalid for withdraw
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemINVALID_FLAG.String())
	})

	// Invalid flags - tfTwoAssetIfEmpty
	// Reference: ammAlice.withdraw(alice, 1'000'000, ..., tfTwoAssetIfEmpty, ..., ter(temINVALID_FLAG));
	t.Run("InvalidFlags_TwoAssetIfEmpty", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 1000000)).
			Flags(amm.TfTwoAssetIfEmpty). // Invalid for withdraw
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemINVALID_FLAG.String())
	})

	// Invalid options - no tokens, no amounts, no flags
	// Reference: {std::nullopt, std::nullopt, std::nullopt, std::nullopt, std::nullopt, temMALFORMED}
	t.Run("InvalidOptions_NoParams", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Invalid options - conflicting flags
	// Reference: {std::nullopt, std::nullopt, std::nullopt, std::nullopt, tfSingleAsset | tfTwoAsset, temMALFORMED}
	t.Run("InvalidOptions_ConflictingFlags", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(100)).
			Flags(amm.TfSingleAsset | amm.TfTwoAsset).
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Invalid options - tokens with tfWithdrawAll
	// Reference: {1'000, std::nullopt, std::nullopt, std::nullopt, tfWithdrawAll, temMALFORMED}
	t.Run("InvalidOptions_TokensWithWithdrawAll", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 1000)).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Invalid options - tfWithdrawAll with tfOneAssetWithdrawAll
	// Reference: {std::nullopt, std::nullopt, std::nullopt, std::nullopt, tfWithdrawAll | tfOneAssetWithdrawAll, temMALFORMED}
	t.Run("InvalidOptions_WithdrawAllAndOneAsset", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Flags(amm.TfWithdrawAll | amm.TfOneAssetWithdrawAll).
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Invalid tokens - zero
	// Reference: ammAlice.withdraw(alice, 0, std::nullopt, std::nullopt, ter(temBAD_AMM_TOKENS));
	t.Run("ZeroTokens", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 0)).
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// Invalid tokens - negative
	// Reference: ammAlice.withdraw(alice, IOUAmount{-1}, std::nullopt, std::nullopt, ter(temBAD_AMM_TOKENS));
	t.Run("NegativeTokens", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", -1)).
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// Mismatched token - invalid Asset1Out issue
	// Reference: ammAlice.withdraw(alice, GBP(100), std::nullopt, std::nullopt, ter(temBAD_AMM_TOKENS));
	t.Run("MismatchedToken_Asset1", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "GBP", 100)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// Mismatched token - invalid Asset2Out issue
	// Reference: ammAlice.withdraw(alice, USD(100), GBP(100), std::nullopt, ter(temBAD_AMM_TOKENS));
	t.Run("MismatchedToken_Asset2", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Amount2(amm.IOUAmount(env.GW, "GBP", 100)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// Asset1Out.issue == Asset2Out.issue
	// Reference: ammAlice.withdraw(alice, USD(100), USD(100), std::nullopt, ter(temBAD_AMM_TOKENS));
	t.Run("SameAssetForBoth", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 100)).
			Amount2(amm.IOUAmount(env.GW, "USD", 100)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// Invalid amount value - zero
	// Reference: ammAlice.withdraw(alice, USD(0), std::nullopt, std::nullopt, ter(temBAD_AMOUNT));
	t.Run("ZeroAmount", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMOUNT.String())
	})

	// Invalid amount value - negative
	// Reference: ammAlice.withdraw(alice, USD(-100), std::nullopt, std::nullopt, ter(temBAD_AMOUNT));
	t.Run("NegativeAmount", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", -100)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMOUNT.String())
	})

	// Withdraw all tokens from one side - tecAMM_BALANCE
	// Reference: ammAlice.withdraw(alice, USD(10'000), std::nullopt, std::nullopt, ter(tecAMM_BALANCE));
	t.Run("WithdrawAllFromOneSide_USD", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 10000)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TecAMM_BALANCE.String())
	})

	// Withdraw all tokens from one side - XRP
	// Reference: ammAlice.withdraw(alice, XRP(10'000), std::nullopt, std::nullopt, ter(tecAMM_BALANCE));
	t.Run("WithdrawAllFromOneSide_XRP", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(10000)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TecAMM_BALANCE.String())
	})

	// Invalid Account (non-existent)
	// Reference: ammAlice.withdraw(bad, 1'000'000, ..., ter(terNO_ACCOUNT));
	t.Run("NonExistentAccount", func(t *testing.T) {
		env := setupAMM(t)

		bad := jtx.NewAccount("bad")
		withdrawTx := amm.AMMWithdraw(bad, amm.XRP(), env.USD).
			LPTokenIn(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.SubmitWithOptions(jtx.WithSeq(withdrawTx, 1), jtx.SubmitOptions{SkipSignature: true})
		amm.ExpectTER(t, result, ter.TerNO_ACCOUNT.String())
	})

	// Invalid AMM (non-existent)
	// Reference: ammAlice.withdraw(alice, 1'000, ..., {{USD, GBP}}, ..., ter(terNO_AMM));
	t.Run("NonExistentAMM", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, env.USD, env.GBP).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 1000)).
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TerNO_AMM.String())
	})

	// Carol is not a Liquidity Provider
	// Reference: ammAlice.withdraw(carol, 10'000, std::nullopt, std::nullopt, ter(tecAMM_BALANCE));
	t.Run("NotLiquidityProvider", func(t *testing.T) {
		env := setupAMM(t)

		// Carol hasn't deposited, so she can't withdraw
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 10000)).
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TecAMM_BALANCE.String())
	})

	// LPTokenIn denominated in an unrelated IOU → temBAD_AMM_TOKENS.
	// Reference: rippled AMMWithdraw.cpp preclaim lines 261-265 — Alice IS an LP
	// (passes the lpTokens<=zero check) but her LPTokenIn issue is not the AMM's.
	t.Run("WrongLPTokenIssue", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.IOUAmount(env.GW, "LPT", 1000)).
			LPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// === testMalformed cases (rippled AMM_test.cpp) ===

	// tfSingleAsset flag alone (no Amount) → temMALFORMED
	t.Run("Malformed_SingleAssetFlagOnly", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Flags(amm.TfSingleAsset).
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// tfOneAssetLPToken flag alone (no Amount, no LPTokenIn) → temMALFORMED
	t.Run("Malformed_OneAssetLPTokenFlagOnly", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Flags(amm.TfOneAssetLPToken).
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// tfLimitLPToken flag alone (no Amount, no EPrice) → temMALFORMED
	t.Run("Malformed_LimitLPTokenFlagOnly", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Flags(amm.TfLimitLPToken).
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemMALFORMED.String())
	})

	// Both assets are XRP → temBAD_AMM_TOKENS
	// Reference: {.asset1Out = XRP(100), .asset2Out = XRP(100), .err = ter(temBAD_AMM_TOKENS)}
	t.Run("Malformed_BothAssetsXRP", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(100)).
			Amount2(amm.XRPAmount(100)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	// tfLimitLPToken with Amount=XRP(100) and EPrice=USD(100) → temBAD_AMM_TOKENS
	// Reference: rippled AMM_test.cpp
	t.Run("Malformed_LimitLPTokenMismatchedEPrice", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(100)).
			EPrice(amm.IOUAmount(env.GW, "USD", 100)).
			LimitLPToken().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, ter.TemBAD_AMM_TOKENS.String())
	})
}

// TestWithdraw tests valid withdrawal scenarios.
// Reference: rippled AMM_test.cpp testWithdraw
func TestWithdraw(t *testing.T) {
	// Equal withdrawal by tokens
	// Reference: ammAlice.withdraw(alice, 1'000'000)
	t.Run("EqualWithdrawalByTokens", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to have tokens to withdraw
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Failed to deposit: %s", result.Code)
		}
		env.Close()

		initialBalance := env.Balance(env.Carol)

		// Withdraw all Carol's tokens
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			LPTokenIn(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result = env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Equal withdrawal by tokens should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		// XRP balance should have increased
		finalBalance := env.Balance(env.Carol)
		if finalBalance <= initialBalance {
			t.Fatal("XRP balance should have increased after withdrawal")
		}

		t.Log("Equal withdrawal by tokens passed")
	})

	// Equal withdrawal with limit
	// Reference: ammAlice.withdraw(alice, XRP(200), USD(100))
	t.Run("EqualWithdrawalWithLimit", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(200)).
			Amount2(amm.IOUAmount(env.GW, "USD", 100)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Equal withdrawal with limit should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Equal withdrawal with limit passed")
	})

	// Single withdrawal by amount - XRP
	// Reference: ammAlice.withdraw(alice, XRP(1'000))
	t.Run("SingleWithdrawal_XRP", func(t *testing.T) {
		env := setupAMM(t)

		initialBalance := env.Balance(env.Alice)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Single XRP withdrawal should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		finalBalance := env.Balance(env.Alice)
		if finalBalance <= initialBalance {
			t.Fatal("XRP balance should have increased after withdrawal")
		}

		t.Log("Single XRP withdrawal passed")
	})

	// Single withdrawal by tokens
	// Reference: ammAlice.withdraw(alice, 10'000, USD(0))
	t.Run("SingleWithdrawalByTokens", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.LPTokenAmount(env, amm.XRP(), env.USD, 10000)).
			Amount(amm.IOUAmount(env.GW, "USD", 0)).
			OneAssetLPToken().
			Build()
		result := env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Single withdrawal by tokens should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Single withdrawal by tokens passed")
	})

	// Withdraw all tokens - deletes AMM
	// Reference: ammAlice.withdrawAll(alice)
	t.Run("WithdrawAll", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Withdraw all should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		require.Nil(t, env.ReadAMMData(amm.XRP(), env.USD))
	})

	// Single deposit then withdraw all in USD
	// Reference: ammAlice.deposit(carol, USD(1'000)); ammAlice.withdrawAll(carol, USD(0));
	t.Run("DepositThenWithdrawAllInUSD", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Withdraw all Carol's tokens in USD
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0)). // USD(0) means withdraw in USD
			OneAssetWithdrawAll().
			Build()
		result = env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Withdraw all in USD should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Deposit then withdraw all in USD passed")
	})

	// Single deposit then withdraw all in XRP
	// Reference: ammAlice.deposit(carol, USD(1'000)); ammAlice.withdrawAll(carol, XRP(0));
	t.Run("DepositThenWithdrawAllInXRP", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Withdraw all Carol's tokens in XRP
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			Amount(tx.NewXRPAmount(0)). // XRP(0) means withdraw in XRP
			OneAssetWithdrawAll().
			Build()
		result = env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Withdraw all in XRP should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Deposit then withdraw all in XRP passed")
	})

	// Equal deposit 10%, withdraw all tokens
	// Reference: ammAlice.deposit(carol, 1'000'000); ammAlice.withdrawAll(carol);
	t.Run("EqualDepositThenWithdrawAll", func(t *testing.T) {
		env := setupAMM(t)

		// Deposit 10% of pool
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Withdraw all Carol's tokens
		withdrawTx := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result = env.Submit(withdrawTx)

		if !result.Success {
			t.Fatalf("Withdraw all should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Equal deposit then withdraw all passed")
	})
}

func TestWithdrawExactPriceZeroDenominator(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cleanup    bool
		cleanup340 bool
		want       string
	}{
		{name: "CleanupEnabled", cleanup: true, want: ter.TecAMM_FAILED.String()},
		{name: "CleanupDisabled", cleanup: false, want: "tefEXCEPTION"},
		{name: "Cleanup340Only", cleanup: false, cleanup340: true, want: ter.TecAMM_FAILED.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 100, 100, 1000, true)
			if !tc.cleanup {
				env.DisableFeature("fixCleanup3_3_0")
				if !tc.cleanup340 {
					env.DisableFeature("fixCleanup3_4_0")
				}
				env.Close()
			}

			if tc.cleanup340 {
				env.EnableFeatureNow("fixCleanup3_4_0")
			}

			beforeGBP, beforeEUR, beforeLP := env.AMMIOUBalances(env.GBP, env.EUR)
			if beforeLP.Value() != "100" {
				t.Fatalf("unexpected initial LP balance: %s", beforeLP.Value())
			}

			// The creator's auction-slot discount changes the 1% pool fee to 0.1%.
			// With a 100/100 pool and 100 LP tokens, EPrice=0.001 makes
			// T*f-A*E exactly zero in singleWithdrawEPrice.
			withdrawTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				Amount(amm.IOUAmount(env.GW, "GBP", 0)).
				EPrice(amm.LPTokenAmount(env, env.GBP, env.EUR, 0.001)).
				LimitLPToken().
				Build()
			result := env.Submit(withdrawTx)
			amm.ExpectTER(t, result, tc.want)

			afterGBP, afterEUR, afterLP := env.AMMIOUBalances(env.GBP, env.EUR)
			if beforeGBP.Compare(afterGBP) != 0 || beforeEUR.Compare(afterEUR) != 0 || beforeLP.Compare(afterLP) != 0 {
				t.Fatalf("failed withdrawal changed AMM state: before=(%s,%s,%s), after=(%s,%s,%s)",
					beforeGBP.Value(), beforeEUR.Value(), beforeLP.Value(),
					afterGBP.Value(), afterEUR.Value(), afterLP.Value())
			}
		})
	}
}

func TestWithdrawPrecisionLossAmendmentMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixAMMv13  bool
		cleanup330 bool
		want       string
	}{
		{name: "AMMv13Disabled", fixAMMv13: false, cleanup330: true, want: ter.TecAMM_BALANCE.String()},
		{name: "CleanupDisabled", fixAMMv13: true, cleanup330: false, want: "tecINVARIANT_FAILED"},
		{name: "BothEnabled", fixAMMv13: true, cleanup330: true, want: "tecPRECISION_LOSS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupAMM(t)
			env.DisableFeature("SingleAssetVault")
			env.DisableFeature("LendingProtocol")
			if !tc.fixAMMv13 {
				env.DisableFeature("fixAMMv1_3")
			}
			if !tc.cleanup330 {
				env.DisableFeature("fixCleanup3_3_0")
			}
			env.Close()

			ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
			beforeXRP := env.AMMPoolXRP(ammAccount)
			beforeUSD := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
			beforeLP := env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance
			amount := tx.NewIssuedAmount(9_999_999_999_999_999, -12, "USD", env.GW.Address)
			result := env.Submit(amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
				Amount(amount).
				SingleAsset().
				Build())
			amm.ExpectTER(t, result, tc.want)

			afterXRP := env.AMMPoolXRP(ammAccount)
			afterUSD := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
			afterLP := env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance
			if beforeXRP != afterXRP || beforeUSD.Compare(afterUSD) != 0 || beforeLP.Compare(afterLP) != 0 {
				t.Fatalf("failed withdrawal changed AMM state: before=(%d,%s,%s), after=(%d,%s,%s)",
					beforeXRP, beforeUSD.Value(), beforeLP.Value(),
					afterXRP, afterUSD.Value(), afterLP.Value())
			}
		})
	}
}

// TestFixReserveCheckOnWithdrawal tests that the fixAMMv1_2 amendment properly
// enforces reserve checks on AMM withdrawals.
// Reference: rippled AMM_test.cpp testFixReserveCheckOnWithdrawal
//
// Setup: accounts are funded with the minimum XRP required (reserve(2) + 5*baseFee).
// GW creates an EUR/USD AMM. Alice deposits USD(1). The withdrawal tests verify
// that with fixAMMv1_2 enabled the withdrawal fails with tecINSUFFICIENT_RESERVE
// (because alice's XRP is below reserve after the withdrawal creates trust lines),
// and without fixAMMv1_2 the withdrawal succeeds.
func TestFixReserveCheckOnWithdrawal(t *testing.T) {
	// reserve(env, 2) = reserveBase + 2 * reserveIncrement = 200M + 2*50M = 300M drops.
	// starting_xrp = reserve(2) + baseFee * 5 = 300_000_050 drops.
	// This leaves accounts with barely enough to cover 2 owner objects + 5 fees.

	// Helper: creates a fresh env, funds gw and alice with minimal XRP,
	// creates EUR/USD AMM, deposits alice USD(1), then runs the callback.
	setupMinimalAMM := func(t *testing.T, enableFixAMMv1_2 bool) *amm.AMMTestEnv {
		t.Helper()

		env := amm.NewAMMTestEnv(t)

		if enableFixAMMv1_2 {
			env.EnableFeature("fixAMMv1_2")
		} else {
			env.DisableFeature("fixAMMv1_2")
		}

		startingXRP := env.ReserveBase() + 2*env.ReserveIncrement() + env.BaseFee()*5

		env.TestEnv.FundAmount(env.GW, startingXRP)
		env.TestEnv.FundAmount(env.Alice, startingXRP)
		env.Close()

		// Alice trusts GW for USD.
		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Close()

		// GW pays alice USD(2000).
		env.PayIOU(env.GW, env.Alice, "USD", 2000)
		env.Close()

		// GW creates AMM with EUR(1000)/USD(1000).
		createTx := amm.AMMCreate(env.GW,
			amm.IOUAmount(env.GW, "EUR", 1000),
			amm.IOUAmount(env.GW, "USD", 1000),
		).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("AMM creation should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		// Alice deposits USD(1) into the EUR/USD AMM.
		depositTx := amm.AMMDeposit(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 1)).
			SingleAsset().
			Build()
		result = env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Alice deposit should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		return env
	}

	// Test with fixAMMv1_2 enabled: withdrawals fail with tecINSUFFICIENT_RESERVE.
	t.Run("WithFix_EqualWithdraw", func(t *testing.T) {
		env := setupMinimalAMM(t, true)

		// Equal withdraw all -> tecINSUFFICIENT_RESERVE
		withdrawTx := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)
		amm.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")
	})

	t.Run("WithFix_EqualWithdrawWithLimit", func(t *testing.T) {
		env := setupMinimalAMM(t, true)

		// Withdraw EUR(0.1)/USD(0.1) -> tecINSUFFICIENT_RESERVE
		withdrawTx1 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "EUR", 0.1)).
			Amount2(amm.IOUAmount(env.GW, "USD", 0.1)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx1)
		amm.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")

		// Withdraw USD(0.1)/EUR(0.1) -> tecINSUFFICIENT_RESERVE
		withdrawTx2 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
			Amount2(amm.IOUAmount(env.GW, "EUR", 0.1)).
			TwoAsset().
			Build()
		result = env.Submit(withdrawTx2)
		amm.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")
	})

	t.Run("WithFix_SingleWithdraw", func(t *testing.T) {
		env := setupMinimalAMM(t, true)

		// Single withdraw EUR(0.1) -> tecINSUFFICIENT_RESERVE
		withdrawTx1 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "EUR", 0.1)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx1)
		amm.ExpectTER(t, result, "tecINSUFFICIENT_RESERVE")

		// Single withdraw USD(0.1) -> tesSUCCESS
		// Note: USD withdrawal does NOT create a new trust line (alice already has one),
		// so it succeeds even with the fix enabled.
		withdrawTx2 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
			SingleAsset().
			Build()
		result = env.Submit(withdrawTx2)
		jtx.RequireTxSuccess(t, result)
	})

	// Test without fixAMMv1_2: same withdrawals succeed.
	t.Run("WithoutFix_EqualWithdraw", func(t *testing.T) {
		env := setupMinimalAMM(t, false)

		withdrawTx := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("WithoutFix_EqualWithdrawWithLimit", func(t *testing.T) {
		env := setupMinimalAMM(t, false)

		withdrawTx1 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "EUR", 0.1)).
			Amount2(amm.IOUAmount(env.GW, "USD", 0.1)).
			TwoAsset().
			Build()
		result := env.Submit(withdrawTx1)
		jtx.RequireTxSuccess(t, result)

		withdrawTx2 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
			Amount2(amm.IOUAmount(env.GW, "EUR", 0.1)).
			TwoAsset().
			Build()
		result = env.Submit(withdrawTx2)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("WithoutFix_SingleWithdraw", func(t *testing.T) {
		env := setupMinimalAMM(t, false)

		withdrawTx1 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "EUR", 0.1)).
			SingleAsset().
			Build()
		result := env.Submit(withdrawTx1)
		jtx.RequireTxSuccess(t, result)

		withdrawTx2 := amm.AMMWithdraw(env.Alice, env.EUR, env.USD).
			Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
			SingleAsset().
			Build()
		result = env.Submit(withdrawTx2)
		jtx.RequireTxSuccess(t, result)
	})
}
