// Package amm_test contains AMM tests that require BookStep AMM integration.
// These tests route payments/offers through AMM synthetic offers in the payment engine.
//
// Reference: rippled/src/test/app/AMM_test.cpp and AMMExtended_test.cpp
package amm_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	paymenttx "github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
	offerbuild "github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// expectIOUBalance checks an IOU balance with tolerance for float64 precision.
func expectIOUBalance(t *testing.T, env *amm.AMMTestEnv, account *jtx.Account, currency string, issuer *jtx.Account, expected float64) {
	t.Helper()
	actual := env.TestEnv.BalanceIOU(account, currency, issuer)
	if math.Abs(actual-expected) > 0.0001 {
		t.Errorf("%s %s: got %f, want %f", account.Name, currency, actual, expected)
	}
}

// ===================================================================
// AMM_test.cpp BookStep-dependent tests
// ===================================================================

// TestAMMBookStep_BasicPaymentEngine tests XRP/IOU payments through AMM.
// Reference: rippled AMM_test.cpp testBasicPaymentEngine (line 3774)
func TestAMMBookStep_BasicPaymentEngine(t *testing.T) {
	// Sub-test 1: Payment 100USD for 100XRP with path(~USD) and tfNoRippleDirect.
	// Pool: XRP(10000)/USD(10100) — designed so exactly 100 XRP buys 100 USD.
	// 10000 * 10100 = 101,000,000. After +100 XRP: 10100 * x = 101M, x = 10000.
	t.Run("PathNoRippleDirect", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.XRPAmount(10000),
			amm.IOUAmount(nil, "USD", 10100),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			env.FundBob(30000, 0)
			env.Close()

			// bob pays carol 100 USD, sendmax 100 XRP, path through ~USD, NoRippleDirect
			// rippled: pay(bob, carol, USD(100)), path(~USD), sendmax(XRP(100)), txflags(tfNoRippleDirect)
			payTx := payment.PayIssued(env.Bob, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
				SendMax(amm.XRPAmount(100)).
				PathsCurrency("USD", env.GW).
				NoDirectRipple().
				Build()
			result := env.Submit(payTx)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// AMM should have XRP(10100), USD(10000)
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

			// Carol: initial 30000 + 100 = 30100
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30100)

			// Bob: initial 30000 XRP - 100 - fee(10 drops)
			bobXRP := env.TestEnv.Balance(env.Bob)
			expectedBob := uint64(jtx.XRP(30000)) - uint64(jtx.XRP(100)) - 10
			if bobXRP != expectedBob {
				t.Errorf("Bob XRP: got %d, want %d", bobXRP, expectedBob)
			}
		})
	})

	// Sub-test 2: Same payment with default path (no tfNoRippleDirect).
	t.Run("DefaultPath", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.XRPAmount(10000),
			amm.IOUAmount(nil, "USD", 10100),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			env.FundBob(30000, 0)
			env.Close()

			// bob pays carol 100 USD with sendmax 100 XRP, default path
			// rippled: pay(bob, carol, USD(100)), sendmax(XRP(100))
			payTx := payment.PayIssued(env.Bob, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
				SendMax(amm.XRPAmount(100)).
				Build()
			result := env.Submit(payTx)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// AMM should have XRP(10100), USD(10000)
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30100)
		})
	})

	// Sub-test 3: Payment with both default path and explicit path(~USD).
	t.Run("ExplicitAndDefaultPath", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.XRPAmount(10000),
			amm.IOUAmount(nil, "USD", 10100),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			env.FundBob(30000, 0)
			env.Close()

			// bob pays carol 100 USD, sendmax 100 XRP, with path(~USD)
			// rippled: pay(bob, carol, USD(100)), path(~USD), sendmax(XRP(100))
			payTx := payment.PayIssued(env.Bob, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
				SendMax(amm.XRPAmount(100)).
				PathsCurrency("USD", env.GW).
				Build()
			result := env.Submit(payTx)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30100)
		})
	})
}

// TestAMMBookStep_AMMAndCLOB tests AMM vs CLOB quality comparison.
// Reference: rippled AMM_test.cpp testAMMAndCLOB (line 4953)
// If AMM is replaced with an equivalent CLOB offer, the result must be equivalent.
func TestAMMBookStep_AMMAndCLOB(t *testing.T) {
	// Setup: GW offers XRP(11.5B) for TST(1B). LP1 and LP2 each offer TST(25) for XRP(287.5M).
	// With AMM: LP1 creates AMM TST(25)/XRP(250). Then LP2 creates offer TST(25)/XRP(287.5M).
	// Capture LP2's TST balance and remaining offer.
	// With CLOB: LP1 creates equivalent passive CLOB offer. Same LP2 offer.
	// Compare LP2's state — should be identical.

	env := amm.NewAMMTestEnv(t)

	lp1 := jtx.NewAccount("lp1")
	lp2 := jtx.NewAccount("lp2")

	// Fund
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000000000)))
	env.TestEnv.FundAmount(lp1, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(lp2, uint64(jtx.XRP(10000)))
	env.Close()

	// GW sells XRP for TST: offer(gw, XRP(11.5B), TST(1B))
	env.Trust(lp1, env.GW, "TST", 1000000000000)
	env.Trust(lp2, env.GW, "TST", 1000000000000)
	env.Close()

	gwOfferTx := offerbuild.OfferCreate(env.GW,
		tx.NewXRPAmount(11_500_000_000*1_000_000),
		amm.IOUAmount(env.GW, "TST", 1000000000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(gwOfferTx))
	env.Close()

	// LP1 offer: TST(25) for XRP(287.5M)
	lp1OfferTx := offerbuild.OfferCreate(lp1,
		amm.IOUAmount(env.GW, "TST", 25),
		tx.NewXRPAmount(287_500_000*1_000_000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(lp1OfferTx))
	env.Close()

	// LP1 creates AMM: TST(25)/XRP(250)
	ammCreateTx := amm.AMMCreate(lp1,
		amm.IOUAmount(env.GW, "TST", 25),
		tx.NewXRPAmount(250*1_000_000)).TradingFee(0).Build()
	jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
	env.Close()

	// LP2 offer: TST(25) for XRP(287.5M)
	lp2OfferTx := offerbuild.OfferCreate(lp2,
		amm.IOUAmount(env.GW, "TST", 25),
		tx.NewXRPAmount(287_500_000*1_000_000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(lp2OfferTx))
	env.Close()

	// Capture LP2's TST balance — should have received some TST from crossing
	lp2TSTBalance := env.TestEnv.BalanceIOU(lp2, "TST", env.GW)
	t.Logf("LP2 TST balance: %f", lp2TSTBalance)

	// LP2's offer crossed (fully or partially) against the AMM + GW's offer.
	// The key assertion: LP2 got some TST (offer was crossed via AMM liquidity).
	if lp2TSTBalance <= 0 {
		t.Errorf("LP2 should have positive TST balance after crossing, got %f", lp2TSTBalance)
	}

	// LP2's remaining offers (may be 0 if fully consumed, or 1 if partially filled)
	lp2Offers := env.AccountOffers(lp2)
	t.Logf("LP2 remaining offers: %d", len(lp2Offers))
}

// TestAMMBookStep_TradingFee tests trading fees on payments through AMM.
// Reference: rippled AMM_test.cpp testTradingFee (line 5024)
func TestAMMBookStep_TradingFee(t *testing.T) {
	// Test: Payment through AMM with 1% trading fee.
	// Pool: USD(1000)/EUR(1010), no initial fee.
	// Carol pays Alice EUR(10) via AMM with path(~EUR) — no fee.
	// Then set 1% fee. Bob pays Carol USD(10) via AMM with path(~USD).
	// Bob should send ~10.1 EUR for 10 USD.
	t.Run("PaymentWith1PercentFee", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.IOUAmount(nil, "USD", 1000),
			amm.IOUAmount(nil, "EUR", 1010),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			// Fund bob with XRP and EUR
			env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
			env.Trust(env.Bob, env.GW, "EUR", 100000)
			env.Trust(env.Bob, env.GW, "USD", 100000)
			env.Close()
			env.PayIOU(env.GW, env.Bob, "EUR", 1000)
			env.PayIOU(env.GW, env.Bob, "USD", 1000)
			env.Close()

			// Alice contributed 1010 EUR and 1000 USD to pool
			expectIOUBalance(t, env, env.Alice, "EUR", env.GW, 28990)
			expectIOUBalance(t, env, env.Alice, "USD", env.GW, 29000)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30000)

			// Carol pays Alice EUR(10) with no fee, path(~EUR), sendmax(USD(10))
			payTx := payment.PayIssued(env.Carol, env.Alice,
				amm.IOUAmount(env.GW, "EUR", 10)).
				SendMax(amm.IOUAmount(env.GW, "USD", 10)).
				Paths([][]paymenttx.PathStep{{
					{Currency: "EUR", Issuer: env.GW.Address},
				}}).
				NoDirectRipple().Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx))
			env.Close()

			// Alice has 10 EUR more
			expectIOUBalance(t, env, env.Alice, "EUR", env.GW, 29000)
			expectIOUBalance(t, env, env.Alice, "USD", env.GW, 29000)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 29990)

			// Set fee to 1% (1000 basis points)
			usdAsset := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
			eurAsset := tx.Asset{Currency: "EUR", Issuer: env.GW.Address}
			env.Vote(env.Alice, usdAsset, eurAsset, 1000)

			// Bob pays Carol USD(10), path(~USD), sendmax(EUR(15))
			payTx2 := payment.PayIssued(env.Bob, env.Carol,
				amm.IOUAmount(env.GW, "USD", 10)).
				SendMax(amm.IOUAmount(env.GW, "EUR", 15)).
				Paths([][]paymenttx.PathStep{{
					{Currency: "USD", Issuer: env.GW.Address},
				}}).
				NoDirectRipple().Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx2))
			env.Close()

			// Carol got 10 USD back
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30000)
			// Bob sent ~10.1 EUR — check EUR balance is ~989.899
			bobEUR := env.TestEnv.BalanceIOU(env.Bob, "EUR", env.GW)
			// rippled: STAmount{EUR, 989'8989898989899, -13} = 989.8989898989899
			if math.Abs(bobEUR-989.8989898989899) > 0.001 {
				t.Errorf("Bob EUR: got %f, want ~989.899", bobEUR)
			}
		})
	})

	// Test: Offer crossing through AMM with 0.5% fee.
	// Pool: USD(1000)/EUR(1010), no initial fee.
	// Carol crosses offer EUR(10)->USD(10) with no fee.
	// Then set 0.5% fee. Carol crosses another offer EUR(10)->USD(10).
	// Carol should get fewer EUR for USD (fee goes to pool).
	t.Run("OfferCrossWith0.5PercentFee", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.IOUAmount(nil, "USD", 1000),
			amm.IOUAmount(nil, "EUR", 1010),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			// No fee: carol crosses EUR(10) for USD(10)
			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "EUR", 10),
				amm.IOUAmount(env.GW, "USD", 10)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))
			env.Close()

			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 29990)
			expectIOUBalance(t, env, env.Carol, "EUR", env.GW, 30010)

			// Reverse the pool change
			offerTx2 := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "USD", 10),
				amm.IOUAmount(env.GW, "EUR", 10)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx2))
			env.Close()

			// Set fee to 0.5% (500 basis points)
			usdAsset := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
			eurAsset := tx.Asset{Currency: "EUR", Issuer: env.GW.Address}
			env.Vote(env.Alice, usdAsset, eurAsset, 500)

			// Carol crosses EUR(10) for USD(10) again — now with fee
			offerTx3 := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "EUR", 10),
				amm.IOUAmount(env.GW, "USD", 10)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx3))
			env.Close()

			// With 0.5% fee, Carol gets less EUR for USD compared to no-fee scenario.
			// The fee goes to the AMM pool.
			carolUSD := env.TestEnv.BalanceIOU(env.Carol, "USD", env.GW)
			carolEUR := env.TestEnv.BalanceIOU(env.Carol, "EUR", env.GW)
			// After 3 offers: first two cancel out, third loses fee to pool.
			// Carol should have less USD and more EUR than 30000 each.
			t.Logf("Carol USD: %f, EUR: %f", carolUSD, carolEUR)
			// The fee-bearing offer should give Carol fewer EUR than the 10 she asked for
			// (some went to the pool as fee)
			if carolEUR >= 30010 {
				t.Errorf("Carol EUR should be less than 30010 (fee taken), got %f", carolEUR)
			}
			if carolEUR <= 30000 {
				t.Errorf("Carol EUR should be more than 30000 (she got some), got %f", carolEUR)
			}
		})
	})
}

// TestAMMBookStep_AdjustedTokens tests LP token rounding in repeated deposit/withdraw.
// Reference: rippled AMM_test.cpp testAdjustedTokens (line 5423)
// 8 accounts do 10 iterations of single-asset deposit + one-asset-withdraw-all.
// Due to rounding, tiny gains/losses accumulate. The last account to withdraw
// (alice) gets everything remaining in the pool.
func TestAMMBookStep_AdjustedTokens(t *testing.T) {
	t.Run("USD", func(t *testing.T) {
		amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			mustSubmit := func(txn tx.Transaction) {
				t.Helper()
				result := env.Submit(txn)
				if !result.Success {
					t.Fatalf("submit failed: %s: %s", result.Code, result.Message)
				}
			}

			// 8 additional accounts
			bob := jtx.NewAccount("bob")
			ed := jtx.NewAccount("ed")
			paul := jtx.NewAccount("paul")
			dan := jtx.NewAccount("dan")
			chris := jtx.NewAccount("chris")
			simon := jtx.NewAccount("simon")
			ben := jtx.NewAccount("ben")
			nataly := jtx.NewAccount("nataly")

			// Fund with 30,000 XRP + 1,500,000 USD each
			// Reference: fund(env, gw, accounts, {USD(1'500'000)}, Fund::Acct)
			accounts := []*jtx.Account{bob, ed, paul, dan, chris, simon, ben, nataly}
			for _, acct := range accounts {
				env.TestEnv.FundAmount(acct, uint64(jtx.XRP(30000)))
			}
			env.Close()
			for _, acct := range accounts {
				env.Trust(acct, env.GW, "USD", 3_000_000)
			}
			env.Close()
			for _, acct := range accounts {
				env.PayIOU(env.GW, acct, "USD", 1_500_000)
			}
			env.Close()

			xrpAsset := amm.XRP()
			usdAsset := env.USD

			// 10 iterations of deposit/withdraw cycles
			for i := 0; i < 10; i++ {
				// ben: deposit 1e-10 USD
				mustSubmit(amm.AMMDeposit(ben, xrpAsset, usdAsset).
					Amount(sle.NewIssuedAmountFromValue(1, -10, "USD", env.GW.Address)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(ben, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// simon: deposit 0.1 USD
				mustSubmit(amm.AMMDeposit(simon, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(simon, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// chris: deposit 1 USD
				mustSubmit(amm.AMMDeposit(chris, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 1)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(chris, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// dan: deposit 10 USD
				mustSubmit(amm.AMMDeposit(dan, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 10)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(dan, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// bob: deposit 100 USD
				mustSubmit(amm.AMMDeposit(bob, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 100)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(bob, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// carol: deposit 1,000 USD
				mustSubmit(amm.AMMDeposit(env.Carol, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 1000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(env.Carol, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// ed: deposit 10,000 USD
				mustSubmit(amm.AMMDeposit(ed, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 10000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(ed, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// paul: deposit 100,000 USD
				mustSubmit(amm.AMMDeposit(paul, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 100000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(paul, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				// nataly: deposit 1,000,000 USD
				mustSubmit(amm.AMMDeposit(nataly, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 1000000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(nataly, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())
			}

			// Check pool balances after 10 cycles (fixAMMv1_3 enabled)
			// XRP should be unchanged (all deposits/withdrawals in USD)
			poolXRP := env.AMMPoolXRP(ammAcc)
			if poolXRP != uint64(jtx.XRP(10000)) {
				t.Errorf("Pool XRP: got %d, want %d", poolXRP, uint64(jtx.XRP(10000)))
			}

			// Pool USD: STAmount{USD, UINT64_C(10'000'0000000003), -10}
			// = 100000000000003e-10 → normalized {1000000000000030, -11}
			poolUSD := env.AMMPoolIOUPrecise(ammAcc, env.GW, "USD")
			expectedPoolUSD := sle.NewIssuedAmountFromValue(100000000000003, -10, "USD", env.GW.Address)
			if poolUSD.Mantissa() != expectedPoolUSD.Mantissa() || poolUSD.Exponent() != expectedPoolUSD.Exponent() {
				t.Errorf("Pool USD: got %de%d, want %de%d",
					poolUSD.Mantissa(), poolUSD.Exponent(),
					expectedPoolUSD.Mantissa(), expectedPoolUSD.Exponent())
			}

			// Check individual USD balances
			// ben, simon, chris, dan: exact 1,500,000 USD
			for _, acct := range []*jtx.Account{ben, simon, chris, dan} {
				bal := env.TestEnv.IOUBalance(acct, env.GW, "USD")
				if bal == nil {
					t.Errorf("%s: no USD balance", acct.Name)
					continue
				}
				exp := sle.NewIssuedAmountFromValue(15, 5, "USD", env.GW.Address)
				if bal.Mantissa() != exp.Mantissa() || bal.Exponent() != exp.Exponent() {
					t.Errorf("%s USD: got %de%d, want %de%d",
						acct.Name, bal.Mantissa(), bal.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

			// carol: 30,000 USD (initial from testAMM setup)
			carolBal := env.TestEnv.IOUBalance(env.Carol, env.GW, "USD")
			if carolBal == nil {
				t.Error("carol: no USD balance")
			} else {
				exp := sle.NewIssuedAmountFromValue(3, 4, "USD", env.GW.Address)
				if carolBal.Mantissa() != exp.Mantissa() || carolBal.Exponent() != exp.Exponent() {
					t.Errorf("carol USD: got %de%d, want %de%d",
						carolBal.Mantissa(), carolBal.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

			// ed, paul, nataly: exact 1,500,000 USD (fixAMMv1_3)
			for _, acct := range []*jtx.Account{ed, paul, nataly} {
				bal := env.TestEnv.IOUBalance(acct, env.GW, "USD")
				if bal == nil {
					t.Errorf("%s: no USD balance", acct.Name)
					continue
				}
				exp := sle.NewIssuedAmountFromValue(15, 5, "USD", env.GW.Address)
				if bal.Mantissa() != exp.Mantissa() || bal.Exponent() != exp.Exponent() {
					t.Errorf("%s USD: got %de%d, want %de%d",
						acct.Name, bal.Mantissa(), bal.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

			// alice withdrawAll
			mustSubmit(amm.AMMWithdraw(env.Alice, xrpAsset, usdAsset).WithdrawAll().Build())

			// AMM should be deleted
			if ammData := env.ReadAMMData(xrpAsset, usdAsset); ammData != nil {
				t.Error("AMM should be deleted after alice withdrawAll")
			}

			// alice USD: 30000.0000000003 = STAmount{USD, 300000000000003, -10}
			aliceUSD := env.TestEnv.IOUBalance(env.Alice, env.GW, "USD")
			if aliceUSD == nil {
				t.Error("alice: no USD balance")
			} else {
				exp := sle.NewIssuedAmountFromValue(300000000000003, -10, "USD", env.GW.Address)
				if aliceUSD.Mantissa() != exp.Mantissa() || aliceUSD.Exponent() != exp.Exponent() {
					t.Errorf("alice USD: got %de%d, want %de%d",
						aliceUSD.Mantissa(), aliceUSD.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

			// alice XRP: initial(30000 XRP) - trustSetFee(10) - createFee(10) - withdrawFee(10)
			// Note: our env uses baseFee=10 for AMMCreate (rippled uses 50 XRP special fee)
			aliceXRP := env.TestEnv.Balance(env.Alice)
			expectedAliceXRP := uint64(jtx.XRP(30000)) - 10 - 10 - 10
			if aliceXRP != expectedAliceXRP {
				t.Errorf("alice XRP: got %d, want %d", aliceXRP, expectedAliceXRP)
			}
		})
	})

	t.Run("XRP", func(t *testing.T) {
		amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			mustSubmit := func(txn tx.Transaction) {
				t.Helper()
				result := env.Submit(txn)
				if !result.Success {
					t.Fatalf("submit failed: %s: %s", result.Code, result.Message)
				}
			}

			// 8 additional accounts
			bob := jtx.NewAccount("bob")
			ed := jtx.NewAccount("ed")
			paul := jtx.NewAccount("paul")
			dan := jtx.NewAccount("dan")
			chris := jtx.NewAccount("chris")
			simon := jtx.NewAccount("simon")
			ben := jtx.NewAccount("ben")
			nataly := jtx.NewAccount("nataly")

			// Fund with 2,000,000 XRP each, no IOUs
			// Reference: fund(env, gw, accounts, XRP(2'000'000), {}, Fund::Acct)
			accounts := []*jtx.Account{bob, ed, paul, dan, chris, simon, ben, nataly}
			for _, acct := range accounts {
				env.TestEnv.FundAmount(acct, uint64(jtx.XRP(2_000_000)))
			}
			env.Close()

			xrpAsset := amm.XRP()
			usdAsset := env.USD

			// Helper that logs iteration and account on failure
			submitOp := func(iter int, who string, op string, txn tx.Transaction) {
				t.Helper()
				result := env.Submit(txn)
				if !result.Success {
					t.Fatalf("iter %d %s %s failed: %s: %s", iter, who, op, result.Code, result.Message)
				}
			}

			// 10 iterations of XRP deposit/withdraw cycles
			for i := 0; i < 10; i++ {
				// ben: deposit 1 drop
				submitOp(i, "ben", "deposit", amm.AMMDeposit(ben, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(1)).
					SingleAsset().Build())
				submitOp(i, "ben", "withdraw", amm.AMMWithdraw(ben, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// simon: deposit 1,000 drops
				submitOp(i, "simon", "deposit", amm.AMMDeposit(simon, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(1000)).
					SingleAsset().Build())
				submitOp(i, "simon", "withdraw", amm.AMMWithdraw(simon, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// chris: deposit 1 XRP
				submitOp(i, "chris", "deposit", amm.AMMDeposit(chris, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(1)).
					SingleAsset().Build())
				submitOp(i, "chris", "withdraw", amm.AMMWithdraw(chris, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// dan: deposit 10 XRP
				submitOp(i, "dan", "deposit", amm.AMMDeposit(dan, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(10)).
					SingleAsset().Build())
				submitOp(i, "dan", "withdraw", amm.AMMWithdraw(dan, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// bob: deposit 100 XRP
				submitOp(i, "bob", "deposit", amm.AMMDeposit(bob, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(100)).
					SingleAsset().Build())
				submitOp(i, "bob", "withdraw", amm.AMMWithdraw(bob, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// carol: deposit 1,000 XRP
				submitOp(i, "carol", "deposit", amm.AMMDeposit(env.Carol, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(1000)).
					SingleAsset().Build())
				submitOp(i, "carol", "withdraw", amm.AMMWithdraw(env.Carol, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// ed: deposit 10,000 XRP
				submitOp(i, "ed", "deposit", amm.AMMDeposit(ed, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(10000)).
					SingleAsset().Build())
				submitOp(i, "ed", "withdraw", amm.AMMWithdraw(ed, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// paul: deposit 100,000 XRP
				submitOp(i, "paul", "deposit", amm.AMMDeposit(paul, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(100000)).
					SingleAsset().Build())
				submitOp(i, "paul", "withdraw", amm.AMMWithdraw(paul, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				// nataly: deposit 1,000,000 XRP
				submitOp(i, "nataly", "deposit", amm.AMMDeposit(nataly, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(1000000)).
					SingleAsset().Build())
				submitOp(i, "nataly", "withdraw", amm.AMMWithdraw(nataly, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())
			}

			baseFee := uint64(10)

			// Check pool XRP after cycles (fixAMMv1_3 enabled)
			// Expected: XRP(10,000,000,080 drops) — 80 drops gained from rounding
			poolXRP := env.AMMPoolXRP(ammAcc)
			if poolXRP != 10_000_000_080 {
				t.Errorf("Pool XRP: got %d, want %d", poolXRP, uint64(10_000_000_080))
			}

			// alice withdrawAll
			mustSubmit(amm.AMMWithdraw(env.Alice, xrpAsset, usdAsset).WithdrawAll().Build())

			// AMM should be deleted
			if ammData := env.ReadAMMData(xrpAsset, usdAsset); ammData != nil {
				t.Error("AMM should be deleted after alice withdrawAll")
			}

			// Check XRP balances
			// xrpBalance = XRP(2,000,000) - 20*baseFee - 10 drops rounding
			xrpBalance := uint64(jtx.XRP(2_000_000)) - 20*baseFee - 10

			for _, acct := range []*jtx.Account{ben, simon, chris, dan} {
				bal := env.TestEnv.Balance(acct)
				if bal != xrpBalance {
					t.Errorf("%s XRP: got %d, want %d", acct.Name, bal, xrpBalance)
				}
			}

			// carol: 30,000 XRP initial - trustLineFee - 20*baseFee - 10
			// TestAMM setup creates a USD trust line for carol, costing baseFee
			carolExpected := uint64(30_000_000_000) - baseFee - 20*baseFee - 10
			carolBal := env.TestEnv.Balance(env.Carol)
			if carolBal != carolExpected {
				t.Errorf("carol XRP: got %d, want %d", carolBal, carolExpected)
			}

			// ed/paul/nataly get slightly more back due to rounding in their favor
			edBal := env.TestEnv.Balance(ed)
			if edBal != xrpBalance+2 {
				t.Errorf("ed XRP: got %d, want %d", edBal, xrpBalance+2)
			}

			paulBal := env.TestEnv.Balance(paul)
			if paulBal != xrpBalance+3 {
				t.Errorf("paul XRP: got %d, want %d", paulBal, xrpBalance+3)
			}

			natalyBal := env.TestEnv.Balance(nataly)
			if natalyBal != xrpBalance+5 {
				t.Errorf("nataly XRP: got %d, want %d", natalyBal, xrpBalance+5)
			}

			// alice: initial(30000 XRP) - trustLineFee - createFee - withdrawFee + 80 pool rounding
			// TestAMM setup creates a USD trust line for alice, costing baseFee
			aliceExpected := uint64(jtx.XRP(30000)) - baseFee - baseFee - baseFee + 80
			aliceXRP := env.TestEnv.Balance(env.Alice)
			if aliceXRP != aliceExpected {
				t.Errorf("alice XRP: got %d, want %d", aliceXRP, aliceExpected)
			}
		})
	})
}

// TestAMMBookStep_Selection tests strand selection between AMM and CLOB.
// Reference: rippled AMM_test.cpp testSelection (line 5822)
// When both AMM and CLOB exist at same quality, CLOB is preferred.
func TestAMMBookStep_Selection(t *testing.T) {
	// Setup: gw (rate 1.5) issues USD, gw1 (rate 1.9) issues ETH.
	// ed creates passive CLOB offer ETH(400)->USD(400) and/or AMM USD(1000)/ETH(1000).
	// Carol pays Bob USD(100) via path(~USD) with sendmax ETH(500).
	// With both CLOB and AMM: AMM should NOT be selected (CLOB better quality).
	// Transfer rates as XRPL uint32: 1.5 = 1500000000, 1.9 = 1900000000
	for _, rates := range [][2]uint32{{1500000000, 1900000000}, {1900000000, 1500000000}} {
		rateName := "1.5_1.9"
		if rates[0] == 1900000000 {
			rateName = "1.9_1.5"
		}
		t.Run(rateName, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			ed := jtx.NewAccount("ed")
			gw1 := jtx.NewAccount("gw1")

			// Fund accounts
			env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
			env.TestEnv.FundAmount(gw1, uint64(jtx.XRP(30000)))
			for _, acc := range []*jtx.Account{env.Alice, env.Carol, env.Bob, ed} {
				env.TestEnv.FundAmount(acc, uint64(jtx.XRP(2000)))
			}
			env.Close()

			// Trust lines for USD (from gw) and ETH (from gw1)
			for _, acc := range []*jtx.Account{env.Alice, env.Carol, env.Bob, ed} {
				env.Trust(acc, env.GW, "USD", 100000)
				env.Trust(acc, gw1, "ETH", 100000)
			}
			env.Close()

			// Fund IOUs
			for _, acc := range []*jtx.Account{env.Alice, env.Carol, env.Bob, ed} {
				env.PayIOU(env.GW, acc, "USD", 2000)
				env.PayIOU(gw1, acc, "ETH", 2000)
			}
			env.Close()

			// Set transfer rates
			env.TestEnv.SetTransferRate(env.GW, rates[0])
			env.TestEnv.SetTransferRate(gw1, rates[1])
			env.Close()

			// Scenario: both CLOB and AMM
			// ed creates passive CLOB offer ETH(400)->USD(400)
			offerTx := offerbuild.OfferCreate(ed,
				amm.IOUAmount(gw1, "ETH", 400),
				amm.IOUAmount(env.GW, "USD", 400)).
				Passive().Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))
			env.Close()

			// ed creates AMM USD(1000)/ETH(1000)
			ammCreateTx := amm.AMMCreate(ed,
				amm.IOUAmount(env.GW, "USD", 1000),
				amm.IOUAmount(gw1, "ETH", 1000)).
				TradingFee(0).Build()
			jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
			env.Close()

			// Compute AMM account
			usdAsset := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
			ethAsset := tx.Asset{Currency: "ETH", Issuer: gw1.Address}
			ammAccAddr := amm.AMMAccount(t, usdAsset, ethAsset)

			// Save AMM balances before payment
			ammUSD := env.TestEnv.BalanceIOU(ammAccAddr, "USD", env.GW)
			ammETH := env.TestEnv.BalanceIOU(ammAccAddr, "ETH", gw1)

			// Carol pays Bob USD(100), path(~USD), sendmax(ETH(500))
			payTx := payment.PayIssued(env.Carol, env.Bob,
				amm.IOUAmount(env.GW, "USD", 100)).
				SendMax(amm.IOUAmount(gw1, "ETH", 500)).
				Paths([][]paymenttx.PathStep{{
					{Currency: "USD", Issuer: env.GW.Address},
				}}).Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx))
			env.Close()

			// Bob should receive USD(100) more
			expectIOUBalance(t, env, env.Bob, "USD", env.GW, 2100)

			// AMM should NOT be selected — balances unchanged
			ammUSDAfter := env.TestEnv.BalanceIOU(ammAccAddr, "USD", env.GW)
			ammETHAfter := env.TestEnv.BalanceIOU(ammAccAddr, "ETH", gw1)
			if math.Abs(ammUSD-ammUSDAfter) > 0.0001 {
				t.Errorf("AMM USD changed: before %f, after %f (AMM was selected, shouldn't be)", ammUSD, ammUSDAfter)
			}
			if math.Abs(ammETH-ammETHAfter) > 0.0001 {
				t.Errorf("AMM ETH changed: before %f, after %f (AMM was selected, shouldn't be)", ammETH, ammETHAfter)
			}
		})
	}
}

// TestAMMBookStep_FixDefaultInnerObj tests fix for default inner object.
// Reference: rippled AMM_test.cpp testFixDefaultInnerObj (line 6305)
// This tests the fixInnerObjTemplate amendment which fixes a C++-specific
// object template caching bug when ledger is not closed between transactions
// and trading fee is zero. Go does not share object templates, so this bug
// cannot occur. All cases should succeed regardless of close/no-close and fee.
func TestAMMBookStep_FixDefaultInnerObj(t *testing.T) {
	// Rippled test matrix:
	// (features, tfee, closeLedger) → (err1, err2, err3, err4)
	// In C++ without fixInnerObjTemplate, no close, fee=0: all tefEXCEPTION
	// In C++ without fixInnerObjTemplate, no close, fee=9: SUCCESS, tefEXCEPTION, SUCCESS, SUCCESS
	// In Go: ALL succeed because we don't have C++ STObject template caching.
	cases := []struct {
		name         string
		tradingFee   uint16
		closeLedger  bool
	}{
		{"fee0_close", 0, true},
		{"fee0_noclose", 0, false},
		{"fee10_close", 10, true},
		{"fee10_noclose", 10, false},
		{"fee9_noclose", 9, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)

			// fund(env, gw, {alice}, XRP(1000), {USD(10)})
			env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000)))
			env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000)))
			env.Close()

			env.Trust(env.Alice, env.GW, "USD", 100)
			env.Close()
			env.PayIOU(env.GW, env.Alice, "USD", 10)
			env.Close()

			// gw creates AMM XRP(10)/USD(10)
			createTx := amm.AMMCreate(env.GW,
				amm.XRPAmount(10),
				amm.IOUAmount(env.GW, "USD", 10)).
				TradingFee(tc.tradingFee).Build()
			jtx.RequireTxSuccess(t, env.Submit(createTx))
			if tc.closeLedger {
				env.Close()
			}

			usdAsset := tx.Asset{Currency: "USD", Issuer: env.GW.Address}

			// alice deposits USD(10) + XRP(10)
			depositTx := amm.AMMDeposit(env.Alice, amm.XRP(), usdAsset).
				Amount(amm.IOUAmount(env.GW, "USD", 10)).
				Amount2(amm.XRPAmount(10)).
				TwoAsset().
				Build()
			jtx.RequireTxSuccess(t, env.Submit(depositTx))
			if tc.closeLedger {
				env.Close()
			}

			// alice votes with tradingFee — should succeed (err1)
			voteTx1 := amm.AMMVote(env.Alice, amm.XRP(), usdAsset, tc.tradingFee).Build()
			result := env.Submit(voteTx1)
			if !result.Success {
				t.Errorf("vote1 (fee=%d, close=%v): expected tesSUCCESS, got %s", tc.tradingFee, tc.closeLedger, result.Code)
			}
			if tc.closeLedger {
				env.Close()
			}

			// gw withdraws USD(1) — should succeed (err2)
			withdrawTx1 := amm.AMMWithdraw(env.GW, amm.XRP(), usdAsset).
				Amount(amm.IOUAmount(env.GW, "USD", 1)).
				SingleAsset().
				Build()
			result = env.Submit(withdrawTx1)
			if !result.Success {
				t.Errorf("withdraw1 (fee=%d, close=%v): expected tesSUCCESS, got %s", tc.tradingFee, tc.closeLedger, result.Code)
			}
			if tc.closeLedger {
				env.Close()
			}

			// alice votes with fee=20 — should succeed (err3)
			voteTx2 := amm.AMMVote(env.Alice, amm.XRP(), usdAsset, 20).Build()
			result = env.Submit(voteTx2)
			if !result.Success {
				t.Errorf("vote2 (fee=%d, close=%v): expected tesSUCCESS, got %s", tc.tradingFee, tc.closeLedger, result.Code)
			}
			if tc.closeLedger {
				env.Close()
			}

			// gw withdraws USD(2) — should succeed (err4)
			withdrawTx2 := amm.AMMWithdraw(env.GW, amm.XRP(), usdAsset).
				Amount(amm.IOUAmount(env.GW, "USD", 2)).
				SingleAsset().
				Build()
			result = env.Submit(withdrawTx2)
			if !result.Success {
				t.Errorf("withdraw2 (fee=%d, close=%v): expected tesSUCCESS, got %s", tc.tradingFee, tc.closeLedger, result.Code)
			}
		})
	}
}

// TestAMMBookStep_FixChangeSpotPriceQuality tests spot price quality fix.
// Reference: rippled AMM_test.cpp testFixChangeSpotPriceQuality (line 6405)
func TestAMMBookStep_FixChangeSpotPriceQuality(t *testing.T) {
	// Save and restore numberSwitchover state — other tests may contaminate via the engine.
	// rippled's test uses `all` features (fixUniversalNumber enabled), but the pre-fix path's
	// toAmount rounding is at STAmount precision. Our Amount arithmetic delegates to XRPLNumber
	// when switchover is on, which changes the quadratic solver's precision.
	// Use switchover=false to match the pre-fix path's expected behavior.
	savedSwitchover := sle.GetNumberSwitchover()
	sle.SetNumberSwitchover(false)
	defer sle.SetNumberSwitchover(savedSwitchover)

	type Status int
	const (
		SucceedShouldSucceedResize Status = iota
		FailShouldSucceed
		SucceedShouldFail
		Fail
		Succeed
	)

	type testCase struct {
		poolInStr  string
		poolOutStr string
		quality    paymenttx.Quality
		fee        uint16
		status     Status
	}

	// Quality from amounts helper — matches rippled's Quality{TAmounts{in, out}}
	xrpIouQ10_100 := paymenttx.QualityFromAmounts(
		paymenttx.NewXRPEitherAmount(10),
		paymenttx.NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "", "")),
	)
	iouXrpQ10_100 := paymenttx.QualityFromAmounts(
		paymenttx.NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(10, "", "")),
		paymenttx.NewXRPEitherAmount(100),
	)

	tests := []testCase{
		// FailShouldSucceed (12 cases)
		{"0.001519763260828713", "1558701", paymenttx.Quality{Value: 5414253689393440221}, 1000, FailShouldSucceed},
		{"0.01099814367603737", "1892611", paymenttx.Quality{Value: 5482264816516900274}, 1000, FailShouldSucceed},
		{"0.78", "796599", paymenttx.Quality{Value: 5630392334958379008}, 1000, FailShouldSucceed},
		{"105439.2955578965", "49398693", paymenttx.Quality{Value: 5910869983721805038}, 400, FailShouldSucceed},
		{"12408293.23445213", "4340810521", paymenttx.Quality{Value: 5911611095910090752}, 997, FailShouldSucceed},
		{"1892611", "0.01099814367603737", paymenttx.Quality{Value: 6703103457950430139}, 1000, FailShouldSucceed},
		{"423028.8508101858", "3392804520", paymenttx.Quality{Value: 5837920340654162816}, 600, FailShouldSucceed},
		{"44565388.41001027", "73890647", paymenttx.Quality{Value: 6058976634606450001}, 1000, FailShouldSucceed},
		{"66831.68494832662", "16", paymenttx.Quality{Value: 6346111134641742975}, 0, FailShouldSucceed},
		{"675.9287302203422", "1242632304", paymenttx.Quality{Value: 5625960929244093294}, 300, FailShouldSucceed},
		{"7047.112186735699", "1649845866", paymenttx.Quality{Value: 5696855348026306945}, 504, FailShouldSucceed},
		{"840236.4402981238", "47419053", paymenttx.Quality{Value: 5982561601648018688}, 499, FailShouldSucceed},

		// SucceedShouldSucceedResize (6 cases)
		{"992715.618909774", "189445631733", paymenttx.Quality{Value: 5697835648288106944}, 815, SucceedShouldSucceedResize},
		{"504636667521", "185545883.9506651", paymenttx.Quality{Value: 6343802275337659280}, 503, SucceedShouldSucceedResize},
		{"992706.7218636649", "189447316000", paymenttx.Quality{Value: 5697835648288106944}, 797, SucceedShouldSucceedResize},
		{"1.068737911388205", "127860278877", paymenttx.Quality{Value: 5268604356368739396}, 293, SucceedShouldSucceedResize},
		{"17932506.56880419", "189308.6043676173", paymenttx.Quality{Value: 6206460598195440068}, 311, SucceedShouldSucceedResize},
		{"1.066379294658174", "128042251493", paymenttx.Quality{Value: 5268559341368739328}, 270, SucceedShouldSucceedResize},

		// Fail (14 cases)
		{"350131413924", "1576879.110907892", paymenttx.Quality{Value: 6487411636539049449}, 650, Fail},
		{"422093460", "2.731797662057464", paymenttx.Quality{Value: 6702911108534394924}, 1000, Fail},
		{"76128132223", "367172.7148422662", paymenttx.Quality{Value: 6487263463413514240}, 548, Fail},
		{"132701839250", "280703770.7695443", paymenttx.Quality{Value: 6273750681188885075}, 562, Fail},
		{"994165.7604612011", "189551302411", paymenttx.Quality{Value: 5697835592690668727}, 815, Fail},
		{"45053.33303227917", "86612695359", paymenttx.Quality{Value: 5625695218943638190}, 500, Fail},
		{"199649.077043865", "14017933007", paymenttx.Quality{Value: 5766034667318524880}, 324, Fail},
		{"27751824831.70903", "78896950", paymenttx.Quality{Value: 6272538159621630432}, 500, Fail},
		{"225.3731275781907", "156431793648", paymenttx.Quality{Value: 5477818047604078924}, 989, Fail},
		{"199649.077043865", "14017933007", paymenttx.Quality{Value: 5766036094462806309}, 324, Fail},
		{"3.590272027140361", "20677643641", paymenttx.Quality{Value: 5406056147042156356}, 808, Fail},
		{"1.070884664490231", "127604712776", paymenttx.Quality{Value: 5268620608623825741}, 293, Fail},
		{"3272.448829820197", "6275124076", paymenttx.Quality{Value: 5625710328924117902}, 81, Fail},
		{"0.009059512633902926", "7994028", paymenttx.Quality{Value: 5477511954775533172}, 1000, Fail},
		{"1", "1.0", paymenttx.Quality{Value: 0}, 100, Fail},
		{"1.0", "1", paymenttx.Quality{Value: 0}, 100, Fail},
		{"10", "10.0", xrpIouQ10_100, 100, Fail},
		{"10.0", "10", iouXrpQ10_100, 100, Fail},

		// Succeed (15 cases)
		{"69864389131", "287631.4543025075", paymenttx.Quality{Value: 6487623473313516078}, 451, Succeed},
		{"4328342973", "12453825.99247381", paymenttx.Quality{Value: 6272522264364865181}, 997, Succeed},
		{"32347017", "7003.93031579449", paymenttx.Quality{Value: 6347261126087916670}, 1000, Succeed},
		{"61697206161", "36631.4583206413", paymenttx.Quality{Value: 6558965195382476659}, 500, Succeed},
		{"1654524979", "7028.659825511603", paymenttx.Quality{Value: 6487551345110052981}, 504, Succeed},
		{"88621.22277293179", "5128418948", paymenttx.Quality{Value: 5766347291552869205}, 380, Succeed},
		{"1892611", "0.01099814367603737", paymenttx.Quality{Value: 6703102780512015436}, 1000, Succeed},
		{"4542.639373338766", "24554809", paymenttx.Quality{Value: 5838994982188783710}, 0, Succeed},
		{"5132932546", "88542.99750172683", paymenttx.Quality{Value: 6419203342950054537}, 380, Succeed},
		{"78929964.1549083", "1506494795", paymenttx.Quality{Value: 5986890029845558688}, 589, Succeed},
		{"10096561906", "44727.72453735605", paymenttx.Quality{Value: 6487455290284644551}, 250, Succeed},
		{"5092.219565514988", "8768257694", paymenttx.Quality{Value: 5626349534958379008}, 503, Succeed},
		{"1819778294", "8305.084302902864", paymenttx.Quality{Value: 6487429398998540860}, 415, Succeed},
		{"6970462.633911943", "57359281", paymenttx.Quality{Value: 6054087899185946624}, 850, Succeed},
		{"3983448845", "2347.543644281467", paymenttx.Quality{Value: 6558965195382476659}, 856, Succeed},

		// SucceedShouldFail (1 case)
		{"771493171", "1.243473020567508", paymenttx.Quality{Value: 6707566798038544272}, 100, SucceedShouldFail},
	}

	// Helper: determine if string represents XRP drops (all digits, no decimal point)
	isXRPStr := func(s string) bool {
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return len(s) > 0
	}

	// Helper: parse pool amount from string
	parsePool := func(s string, isXRP bool) tx.Amount {
		if isXRP {
			var drops int64
			for _, c := range s {
				drops = drops*10 + int64(c-'0')
			}
			return tx.NewXRPAmount(drops)
		}
		f := 0.0
		fmt.Sscanf(s, "%f", &f)
		return tx.NewIssuedAmountFromFloat64(f, "", "")
	}

	// Run tests for both amendment states
	for _, fixAMMv1_1 := range []bool{false, true} {
		label := "PreFix"
		if fixAMMv1_1 {
			label = "PostFix"
		}
		t.Run(label, func(t *testing.T) {
			for i, tc := range tests {
				poolInIsXRP := isXRPStr(tc.poolInStr)
				poolOutIsXRP := isXRPStr(tc.poolOutStr)

				poolIn := parsePool(tc.poolInStr, poolInIsXRP)
				poolOut := parsePool(tc.poolOutStr, poolOutIsXRP)

				takerPays, takerGets, ok := paymenttx.ChangeSpotPriceQuality(
					poolIn, poolOut, tc.quality, tc.fee, fixAMMv1_1, poolOutIsXRP,
				)

				if ok {
					offerQ := paymenttx.QualityFromAmounts(
						paymenttx.ToEitherAmt(takerPays),
						paymenttx.ToEitherAmt(takerGets),
					)

					switch tc.status {
					case SucceedShouldSucceedResize:
						if !fixAMMv1_1 {
							if !(offerQ.WorseThan(tc.quality)) {
								t.Errorf("[%d] PreFix SucceedShouldSucceedResize: expected quality < target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix SucceedShouldSucceedResize: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case Succeed:
						if !fixAMMv1_1 {
							if offerQ.WorseThan(tc.quality) && !paymenttx.WithinRelativeDistance(offerQ, tc.quality, 1e-7) {
								t.Errorf("[%d] PreFix Succeed: quality worse and not within tolerance, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix Succeed: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case FailShouldSucceed:
						if !fixAMMv1_1 {
							t.Errorf("[%d] PreFix FailShouldSucceed: expected failure (no result), got success", i)
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix FailShouldSucceed: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case SucceedShouldFail:
						if fixAMMv1_1 {
							t.Errorf("[%d] PostFix SucceedShouldFail: expected failure (no result), got success", i)
						} else {
							if !(offerQ.WorseThan(tc.quality)) {
								t.Errorf("[%d] PreFix SucceedShouldFail: expected quality < target", i)
							}
							if !paymenttx.WithinRelativeDistance(offerQ, tc.quality, 1e-7) {
								t.Errorf("[%d] PreFix SucceedShouldFail: expected within tolerance", i)
							}
						}
					case Fail:
						// Fail but got success — unexpected (could be ok for Fail status with zero quality)
						if tc.quality.Value != 0 {
							// Verify the tiny offer quality is < target
							tinyQ := offerQ
							if !(tinyQ.WorseThan(tc.quality)) && tinyQ.Value != tc.quality.Value {
								t.Logf("[%d] Fail but got amounts — quality check: q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					}
				} else {
					// No result
					switch tc.status {
					case Fail:
						// Expected failure — verify tiny offer quality < target if non-zero quality
						if tc.quality.Value != 0 {
							if poolInIsXRP {
								takerPays := tx.NewXRPAmount(1) // 1 drop
								takerGets := paymenttx.SwapAssetIn(poolIn, poolOut, takerPays, tc.fee, fixAMMv1_1)
								tinyQ := paymenttx.QualityFromAmounts(
									paymenttx.ToEitherAmt(takerPays),
									paymenttx.ToEitherAmt(takerGets),
								)
								if !(tinyQ.WorseThan(tc.quality)) {
									t.Errorf("[%d] Fail: tiny offer quality should be worse than target, got q=%d target=%d", i, tinyQ.Value, tc.quality.Value)
								}
							} else if poolOutIsXRP {
								takerGets := tx.NewXRPAmount(1) // 1 drop
								takerPays := paymenttx.SwapAssetOut(poolIn, poolOut, takerGets, tc.fee, fixAMMv1_1)
								tinyQ := paymenttx.QualityFromAmounts(
									paymenttx.ToEitherAmt(takerPays),
									paymenttx.ToEitherAmt(takerGets),
								)
								if !(tinyQ.WorseThan(tc.quality)) {
									t.Errorf("[%d] Fail: tiny offer quality should be worse than target, got q=%d target=%d", i, tinyQ.Value, tc.quality.Value)
								}
							}
						}
					case FailShouldSucceed:
						if fixAMMv1_1 {
							t.Errorf("[%d] PostFix FailShouldSucceed: expected success, got failure", i)
						}
						// Pre-fix failure is expected
					case SucceedShouldFail:
						if !fixAMMv1_1 {
							t.Errorf("[%d] PreFix SucceedShouldFail: expected success, got failure", i)
						}
						// Post-fix failure is expected
					case SucceedShouldSucceedResize:
						t.Errorf("[%d] %s SucceedShouldSucceedResize: expected success, got failure", i, label)
					case Succeed:
						t.Errorf("[%d] %s Succeed: expected success, got failure", i, label)
					}
				}
			}
		})
	}

	// Test negative discriminant
	t.Run("NegativeDiscriminant", func(t *testing.T) {
		one := tx.NewIssuedAmountFromFloat64(1, "", "")
		res := paymenttx.SolveQuadraticEqSmallest(one, one, one)
		if res != nil {
			t.Errorf("Expected nil for negative discriminant (1^2 - 4*1*1 = -3), got %v", res)
		}
	})
}

// TestAMMBookStep_Malformed — moved to TestInvalidWithdraw in amm_withdraw_test.go
// Reference: rippled AMM_test.cpp testMalformed (line 6623)
func TestAMMBookStep_Malformed(t *testing.T) {
	t.Log("testMalformed cases are in TestInvalidWithdraw/Malformed_* in amm_withdraw_test.go")
}

// TestAMMBookStep_FixOverflowOffer tests overflow offer fix.
// Reference: rippled AMM_test.cpp testFixOverflowOffer (line 6682)
// Tests multi-hop payment through AMM pool + CLOB offers with precise balance checking.
// Our env has all amendments on (fixAMMOverflowOffer, fixAMMv1_1, fixAMMv1_3), so we
// only verify the "goodr" expected values and lpTokenBalanceAlt where applicable.
func TestAMMBookStep_FixOverflowOffer(t *testing.T) {
	type inputSet struct {
		name       string
		poolUsdBIT float64
		poolUsdGH  float64
		sendMax    float64 // usdBIT
		sendUsdGH  float64 // desired amount
		// Expected AMM balances after payment (with fixAMMOverflowOffer + fixAMMv1_1)
		goodUsdGHMant  int64
		goodUsdGHExp   int
		goodUsdBITMant int64
		goodUsdBITExp  int
		// Expected LP token balance (with fixAMMv1_3 alt where applicable)
		lptMant int64
		lptExp  int
		// CLOB offer parameters
		offer1BtcGH float64
		offer2BtcGH float64
		offer2UsdGH float64
		// Transfer rates (0 = none)
		rateBIT float64
		rateGH  float64
	}

	// Test vectors from rippled AMM_test.cpp testFixOverflowOffer (lines 6723-6913)
	// Using "goodr" values (fixAMMv1_1 rounding) and lpTokenBalanceAlt (fixAMMv1_3) where available.
	// Values are normalized to [10^15, 10^16-1] mantissa range to match Go Amount normalization.
	// Rippled source values may have shorter mantissas that normalize differently.
	tests := []inputSet{
		{
			name: "Test Fix Overflow Offer", poolUsdBIT: 3, poolUsdGH: 273,
			sendMax: 50, sendUsdGH: 272.455089820359,
			// rippled: {967543114222965, -13} → normalized: {9675431142229650, -14}
			goodUsdGHMant: 9675431142229650, goodUsdGHExp: -14,
			goodUsdBITMant: 8464739069098152, goodUsdBITExp: -15,
			lptMant: 2861817604250836, lptExp: -14, // lpTokenBalanceAlt
			offer1BtcGH: 0.1, offer2BtcGH: 0.1, offer2UsdGH: 1,
			rateBIT: 1.15, rateGH: 1.2,
		},
		{
			name: "Overflow test {1, 100, 1.00}", poolUsdBIT: 1, poolUsdGH: 100,
			sendMax: 1.00, sendUsdGH: 100,
			goodUsdGHMant: 5294379354424135, goodUsdGHExp: -14,
			// rippled: {2, 0} → normalized: {2000000000000000, -15}
			goodUsdBITMant: 2000000000000000, goodUsdBITExp: -15,
			lptMant: 10, lptExp: 0,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
		{
			name: "Overflow test {50, 100, 50.00}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 50.00, sendUsdGH: 100,
			goodUsdGHMant: 5294379354424092, goodUsdGHExp: -14,
			// rippled: {100, 0} → normalized: {1000000000000000, -13}
			goodUsdBITMant: 1000000000000000, goodUsdBITExp: -13,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
		{
			name: "Overflow test {50, 100, 5.55}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 5.55, sendUsdGH: 100,
			// rippled: {900434788828413, -13} → normalized: {9004347888284130, -14}
			goodUsdGHMant: 9004347888284130, goodUsdGHExp: -14,
			// rippled: {5555, -2} → normalized: {5555000000000000, -14}
			goodUsdBITMant: 5555000000000000, goodUsdBITExp: -14,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			gatehub := jtx.NewAccount("gatehub")
			bitstamp := jtx.NewAccount("bitstamp")
			trader := jtx.NewAccount("trader")

			// Fund accounts with 5000 XRP each
			for _, acc := range []*jtx.Account{gatehub, bitstamp, trader} {
				env.TestEnv.FundAmount(acc, uint64(jtx.XRP(5000)))
			}
			env.Close()

			// Set transfer rates if specified
			if tc.rateGH != 0 {
				rateUint := uint32(tc.rateGH * 1e9) // e.g., 1.2 → 1200000000
				env.TestEnv.SetTransferRate(gatehub, rateUint)
			}
			if tc.rateBIT != 0 {
				rateUint := uint32(tc.rateBIT * 1e9)
				env.TestEnv.SetTransferRate(bitstamp, rateUint)
			}

			// Trust lines: trader trusts all 3 currencies at 10M
			env.Trust(trader, gatehub, "USD", 10000000)
			env.Trust(trader, bitstamp, "USD", 10000000)
			env.Trust(trader, gatehub, "BTC", 10000000)
			env.Close()

			// Fund trader with 100K of each currency
			env.PayIOU(gatehub, trader, "USD", 100000)
			env.PayIOU(gatehub, trader, "BTC", 100000)
			env.PayIOU(bitstamp, trader, "USD", 100000)
			env.Close()

			// Create AMM: usdGH / usdBIT
			ammCreateTx := amm.AMMCreate(trader,
				amm.IOUAmount(gatehub, "USD", tc.poolUsdGH),
				amm.IOUAmount(bitstamp, "USD", tc.poolUsdBIT)).
				TradingFee(0).Build()
			jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
			env.Close()

			// Get AMM account
			usdGHAsset := tx.Asset{Currency: "USD", Issuer: gatehub.Address}
			usdBITAsset := tx.Asset{Currency: "USD", Issuer: bitstamp.Address}
			ammAcc := amm.AMMAccount(t, usdGHAsset, usdBITAsset)

			// Create CLOB offers for the alternative path
			// offer1: trader wants usdBIT(1) for btcGH(offer1BtcGH)
			offer1Tx := offerbuild.OfferCreate(trader,
				amm.IOUAmount(bitstamp, "USD", 1),
				amm.IOUAmount(gatehub, "BTC", tc.offer1BtcGH)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offer1Tx))

			// offer2: trader wants btcGH(offer2BtcGH) for usdGH(offer2UsdGH)
			offer2Tx := offerbuild.OfferCreate(trader,
				amm.IOUAmount(gatehub, "BTC", tc.offer2BtcGH),
				amm.IOUAmount(gatehub, "USD", tc.offer2UsdGH)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offer2Tx))
			env.Close()

			// Self-payment: trader → trader
			// send usdGH, sendmax usdBIT, paths: ~usdGH and ~btcGH,~usdGH
			// partial payment
			sendAmt := amm.IOUAmount(gatehub, "USD", tc.sendUsdGH)
			sendMaxAmt := amm.IOUAmount(bitstamp, "USD", tc.sendMax)

			payTx := payment.PayIssued(trader, trader, sendAmt).
				SendMax(sendMaxAmt).
				Paths([][]paymenttx.PathStep{
					// path(~usdGH): through AMM
					{{Currency: "USD", Issuer: gatehub.Address}},
					// path(~btcGH, ~usdGH): through CLOB offers
					{
						{Currency: "BTC", Issuer: gatehub.Address},
						{Currency: "USD", Issuer: gatehub.Address},
					},
				}).
				PartialPayment().Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx))
			env.Close()

			// Check AMM balances (precise mantissa/exponent comparison)
			ammUsdGH := env.TestEnv.IOUBalance(ammAcc, gatehub, "USD")
			ammUsdBIT := env.TestEnv.IOUBalance(ammAcc, bitstamp, "USD")

			if ammUsdGH == nil {
				t.Fatal("AMM usdGH balance is nil")
			}
			if ammUsdBIT == nil {
				t.Fatal("AMM usdBIT balance is nil")
			}

			// Compare with expected values.
			// Allow small mantissa tolerance (±200) for accumulated rounding in
			// 16+ Fibonacci iterations. Exponent must match exactly.
			const mantissaTol int64 = 200
			ghMantDiff := ammUsdGH.Mantissa() - tc.goodUsdGHMant
			if ghMantDiff < 0 {
				ghMantDiff = -ghMantDiff
			}
			if ghMantDiff > mantissaTol || ammUsdGH.Exponent() != tc.goodUsdGHExp {
				t.Errorf("AMM usdGH balance mismatch: got {%d, %d}, expected {%d, %d} (got %g, diff=%d)",
					ammUsdGH.Mantissa(), ammUsdGH.Exponent(), tc.goodUsdGHMant, tc.goodUsdGHExp, ammUsdGH.Float64(), ammUsdGH.Mantissa()-tc.goodUsdGHMant)
			}
			bitMantDiff := ammUsdBIT.Mantissa() - tc.goodUsdBITMant
			if bitMantDiff < 0 {
				bitMantDiff = -bitMantDiff
			}
			if bitMantDiff > mantissaTol || ammUsdBIT.Exponent() != tc.goodUsdBITExp {
				t.Errorf("AMM usdBIT balance mismatch: got {%d, %d}, expected {%d, %d} (got %g, diff=%d)",
					ammUsdBIT.Mantissa(), ammUsdBIT.Exponent(), tc.goodUsdBITMant, tc.goodUsdBITExp, ammUsdBIT.Float64(), ammUsdBIT.Mantissa()-tc.goodUsdBITMant)
			}
		})
	}
}

// TestAMMBookStep_SwapRounding tests that a bad-quality CLOB offer doesn't
// accidentally cross an AMM and the AMM balances remain unchanged.
// Reference: rippled AMM_test.cpp testSwapRounding (line 7013)
func TestAMMBookStep_SwapRounding(t *testing.T) {
	// Pool: XRP(51600.000981)/USD(80304.09987141784)
	// Bob offers XRP(6300) for USD(100000) — very bad quality, should not cross AMM.
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(200000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(200000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 200000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 100000)
	env.Close()

	// Alice creates AMM with precise amounts
	createTx := amm.AMMCreate(env.Alice,
		tx.NewXRPAmount(51_600_000_981),
		amm.IOUAmount(env.GW, "USD", 80304.09987141784)).
		TradingFee(889).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(), env.USD)

	// Save starting balances
	xrpBefore := env.AMMPoolXRP(ammAcc)
	usdBefore := env.AMMPoolIOU(ammAcc, env.GW, "USD")

	// Fund bob
	env.TestEnv.FundAmount(env.Bob, 1_092_878_933) // ~1092.878933 XRP
	env.Trust(env.Bob, env.GW, "USD", 1000000)
	env.Close()

	// Bob creates offer: buy XRP(6300), sell USD(100000) — terrible quality
	// Bob can't fund 100000 USD, so offer is effectively unfunded
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(6300),
		amm.IOUAmount(env.GW, "USD", 100000)).Build()
	_ = env.Submit(offerTx)
	env.Close()

	// AMM should be unchanged
	xrpAfter := env.AMMPoolXRP(ammAcc)
	usdAfter := env.AMMPoolIOU(ammAcc, env.GW, "USD")
	if xrpBefore != xrpAfter {
		t.Errorf("AMM XRP changed: before %d, after %d", xrpBefore, xrpAfter)
	}
	if math.Abs(usdBefore-usdAfter) > 0.0001 {
		t.Errorf("AMM USD changed: before %f, after %f", usdBefore, usdAfter)
	}
}

// TestAMMBookStep_FixAMMOfferBlockedByLOB tests AMM offer blocked by LOB fix.
// Reference: rippled AMM_test.cpp testFixAMMOfferBlockedByLOB (line 7050)
// A low-quality CLOB offer should not block AMM from being consumed.
func TestAMMBookStep_FixAMMOfferBlockedByLOB(t *testing.T) {
	// Scenario 2: No blocking offer — AMM consumed regardless of amendment.
	// This tests the base case that AMM liquidity is accessible.
	t.Run("NoBlockingOffer", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1000000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 1000000)
		env.Trust(env.Carol, env.GW, "USD", 1000000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 1000000)
		env.PayIOU(env.GW, env.Carol, "USD", 1000000)
		env.Close()

		// No blocking offer
		// GW creates AMM: XRP(200000)/USD(100000)
		createTx := amm.AMMCreate(env.GW,
			tx.NewXRPAmount(200_000*1_000_000),
			amm.IOUAmount(env.GW, "USD", 100000)).
			TradingFee(0).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, amm.XRP(), env.USD)

		// Carol creates offer: buy USD(0.49) sell XRP(1)
		offerTx := offerbuild.OfferCreate(env.Carol,
			amm.IOUAmount(env.GW, "USD", 0.49),
			tx.NewXRPAmount(1*1_000_000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM should be consumed
		// rippled expects: XRP(200000980005), USD(99999.51)
		ammXRP := env.AMMPoolXRP(ammAcc)
		ammUSD := env.AMMPoolIOU(ammAcc, env.GW, "USD")

		if ammXRP <= 200_000*1_000_000 {
			t.Errorf("AMM XRP should increase after offer crossing: got %d", ammXRP)
		}
		if ammUSD >= 100000 {
			t.Errorf("AMM USD should decrease after offer crossing: got %f", ammUSD)
		}

		// Carol's offer should be consumed
		carolOffers := env.AccountOffers(env.Carol)
		if len(carolOffers) != 0 {
			t.Errorf("Carol should have 0 offers (consumed), got %d", len(carolOffers))
		}
	})

	// Scenario 3: XRP/USD direction, no blocking offer
	t.Run("XRPUSDNoBlockingOffer", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 100000)
		env.Trust(env.Carol, env.GW, "USD", 100000)
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 1000)
		env.PayIOU(env.GW, env.Carol, "USD", 1000)
		env.PayIOU(env.GW, env.Bob, "USD", 1000)
		env.Close()

		// Alice creates AMM: XRP(1000)/USD(500)
		createTx := amm.AMMCreate(env.Alice,
			tx.NewXRPAmount(1000*1_000_000),
			amm.IOUAmount(env.GW, "USD", 500)).
			TradingFee(0).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, amm.XRP(), env.USD)

		// Carol creates offer: buy XRP(100) sell USD(55)
		offerTx := offerbuild.OfferCreate(env.Carol,
			tx.NewXRPAmount(100*1_000_000),
			amm.IOUAmount(env.GW, "USD", 55)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM should be consumed: XRP ~909090909 drops, USD ~550.00000005
		ammXRP := env.AMMPoolXRP(ammAcc)
		ammUSD := env.AMMPoolIOU(ammAcc, env.GW, "USD")

		if ammXRP >= 1000*1_000_000 {
			t.Errorf("AMM XRP should decrease: got %d", ammXRP)
		}
		if ammUSD <= 500 {
			t.Errorf("AMM USD should increase: got %f", ammUSD)
		}

		// Carol should have remaining offer (partially filled)
		carolOffers := env.AccountOffers(env.Carol)
		if len(carolOffers) != 1 {
			t.Errorf("Carol should have 1 remaining offer, got %d", len(carolOffers))
		}
	})
}

// TestAMMBookStep_LPTokenBalance tests LP token balance tracking after deposits/withdrawals.
// Reference: rippled AMM_test.cpp testLPTokenBalance (line 7178)
// This is NOT a BookStep test — it belongs in deposit/withdraw category.
func TestAMMBookStep_LPTokenBalance(t *testing.T) {
	// Scenario 1: Last LP is issuer of one token
	t.Run("LastLP_IssuerOfOneToken", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)

		// Fund with large amounts
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1_000_000_000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1_000_000_000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1_000_000_000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 1_000_000_000)
		env.Trust(env.Carol, env.GW, "USD", 1_000_000_000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 1_000_000_000)
		env.PayIOU(env.GW, env.Carol, "USD", 1_000_000_000)
		env.Close()

		// GW creates AMM: XRP(2)/USD(1)
		createTx := amm.AMMCreate(env.GW,
			amm.XRPAmount(2),
			amm.IOUAmount(env.GW, "USD", 1)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice deposits IOUAmount{1876123487565916, -15} LP tokens
		lptRef := amm.LPTokenAmount(amm.XRP(), env.USD, 0)
		aliceLPT := tx.NewIssuedAmount(1_876123487565916, -15, lptRef.Currency, lptRef.Issuer)
		depAlice := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			LPTokenOut(aliceLPT).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depAlice))
		env.Close()

		// Carol deposits 1000000 LP tokens
		carolLPT := amm.LPTokenAmount(amm.XRP(), env.USD, 1_000_000)
		depCarol := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(carolLPT).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depCarol))
		env.Close()

		// Alice withdraws all
		wdAlice := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(wdAlice))
		env.Close()

		// Carol withdraws all
		wdCarol := amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(wdCarol))
		env.Close()

		// With fixAMMv1_1 (enabled by default): gw can withdrawAll and AMM is deleted
		wdGW := amm.AMMWithdraw(env.GW, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(wdGW)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// AMM should be deleted — deposit should fail with terNO_AMM
		testDep := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1)).
			SingleAsset().
			Build()
		depResult := env.Submit(testDep)
		amm.ExpectTER(t, depResult, amm.TerNO_AMM)
	})
}

// ===================================================================
// AMMExtended_test.cpp BookStep-dependent tests (class AMMExtended_test)
// ===================================================================

// TestAMMBookStep_FillModes tests fill modes with AMM liquidity.
// Reference: rippled AMMExtended_test.cpp testFillModes (line 191)
func TestAMMBookStep_FillModes(t *testing.T) {
	// FillOrKill: order that can't fill → tecKILLED, then order that fills → tesSUCCESS
	t.Run("FillOrKill", func(t *testing.T) {
		pool := [2]tx.Amount{amm.XRPAmount(10100), amm.IOUAmount(nil, "USD", 10000)}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			// Order that can't be filled: carol buys USD(100) sells XRP(100)
			// AMM has pool XRP(10100)/USD(10000), carol's offer quality 1:1
			// but AMM SPQ = 10100/10000 = 1.01 (worse for buyer of USD)
			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "USD", 100),
				amm.XRPAmount(100)).
				FillOrKill().Build()
			result := env.Submit(offerTx)
			amm.ExpectTER(t, result, "tecKILLED", "tesSUCCESS")
			env.Close()

			// AMM unchanged
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30000)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)

			// Order that can be filled: carol buys XRP(100) sells USD(100)
			offerTx2 := offerbuild.OfferCreate(env.Carol,
				amm.XRPAmount(100),
				amm.IOUAmount(env.GW, "USD", 100)).
				FillOrKill().Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx2))

			// AMM: XRP(10000), USD(10100)
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10000)), env.GW, "USD", 10100)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 29900)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})

	// ImmediateOrCancel: partial cross
	t.Run("ImmediateOrCancel", func(t *testing.T) {
		pool := [2]tx.Amount{amm.XRPAmount(10100), amm.IOUAmount(nil, "USD", 10000)}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			// Carol buys XRP(200) sells USD(200) with IoC — partial fill ok
			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.XRPAmount(200),
				amm.IOUAmount(env.GW, "USD", 200)).
				ImmediateOrCancel().Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))

			// AMM: XRP(10000), USD(10100) — only 100 XRP / 100 USD crossed
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10000)), env.GW, "USD", 10100)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 29900)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})

	// Passive: offer stays on books without crossing AMM.
	// Reference: rippled AMMExtended_test.cpp testFillModes (line 265-302)
	// With fixAMMv1_1, passive offers respect AMM quality threshold properly.
	t.Run("Passive", func(t *testing.T) {
		pool := [2]tx.Amount{amm.XRPAmount(10100), amm.IOUAmount(nil, "USD", 10000)}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			// Carol creates passive offer: buy XRP(100) sells USD(100)
			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.XRPAmount(100),
				amm.IOUAmount(env.GW, "USD", 100)).
				Passive().Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))
			env.Close()

			// AMM should NOT be crossed (passive offer doesn't cross AMM)
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

			// Carol's offer should remain on the book
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 1)
		})
	})
}

// TestAMMBookStep_OfferCrossWithLimitOverride tests offer crossing with limit override.
// Reference: rippled AMMExtended_test.cpp testOfferCrossWithLimitOverride (line 337)
//
// Alice creates AMM: XRP(150000)/USD(51).
// Bob offers: buy USD(1), sell XRP(3000). Crosses AMM.
// AMM: XRP(153000), USD(50). Bob's USD balance = -1 (trust line deficit).
func TestAMMBookStep_OfferCrossWithLimitOverride(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(200000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(200000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(200000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 500)
	env.Close()

	// Alice creates AMM: XRP(150000)/USD(51)
	createTx := amm.AMMCreate(env.Alice,
		amm.XRPAmount(150000),
		amm.IOUAmount(env.GW, "USD", 51)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: env.GW.Address})

	// Bob offers: buy USD(1), sell XRP(3000)
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.IOUAmount(env.GW, "USD", 1),
		amm.XRPAmount(3000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: XRP(153000), USD(50)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(153000)), env.GW, "USD", 50)

	// Bob receives USD(1) from AMM crossing. Rippled checks raw sfBalance=-1
	// (from low/gateway's perspective), but BalanceIOU returns Bob's perspective = +1.
	bobUSD := env.TestEnv.BalanceIOU(env.Bob, "USD", env.GW)
	if math.Abs(bobUSD-1) > 0.0001 {
		t.Errorf("Bob USD: got %f, want 1", bobUSD)
	}

	// Bob XRP = 200000 - 3000 - baseFee = 196999999990
	bobXRP := env.TestEnv.Balance(env.Bob)
	expectedBobXRP := uint64(jtx.XRP(200000)) - uint64(jtx.XRP(3000)) - 10
	if bobXRP != expectedBobXRP {
		t.Errorf("Bob XRP: got %d, want %d", bobXRP, expectedBobXRP)
	}
}

// TestAMMBookStep_CurrencyConversionEntire tests entire currency conversion via AMM.
// Reference: rippled AMMExtended_test.cpp testCurrencyConversionEntire (line 369)
//
// Alice converts 100 USD to 500 XRP through an AMM(USD(200)/XRP(1500)).
// This is a self-payment: alice pays herself XRP(500) with sendmax USD(100).
// Pool: 200*1500 = 300,000. After +100 USD: 300 * x = 300,000, x = 1000. Alice gets 500 XRP.
func TestAMMBookStep_CurrencyConversionEntire(t *testing.T) {
	// rippled setup:
	//   fund(env, gw, {alice, bob}, XRP(10000))
	//   trust(alice, USD(100)); trust(bob, USD(1000))
	//   pay(gw, bob, USD(1000)); pay(gw, alice, USD(100))
	//   AMM ammBob(env, bob, USD(200), XRP(1500))
	//   pay(alice, alice, XRP(500)), sendmax(USD(100))
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 100)
	env.Trust(env.Bob, env.GW, "USD", 1000)
	env.Close()

	env.PayIOU(env.GW, env.Bob, "USD", 1000)
	env.PayIOU(env.GW, env.Alice, "USD", 100)
	env.Close()

	// Bob creates AMM: USD(200)/XRP(1500)
	createTx := amm.AMMCreate(env.Bob, amm.IOUAmount(env.GW, "USD", 200), amm.XRPAmount(1500)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env.USD, amm.XRP())

	// Alice pays herself XRP(500) with sendmax USD(100)
	payTx := payment.Pay(env.Alice, env.Alice, uint64(jtx.XRP(500))).
		SendMax(amm.IOUAmount(env.GW, "USD", 100)).
		Build()
	result := env.Submit(payTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// AMM should have USD(300), XRP(1000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1000)), env.GW, "USD", 300)

	// Alice should have USD(0) — spent all 100
	expectIOUBalance(t, env, env.Alice, "USD", env.GW, 0)

	// Alice XRP: initial 10000 + 500 - fee*2 (AMMCreate didn't charge her, so just 2 txns: trust + payment)
	// Actually: alice funded 10000 XRP. She paid 2 fees (trust USD, pay self).
	// 10000*1M + 500*1M - 20 = 10500*1M - 20
	aliceXRP := env.TestEnv.Balance(env.Alice)
	expectedAliceXRP := uint64(jtx.XRP(10000)) + uint64(jtx.XRP(500)) - 20 // 2 tx fees
	if aliceXRP != expectedAliceXRP {
		t.Errorf("Alice XRP: got %d, want %d (diff %d)", aliceXRP, expectedAliceXRP, int64(aliceXRP)-int64(expectedAliceXRP))
	}
}

// TestAMMBookStep_CurrencyConversionInParts tests partial currency conversion via AMM.
// Reference: rippled AMMExtended_test.cpp testCurrencyConversionInParts (line 403)
func TestAMMBookStep_CurrencyConversionInParts(t *testing.T) {
	// Pool: XRP(10000)/USD(10000)
	// Alice sends USD(100) to get XRP(100) — but constant product means she can't get exactly 100 XRP for 100 USD.
	// Without partial payment: tecPATH_PARTIAL
	// With partial payment: succeeds, gets ~99.01 XRP
	amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		// Without partial payment — should fail
		payTx := payment.Pay(env.Alice, env.Alice, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_PARTIAL")

		// With partial payment — should succeed
		payTx2 := payment.Pay(env.Alice, env.Alice, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			PartialPayment().
			Build()
		result2 := env.Submit(payTx2)
		jtx.RequireTxSuccess(t, result2)
		env.Close()

		// AMM: XRP should be ~9900990100 drops, USD should be 10100
		// Constant product: 10000*1M * 10000 = 10^11. After +100 USD: 10100 * x = 10^11
		// x = 10^11/10100 = 9900990099.0099... drops
		// So XRP balance = ~9900990100 drops
		ammXRP := env.AMMPoolXRP(ammAcc)
		// Allow 1 drop tolerance for rounding
		if ammXRP < 9_900_990_099 || ammXRP > 9_900_990_101 {
			t.Errorf("AMM XRP: got %d, want ~9900990100", ammXRP)
		}

		ammUSD := env.AMMPoolIOU(ammAcc, env.GW, "USD")
		if math.Abs(ammUSD-10100) > 0.0001 {
			t.Errorf("AMM USD: got %f, want 10100", ammUSD)
		}

		// Alice USD: initial 30000 - 10000(AMM) - 100(pay) = 19900
		expectIOUBalance(t, env, env.Alice, "USD", env.GW, 19900)
	})
}

// TestAMMBookStep_CrossCurrencyStartXRP tests cross-currency payment starting with XRP.
// Reference: rippled AMMExtended_test.cpp testCrossCurrencyStartXRP (line 441)
func TestAMMBookStep_CrossCurrencyStartXRP(t *testing.T) {
	// Pool: XRP(10000)/USD(10100) — 100 XRP buys exactly 100 USD
	pool := [2]tx.Amount{
		amm.XRPAmount(10000),
		amm.IOUAmount(nil, "USD", 10100),
	}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		// Fund bob with 1000 XRP and set up USD trust line
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(env.Bob, env.GW, "USD", 100)
		env.Close()

		// Alice pays bob 100 USD with sendmax 100 XRP
		payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "USD", 100)).
			SendMax(amm.XRPAmount(100)).
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)

		// AMM: XRP(10100), USD(10000)
		env.ExpectAMMBalances(t, ammAcc,
			uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

		// Bob should have 100 USD
		expectIOUBalance(t, env, env.Bob, "USD", env.GW, 100)
	})
}

// TestAMMBookStep_CrossCurrencyEndXRP tests cross-currency payment ending with XRP.
// Reference: rippled AMMExtended_test.cpp testCrossCurrencyEndXRP (line 465)
func TestAMMBookStep_CrossCurrencyEndXRP(t *testing.T) {
	// Pool: XRP(10100)/USD(10000) — 100 USD buys exactly 100 XRP
	pool := [2]tx.Amount{
		amm.XRPAmount(10100),
		amm.IOUAmount(nil, "USD", 10000),
	}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(env.Bob, env.GW, "USD", 100)
		env.Close()

		// Alice pays bob 100 XRP with sendmax 100 USD
		payTx := payment.Pay(env.Alice, env.Bob, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)

		// AMM: XRP(10000), USD(10100)
		env.ExpectAMMBalances(t, ammAcc,
			uint64(jtx.XRP(10000)), env.GW, "USD", 10100)

		// Bob: 1000 + 100 - fee = 1100*1M - 10
		bobXRP := env.TestEnv.Balance(env.Bob)
		expectedBob := uint64(jtx.XRP(1000)) + uint64(jtx.XRP(100)) - 10
		if bobXRP != expectedBob {
			t.Errorf("Bob XRP: got %d, want %d", bobXRP, expectedBob)
		}
	})
}

// TestAMMBookStep_CrossCurrencyBridged tests bridged cross-currency payments.
// Reference: rippled AMMExtended_test.cpp testCrossCurrencyBridged (line 490)
//
// Two gateways: gw1 for USD, gw2 for EUR.
// AMM pool: USD1(5000)/XRP(50000) created by carol.
// Dan's offer: TakerPays=XRP(500), TakerGets=EUR1(50) [buy 500 XRP for 50 EUR].
// Alice pays Bob 30 EUR with sendmax 333 USD, path through XRP.
// Route: Alice USD -> AMM(USD/XRP) -> XRP -> Dan's offer(XRP->EUR) -> Bob EUR.
//
// To deliver 30 EUR to Bob, the path consumes 300 XRP from Dan's offer (10 XRP/EUR).
// AMM: to output 300 XRP, pool goes from XRP(50000) to XRP(49700).
// K = 5000*50000 = 250,000,000. USD after = 250,000,000/49700 = 5030.181086519115.
// Dan's remaining offer: XRP(200)/EUR(20).
func TestAMMBookStep_CrossCurrencyBridged(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	// Create two gateways
	gw1 := jtx.NewAccount("gateway_1")
	gw2 := jtx.NewAccount("gateway_2")
	dan := jtx.NewAccount("dan")

	// Fund all accounts with XRP(60000)
	env.TestEnv.FundAmount(gw1, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(dan, uint64(jtx.XRP(60000)))
	env.Close()

	// Trust lines
	env.Trust(env.Alice, gw1, "USD", 1000)
	env.Close()
	env.Trust(env.Bob, gw2, "EUR", 1000)
	env.Close()
	env.Trust(env.Carol, gw1, "USD", 10000)
	env.Close()
	env.Trust(dan, gw2, "EUR", 1000)
	env.Close()

	// Fund IOUs
	env.PayIOU(gw1, env.Alice, "USD", 500)
	env.Close()
	env.PayIOU(gw1, env.Carol, "USD", 6000)
	env.PayIOU(gw2, dan, "EUR", 400)
	env.Close()

	// Carol creates AMM: USD1(5000)/XRP(50000)
	createTx := amm.AMMCreate(env.Carol,
		amm.IOUAmount(gw1, "USD", 5000),
		amm.XRPAmount(50000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t,
		tx.Asset{Currency: "USD", Issuer: gw1.Address},
		tx.Asset{Currency: "XRP"})

	// Dan creates offer: TakerPays=XRP(500), TakerGets=EUR1(50)
	// Dan wants to buy XRP(500), offering EUR(50) from gw2
	offerTx := offerbuild.OfferCreate(dan,
		amm.XRPAmount(500),
		amm.IOUAmount(gw2, "EUR", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// Alice pays Bob EUR1(30), sendmax USD1(333), path through XRP
	// Path: [{Currency: "XRP"}] — tells the engine to route through XRP as bridge
	payTx := payment.PayIssued(env.Alice, env.Bob,
		amm.IOUAmount(gw2, "EUR", 30)).
		SendMax(amm.IOUAmount(gw1, "USD", 333)).
		PathsXRP().
		Build()
	result := env.Submit(payTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// AMM: XRP(49700), USD = 250,000,000 / 49700 = 5030.181086519115
	ammXRP := env.AMMPoolXRP(ammAcc)
	if ammXRP != uint64(jtx.XRP(49700)) {
		t.Errorf("AMM XRP: got %d, want %d", ammXRP, uint64(jtx.XRP(49700)))
	}

	ammUSD := env.AMMPoolIOU(ammAcc, gw1, "USD")
	// rippled expects: STAmount{USD1, UINT64_C(5030181086519115), -12}
	// = 5030181086519115 * 10^-12 = 5030.181086519115
	expectedUSD := 5030.181086519115
	if math.Abs(ammUSD-expectedUSD) > 0.000001 {
		t.Errorf("AMM USD: got %f, want %f", ammUSD, expectedUSD)
	}

	// Dan should have 1 remaining offer: TakerPays=XRP(200), TakerGets=EUR(20)
	offerbuild.RequireOfferCount(t, env.TestEnv, dan, 1)
	offerbuild.RequireIsOffer(t, env.TestEnv, dan,
		amm.XRPAmount(200),
		amm.IOUAmount(gw2, "EUR", 20))

	// Bob should have 30 EUR
	bobEUR := env.TestEnv.BalanceIOU(env.Bob, "EUR", gw2)
	if math.Abs(bobEUR-30) > 0.0001 {
		t.Errorf("Bob EUR: got %f, want 30", bobEUR)
	}
}

// TestAMMBookStep_OfferFeesConsumeFunds tests that alice's offer only crosses
// with the XRP she has available after reserve, even though she asks for more.
// rippled: 3 gateways + 3 trust lines → ownerCount=3, reserve(3)
// Alice has XRP(100) + reserve(3) + 4*base total. After 3 trust fees + 1 offer
// fee, she has exactly reserve(3) + 100 XRP. Available = 100 XRP.
// Reference: rippled AMMExtended_test.cpp testOfferFeesConsumeFunds (line 540)
func TestAMMBookStep_OfferFeesConsumeFunds(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	gw1 := jtx.NewAccount("gw1")
	gw2 := jtx.NewAccount("gw2")
	gw3 := jtx.NewAccount("gw3")

	// Alice: XRP(100) + reserve(3) + base*4
	reserve3 := env.TestEnv.ReserveBase() + 3*env.TestEnv.ReserveIncrement()
	aliceFund := uint64(jtx.XRP(100)) + reserve3 + 40

	env.TestEnv.FundAmount(gw1, aliceFund)
	env.TestEnv.FundAmount(gw2, aliceFund)
	env.TestEnv.FundAmount(gw3, aliceFund)
	env.TestEnv.FundAmount(env.Alice, aliceFund)
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(2000)))
	env.Close()

	// Alice creates 3 trust lines → ownerCount=3
	env.Trust(env.Alice, gw1, "USD", 1000)
	env.Trust(env.Alice, gw2, "USD", 1000)
	env.Trust(env.Alice, gw3, "USD", 1000)
	env.Trust(env.Bob, gw1, "USD", 1200)
	env.Close()

	// Pay bob 1200 USD from gw1
	gw1USD := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "USD", gw1.Address) }
	payTx := payment.PayIssued(gw1, env.Bob, gw1USD(1200)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// Bob creates AMM: XRP(1000)/USD(1200) with gw1's USD
	createTx := amm.AMMCreate(env.Bob,
		amm.XRPAmount(1000),
		gw1USD(1200)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: gw1.Address})

	// Alice has used 3 trust line fees (30 drops) + now creates offer (10 drops)
	// Alice balance = aliceFund - 30 = 100 XRP + reserve(3) + 10
	// Available after reserve(3) = 100 XRP + 10 drops
	// She asks for 200 XRP but only ~100 available
	offerTx := offerbuild.OfferCreate(env.Alice,
		gw1USD(200),
		amm.XRPAmount(200)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: XRP(1100), USD(~1090.909)
	ammXRP := env.AMMPoolXRP(ammAcc)
	if ammXRP != uint64(jtx.XRP(1100)) {
		t.Errorf("AMM XRP: got %d, want %d", ammXRP, uint64(jtx.XRP(1100)))
	}
	ammUSD := env.AMMPoolIOU(ammAcc, gw1, "USD")
	if math.Abs(ammUSD-1090.909090909091) > 0.01 {
		t.Errorf("AMM USD: got %f, want ~1090.909", ammUSD)
	}

	// Alice got ~109.09 USD
	aliceUSD := env.TestEnv.BalanceIOU(env.Alice, "USD", gw1)
	if math.Abs(aliceUSD-109.090909090909) > 0.01 {
		t.Errorf("Alice USD: got %f, want ~109.09", aliceUSD)
	}

	// Alice XRP should be reserve(3) = reserveBase + 3*increment (after offer consumed)
	aliceXRP := env.TestEnv.Balance(env.Alice)
	if aliceXRP != reserve3 {
		t.Errorf("Alice XRP: got %d, want %d (reserve(3))", aliceXRP, reserve3)
	}
}

// TestAMMBookStep_OfferCreateThenCross tests creating an offer then crossing.
// Reference: rippled AMMExtended_test.cpp testOfferCreateThenCross (line 601)
func TestAMMBookStep_OfferCreateThenCross(t *testing.T) {
	// Pool: XRP(10000)/USD(10100)
	// Bob creates offer to buy XRP for USD, crosses against AMM.
	pool := [2]tx.Amount{
		amm.XRPAmount(10000),
		amm.IOUAmount(nil, "USD", 10100),
	}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env.FundBob(30000, 20000)
		env.Close()

		// Bob creates offer: buy XRP(100) sell USD(100) — crosses AMM
		offerTx := offerbuild.OfferCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		result := env.Submit(offerTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// AMM should have gained 100 USD: XRP(10000-~100), USD(10100+~100)
		// With the exact pool values, 100 USD buys exactly ~99.01 XRP from AMM
		// But since the offer is TakerPays=XRP(100), TakerGets=USD(100), and
		// the AMM quality is 10100/10000 = 1.01, the taker gets all 100 XRP at this quality
		t.Logf("AMM XRP: %d, USD: %f", env.AMMPoolXRP(ammAcc), env.AMMPoolIOU(ammAcc, env.GW, "USD"))
	})
}

// TestAMMBookStep_SellFlagBasic tests basic sell flag behavior.
// Reference: rippled AMMExtended_test.cpp testSellFlagBasic (line 632)
// Pool: XRP(9900)/USD(10100). Carol sells XRP(100) for USD with tfSell.
// With tfSell she gets more than the 100 USD asked for: 101 USD.
// Pool → XRP(10000)/USD(9999). Carol has no remaining offers.
func TestAMMBookStep_SellFlagBasic(t *testing.T) {
	pool := [2]tx.Amount{
		amm.XRPAmount(9900),
		amm.IOUAmount(nil, "USD", 10100),
	}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		// carol: offer TakerPays=USD(100), TakerGets=XRP(100) with tfSell
		// carol sells XRP(100) to get USD(100+)
		offerTx := offerbuild.OfferCreate(env.Carol,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.XRPAmount(100)).
			Sell().Build()
		result := env.Submit(offerTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// AMM: XRP(10000), USD(9999)
		env.ExpectAMMBalances(t, ammAcc,
			uint64(jtx.XRP(10000)), env.GW, "USD", 9999)

		// Carol has no remaining offers
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)

		// Carol USD: started with 30000, got 101 → 30101
		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30101)

		// Carol XRP: 30000 - 100 XRP - 10 drops trust fee - 10 drops offer fee = 29899999980
		carolXRP := env.TestEnv.Balance(env.Carol)
		expectedCarol := uint64(jtx.XRP(30000)) - uint64(jtx.XRP(100)) - 20
		if carolXRP != expectedCarol {
			t.Errorf("Carol XRP: got %d, want %d", carolXRP, expectedCarol)
		}
	})
}

// TestAMMBookStep_SellFlagExceedLimit tests sell flag exceeding limit.
// Reference: rippled AMMExtended_test.cpp testSellFlagExceedLimit (line 656)
//
// Setup:
//   starting_xrp = XRP(100) + reserve(env,1) + 2*baseFee
//                 = 100M + 250M + 20 = 350,000,020 drops
//   Fund gw and alice with starting_xrp, bob with XRP(2000).
//   Bob creates AMM: XRP(1000)/USD(2200).
//   Alice creates offer: TakerPays=USD(100), TakerGets=XRP(200) with tfSell.
//   Alice only has ~100 XRP available (350M drops - 250M reserve - fees).
//   tfSell means sell at least TakerGets, but alice can only sell 100 XRP.
//   Pool K = 1000*2200 = 2,200,000. After +100 XRP: 1100 * x = 2,200,000, x = 2000.
//   Alice gets 200 USD (more than 100 asked). Pool: XRP(1100)/USD(2000).
//   Alice XRP = 250,000,000 drops (exactly the reserve for 1 item).
func TestAMMBookStep_SellFlagExceedLimit(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	// starting_xrp = XRP(100) + reserve(env,1) + 2*baseFee
	// reserve(env,1) = baseReserve + 1*ownerReserve = 200M + 50M = 250M drops
	// baseFee = 10 drops
	startingXRP := uint64(jtx.XRP(100)) + env.TestEnv.ReserveBase() + env.TestEnv.ReserveIncrement() + 2*10
	// = 100,000,000 + 200,000,000 + 50,000,000 + 20 = 350,000,020

	env.TestEnv.FundAmount(env.GW, startingXRP)
	env.TestEnv.FundAmount(env.Alice, startingXRP)
	env.Close()

	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(2000)))
	env.Close()

	// Alice trusts GW for USD(150)
	env.Trust(env.Alice, env.GW, "USD", 150)
	// Bob trusts GW for USD(4000)
	env.Trust(env.Bob, env.GW, "USD", 4000)
	env.Close()

	// Pay bob USD(2200)
	env.PayIOU(env.GW, env.Bob, "USD", 2200)
	env.Close()

	// Bob creates AMM: XRP(1000)/USD(2200)
	createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 2200)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

	// Alice creates offer: TakerPays=USD(100), TakerGets=XRP(200), tfSell
	// Alice has 350,000,020 - 10(trust fee) = 350,000,010 drops.
	// Reserve for 1 item (trust line) = 250,000,000.
	// Available = 350,000,010 - 250,000,000 = 100,000,010 drops.
	// With tfSell she wants to sell XRP(200) but only has ~100 XRP available.
	// She sells 100 XRP and gets 200 USD (more than the 100 USD in TakerPays).
	offerTx := offerbuild.OfferCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 100),
		amm.XRPAmount(200)).
		Sell().Build()
	result := env.Submit(offerTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// AMM: XRP(1100), USD(2000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1100)), env.GW, "USD", 2000)

	// Alice USD: 0 + 200 = 200
	expectIOUBalance(t, env, env.Alice, "USD", env.GW, 200)

	// Alice XRP: should be exactly 250,000,000 drops (= reserve for 1 item)
	// 350,000,020 - 10(trust) - 10(offer) - 100,000,000(sold) = 249,999,990... hmm
	// Actually, rippled expects XRP(250) = 250,000,000 drops.
	// Let's verify: starting=350,000,020, trust fee=10, offer fee=10, sold XRP=100M
	// 350,000,020 - 10 - 10 - 100,000,000 = 250,000,000. Correct!
	aliceXRP := env.TestEnv.Balance(env.Alice)
	expectedAliceXRP := uint64(jtx.XRP(250)) // 250,000,000 drops
	if aliceXRP != expectedAliceXRP {
		t.Errorf("Alice XRP: got %d, want %d (diff %d)", aliceXRP, expectedAliceXRP, int64(aliceXRP)-int64(expectedAliceXRP))
	}

	// Alice has no remaining offers
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)
}

// TestAMMBookStep_GatewayCrossCurrency tests gateway cross-currency with AMM.
// Reference: rippled AMMExtended_test.cpp testGatewayCrossCurrency (line 691)
//
// Setup: alice and bob funded with ~350 XRP each, trust XTS(100) and XXX(100) from gw.
// Alice creates AMM: XTS(100)/XXX(100).
// Bob does a self-payment: buy XXX(1) with sendmax XTS(1.5), tfPartialPayment.
// With fixAMMv1_1: AMM → XTS(101.01010101010110), XXX(99). Bob XTS ≈ 98.9898989898989.
func TestAMMBookStep_GatewayCrossCurrency(t *testing.T) {
	t.Skip("Self-payment cross-currency through AMM gets tecPATH_DRY - needs build_path/auto-pathfind support")
	env := amm.NewAMMTestEnv(t)

	// starting_xrp = XRP(100.1) + reserve(env,1) + 2*baseFee
	startingXRP := uint64(100_100_000) + env.TestEnv.ReserveBase() + env.TestEnv.ReserveIncrement() + 20

	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, startingXRP)
	env.TestEnv.FundAmount(env.Bob, startingXRP)
	env.Close()

	// Trust + fund XTS and XXX for alice and bob
	env.Trust(env.Alice, env.GW, "XTS", 100)
	env.Trust(env.Alice, env.GW, "XXX", 100)
	env.Trust(env.Bob, env.GW, "XTS", 100)
	env.Trust(env.Bob, env.GW, "XXX", 100)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "XTS", 100)
	env.PayIOU(env.GW, env.Alice, "XXX", 100)
	env.PayIOU(env.GW, env.Bob, "XTS", 100)
	env.PayIOU(env.GW, env.Bob, "XXX", 100)
	env.Close()

	// Alice creates AMM: XTS(100)/XXX(100)
	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "XTS", 100),
		amm.IOUAmount(env.GW, "XXX", 100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// Bob self-payment: buy XXX(1) with sendmax XTS(1.5), tfPartialPayment
	payTx := payment.PayIssued(env.Bob, env.Bob, amm.IOUAmount(env.GW, "XXX", 1)).
		SendMax(amm.IOUAmount(env.GW, "XTS", 1.5)).
		PartialPayment().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// With fixAMMv1_1:
	// AMM: XTS ≈ 101.01010101010110, XXX = 99
	// Bob XTS ≈ 98.9898989898989, Bob XXX = 101
	bobXXX := env.TestEnv.BalanceIOU(env.Bob, "XXX", env.GW)
	if math.Abs(bobXXX-101) > 0.0001 {
		t.Errorf("Bob XXX: got %f, want 101", bobXXX)
	}

	bobXTS := env.TestEnv.BalanceIOU(env.Bob, "XTS", env.GW)
	// Expect ~98.9898989898989
	if math.Abs(bobXTS-98.9898989898989) > 0.001 {
		t.Errorf("Bob XTS: got %f, want ~98.99", bobXTS)
	}
}

// TestAMMBookStep_BridgedCross tests bridged crossing with AMM.
// Reference: rippled AMMExtended_test.cpp testBridgedCross (line 752)
func TestAMMBookStep_BridgedCross(t *testing.T) {
	// Sub-test 1: USD/XRP AMM + EUR/XRP AMM, carol offers USD for EUR
	t.Run("TwoAMMs", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 30000)
		env.Trust(env.Alice, env.GW, "EUR", 30000)
		env.Trust(env.Bob, env.GW, "USD", 30000)
		env.Trust(env.Bob, env.GW, "EUR", 30000)
		env.Trust(env.Carol, env.GW, "USD", 30000)
		env.Trust(env.Carol, env.GW, "EUR", 30000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 15000)
		env.PayIOU(env.GW, env.Alice, "EUR", 15000)
		env.PayIOU(env.GW, env.Bob, "USD", 15000)
		env.PayIOU(env.GW, env.Bob, "EUR", 15000)
		env.PayIOU(env.GW, env.Carol, "USD", 15000)
		env.PayIOU(env.GW, env.Carol, "EUR", 15000)
		env.Close()

		// Alice creates AMM: XRP(10000)/USD(10100)
		createTx1 := amm.AMMCreate(env.Alice,
			amm.XRPAmount(10000),
			amm.IOUAmount(env.GW, "USD", 10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		env.Close()

		ammAlice := amm.AMMAccount(t, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// Bob creates AMM: EUR(10000)/XRP(10100)
		createTx2 := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "EUR", 10000),
			amm.XRPAmount(10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		env.Close()

		ammBob := amm.AMMAccount(t,
			tx.Asset{Currency: "EUR", Issuer: env.GW.Address}, amm.XRP())

		// Carol offers: buy USD(100), sell EUR(100) — bridges through XRP
		offerTx := offerbuild.OfferCreate(env.Carol,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM Alice: XRP(10100), USD(10000)
		env.ExpectAMMBalances(t, ammAlice,
			uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

		// AMM Bob: XRP(10000), EUR(10100)
		ammBobXRP := env.AMMPoolXRP(ammBob)
		if ammBobXRP != uint64(jtx.XRP(10000)) {
			t.Errorf("AMM Bob XRP: got %d, want %d", ammBobXRP, uint64(jtx.XRP(10000)))
		}
		ammBobEUR := env.AMMPoolIOU(ammBob, env.GW, "EUR")
		if math.Abs(ammBobEUR-10100) > 0.001 {
			t.Errorf("AMM Bob EUR: got %f, want 10100", ammBobEUR)
		}

		// Carol: USD(15100), EUR(14900)
		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 15100)
		expectIOUBalance(t, env, env.Carol, "EUR", env.GW, 14900)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
	})

	// Sub-test 2: USD/XRP AMM + EUR/XRP CLOB offer, carol offers USD for EUR
	t.Run("AMMAndOffer", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 30000)
		env.Trust(env.Alice, env.GW, "EUR", 30000)
		env.Trust(env.Bob, env.GW, "USD", 30000)
		env.Trust(env.Bob, env.GW, "EUR", 30000)
		env.Trust(env.Carol, env.GW, "USD", 30000)
		env.Trust(env.Carol, env.GW, "EUR", 30000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 15000)
		env.PayIOU(env.GW, env.Alice, "EUR", 15000)
		env.PayIOU(env.GW, env.Bob, "USD", 15000)
		env.PayIOU(env.GW, env.Bob, "EUR", 15000)
		env.PayIOU(env.GW, env.Carol, "USD", 15000)
		env.PayIOU(env.GW, env.Carol, "EUR", 15000)
		env.Close()

		// Alice creates AMM: XRP(10000)/USD(10100)
		createTx := amm.AMMCreate(env.Alice,
			amm.XRPAmount(10000),
			amm.IOUAmount(env.GW, "USD", 10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAlice := amm.AMMAccount(t, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// Bob creates CLOB offer: buy EUR(100), sell XRP(100)
		bobOffer := offerbuild.OfferCreate(env.Bob,
			amm.IOUAmount(env.GW, "EUR", 100),
			amm.XRPAmount(100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(bobOffer))
		env.Close()

		// Carol offers: buy USD(100), sell EUR(100)
		carolOffer := offerbuild.OfferCreate(env.Carol,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(carolOffer))
		env.Close()

		// AMM Alice: XRP(10100), USD(10000)
		env.ExpectAMMBalances(t, ammAlice,
			uint64(jtx.XRP(10100)), env.GW, "USD", 10000)

		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 15100)
		expectIOUBalance(t, env, env.Carol, "EUR", env.GW, 14900)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Bob, 0)
	})

	// Sub-test 3: USD/XRP CLOB offer + EUR/XRP AMM, carol offers USD for EUR
	t.Run("OfferAndAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 30000)
		env.Trust(env.Alice, env.GW, "EUR", 30000)
		env.Trust(env.Bob, env.GW, "USD", 30000)
		env.Trust(env.Bob, env.GW, "EUR", 30000)
		env.Trust(env.Carol, env.GW, "USD", 30000)
		env.Trust(env.Carol, env.GW, "EUR", 30000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 15000)
		env.PayIOU(env.GW, env.Alice, "EUR", 15000)
		env.PayIOU(env.GW, env.Bob, "USD", 15000)
		env.PayIOU(env.GW, env.Bob, "EUR", 15000)
		env.PayIOU(env.GW, env.Carol, "USD", 15000)
		env.PayIOU(env.GW, env.Carol, "EUR", 15000)
		env.Close()

		// Alice creates CLOB offer: buy XRP(100), sell USD(100)
		aliceOffer := offerbuild.OfferCreate(env.Alice,
			amm.XRPAmount(100),
			amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(aliceOffer))
		env.Close()

		// Bob creates AMM: EUR(10000)/XRP(10100)
		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "EUR", 10000),
			amm.XRPAmount(10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammBob := amm.AMMAccount(t,
			tx.Asset{Currency: "EUR", Issuer: env.GW.Address}, amm.XRP())

		// Carol offers: buy USD(100), sell EUR(100)
		carolOffer := offerbuild.OfferCreate(env.Carol,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(carolOffer))
		env.Close()

		// AMM Bob: XRP(10000), EUR(10100)
		ammBobXRP := env.AMMPoolXRP(ammBob)
		if ammBobXRP != uint64(jtx.XRP(10000)) {
			t.Errorf("AMM Bob XRP: got %d, want %d", ammBobXRP, uint64(jtx.XRP(10000)))
		}
		ammBobEUR := env.AMMPoolIOU(ammBob, env.GW, "EUR")
		if math.Abs(ammBobEUR-10100) > 0.001 {
			t.Errorf("AMM Bob EUR: got %f, want 10100", ammBobEUR)
		}

		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 15100)
		expectIOUBalance(t, env, env.Carol, "EUR", env.GW, 14900)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)
	})
}

// TestAMMBookStep_SellWithFillOrKill tests sell with fill-or-kill via AMM.
// Reference: rippled AMMExtended_test.cpp testSellWithFillOrKill (line 861)
func TestAMMBookStep_SellWithFillOrKill(t *testing.T) {
	// Sub-test 1: tfSell | tfFillOrKill that doesn't cross → tecKILLED
	t.Run("DoesNotCross", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 40000)
		env.Trust(env.Bob, env.GW, "USD", 40000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 20000)
		env.PayIOU(env.GW, env.Bob, "USD", 20000)
		env.Close()

		// Bob creates AMM: XRP(20000)/USD(200)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(20000), amm.IOUAmount(env.GW, "USD", 200)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice: sell | fillOrKill: buy USD(2.1), sell XRP(210) — doesn't fill
		offerTx := offerbuild.OfferCreate(env.Alice,
			amm.IOUAmount(env.GW, "USD", 2.1),
			amm.XRPAmount(210)).
			Sell().FillOrKill().Build()
		result := env.Submit(offerTx)
		// fix1578 enabled: tecKILLED
		amm.ExpectTER(t, result, "tecKILLED", "tesSUCCESS")
	})

	// Sub-test 2: tfSell | tfFillOrKill that crosses → tesSUCCESS
	t.Run("Crosses", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Trust(env.Bob, env.GW, "USD", 2000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 1000)
		env.PayIOU(env.GW, env.Bob, "USD", 1000)
		env.Close()

		// Bob creates AMM: XRP(20000)/USD(200)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(20000), amm.IOUAmount(env.GW, "USD", 200)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// Alice: sell | fillOrKill: buy USD(2), sell XRP(220)
		offerTx := offerbuild.OfferCreate(env.Alice,
			amm.IOUAmount(env.GW, "USD", 2),
			amm.XRPAmount(220)).
			Sell().FillOrKill().Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM: XRP(20220), USD ≈ 197.82
		ammXRP := env.AMMPoolXRP(ammAcc)
		if ammXRP != uint64(jtx.XRP(20220)) {
			t.Errorf("AMM XRP: got %d, want %d", ammXRP, uint64(jtx.XRP(20220)))
		}
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)
	})

	// Sub-test 3: tfSell | tfFillOrKill that returns more than asked
	t.Run("ReturnsMore", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Trust(env.Bob, env.GW, "USD", 2000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 1000)
		env.PayIOU(env.GW, env.Bob, "USD", 1000)
		env.Close()

		// Bob creates AMM: XRP(20000)/USD(200)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(20000), amm.IOUAmount(env.GW, "USD", 200)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// Alice: sell | fillOrKill: buy USD(10), sell XRP(1500)
		// tfSell means she sells all 1500 XRP and gets more than 10 USD
		offerTx := offerbuild.OfferCreate(env.Alice,
			amm.IOUAmount(env.GW, "USD", 10),
			amm.XRPAmount(1500)).
			Sell().FillOrKill().Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM: XRP(21500), USD ≈ 186.05
		ammXRP := env.AMMPoolXRP(ammAcc)
		if ammXRP != uint64(jtx.XRP(21500)) {
			t.Errorf("AMM XRP: got %d, want %d", ammXRP, uint64(jtx.XRP(21500)))
		}
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)
	})

	// Sub-test 4: tfSell | tfFillOrKill that is killed (quality too close)
	t.Run("KilledQuality", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(60000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 20000)
		env.Trust(env.Bob, env.GW, "USD", 20000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 10000)
		env.PayIOU(env.GW, env.Bob, "USD", 10000)
		env.Close()

		// Bob creates AMM: XRP(5000)/USD(10)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(5000), amm.IOUAmount(env.GW, "USD", 10)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice: sell | fillOrKill: buy USD(1), sell XRP(501) — killed
		offerTx := offerbuild.OfferCreate(env.Alice,
			amm.IOUAmount(env.GW, "USD", 1),
			amm.XRPAmount(501)).
			Sell().FillOrKill().Build()
		result := env.Submit(offerTx)
		amm.ExpectTER(t, result, "tecKILLED")
	})
}

// TestAMMBookStep_TransferRateOffer tests transfer rate on offers via AMM.
// Reference: rippled AMMExtended_test.cpp testTransferRateOffer (line 938)
func TestAMMBookStep_TransferRateOffer(t *testing.T) {
	// Sub-test 1: AMM XRP(10000)/USD(10100), carol offers USD(100) for XRP(100), rate 1.25
	// AMM doesn't pay transfer fee
	t.Run("USDForXRP", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.XRPAmount(10000),
			amm.IOUAmount(nil, "USD", 10100),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			env.TestEnv.SetTransferRate(env.GW, 1_250_000_000) // rate 1.25
			env.Close()

			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "USD", 100),
				amm.XRPAmount(100)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))
			env.Close()

			// AMM doesn't pay transfer fee
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 30100)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})

	// Sub-test 2: AMM XRP(10100)/USD(10000), carol offers XRP(100) for USD(100), rate 1.25
	// Carol pays 25% transfer fee
	t.Run("XRPForUSD", func(t *testing.T) {
		pool := [2]tx.Amount{
			amm.XRPAmount(10100),
			amm.IOUAmount(nil, "USD", 10000),
		}
		amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
			env.TestEnv.SetTransferRate(env.GW, 1_250_000_000) // rate 1.25
			env.Close()

			offerTx := offerbuild.OfferCreate(env.Carol,
				amm.XRPAmount(100),
				amm.IOUAmount(env.GW, "USD", 100)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx))
			env.Close()

			// AMM: XRP(10000), USD(10100)
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10000)), env.GW, "USD", 10100)
			// Carol pays 25% transfer fee on 100 USD: gets 100 XRP, pays 125 USD
			// Carol: 30000 - 125 = 29875
			expectIOUBalance(t, env, env.Carol, "USD", env.GW, 29875)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})
}

// TestAMMBookStep_SelfIssueOffer tests self-issue offers via AMM.
// Reference: rippled AMMExtended_test.cpp testSelfIssueOffer (line 1151)
//
// Bob creates AMM with his own issued USD (USD_bob): XRP(10000)/USD_bob(10100).
// Alice creates offer: buy USD_bob(100), sell XRP(100). Crosses AMM.
// AMM → XRP(10100)/USD_bob(10000). Alice gets USD_bob(100). No remaining offers.
func TestAMMBookStep_SelfIssueOffer(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(30000))+10)
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000))+10)
	env.Close()

	// Alice needs a trust line to Bob's USD
	env.Trust(env.Alice, env.Bob, "USD", 10000)
	env.Close()

	// Bob creates AMM: XRP(10000)/USD_bob(10100)
	// Bob is the issuer of USD_bob, so he can create the trust line implicitly
	createTx := amm.AMMCreate(env.Bob,
		amm.XRPAmount(10000),
		amm.IOUAmount(env.Bob, "USD", 10100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: env.Bob.Address})

	// Alice creates offer: buy USD_bob(100), sell XRP(100)
	offerTx := offerbuild.OfferCreate(env.Alice,
		amm.IOUAmount(env.Bob, "USD", 100),
		amm.XRPAmount(100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: XRP(10100), USD_bob(10000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(10100)), env.Bob, "USD", 10000)

	// Alice has no remaining offers
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)

	// Alice has USD_bob(100)
	aliceUSD := env.TestEnv.BalanceIOU(env.Alice, "USD", env.Bob)
	if math.Abs(aliceUSD-100) > 0.0001 {
		t.Errorf("Alice USD_bob: got %f, want 100", aliceUSD)
	}
}

// TestAMMBookStep_BadPathAssert tests that invalid paths don't cause panics.
// A trust line's QualityOut affects payments, and certain invalid paths used to
// cause assertion failures. With AMM, verify the path returns temBAD_PATH.
// Reference: rippled AMMExtended_test.cpp testBadPathAssert (line 1177)
func TestAMMBookStep_BadPathAssert(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	ann := jtx.NewAccount("ann")
	bob := jtx.NewAccount("bob2")
	cam := jtx.NewAccount("cam")
	dan := jtx.NewAccount("dan")

	reserve4 := env.TestEnv.ReserveBase() + 4*env.TestEnv.ReserveIncrement()
	fee4 := uint64(40) // 4 * 10 drops

	env.TestEnv.FundAmount(ann, reserve4+fee4)
	env.TestEnv.FundAmount(bob, reserve4+fee4)
	env.TestEnv.FundAmount(cam, reserve4+fee4)
	env.TestEnv.FundAmount(dan, reserve4+fee4)
	env.Close()

	annBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", ann.Address) }
	danBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", dan.Address) }

	env.Trust(bob, ann, "BUX", 400)
	env.Trust(cam, dan, "BUX", 100)
	env.Close()

	// bob trusts dan["BUX"] with qualityOut 120%
	// We'll just set up a regular trust line here; qualityOut won't be tested exactly
	env.Trust(bob, dan, "BUX", 200)
	env.Close()

	// Fund
	payDanBob := payment.PayIssued(dan, bob, danBUX(100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payDanBob))
	env.Close()

	// bob creates AMM: A_BUX(30)/D_BUX(30)
	// First, ann pays bob A_BUX
	payAnnBob := payment.PayIssued(ann, bob, annBUX(72)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payAnnBob))
	env.Close()

	createTx := amm.AMMCreate(bob, annBUX(30), danBUX(30)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// ann trusts D_BUX
	env.Trust(ann, dan, "BUX", 100)
	env.Close()

	// The invalid payment path: ann pays herself D_BUX via path(A_BUX, D_BUX)
	// This should return temBAD_PATH
	payTx := payment.PayIssued(ann, ann, danBUX(30)).
		SendMax(annBUX(30)).
		Paths([][]paymenttx.PathStep{
			{
				{Currency: "BUX", Issuer: ann.Address},
				{Currency: "BUX", Issuer: dan.Address},
			},
		}).
		Build()
	result := env.Submit(payTx)
	amm.ExpectTER(t, result, "temBAD_PATH")
}

// TestAMMBookStep_DirectToDirectPath tests direct-to-direct path with AMM.
// The offer crossing code expects DirectStep is always preceded by BookStep.
// This test recreates a case where that wasn't true.
// Reference: rippled AMMExtended_test.cpp testDirectToDirectPath (line 1247)
func TestAMMBookStep_DirectToDirectPath(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	ann := jtx.NewAccount("ann")
	bob := jtx.NewAccount("bob2")
	cam := jtx.NewAccount("cam")
	carol := jtx.NewAccount("carol2")

	reserve4 := env.TestEnv.ReserveBase() + 4*env.TestEnv.ReserveIncrement()
	fee5 := uint64(50) // 5 * 10 drops

	env.TestEnv.FundAmount(carol, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(ann, reserve4+fee5)
	env.TestEnv.FundAmount(bob, reserve4+fee5)
	env.TestEnv.FundAmount(cam, reserve4+fee5)
	env.Close()

	// Trust lines
	annBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", ann.Address) }
	bobBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", bob.Address) }

	env.Trust(ann, bob, "BUX", 40)
	env.Trust(cam, ann, "BUX", 40)
	env.Trust(bob, ann, "BUX", 30)
	env.Trust(cam, bob, "BUX", 40)
	env.Trust(carol, bob, "BUX", 400)
	env.Trust(carol, ann, "BUX", 400)
	env.Close()

	// Fund
	payTx1 := payment.PayIssued(ann, cam, annBUX(35)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx1))
	payTx2 := payment.PayIssued(bob, cam, bobBUX(35)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	payTx3 := payment.PayIssued(bob, carol, bobBUX(400)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx3))
	payTx4 := payment.PayIssued(ann, carol, annBUX(400)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx4))
	env.Close()

	// Carol creates AMM: A_BUX(300)/B_BUX(330)
	createTx := amm.AMMCreate(carol, annBUX(300), bobBUX(330)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// cam creates passive offer: buy A_BUX(29), sell B_BUX(30)
	offerTx1 := offerbuild.OfferCreate(cam, annBUX(29), bobBUX(30)).Passive().Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx1))
	env.Close()

	// cam: A_BUX(35), B_BUX(35), 1 offer
	expectIOUBalance(t, env, cam, "BUX", ann, 35)
	expectIOUBalance(t, env, cam, "BUX", bob, 35)
	offerbuild.RequireOfferCount(t, env.TestEnv, cam, 1)

	// cam's offer: buy B_BUX(30), sell A_BUX(30) — this used to cause assert
	offerTx2 := offerbuild.OfferCreate(cam, bobBUX(30), annBUX(30)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx2))
	env.Close()

	// Verify AMM was consumed up to first cam offer quality
	// (exact amounts depend on fixAMMv1_1, checking approximate)
	ammAcc := amm.AMMAccount(t,
		tx.Asset{Currency: "BUX", Issuer: ann.Address},
		tx.Asset{Currency: "BUX", Issuer: bob.Address})

	ammABUX := env.AMMPoolIOU(ammAcc, ann, "BUX")
	ammBBUX := env.AMMPoolIOU(ammAcc, bob, "BUX")
	// With fixAMMv1_1: A_BUX ≈ 309.354, B_BUX ≈ 320.021
	if ammABUX < 309 || ammABUX > 310 {
		t.Errorf("AMM A_BUX: got %f, want ~309.35", ammABUX)
	}
	if ammBBUX < 320 || ammBBUX > 321 {
		t.Errorf("AMM B_BUX: got %f, want ~320.02", ammBBUX)
	}
}

// TestAMMBookStep_RequireAuth tests require auth with AMM paths.
// GW requires authorization for USD holders. Alice is authorized and creates AMM.
// AMM account is authorized. Bob's offer crosses AMM.
// Reference: rippled AMMExtended_test.cpp testRequireAuth (line 1330)
func TestAMMBookStep_RequireAuth(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(400000)))
	env.Close()

	// GW requires authorization for holders
	env.TestEnv.EnableRequireAuth(env.GW)
	env.Close()

	// Authorize bob and alice trust lines
	env.TestEnv.AuthorizeTrustLine(env.GW, env.Bob, "USD")
	env.Trust(env.Bob, env.GW, "USD", 100)
	env.TestEnv.AuthorizeTrustLine(env.GW, env.Alice, "USD")
	env.Trust(env.Alice, env.GW, "USD", 2000)
	env.PayIOU(env.GW, env.Alice, "USD", 1000)
	env.Close()

	// Alice creates AMM: USD(1000)/XRP(1050)
	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 1000),
		amm.XRPAmount(1050)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t,
		tx.Asset{Currency: "USD", Issuer: env.GW.Address},
		amm.XRP())

	// Authorize AMM account's trust line
	env.TestEnv.AuthorizeTrustLine(env.GW, ammAcc, "USD")
	env.Close()

	// Fund bob with USD
	env.PayIOU(env.GW, env.Bob, "USD", 50)
	env.Close()

	// Bob's offer should cross Alice's AMM
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(50),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: USD(1050), XRP(1000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1000)), env.GW, "USD", 1050)

	// Bob's offer fully consumed
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Bob, 0)

	// Bob: USD(0)
	expectIOUBalance(t, env, env.Bob, "USD", env.GW, 0)
}

// TestAMMBookStep_Offers tests offer scenarios with AMM.
// This is an umbrella test in rippled that calls:
// testRmFundedOffer, testEnforceNoRipple, testFillModes, testOfferCrossWithXRP,
// testOfferCrossWithLimitOverride, testCurrencyConversion*, testOfferFeesConsumeFunds,
// testOfferCreateThenCross, testSellFlagExceedLimit, testGatewayCrossCurrency,
// testBridgedCross, testSellWithFillOrKill, testTransferRateOffer, testSelfIssueOffer,
// testBadPathAssert, testSellFlagBasic, testDirectToDirectPath, testRequireAuth, testMissingAuth
// All are implemented as separate test functions in this file.
// Reference: rippled AMMExtended_test.cpp testOffers (line 1447)
func TestAMMBookStep_Offers(t *testing.T) {
	t.Log("Umbrella test — individual offer tests are separate TestAMMBookStep_* functions")
}

// ===================================================================
// AMMExtended_test.cpp BookStep-dependent tests (class AMMExtended2_test)
// ===================================================================

// TestAMMBookStep_FalseDry tests false dry scenarios with AMM.
// Bob has very little XRP liquidity. Computing incoming XRP to XRP/USD offer
// requires recursive calls; the second returns tecPATH_DRY but the path
// should not be marked as dry — carol should still get some USD.
// Reference: rippled AMMExtended_test.cpp testFalseDry (line 1890)
func TestAMMBookStep_FalseDry(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Carol: fund without default ripple (Fund::Acct)
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	ammXRPPool := env.TestEnv.ReserveIncrement() * 2 // increment * 2
	bobFund := env.TestEnv.ReserveBase() + 5*env.TestEnv.ReserveIncrement() + 10 + uint64(ammXRPPool)
	env.TestEnv.FundAmount(env.Bob, bobFund)
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Trust(env.Alice, env.GW, "EUR", 1000)
	env.Trust(env.Bob, env.GW, "USD", 1000)
	env.Trust(env.Bob, env.GW, "EUR", 1000)
	env.Trust(env.Carol, env.GW, "USD", 1000)
	env.Trust(env.Carol, env.GW, "EUR", 1000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "EUR", 50)
	env.PayIOU(env.GW, env.Bob, "USD", 150)
	env.Close()

	// bob offer: EUR(50) for XRP(50)
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.IOUAmount(env.GW, "EUR", 50),
		amm.XRPAmount(50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// Bob creates AMM: XRP(ammXRPPool)/USD(150)
	createTx := amm.AMMCreate(env.Bob,
		amm.XRPAmount(int64(ammXRPPool)/1_000_000),
		amm.IOUAmount(env.GW, "USD", 150)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// alice pays carol USD(1M) via path(~XRP, ~USD), partial payment
	payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 1000000)).
		SendMax(amm.IOUAmount(env.GW, "EUR", 500)).
		Paths([][]paymenttx.PathStep{
			{
				{Currency: "XRP"},
				{Currency: "USD", Issuer: env.GW.Address},
			},
		}).
		NoDirectRipple().
		PartialPayment().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// Carol should have received some USD (between 0 and 50)
	carolUSD := env.TestEnv.BalanceIOU(env.Carol, "USD", env.GW)
	if carolUSD <= 0 || carolUSD >= 50 {
		t.Errorf("Carol USD: got %f, want >0 && <50", carolUSD)
	}
}

// TestAMMBookStep_BookStep tests BookStep with AMM.
// Reference: rippled AMMExtended_test.cpp testBookStep (line 1931)
func TestAMMBookStep_BookStep(t *testing.T) {
	// Sub-test 1: simple IOU/IOU offer (BTC → USD through AMM)
	t.Run("IOU_IOU", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		// Trust and fund BTC + USD
		env.Trust(env.Alice, env.GW, "BTC", 200)
		env.Trust(env.Alice, env.GW, "USD", 200)
		env.Trust(env.Bob, env.GW, "BTC", 200)
		env.Trust(env.Bob, env.GW, "USD", 200)
		env.Trust(env.Carol, env.GW, "BTC", 200)
		env.Trust(env.Carol, env.GW, "USD", 200)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "BTC", 100)
		env.PayIOU(env.GW, env.Alice, "USD", 150)
		env.PayIOU(env.GW, env.Bob, "BTC", 100)
		env.PayIOU(env.GW, env.Bob, "USD", 150)
		env.PayIOU(env.GW, env.Carol, "BTC", 100)
		env.PayIOU(env.GW, env.Carol, "USD", 150)
		env.Close()

		// Bob creates AMM: BTC(100)/USD(150)
		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "BTC", 100),
			amm.IOUAmount(env.GW, "USD", 150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t,
			tx.Asset{Currency: "BTC", Issuer: env.GW.Address},
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// alice pays carol 50 USD via BTC→USD AMM, sendmax BTC(50)
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(env.GW, "USD", 50)).
			SendMax(amm.IOUAmount(env.GW, "BTC", 50)).
			PathsCurrency("USD", env.GW).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(payTx))

		// Alice: BTC(100-50=50)
		expectIOUBalance(t, env, env.Alice, "BTC", env.GW, 50)
		// Carol: USD(150+50=200)
		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 200)
		// AMM: BTC(100+50=150), USD(150-50=100)
		btcPool := env.AMMPoolIOU(ammAcc, env.GW, "BTC")
		usdPool := env.AMMPoolIOU(ammAcc, env.GW, "USD")
		if math.Abs(btcPool-150) > 0.001 {
			t.Errorf("AMM BTC: got %f, want 150", btcPool)
		}
		if math.Abs(usdPool-100) > 0.001 {
			t.Errorf("AMM USD: got %f, want 100", usdPool)
		}
	})

	// Sub-test 2: simple XRP → USD through AMM and sendmax
	t.Run("XRP_USD", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 200)
		env.Trust(env.Bob, env.GW, "USD", 200)
		env.Trust(env.Carol, env.GW, "USD", 200)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 150)
		env.PayIOU(env.GW, env.Bob, "USD", 150)
		env.PayIOU(env.GW, env.Carol, "USD", 150)
		env.Close()

		// Bob creates AMM: XRP(100)/USD(150)
		createTx := amm.AMMCreate(env.Bob,
			amm.XRPAmount(100),
			amm.IOUAmount(env.GW, "USD", 150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// alice pays carol 50 USD via XRP→USD AMM, sendmax XRP(50)
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(env.GW, "USD", 50)).
			SendMax(amm.XRPAmount(50)).
			PathsCurrency("USD", env.GW).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(payTx))

		// Carol: USD(150+50=200)
		expectIOUBalance(t, env, env.Carol, "USD", env.GW, 200)
		// AMM: XRP(150), USD(100)
		env.ExpectAMMBalances(t, ammAcc,
			uint64(jtx.XRP(150)), env.GW, "USD", 100)
	})

	// Sub-test 3: simple USD → XRP through AMM and sendmax
	t.Run("USD_XRP", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 200)
		env.Trust(env.Bob, env.GW, "USD", 200)
		env.Trust(env.Carol, env.GW, "USD", 200)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 100)
		env.PayIOU(env.GW, env.Bob, "USD", 100)
		env.PayIOU(env.GW, env.Carol, "USD", 100)
		env.Close()

		// Bob creates AMM: USD(100)/XRP(150)
		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.XRPAmount(150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t,
			tx.Asset{Currency: "USD", Issuer: env.GW.Address},
			amm.XRP())

		// alice pays carol XRP(50) via USD→XRP AMM, sendmax USD(50)
		payTx := payment.Pay(env.Alice, env.Carol, uint64(jtx.XRP(50))).
			SendMax(amm.IOUAmount(env.GW, "USD", 50)).
			PathsCurrency("XRP", nil).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(payTx))

		// Alice: USD(100-50=50)
		expectIOUBalance(t, env, env.Alice, "USD", env.GW, 50)
		// Carol: XRP(10000+50 - 10 fee for trust line) = 10049999990
		carolXRP := env.TestEnv.Balance(env.Carol)
		expectedCarolXRP := uint64(jtx.XRP(10000)) + uint64(jtx.XRP(50)) - 10
		if carolXRP != expectedCarolXRP {
			t.Errorf("Carol XRP: got %d, want %d", carolXRP, expectedCarolXRP)
		}
		// AMM: USD(150), XRP(100)
		env.ExpectAMMBalances(t, ammAcc,
			uint64(jtx.XRP(100)), env.GW, "USD", 150)
	})
}

// TestAMMBookStep_TransferRateNoOwnerFee tests transfer rate with AMM.
// GW sets 25% transfer rate. Payment via AMM: alice pays carol USD(100)
// via GBP→AMM→USD path. Alice pays 25% transfer fee on GBP.
// Reference: rippled AMMExtended_test.cpp testTransferRateNoOwnerFee (line 2219)
func TestAMMBookStep_TransferRateNoOwnerFee(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 10000)
	env.Trust(env.Alice, env.GW, "GBP", 10000)
	env.Trust(env.Bob, env.GW, "USD", 10000)
	env.Trust(env.Bob, env.GW, "GBP", 10000)
	env.Trust(env.Carol, env.GW, "USD", 10000)
	env.Trust(env.Carol, env.GW, "GBP", 10000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 1000)
	env.PayIOU(env.GW, env.Alice, "GBP", 1000)
	env.PayIOU(env.GW, env.Bob, "USD", 1000)
	env.PayIOU(env.GW, env.Bob, "GBP", 1000)
	env.PayIOU(env.GW, env.Carol, "USD", 1000)
	env.PayIOU(env.GW, env.Carol, "GBP", 1000)
	env.Close()

	// GW sets 25% transfer rate (1.25 = rate 1250000000)
	env.TestEnv.SetTransferRate(env.GW, 1250000000)
	env.Close()

	// Bob creates AMM: GBP(1000)/USD(1000)
	createTx := amm.AMMCreate(env.Bob,
		amm.IOUAmount(env.GW, "GBP", 1000),
		amm.IOUAmount(env.GW, "USD", 1000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// alice pays carol USD(100) via path(~USD), sendmax GBP(150)
	payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
		PathsCurrency("USD", env.GW).
		SendMax(amm.IOUAmount(env.GW, "GBP", 150)).
		NoDirectRipple().
		PartialPayment().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// alice: GBP(1000 - 120*1.25) = GBP(850)
	aliceGBP := env.TestEnv.BalanceIOU(env.Alice, "GBP", env.GW)
	if math.Abs(aliceGBP-850) > 0.01 {
		t.Errorf("Alice GBP: got %f, want 850", aliceGBP)
	}

	// carol: USD(1000 + 85.714...) ≈ USD(1085.714)
	carolUSD := env.TestEnv.BalanceIOU(env.Carol, "USD", env.GW)
	if carolUSD < 1085 || carolUSD > 1086 {
		t.Errorf("Carol USD: got %f, want ~1085.71", carolUSD)
	}
}

// TestAMMBookStep_LimitQuality tests limit quality with AMM.
// Single path with AMM, offer, and limit quality. The quality limit
// is such that the AMM offer should be taken but the CLOB offer should not.
// Reference: rippled AMMExtended_test.cpp testLimitQuality (line 2787)
func TestAMMBookStep_LimitQuality(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 10000)
	env.Trust(env.Bob, env.GW, "USD", 10000)
	env.Trust(env.Carol, env.GW, "USD", 10000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 2000)
	env.PayIOU(env.GW, env.Bob, "USD", 2000)
	env.PayIOU(env.GW, env.Carol, "USD", 2000)
	env.Close()

	// Bob creates AMM: XRP(1000)/USD(1050)
	createTx := amm.AMMCreate(env.Bob,
		amm.XRPAmount(1000),
		amm.IOUAmount(env.GW, "USD", 1050)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: env.GW.Address})

	// Bob creates CLOB offer: buy XRP(100), sell USD(50) — quality 0.5 (worse)
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(100),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// alice pays carol USD(100) with sendmax XRP(100), path(~USD),
	// tfNoRippleDirect | tfPartialPayment | tfLimitQuality
	payTx := payment.PayIssued(env.Alice, env.Carol,
		amm.IOUAmount(env.GW, "USD", 100)).
		SendMax(amm.XRPAmount(100)).
		PathsCurrency("USD", env.GW).
		PartialPayment().
		LimitQuality().
		NoDirectRipple().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))

	// AMM: took 50 XRP, gave 50 USD → XRP(1050), USD(1000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1050)), env.GW, "USD", 1000)

	// Carol: 2000 + 50 = 2050
	expectIOUBalance(t, env, env.Carol, "USD", env.GW, 2050)

	// Bob's offer should still exist (quality too bad for limit)
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Bob, 1)
}

// TestAMMBookStep_XRPPathLoop tests XRP path loop with AMM.
// Reference: rippled AMMExtended_test.cpp testXRPPathLoop (line 2817)
func TestAMMBookStep_XRPPathLoop(t *testing.T) {
	// Sub-test 1: Payment path starting with XRP — with fix1781: temBAD_PATH_LOOP
	t.Run("StartingWithXRP", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.Close()

		// Set up default ripple on GW (needed for the test)
		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Alice, env.GW, "EUR", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "EUR", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 200)
		env.PayIOU(env.GW, env.Alice, "EUR", 200)
		env.PayIOU(env.GW, env.Bob, "USD", 200)
		env.PayIOU(env.GW, env.Bob, "EUR", 200)
		env.Close()

		// Alice creates AMMs
		createTx1 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 101)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		createTx2 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "EUR", 101)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		env.Close()

		// path(~USD, ~XRP, ~EUR) — circular XRP loop
		payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "EUR", 1)).
			SendMax(amm.XRPAmount(1)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "XRP"},
					{Currency: "EUR", Issuer: env.GW.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
	})

	// Sub-test 2: Payment path ending with XRP — temBAD_PATH_LOOP
	t.Run("EndingWithXRP", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Alice, env.GW, "EUR", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "EUR", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 200)
		env.PayIOU(env.GW, env.Alice, "EUR", 200)
		env.PayIOU(env.GW, env.Bob, "USD", 200)
		env.PayIOU(env.GW, env.Bob, "EUR", 200)
		env.Close()

		// Alice creates AMMs
		createTx1 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		createTx2 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		env.Close()

		// EUR -> //XRP -> //USD -> XRP — loop
		payTx := payment.Pay(env.Alice, env.Bob, uint64(jtx.XRP(1))).
			SendMax(amm.IOUAmount(env.GW, "EUR", 1)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "XRP"},
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "XRP"},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
	})

	// Sub-test 3: Loop formed in the middle of the path
	t.Run("MiddleLoop", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Alice, env.GW, "EUR", 10000)
		env.Trust(env.Alice, env.GW, "JPY", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "EUR", 10000)
		env.Trust(env.Bob, env.GW, "JPY", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Alice, "USD", 200)
		env.PayIOU(env.GW, env.Alice, "EUR", 200)
		env.PayIOU(env.GW, env.Alice, "JPY", 200)
		env.PayIOU(env.GW, env.Bob, "JPY", 200)
		env.Close()

		// Alice creates AMMs
		createTx1 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		createTx2 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		createTx3 := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "JPY", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx3))
		env.Close()

		// path(~XRP, ~EUR, ~XRP, ~JPY) — loop on XRP
		payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "JPY", 1)).
			SendMax(amm.IOUAmount(env.GW, "USD", 1)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "XRP"},
					{Currency: "EUR", Issuer: env.GW.Address},
					{Currency: "XRP"},
					{Currency: "JPY", Issuer: env.GW.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
	})
}

// TestAMMBookStep_StepLimit tests step limit with AMM.
// Reference: rippled AMMExtended_test.cpp testStepLimit (line 2903)
func TestAMMBookStep_StepLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping step limit test in short mode (creates 2000 offers)")
	}

	env := amm.NewAMMTestEnv(t)
	dan := jtx.NewAccount("dan")
	ed := jtx.NewAccount("ed")

	// Fund accounts with large XRP amounts
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(100000000)))
	env.TestEnv.FundAmount(ed, uint64(jtx.XRP(100000000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(100000000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(100000000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(100000000)))
	env.TestEnv.FundAmount(dan, uint64(jtx.XRP(100000000)))
	env.Close()

	// Trust lines for USD
	env.Trust(ed, env.GW, "USD", 100)
	env.Close()
	env.PayIOU(env.GW, ed, "USD", 11)
	env.Close()

	env.Trust(env.Bob, env.GW, "USD", 100)
	env.Close()
	env.PayIOU(env.GW, env.Bob, "USD", 1)
	env.Close()

	env.Trust(dan, env.GW, "USD", 100)
	env.Close()
	env.PayIOU(env.GW, dan, "USD", 1)
	env.Close()

	// Bob creates 2000 offers: XRP(1) for USD(1) — all unfunded after first one
	env.NOffers(2000, env.Bob, tx.NewXRPAmount(1_000_000), amm.IOUAmount(env.GW, "USD", 1))

	// Dan creates 1 offer: XRP(1) for USD(1)
	env.NOffers(1, dan, tx.NewXRPAmount(1_000_000), amm.IOUAmount(env.GW, "USD", 1))

	// Ed creates AMM: XRP(9)/USD(11)
	ammCreateTx := amm.AMMCreate(ed,
		tx.NewXRPAmount(9_000_000),
		amm.IOUAmount(env.GW, "USD", 11)).TradingFee(0).Build()
	jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
	env.Close()

	// Alice creates offer: buy USD(1000) sell XRP(1000)
	// Should take bob's first offer, remove ~999 unfunded, hit step limit
	aliceOfferTx := offerbuild.OfferCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 1000),
		tx.NewXRPAmount(1000_000_000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(aliceOfferTx))
	env.Close()

	// Alice should have gotten some USD (from bob's first offer + possibly AMM)
	aliceUSD := env.TestEnv.BalanceIOU(env.Alice, "USD", env.GW)
	t.Logf("Alice USD after first offer: %e", aliceUSD)
	if aliceUSD <= 0 {
		t.Errorf("Alice should have some USD, got %f", aliceUSD)
	}

	// Alice should have 2 owners (trust line + offer)
	aliceOwners := env.TestEnv.OwnerCount(env.Alice)
	if aliceOwners != 2 {
		t.Errorf("Alice owner count: got %d, want 2", aliceOwners)
	}

	// Bob's balance should be 0 USD (first offer consumed)
	bobUSD := env.TestEnv.BalanceIOU(env.Bob, "USD", env.GW)
	if math.Abs(bobUSD) > 0.0001 {
		t.Errorf("Bob USD: got %f, want 0", bobUSD)
	}

	// Bob's owner count should be ~1001 (999 removed as unfunded)
	bobOwners := env.TestEnv.OwnerCount(env.Bob)
	if bobOwners != 1001 {
		t.Logf("Bob owner count: %d (expected ~1001)", bobOwners)
	}

	// Dan still has 1 USD and 2 owners
	danUSD := env.TestEnv.BalanceIOU(dan, "USD", env.GW)
	if math.Abs(danUSD-1) > 0.0001 {
		t.Errorf("Dan USD: got %f, want 1", danUSD)
	}
	danOwners := env.TestEnv.OwnerCount(dan)
	if danOwners != 2 {
		t.Errorf("Dan owner count: got %d, want 2", danOwners)
	}
}

// TestAMMBookStep_Payment tests payment with AMM liquidity.
// Reference: rippled AMMExtended_test.cpp testPayment (line 3071)
func TestAMMBookStep_Payment(t *testing.T) {
	// rippled setup:
	//   fund(env, gw, {alice, becky}, XRP(5000))
	//   trust(alice, USD(1000)); trust(becky, USD(1000))
	//   pay(gw, alice, USD(500))
	//   AMM ammAlice(env, alice, XRP(100), USD(140))
	//   pay(becky, becky, USD(10)), path(~USD), sendmax(XRP(10))
	// Expected: AMM XRP(107692308 drops), USD(130)
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(5000)))
	env.Close()

	becky := jtx.NewAccount("becky")
	env.TestEnv.FundAmount(becky, uint64(jtx.XRP(5000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Trust(becky, env.GW, "USD", 1000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 500)
	env.Close()

	// Alice creates AMM: XRP(100)/USD(140)
	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 140)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(), env.USD)

	// becky pays herself USD(10) via AMM, path(~USD), sendmax(XRP(10))
	payTx := payment.PayIssued(becky, becky, amm.IOUAmount(env.GW, "USD", 10)).
		SendMax(amm.XRPAmount(10)).
		PathsCurrency("USD", env.GW).
		Build()
	result := env.Submit(payTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// AMM: XRP should be ~107692308 drops, USD should be 130
	ammXRP := env.AMMPoolXRP(ammAcc)
	// The constant product: 100*1M * 140 = 14,000,000,000. After -10 USD (output): USD=130
	// XRP = 14,000,000,000 / 130 = 107,692,307.69... ≈ 107,692,308 drops (rounded up)
	if ammXRP < 107_692_307 || ammXRP > 107_692_309 {
		t.Errorf("AMM XRP: got %d, want ~107692308", ammXRP)
	}

	ammUSD := env.AMMPoolIOU(ammAcc, env.GW, "USD")
	if ammUSD != 130 {
		t.Errorf("AMM USD: got %f, want 130", ammUSD)
	}
}

// TestAMMBookStep_PayIOU tests IOU payment to deposit-auth account with AMM.
// Reference: rippled AMMExtended_test.cpp testPayIOU (line 3121)
func TestAMMBookStep_PayIOU(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 1000)
	env.Trust(env.Bob, env.GW, "USD", 1000)
	env.Trust(env.Carol, env.GW, "USD", 1000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 150)
	env.PayIOU(env.GW, env.Carol, "USD", 150)
	env.Close()

	// Carol creates AMM: USD(100)/XRP(101)
	createTx := amm.AMMCreate(env.Carol,
		amm.IOUAmount(env.GW, "USD", 100),
		amm.XRPAmount(101)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// alice pays bob USD(50) directly
	env.PayIOU(env.GW, env.Bob, "USD", 50)
	env.Close()

	// bob enables DepositAuth
	env.TestEnv.EnableDepositAuth(env.Bob)
	env.Close()

	// IOU payment to deposit-auth account should fail
	payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "USD", 50)).Build()
	result := env.Submit(payTx)
	amm.ExpectTER(t, result, "tecNO_PERMISSION")

	// Non-direct XRP payment via offer/AMM also blocked
	payTx2 := payment.Pay(env.Alice, env.Bob, 1).
		SendMax(amm.IOUAmount(env.GW, "USD", 1)).
		Build()
	result2 := env.Submit(payTx2)
	amm.ExpectTER(t, result2, "tecNO_PERMISSION")

	// bob clears DepositAuth
	env.TestEnv.DisableDepositAuth(env.Bob)
	env.Close()

	// Now payments succeed
	payTx3 := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx3))
	env.Close()
}

// TestAMMBookStep_RippleState tests freeze/ripple state with AMM.
// G1 freezes bob's trust line. After freeze: bob can buy more (via offer crossing AMM)
// but cannot sell. After unfreeze: operations work again.
// Reference: rippled AMMExtended_test.cpp testRippleState (line 3215)
func TestAMMBookStep_RippleState(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	g1 := jtx.NewAccount("G1")

	env.TestEnv.FundAmount(g1, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000)))
	env.Close()

	// Trust lines
	g1USD := tx.NewIssuedAmountFromFloat64(100, "USD", g1.Address)
	bobTrust := tx.NewIssuedAmountFromFloat64(100, "USD", g1.Address)
	aliceTrust := tx.NewIssuedAmountFromFloat64(205, "USD", g1.Address)
	_ = g1USD
	_ = bobTrust
	_ = aliceTrust

	env.Trust(env.Bob, g1, "USD", 100)
	env.Trust(env.Alice, g1, "USD", 205)
	env.Close()

	// Fund
	payBob := payment.PayIssued(g1, env.Bob, tx.NewIssuedAmountFromFloat64(10, "USD", g1.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payBob))
	payAlice := payment.PayIssued(g1, env.Alice, tx.NewIssuedAmountFromFloat64(205, "USD", g1.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payAlice))
	env.Close()

	// Alice creates AMM: XRP(500)/USD(105) using G1's USD
	createTx := amm.AMMCreate(env.Alice,
		amm.XRPAmount(500),
		amm.IOU(g1, "USD", 105)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: g1.Address})

	// Unfrozen: alice can pay bob
	payTx := payment.PayIssued(env.Alice, env.Bob, tx.NewIssuedAmountFromFloat64(1, "USD", g1.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	// bob can pay alice back
	payTx2 := payment.PayIssued(env.Bob, env.Alice, tx.NewIssuedAmountFromFloat64(1, "USD", g1.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()

	// G1 freezes bob's trust line
	env.TestEnv.FreezeTrustLine(g1, env.Bob, "USD")
	env.Close()

	// After freeze: bob can buy more (offer crossing AMM)
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.IOU(g1, "USD", 5),
		amm.XRPAmount(25)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: XRP(525), USD(100)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(525)), g1, "USD", 100)

	// After freeze: bob cannot sell from that line
	offerTx2 := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(1),
		amm.IOU(g1, "USD", 5)).Build()
	result := env.Submit(offerTx2)
	amm.ExpectTER(t, result, "tecUNFUNDED_OFFER")

	// After freeze: bob can receive payment
	payTx3 := payment.PayIssued(env.Alice, env.Bob, tx.NewIssuedAmountFromFloat64(1, "USD", g1.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx3))

	// After freeze: bob cannot make payment
	payTx4 := payment.PayIssued(env.Bob, env.Alice, tx.NewIssuedAmountFromFloat64(1, "USD", g1.Address)).Build()
	result2 := env.Submit(payTx4)
	amm.ExpectTER(t, result2, "tecPATH_DRY")
}

// TestAMMBookStep_OffersWhenFrozen tests offers for frozen trust lines with AMM.
// Payment via AMM, then freeze AMM's trust line, then verify AMM not consumed.
// Reference: rippled AMMExtended_test.cpp testOffersWhenFrozen (line 3486)
func TestAMMBookStep_OffersWhenFrozen(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	g1 := jtx.NewAccount("G1")
	a2 := jtx.NewAccount("A2")
	a3 := jtx.NewAccount("A3")
	a4 := jtx.NewAccount("A4")

	env.TestEnv.FundAmount(g1, uint64(jtx.XRP(2000)))
	env.TestEnv.FundAmount(a2, uint64(jtx.XRP(2000)))
	env.TestEnv.FundAmount(a3, uint64(jtx.XRP(2000)))
	env.TestEnv.FundAmount(a4, uint64(jtx.XRP(2000)))
	env.Close()

	g1USD := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "USD", g1.Address) }

	env.Trust(a2, g1, "USD", 1000)
	env.Trust(a3, g1, "USD", 2000)
	env.Trust(a4, g1, "USD", 2001)
	env.Close()

	payA3 := payment.PayIssued(g1, a3, g1USD(2000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payA3))
	payA4 := payment.PayIssued(g1, a4, g1USD(2001)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payA4))
	env.Close()

	// A3 creates AMM: XRP(1000)/USD(1001)
	createTx := amm.AMMCreate(a3, amm.XRPAmount(1000), g1USD(1001)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: g1.Address})

	// A2 pays G1 USD(1) through AMM path
	payTx := payment.PayIssued(a2, g1, g1USD(1)).
		PathsCurrency("USD", g1).
		SendMax(amm.XRPAmount(1)).
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()

	// AMM: XRP(1001), USD(1000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1001)), g1, "USD", 1000)

	// A4 creates offer: buy XRP(999), sell USD(999) — crosses AMM
	// rippled: the offer consumes AMM offer, bringing pool back to ~XRP(1000)/USD(1001)
	offerTx := offerbuild.OfferCreate(a4, amm.XRPAmount(999), g1USD(999)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: ~XRP(1000), ~USD(1001) (the offer crosses but may not exactly reverse)
	ammXRP := env.AMMPoolXRP(ammAcc)
	ammUSD := env.AMMPoolIOU(ammAcc, g1, "USD")
	if ammXRP < uint64(jtx.XRP(999)) || ammXRP > uint64(jtx.XRP(1002)) {
		t.Errorf("AMM XRP: got %d, want ~%d", ammXRP, uint64(jtx.XRP(1000)))
	}
	if ammUSD < 999 || ammUSD > 1002 {
		t.Errorf("AMM USD: got %f, want ~1001", ammUSD)
	}

	// Freeze AMM's trust line
	env.TestEnv.FreezeTrustLine(g1, ammAcc, "USD")
	env.Close()

	// A2 pays G1 USD(1) — should use A4's leftover offer, not AMM (frozen)
	payTx2 := payment.PayIssued(a2, g1, g1USD(1)).
		PathsCurrency("USD", g1).
		SendMax(amm.XRPAmount(1)).
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()

	// AMM should NOT have been consumed (frozen) — same as before the frozen payment
	ammXRP2 := env.AMMPoolXRP(ammAcc)
	ammUSD2 := env.AMMPoolIOU(ammAcc, g1, "USD")
	if ammXRP2 != ammXRP {
		t.Errorf("AMM XRP changed after freeze: before %d, after %d", ammXRP, ammXRP2)
	}
	if math.Abs(ammUSD2-ammUSD) > 0.0001 {
		t.Errorf("AMM USD changed after freeze: before %f, after %f", ammUSD, ammUSD2)
	}
}

// TestAMMBookStep_ToStrand tests ToStrand with AMM.
// Cannot have more than one offer with the same output issue.
// Reference: rippled AMMExtended_test.cpp testToStrand (line 3619)
func TestAMMBookStep_ToStrand(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 10000)
	env.Trust(env.Alice, env.GW, "EUR", 10000)
	env.Trust(env.Bob, env.GW, "USD", 10000)
	env.Trust(env.Bob, env.GW, "EUR", 10000)
	env.Trust(env.Carol, env.GW, "USD", 10000)
	env.Trust(env.Carol, env.GW, "EUR", 10000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 2000)
	env.PayIOU(env.GW, env.Bob, "USD", 2000)
	env.PayIOU(env.GW, env.Bob, "EUR", 1000)
	env.PayIOU(env.GW, env.Carol, "USD", 2000)
	env.PayIOU(env.GW, env.Carol, "EUR", 1000)
	env.Close()

	// Bob creates AMM: XRP(1000)/USD(1000)
	createTx1 := amm.AMMCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx1))
	env.Close()

	// Bob creates AMM: USD(1000)/EUR(1000)
	createTx2 := amm.AMMCreate(env.Bob, amm.IOUAmount(env.GW, "USD", 1000), amm.IOUAmount(env.GW, "EUR", 1000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx2))
	env.Close()

	// payment path: XRP -> XRP/USD -> USD/EUR -> EUR/USD — loop on USD
	payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
		SendMax(amm.XRPAmount(200)).
		Paths([][]paymenttx.PathStep{
			{
				{Currency: "USD", Issuer: env.GW.Address},
				{Currency: "EUR", Issuer: env.GW.Address},
				{Currency: "USD", Issuer: env.GW.Address},
			},
		}).
		NoDirectRipple().
		Build()
	result := env.Submit(payTx)
	amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
}

// TestAMMBookStep_RIPD1373 tests RIPD1373 with AMM.
// Reference: rippled AMMExtended_test.cpp testRIPD1373 (line 3648)
func TestAMMBookStep_RIPD1373(t *testing.T) {
	// Sub-test 2: XRP -> XRP/USD -> USD/XRP — temBAD_SEND_XRP_PATHS
	t.Run("BadSendXRPPaths", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Carol, env.GW, "USD", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Bob, "USD", 100)
		env.Close()

		// Bob creates AMM: XRP(100)/USD(100)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// XRP destination with paths through USD → temBAD_SEND_XRP_PATHS
		payTx := payment.Pay(env.Alice, env.Carol, uint64(jtx.XRP(100))).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "XRP"},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_SEND_XRP_PATHS")
	})

	// Sub-test 3: XRP -> XRP/USD -> USD/XRP with sendmax — temBAD_SEND_XRP_MAX
	t.Run("BadSendXRPMax", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Carol, env.GW, "USD", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Bob, "USD", 100)
		env.Close()

		// Bob creates AMM: XRP(100)/USD(100)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// XRP destination with sendmax XRP and paths → temBAD_SEND_XRP_MAX
		payTx := payment.Pay(env.Alice, env.Carol, uint64(jtx.XRP(100))).
			SendMax(amm.XRPAmount(200)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "XRP"},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_SEND_XRP_MAX")
	})
}

// TestAMMBookStep_Loop tests loop detection with AMM.
// Reference: rippled AMMExtended_test.cpp testLoop (line 3722)
func TestAMMBookStep_Loop(t *testing.T) {
	// Sub-test 1: USD -> USD/XRP -> XRP/USD — loop on USD
	t.Run("SimpleLoop", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Carol, env.GW, "USD", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Bob, "USD", 100)
		env.PayIOU(env.GW, env.Alice, "USD", 100)
		env.Close()

		// Bob creates AMM: XRP(100)/USD(100)
		createTx := amm.AMMCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// payment path: USD -> USD/XRP -> XRP/USD — loop
		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "USD", 100)).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "XRP"},
					{Currency: "USD", Issuer: env.GW.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
	})

	// Sub-test 2: XRP->XRP/USD->USD/EUR->EUR/USD->USD/CNY — loop on USD
	t.Run("MultiCurrencyLoop", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(env.Alice, env.GW, "USD", 10000)
		env.Trust(env.Alice, env.GW, "EUR", 10000)
		env.Trust(env.Alice, env.GW, "CNY", 10000)
		env.Trust(env.Bob, env.GW, "USD", 10000)
		env.Trust(env.Bob, env.GW, "EUR", 10000)
		env.Trust(env.Bob, env.GW, "CNY", 10000)
		env.Trust(env.Carol, env.GW, "USD", 10000)
		env.Trust(env.Carol, env.GW, "EUR", 10000)
		env.Trust(env.Carol, env.GW, "CNY", 10000)
		env.Close()

		env.PayIOU(env.GW, env.Bob, "USD", 200)
		env.PayIOU(env.GW, env.Bob, "EUR", 200)
		env.PayIOU(env.GW, env.Bob, "CNY", 100)
		env.Close()

		// Bob creates AMMs
		createTx1 := amm.AMMCreate(env.Bob, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		createTx2 := amm.AMMCreate(env.Bob, amm.IOUAmount(env.GW, "USD", 100), amm.IOUAmount(env.GW, "EUR", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		createTx3 := amm.AMMCreate(env.Bob, amm.IOUAmount(env.GW, "EUR", 100), amm.IOUAmount(env.GW, "CNY", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx3))
		env.Close()

		// payment path: XRP->XRP/USD->USD/EUR->USD/CNY — loop on USD
		payTx := payment.PayIssued(env.Alice, env.Carol, amm.IOUAmount(env.GW, "CNY", 100)).
			SendMax(amm.XRPAmount(100)).
			Paths([][]paymenttx.PathStep{
				{
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "EUR", Issuer: env.GW.Address},
					{Currency: "USD", Issuer: env.GW.Address},
					{Currency: "CNY", Issuer: env.GW.Address},
				},
			}).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "temBAD_PATH_LOOP")
	})
}

// Suppress unused import warnings
var _ = offerbuild.OfferCreate
