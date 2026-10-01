// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30100)

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

			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30100)
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

			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30100)
		})
	})
}

func TestAMMBookStep_AMMAndCLOB(t *testing.T) {
	// The v3.4.1 test runs the same book twice: once with the AMM's
	// generated offer and once with the equivalent passive CLOB offer. XRPAmount
	// values in this fixture are drops, while XRP(...) values are whole XRP.
	type result struct {
		lp2TST tx.Amount
		offer  *state.LedgerOffer
	}

	run := func(t *testing.T, useAMM, fixAMMv1_1 bool) result {
		t.Helper()
		env := amm.NewAMMTestEnv(t)
		env.DisableFeature("SingleAssetVault")
		env.DisableFeature("LendingProtocol")
		if !fixAMMv1_1 {
			env.DisableFeature("fixAMMv1_1")
			env.DisableFeature("fixAMMv1_3")
		}
		env.Close()

		lp1 := jtx.NewAccount("lp1")
		lp2 := jtx.NewAccount("lp2")
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30_000_000_000)))
		env.TestEnv.FundAmount(lp1, uint64(jtx.XRP(10_000)))
		env.TestEnv.FundAmount(lp2, uint64(jtx.XRP(10_000)))
		env.Close()
		env.Trust(lp1, env.GW, "TST", 1_000_000_000_000)
		env.Trust(lp2, env.GW, "TST", 1_000_000_000_000)
		env.Close()

		gwOfferTx := offerbuild.OfferCreate(env.GW,
			tx.NewXRPAmount(11_500_000_000*1_000_000),
			amm.IOUAmount(env.GW, "TST", 1_000_000_000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(gwOfferTx))
		env.Close()

		// The C++ fixture uses XRPAmount(287'500'000), which is a drop amount.
		lp1OfferTx := offerbuild.OfferCreate(lp1,
			amm.IOUAmount(env.GW, "TST", 25),
			tx.NewXRPAmount(287_500_000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(lp1OfferTx))
		env.Close()

		if useAMM {
			ammCreateTx := amm.AMMCreate(lp1,
				amm.IOUAmount(env.GW, "TST", 25),
				tx.NewXRPAmount(250*1_000_000)).TradingFee(0).Build()
			jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
		} else {
			payDrops := int64(18_095_132)
			getsMantissa := int64(168_737_976_189_735)
			if !fixAMMv1_1 {
				payDrops = 18_095_133
				getsMantissa = 168_737_984_885_388
			}
			clobOffer := offerbuild.OfferCreate(lp1,
				tx.NewXRPAmount(payDrops),
				tx.NewIssuedAmount(getsMantissa, -14, "TST", env.GW.Address)).
				Passive().Build()
			jtx.RequireTxSuccess(t, env.Submit(clobOffer))
		}
		env.Close()

		lp2OfferTx := offerbuild.OfferCreate(lp2,
			amm.IOUAmount(env.GW, "TST", 25),
			tx.NewXRPAmount(287_500_000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(lp2OfferTx))
		env.Close()

		lp2TST, ok := env.LookupIOUBalance(lp2, env.GW, "TST")
		require.True(t, ok, "LP2 TST trust line must remain present")
		lp2Offers := env.AccountOffers(lp2)
		require.Len(t, lp2Offers, 1, "LP2 offer should be partially filled")
		return result{lp2TST: *lp2TST, offer: lp2Offers[0]}
	}

	for _, fixAMMv1_1 := range []bool{false, true} {
		label := "PreFix"
		if fixAMMv1_1 {
			label = "PostFix"
		}
		t.Run(label, func(t *testing.T) {
			ammResult := run(t, true, fixAMMv1_1)
			clobResult := run(t, false, fixAMMv1_1)

			require.Equal(t, 0, ammResult.lp2TST.Compare(clobResult.lp2TST),
				"LP2 TST balance differs between AMM and CLOB")
			require.Equal(t, 0, ammResult.offer.TakerGets.Compare(clobResult.offer.TakerGets),
				"LP2 TakerGets differs between AMM and CLOB")
			require.Equal(t, 0, ammResult.offer.TakerPays.Compare(clobResult.offer.TakerPays),
				"LP2 TakerPays differs between AMM and CLOB")
		})
	}
}

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
			ammAccAddr := amm.AMMAccount(t, env, usdAsset, ethAsset)

			// Save AMM balances before payment
			ammUSD := ammHolding(t, env, ammAccAddr, env.USD)
			ammETH := ammHolding(t, env, ammAccAddr, tx.Asset{Currency: "ETH", Issuer: gw1.Address})

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
			requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 2100)

			// AMM should NOT be selected — balances unchanged
			require.Equal(t, ammUSD, ammHolding(t, env, ammAccAddr, env.USD))
			require.Equal(t, ammETH, ammHolding(t, env, ammAccAddr, tx.Asset{Currency: "ETH", Issuer: gw1.Address}))
		})
	}
}

func TestAMMBookStep_FalseDry(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Carol: fund without default ripple (Fund::Acct)
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	ammXRPPool := env.TestEnv.ReserveIncrement() * 2 // increment * 2
	// AMMCreate's special fee is one ReserveIncrement.
	bobFund := env.TestEnv.ReserveBase() + 5*env.TestEnv.ReserveIncrement() + 10 + ammXRPPool + env.TestEnv.ReserveIncrement()
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

	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.IOUAmount(env.GW, "EUR", 50),
		amm.XRPAmount(50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

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

func TestAMMBookStep_BookStep(t *testing.T) {
	// Sub-test 1: simple IOU/IOU offer (BTC → USD through AMM)
	t.Run("IOU_IOU", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
		env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
		env.Close()

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

		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "BTC", 100),
			amm.IOUAmount(env.GW, "USD", 150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, env,
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
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "BTC", 50)
		// Carol: USD(150+50=200)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 200)
		// AMM: BTC(100+50=150), USD(150-50=100)
		requireAMMAmount(t, ammHolding(t, env, ammAcc, env.BTC), "150")
		requireAMMAmount(t, ammHolding(t, env, ammAcc, env.USD), "100")
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

		createTx := amm.AMMCreate(env.Bob,
			amm.XRPAmount(100),
			amm.IOUAmount(env.GW, "USD", 150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, env, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		// alice pays carol 50 USD via XRP→USD AMM, sendmax XRP(50)
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(env.GW, "USD", 50)).
			SendMax(amm.XRPAmount(50)).
			PathsCurrency("USD", env.GW).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(payTx))

		// Carol: USD(150+50=200)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 200)
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

		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "USD", 100),
			amm.XRPAmount(150)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := amm.AMMAccount(t, env,
			tx.Asset{Currency: "USD", Issuer: env.GW.Address},
			amm.XRP())

		// alice pays carol XRP(50) via USD→XRP AMM, sendmax USD(50)
		payTx := payment.Pay(env.Alice, env.Carol, uint64(jtx.XRP(50))).
			SendMax(amm.IOUAmount(env.GW, "USD", 50)).
			PathsCurrency("XRP", nil).
			Build()
		jtx.RequireTxSuccess(t, env.Submit(payTx))

		// Alice: USD(100-50=50)
		requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 50)
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
