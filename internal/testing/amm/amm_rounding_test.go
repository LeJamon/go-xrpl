// Package amm_test contains AMM deposit and withdraw rounding tests.
// Reference: rippled/src/test/app/AMM_test.cpp
//   - testDepositAndWithdrawRounding (line 7527)
//   - testDepositRounding (line 7598)
//   - testWithdrawRounding (line 7779)
//
// These tests verify that the AMM math correctly handles rounding for
// various deposit and withdrawal modes. The key invariant is:
//   sqrt(amount1 * amount2) >= lptBalance
// which must hold after every operation to prevent value leakage.
//
// The tests use GBP/EUR IOU pairs with precise mantissa/exponent values.
// rippled runs each test with both all-amendments-enabled and
// all-minus-fixAMMv1_3 configurations. The fixAMMv1_3 amendment fixes
// several rounding issues that caused invariant violations.
package amm_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
)

// setupGBPEURPoolWithBob creates a GBP/EUR AMM and funds Bob with
// large IOU balances so he can deposit. This matches the rippled pattern
// where fund() is called inside the testAMM callback to add Bob.
// When fixV1_3 is false, the fixAMMv1_3 amendment is disabled.
func setupGBPEURPoolWithBob(t *testing.T, gbpPool, eurPool float64,
	gbpFund, eurFund float64, tradingFee uint16, fixV1_3 bool,
) (*amm.AMMTestEnv, *jtx.Account) {
	t.Helper()

	env := amm.NewAMMTestEnv(t)

	// Toggle fixAMMv1_3 amendment
	if !fixV1_3 {
		env.DisableFeature("fixAMMv1_3")
	}

	// Fund gw, alice, carol with 30,000 XRP (matching rippled's testAMM default)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000000)))
	env.Close()

	// Determine IOU funding amounts
	gbpLimit := gbpPool * 2
	if gbpLimit < 100000 {
		gbpLimit = 100000
	}
	eurLimit := eurPool * 2
	if eurLimit < 100000 {
		eurLimit = 100000
	}

	// Trust lines for alice and carol
	env.Trust(env.Alice, env.GW, "GBP", gbpLimit)
	env.Trust(env.Alice, env.GW, "EUR", eurLimit)
	env.Trust(env.Carol, env.GW, "GBP", gbpLimit)
	env.Trust(env.Carol, env.GW, "EUR", eurLimit)
	env.Close()

	// Fund alice and carol with IOUs
	env.PayIOU(env.GW, env.Alice, "GBP", gbpPool)
	env.PayIOU(env.GW, env.Alice, "EUR", eurPool)
	env.PayIOU(env.GW, env.Carol, "GBP", gbpPool)
	env.PayIOU(env.GW, env.Carol, "EUR", eurPool)
	env.Close()

	// Alice creates the AMM
	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "GBP", gbpPool),
		amm.IOUAmount(env.GW, "EUR", eurPool),
	).TradingFee(tradingFee).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// Fund Bob with IOUs inside the callback (matching rippled's fund() pattern)
	env.Trust(env.Bob, env.GW, "GBP", gbpFund*2)
	env.Trust(env.Bob, env.GW, "EUR", eurFund*2)
	env.Close()
	env.PayIOU(env.GW, env.Bob, "GBP", gbpFund)
	env.PayIOU(env.GW, env.Bob, "EUR", eurFund)
	env.Close()

	ammAcc := amm.AMMAccount(t, env.GBP, env.EUR)
	return env, ammAcc
}

// setupGBPEURPoolAliceOnly creates a GBP/EUR AMM with only alice as LP (no Bob).
// Used for withdraw tests where alice withdraws from her own LP position.
func setupGBPEURPoolAliceOnly(t *testing.T, gbpPool, eurPool float64, tradingFee uint16, fixV1_3 bool,
) (*amm.AMMTestEnv, *jtx.Account) {
	t.Helper()

	env := amm.NewAMMTestEnv(t)

	if !fixV1_3 {
		env.DisableFeature("fixAMMv1_3")
	}

	// Fund gw, alice, carol with 30,000 XRP
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(30000)))
	env.Close()

	gbpLimit := gbpPool * 2
	if gbpLimit < 100000 {
		gbpLimit = 100000
	}
	eurLimit := eurPool * 2
	if eurLimit < 100000 {
		eurLimit = 100000
	}

	env.Trust(env.Alice, env.GW, "GBP", gbpLimit)
	env.Trust(env.Alice, env.GW, "EUR", eurLimit)
	env.Trust(env.Carol, env.GW, "GBP", gbpLimit)
	env.Trust(env.Carol, env.GW, "EUR", eurLimit)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "GBP", gbpPool)
	env.PayIOU(env.GW, env.Alice, "EUR", eurPool)
	env.PayIOU(env.GW, env.Carol, "GBP", gbpPool)
	env.PayIOU(env.GW, env.Carol, "EUR", eurPool)
	env.Close()

	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "GBP", gbpPool),
		amm.IOUAmount(env.GW, "EUR", eurPool),
	).TradingFee(tradingFee).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env.GBP, env.EUR)
	return env, ammAcc
}

// TestDepositAndWithdrawRounding tests rounding behavior for deposits and withdrawals.
// Reference: rippled AMM_test.cpp testDepositAndWithdrawRounding (line 7527)
//
// The test creates an AMM with specific XRP/XPM balances, burns tokens to reach a
// specific LP state, then verifies single-asset deposit and withdrawal rounding.
// This test uses XRP/XPM (not GBP/EUR) and requires precise bid-based token burning.
func TestDepositAndWithdrawRounding(t *testing.T) {
	// Rippled values:
	// xrpBalance = XRPAmount(692'614'492'126)
	// xpmBalance = XPM{UINT64_C(18'610'359'80246901), -8} = 186103598.0246901
	// amount = XPM{UINT64_C(6'566'496939465400), -12} = 6566.496939465400
	// lptAMMBalance = lptIssue{UINT64_C(3'234'987'266'485968), -6} = 3234987266.485968

	XPM := tx.Asset{Currency: "XPM", Issuer: ""} // placeholder, will be set per env

	for _, fixV1_3 := range []bool{true, false} {
		suffix := "WithFixAMMv1_3"
		if !fixV1_3 {
			suffix = "WithoutFixAMMv1_3"
		}

		// setupAndBurn creates the AMM, burns LP tokens to the target state, and calls cb.
		setupAndBurn := func(t *testing.T, tfee uint16, cb func(env *amm.AMMTestEnv)) {
			t.Helper()

			env := amm.NewAMMTestEnv(t)
			if !fixV1_3 {
				env.TestEnv.DisableFeature("fixAMMv1_3")
			}
			XPM = tx.Asset{Currency: "XPM", Issuer: env.GW.Address}

			// Fund gw with large XRP; fund alice with 1000 XRP + XPM
			env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1_000_000)))
			env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1_000)))
			env.Close()

			env.Trust(env.Alice, env.GW, "XPM", 7_000)
			env.Close()

			// Pay alice XPM amount: {6566496939465400, -12}
			xpmAmt := tx.NewIssuedAmount(6_566_496939465400, -12, "XPM", env.GW.Address)
			env.PayIOUAmount(env.GW, env.Alice, xpmAmt)
			env.Close()

			// Create AMM: XRP(692614492126 drops) / XPM{18610359'80246901, -8}
			xrpBal := tx.NewXRPAmount(692_614_492_126)
			xpmBal := tx.NewIssuedAmount(18_610_359_80246901, -8, "XPM", env.GW.Address)
			createTx := amm.AMMCreate(env.GW, xrpBal, xpmBal).TradingFee(tfee).Build()
			jtx.RequireTxSuccess(t, env.Submit(createTx))
			env.Close()

			// Read current LP token balance
			ammData := env.ReadAMMData(amm.XRP(), XPM)
			if ammData == nil {
				t.Fatal("AMM not found after creation")
			}

			// LP token issue from the AMM
			lptRef := amm.LPTokenAmount(amm.XRP(), XPM, 0)
			lptCurrency := lptRef.Currency
			lptIssuer := lptRef.Issuer

			// Current LPT balance from AMM SLE (may have empty currency/issuer from deserialization)
			currentLPT := tx.NewIssuedAmount(
				ammData.LPTokenBalance.Mantissa(),
				ammData.LPTokenBalance.Exponent(),
				lptCurrency, lptIssuer,
			)

			// Target LPT balance: {3234987266485968, -6}
			targetLPT := tx.NewIssuedAmount(3_234_987_266_485968, -6, lptCurrency, lptIssuer)

			// Burn = current - target
			burnRaw, err := currentLPT.Sub(targetLPT)
			if err != nil {
				t.Fatalf("failed to compute burn: %v", err)
			}
			// Ensure burn has proper LP token issue
			burn := tx.NewIssuedAmount(burnRaw.Mantissa(), burnRaw.Exponent(), lptCurrency, lptIssuer)

			// Use AMMBid to burn tokens
			bidTx := amm.AMMBid(env.GW, amm.XRP(), XPM).
				BidMin(burn).
				BidMax(burn).
				Build()
			jtx.RequireTxSuccess(t, env.Submit(bidTx))
			env.Close()

			cb(env)
		}

		t.Run("SingleAssetDeposit/"+suffix, func(t *testing.T) {
			setupAndBurn(t, 941, func(env *amm.AMMTestEnv) {
				// Alice deposits XPM amount {6566496939465400, -12}
				depositAmt := tx.NewIssuedAmount(6_566_496939465400, -12, "XPM", env.GW.Address)
				depTx := amm.AMMDeposit(env.Alice, amm.XRP(), XPM).
					Amount(depositAmt).
					SingleAsset().
					Build()
				result := env.Submit(depTx)
				env.Close()

				if fixV1_3 {
					jtx.RequireTxSuccess(t, result)
				} else {
					amm.ExpectTER(t, result, amm.TecUNFUNDED_AMM)
				}
			})
		})

		t.Run("SingleAssetWithdraw/"+suffix, func(t *testing.T) {
			setupAndBurn(t, 0, func(env *amm.AMMTestEnv) {
				// Read XPM pool balance before withdraw
				ammAcc := amm.AMMAccount(t, amm.XRP(), XPM)
				amount2Before := env.AMMPoolIOUPrecise(ammAcc, env.GW, "XPM")

				// Withdraw tiny XPM: {1, -5} = 0.00001
				// Rippled default account = AMM creator (gw)
				withdrawAmt := tx.NewIssuedAmount(1, -5, "XPM", env.GW.Address)
				wdTx := amm.AMMWithdraw(env.GW, amm.XRP(), XPM).
					Amount(withdrawAmt).
					SingleAsset().
					Build()
				jtx.RequireTxSuccess(t, env.Submit(wdTx))
				env.Close()

				// Read XPM pool balance after withdraw
				amount2After := env.AMMPoolIOUPrecise(ammAcc, env.GW, "XPM")

				// diff = amount2Before - amount2After
				diff, err := amount2Before.Sub(amount2After)
				if err != nil {
					t.Fatalf("Sub failed: %v", err)
				}
				diffIOU := amm.ToIOUForCalc(diff)
				wdIOU := amm.ToIOUForCalc(withdrawAmt)

				if !fixV1_3 {
					// Without fix: actual withdrawn > requested (rounding error)
					if diffIOU.Compare(wdIOU) <= 0 {
						t.Errorf("expected diff > withdraw without fixAMMv1_3, got diff=%v withdraw=%v",
							diff, withdrawAmt)
					}
				} else {
					// With fix: actual withdrawn <= requested
					if diffIOU.Compare(wdIOU) > 0 {
						t.Errorf("expected diff <= withdraw with fixAMMv1_3, got diff=%v withdraw=%v",
							diff, withdrawAmt)
					}
				}
			})
		})
	}
}

// TestDepositRounding tests deposit rounding for various deposit modes.
// Reference: rippled AMM_test.cpp testDepositRounding (line 7598)
//
// All subtests use GBP/EUR IOU pools and verify the invariant:
//
//	sqrt(amount1 * amount2) >= lptBalance
//
// rippled runs each test with both `all` and `all - fixAMMv1_3`.
// The fixAMMv1_3 amendment uses Number::upward rounding mode in the
// invariant check, which fixes rounding violations for certain exponents.
func TestDepositRounding(t *testing.T) {
	for _, fixV1_3 := range []bool{true, false} {
		suffix := "WithFixAMMv1_3"
		if !fixV1_3 {
			suffix = "WithoutFixAMMv1_3"
		}

		// Single asset deposit with various exponents
		// Reference: rippled AMM_test.cpp lines 7603-7636
		// Pool: GBP(30,000) / EUR(30,000) with tfee=0
		// Deposits EUR with mantissa=1 and various exponents
		// The EUR(1, -3) case fails the invariant without fixAMMv1_3
		t.Run("SingleAssetDeposit/"+suffix, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				mantissa   int64
				exponent   int
				shouldFail bool // invariant fails without fixAMMv1_3
			}{
				{"EUR_1e1", 1, 1, false},
				{"EUR_1e2", 1, 2, false},
				{"EUR_1e5", 1, 5, false},
				{"EUR_1e-3", 1, -3, true}, // fails invariant without fixAMMv1_3
				{"EUR_1e-6", 1, -6, false},
				{"EUR_1e-9", 1, -9, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env, _ := setupGBPEURPoolWithBob(t, 30000, 30000, 100000, 100000, 0, fixV1_3)

					// Deposit EUR(mantissa, exponent) as single asset
					deposit := tx.NewIssuedAmount(tc.mantissa, tc.exponent, "EUR", env.GW.Address)
					depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
						Amount(deposit).
						SingleAsset().
						Build()
					jtx.RequireTxSuccess(t, env.Submit(depTx))
					env.Close()

					// Check invariant: sqrt(gbp * eur) >= lptBalance
					// shouldFail only applies when fixAMMv1_3 is disabled
					shouldFail := tc.shouldFail && !fixV1_3
					env.CheckInvariant(env.GBP, env.EUR, fixV1_3, shouldFail, "dep1")
				})
			}
		})

		// Two-asset proportional deposit (1:1 pool ratio)
		// Reference: rippled AMM_test.cpp lines 7638-7664
		t.Run("TwoAssetProportional_1to1/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolWithBob(t, 30000, 30000, 100000, 100000, 0, fixV1_3)

			// Deposit EUR(101234567890123456, -16) + GBP(101234567890123456, -16)
			depositEUR := tx.NewIssuedAmount(101234567890123456, -16, "EUR", env.GW.Address)
			depositGBP := tx.NewIssuedAmount(101234567890123456, -16, "GBP", env.GW.Address)

			depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
				Amount(depositEUR).
				Amount2(depositGBP).
				TwoAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "dep2")
		})

		// Two-asset proportional deposit (1:3 pool ratio)
		// Reference: rippled AMM_test.cpp lines 7666-7697
		t.Run("TwoAssetProportional_1to3/"+suffix, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				exponent   int
				shouldFail bool // invariant fails without fixAMMv1_3
			}{
				{"exp_1", 1, true},
				{"exp_2", 2, true},
				{"exp_3", 3, true},
				{"exp_4", 4, true},
				{"exp_-3", -3, false}, // does NOT fail without fixAMMv1_3
				{"exp_-6", -6, true},
				{"exp_-9", -9, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env, _ := setupGBPEURPoolWithBob(t, 10000, 30000, 100000, 100000, 0, fixV1_3)

					depositEUR := tx.NewIssuedAmount(1, tc.exponent, "EUR", env.GW.Address)
					depositGBP := tx.NewIssuedAmount(1, tc.exponent, "GBP", env.GW.Address)

					depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
						Amount(depositEUR).
						Amount2(depositGBP).
						TwoAsset().
						Build()
					jtx.RequireTxSuccess(t, env.Submit(depTx))
					env.Close()

					shouldFail := tc.shouldFail && !fixV1_3
					env.CheckInvariant(env.GBP, env.EUR, fixV1_3, shouldFail, fmt.Sprintf("dep3_exp%d", tc.exponent))
				})
			}
		})

		// tfLPToken deposit
		// Reference: rippled AMM_test.cpp lines 7699-7719
		t.Run("LPTokenDeposit/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolWithBob(t, 7000, 30000, 100000, 100000, 0, fixV1_3)

			// LP token amount: IOUAmount(101234567890123456, -16)
			lptRef := amm.LPTokenAmount(env.GBP, env.EUR, 0)
			tokens := tx.NewIssuedAmount(101234567890123456, -16, lptRef.Currency, lptRef.Issuer)

			depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
				LPTokenOut(tokens).
				LPToken().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "dep4")
		})

		// tfOneAssetLPToken deposit
		// Reference: rippled AMM_test.cpp lines 7721-7753
		t.Run("OneAssetLPTokenDeposit/"+suffix, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				mantissa int64
				exponent int
			}{
				{"tokens_0.001", 1, -3},
				{"tokens_0.01", 1, -2},
				{"tokens_0.1", 1, -1},
				{"tokens_1", 1, 0},
				{"tokens_10", 10, 0},
				{"tokens_100", 100, 0},
				{"tokens_1000", 1000, 0},
				{"tokens_10000", 10000, 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env, _ := setupGBPEURPoolWithBob(t, 7000, 30000, 100000, 1000000, 0, fixV1_3)

					// Build LP token amount with specific mantissa/exponent
					lptRef := amm.LPTokenAmount(env.GBP, env.EUR, 0)
					tokens := tx.NewIssuedAmount(tc.mantissa, tc.exponent, lptRef.Currency, lptRef.Issuer)

					// EUR(1e6) as asset1In
					eurAsset := tx.NewIssuedAmount(1, 6, "EUR", env.GW.Address)

					depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
						LPTokenOut(tokens).
						Amount(eurAsset).
						OneAssetLPToken().
						Build()
					jtx.RequireTxSuccess(t, env.Submit(depTx))
					env.Close()

					env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "dep5")
				})
			}
		})

		// Single deposit with EP (effective price) limit
		// Reference: rippled AMM_test.cpp lines 7755-7776
		t.Run("SingleDepositWithEP/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolWithBob(t, 30000, 30000, 100000, 100000, 0, fixV1_3)

			// Deposit 1,000 GBP with EP not to exceed 5
			gbp1000 := amm.IOUAmount(env.GW, "GBP", 1000)
			ep := amm.IOUAmount(env.GW, "GBP", 5)

			depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
				Amount(gbp1000).
				EPrice(ep).
				LimitLPToken().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "dep6")
		})
	}
}

// TestWithdrawRounding tests withdrawal rounding for various withdraw modes.
// Reference: rippled AMM_test.cpp testWithdrawRounding (line 7779)
//
// All subtests use GBP/EUR IOU pools and verify the invariant:
//
//	sqrt(amount1 * amount2) >= lptBalance
//
// rippled runs each test with both `all` and `all - fixAMMv1_3`.
func TestWithdrawRounding(t *testing.T) {
	for _, fixV1_3 := range []bool{true, false} {
		suffix := "WithFixAMMv1_3"
		if !fixV1_3 {
			suffix = "WithoutFixAMMv1_3"
		}

		// tfLPToken withdraw
		// Reference: rippled AMM_test.cpp lines 7786-7794
		t.Run("LPTokenWithdraw/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			// Alice withdraws 1,000 LP tokens
			lptRef := amm.LPTokenAmount(env.GBP, env.EUR, 1000)
			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				LPTokenIn(lptRef).
				LPToken().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with1")
		})

		// tfWithdrawAll mode
		// Reference: rippled AMM_test.cpp lines 7797-7806
		t.Run("WithdrawAll/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				WithdrawAll().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			// After withdraw-all, invariant holds trivially (balances go to zero)
			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with2")
		})

		// tfTwoAsset withdraw mode
		// Reference: rippled AMM_test.cpp lines 7808-7821
		t.Run("TwoAssetWithdraw/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			gbp3500 := amm.IOUAmount(env.GW, "GBP", 3500)
			eur15000 := amm.IOUAmount(env.GW, "EUR", 15000)

			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				Amount(gbp3500).
				Amount2(eur15000).
				TwoAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with3")
		})

		// tfSingleAsset withdraw mode
		// Reference: rippled AMM_test.cpp lines 7823-7839
		t.Run("SingleAssetWithdraw/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			gbp1234 := amm.IOUAmount(env.GW, "GBP", 1234)

			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				Amount(gbp1234).
				SingleAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with4")
		})

		// tfOneAssetWithdrawAll mode
		// Reference: rippled AMM_test.cpp lines 7841-7865
		t.Run("OneAssetWithdrawAll/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolWithBob(t, 7000, 30000, 100000, 100000, 0, fixV1_3)

			// Bob deposits GBP(3,456) as single asset first
			gbp3456 := amm.IOUAmount(env.GW, "GBP", 3456)
			depTx := amm.AMMDeposit(env.Bob, env.GBP, env.EUR).
				Amount(gbp3456).
				SingleAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depTx))
			env.Close()

			// Bob withdraws all his LP tokens as GBP(1,000) with tfOneAssetWithdrawAll
			gbp1000 := amm.IOUAmount(env.GW, "GBP", 1000)
			wdTx := amm.AMMWithdraw(env.Bob, env.GBP, env.EUR).
				Amount(gbp1000).
				OneAssetWithdrawAll().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with5")
		})

		// tfOneAssetLPToken mode
		// Reference: rippled AMM_test.cpp lines 7867-7880
		t.Run("OneAssetLPToken/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			// Alice withdraws 1,000 LP tokens as GBP(100)
			lptRef := amm.LPTokenAmount(env.GBP, env.EUR, 1000)
			gbp100 := amm.IOUAmount(env.GW, "GBP", 100)

			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				LPTokenIn(lptRef).
				Amount(gbp100).
				OneAssetLPToken().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, false, "with6")
		})

		// tfLimitLPToken mode
		// Reference: rippled AMM_test.cpp lines 7882-7895
		// NOTE: The invariant INTENTIONALLY FAILS here (shouldFail=true in rippled)
		t.Run("LimitLPToken/"+suffix, func(t *testing.T) {
			env, _ := setupGBPEURPoolAliceOnly(t, 7000, 30000, 0, fixV1_3)

			// Alice withdraws GBP(100) with maxEP=2 and tfLimitLPToken
			gbp100 := amm.IOUAmount(env.GW, "GBP", 100)

			// maxEP is IOUAmount{2} — a raw IOUAmount, not an STAmount with issue
			// In rippled: .maxEP = IOUAmount{2} creates a number value for the EP
			// For the withdraw builder, EPrice needs the LP token issue
			lptRef := amm.LPTokenAmount(env.GBP, env.EUR, 0)
			ep := tx.NewIssuedAmount(2, 0, lptRef.Currency, lptRef.Issuer)

			wdTx := amm.AMMWithdraw(env.Alice, env.GBP, env.EUR).
				Amount(gbp100).
				EPrice(ep).
				LimitLPToken().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(wdTx))
			env.Close()

			// This intentionally fails the invariant in rippled
			env.CheckInvariant(env.GBP, env.EUR, fixV1_3, true, "with7")
		})
	}
}
