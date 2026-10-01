// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

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

			bob := jtx.NewAccount("bob")
			ed := jtx.NewAccount("ed")
			paul := jtx.NewAccount("paul")
			dan := jtx.NewAccount("dan")
			chris := jtx.NewAccount("chris")
			simon := jtx.NewAccount("simon")
			ben := jtx.NewAccount("ben")
			nataly := jtx.NewAccount("nataly")

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

			for range 10 {
				mustSubmit(amm.AMMDeposit(ben, xrpAsset, usdAsset).
					Amount(state.NewIssuedAmountFromValue(1, -10, "USD", env.GW.Address)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(ben, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(simon, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0.1)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(simon, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(chris, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 1)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(chris, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(dan, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 10)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(dan, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(bob, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 100)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(bob, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(env.Carol, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 1000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(env.Carol, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(ed, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 10000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(ed, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

				mustSubmit(amm.AMMDeposit(paul, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 100000)).
					SingleAsset().Build())
				mustSubmit(amm.AMMWithdraw(paul, xrpAsset, usdAsset).
					Amount(amm.IOUAmount(env.GW, "USD", 0)).
					OneAssetWithdrawAll().Build())

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
			expectedPoolUSD := state.NewIssuedAmountFromValue(100000000000003, -10, "USD", env.GW.Address)
			if poolUSD.Mantissa() != expectedPoolUSD.Mantissa() || poolUSD.Exponent() != expectedPoolUSD.Exponent() {
				t.Errorf("Pool USD: got %de%d, want %de%d",
					poolUSD.Mantissa(), poolUSD.Exponent(),
					expectedPoolUSD.Mantissa(), expectedPoolUSD.Exponent())
			}

			// ben, simon, chris, dan: exact 1,500,000 USD
			for _, acct := range []*jtx.Account{ben, simon, chris, dan} {
				bal := env.TestEnv.IOUBalance(acct, env.GW, "USD")
				if bal == nil {
					t.Errorf("%s: no USD balance", acct.Name)
					continue
				}
				exp := state.NewIssuedAmountFromValue(15, 5, "USD", env.GW.Address)
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
				exp := state.NewIssuedAmountFromValue(3, 4, "USD", env.GW.Address)
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
				exp := state.NewIssuedAmountFromValue(15, 5, "USD", env.GW.Address)
				if bal.Mantissa() != exp.Mantissa() || bal.Exponent() != exp.Exponent() {
					t.Errorf("%s USD: got %de%d, want %de%d",
						acct.Name, bal.Mantissa(), bal.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

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
				exp := state.NewIssuedAmountFromValue(300000000000003, -10, "USD", env.GW.Address)
				if aliceUSD.Mantissa() != exp.Mantissa() || aliceUSD.Exponent() != exp.Exponent() {
					t.Errorf("alice USD: got %de%d, want %de%d",
						aliceUSD.Mantissa(), aliceUSD.Exponent(),
						exp.Mantissa(), exp.Exponent())
				}
			}

			// alice XRP: initial(30000 XRP) - trustSetFee(10) - createFee(50M) - withdrawFee(10)
			// AMMCreate's special fee is one ReserveIncrement (50 XRP).
			aliceXRP := env.TestEnv.Balance(env.Alice)
			expectedAliceXRP := uint64(jtx.XRP(30000)) - 10 - env.TestEnv.ReserveIncrement() - 10
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

			bob := jtx.NewAccount("bob")
			ed := jtx.NewAccount("ed")
			paul := jtx.NewAccount("paul")
			dan := jtx.NewAccount("dan")
			chris := jtx.NewAccount("chris")
			simon := jtx.NewAccount("simon")
			ben := jtx.NewAccount("ben")
			nataly := jtx.NewAccount("nataly")

			// Reference: fund(env, gw, accounts, XRP(2'000'000), {}, Fund::Acct)
			accounts := []*jtx.Account{bob, ed, paul, dan, chris, simon, ben, nataly}
			for _, acct := range accounts {
				env.TestEnv.FundAmount(acct, uint64(jtx.XRP(2_000_000)))
			}
			env.Close()

			xrpAsset := amm.XRP()
			usdAsset := env.USD

			submitOp := func(iter int, who string, op string, txn tx.Transaction) {
				t.Helper()
				result := env.Submit(txn)
				if !result.Success {
					t.Fatalf("iter %d %s %s failed: %s: %s", iter, who, op, result.Code, result.Message)
				}
			}

			for i := range 10 {
				submitOp(i, "ben", "deposit", amm.AMMDeposit(ben, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(1)).
					SingleAsset().Build())
				submitOp(i, "ben", "withdraw", amm.AMMWithdraw(ben, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "simon", "deposit", amm.AMMDeposit(simon, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(1000)).
					SingleAsset().Build())
				submitOp(i, "simon", "withdraw", amm.AMMWithdraw(simon, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "chris", "deposit", amm.AMMDeposit(chris, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(1)).
					SingleAsset().Build())
				submitOp(i, "chris", "withdraw", amm.AMMWithdraw(chris, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "dan", "deposit", amm.AMMDeposit(dan, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(10)).
					SingleAsset().Build())
				submitOp(i, "dan", "withdraw", amm.AMMWithdraw(dan, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "bob", "deposit", amm.AMMDeposit(bob, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(100)).
					SingleAsset().Build())
				submitOp(i, "bob", "withdraw", amm.AMMWithdraw(bob, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "carol", "deposit", amm.AMMDeposit(env.Carol, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(1000)).
					SingleAsset().Build())
				submitOp(i, "carol", "withdraw", amm.AMMWithdraw(env.Carol, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "ed", "deposit", amm.AMMDeposit(ed, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(10000)).
					SingleAsset().Build())
				submitOp(i, "ed", "withdraw", amm.AMMWithdraw(ed, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

				submitOp(i, "paul", "deposit", amm.AMMDeposit(paul, xrpAsset, usdAsset).
					Amount(amm.XRPAmount(100000)).
					SingleAsset().Build())
				submitOp(i, "paul", "withdraw", amm.AMMWithdraw(paul, xrpAsset, usdAsset).
					Amount(tx.NewXRPAmount(0)).
					OneAssetWithdrawAll().Build())

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

			mustSubmit(amm.AMMWithdraw(env.Alice, xrpAsset, usdAsset).WithdrawAll().Build())

			// AMM should be deleted
			if ammData := env.ReadAMMData(xrpAsset, usdAsset); ammData != nil {
				t.Error("AMM should be deleted after alice withdrawAll")
			}

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

			// alice: initial(30000 XRP) - trustLineFee - createFee(ReserveIncrement) - withdrawFee + 80 pool rounding
			// TestAMM setup creates a USD trust line for alice, costing baseFee.
			// AMMCreate's special fee is one ReserveIncrement (50 XRP).
			aliceExpected := uint64(jtx.XRP(30000)) - baseFee - env.TestEnv.ReserveIncrement() - baseFee + 80
			aliceXRP := env.TestEnv.Balance(env.Alice)
			if aliceXRP != aliceExpected {
				t.Errorf("alice XRP: got %d, want %d", aliceXRP, aliceExpected)
			}
		})
	})
}

func TestAMMBookStep_SwapRounding(t *testing.T) {
	// Pool: XRP(51600.000981)/USD(803040.9987141784)
	// Bob offers XRP(6300) for USD(100000) — very bad quality, should not cross AMM.
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(200000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(200000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 1000000)
	env.Close()

	env.PayIOUAmount(env.GW, env.Alice,
		state.NewIssuedAmountFromValue(8040409987141784, -10, "USD", env.GW.Address))
	env.Close()

	createTx := amm.AMMCreate(env.Alice,
		tx.NewXRPAmount(51_600_000_981),
		state.NewIssuedAmountFromValue(8030409987141784, -10, "USD", env.GW.Address)).
		TradingFee(889).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)

	xrpBefore := env.AMMPoolXRP(ammAcc)
	usdBefore := ammHolding(t, env, ammAcc, env.USD)

	env.TestEnv.FundAmount(env.Bob, 1_092_878_933) // ~1092.878933 XRP
	env.Trust(env.Bob, env.GW, "USD", 1000000)
	env.PayIOUAmount(env.GW, env.Bob,
		state.NewIssuedAmountFromValue(3_988035892323031, -28, "USD", env.GW.Address))
	env.Close()

	// Bob creates offer: buy XRP(6300), sell USD(100000) — terrible quality
	// Bob can't fund 100000 USD, so offer is effectively unfunded
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(6300),
		amm.IOUAmount(env.GW, "USD", 100000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM should be unchanged
	xrpAfter := env.AMMPoolXRP(ammAcc)
	usdAfter := ammHolding(t, env, ammAcc, env.USD)
	if xrpBefore != xrpAfter {
		t.Errorf("AMM XRP changed: before %d, after %d", xrpBefore, xrpAfter)
	}
	require.Equal(t, usdBefore, usdAfter)
}

func TestAMMBookStep_LPTokenBalance(t *testing.T) {
	// Scenario 1: Last LP is issuer of one token
	t.Run("LastLP_IssuerOfOneToken", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)

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

		createTx := amm.AMMCreate(env.GW,
			amm.XRPAmount(2),
			amm.IOUAmount(env.GW, "USD", 1)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Alice deposits IOUAmount{1876123487565916, -15} LP tokens
		lptRef := amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)
		aliceLPT := tx.NewIssuedAmount(1_876123487565916, -15, lptRef.Currency, lptRef.Issuer)
		depAlice := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			LPTokenOut(aliceLPT).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depAlice))
		env.Close()

		// Carol deposits 1000000 LP tokens
		carolLPT := amm.LPTokenAmount(env, amm.XRP(), env.USD, 1_000_000)
		depCarol := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(carolLPT).
			LPToken().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(depCarol))
		env.Close()

		wdAlice := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		jtx.RequireTxSuccess(t, env.Submit(wdAlice))
		env.Close()

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
		amm.ExpectTER(t, depResult, ter.TerNO_AMM.String())
	})
}
