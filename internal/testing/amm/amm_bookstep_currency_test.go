// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

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

	createTx := amm.AMMCreate(env.Bob, amm.IOUAmount(env.GW, "USD", 200), amm.XRPAmount(1500)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, env.USD, amm.XRP())

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
	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 0)

	// Alice XRP: initial 10000 + 500 - fee*2 (AMMCreate didn't charge her, so just 2 txns: trust + payment)
	// Actually: alice funded 10000 XRP. She paid 2 fees (trust USD, pay self).
	// 10000*1M + 500*1M - 20 = 10500*1M - 20
	aliceXRP := env.TestEnv.Balance(env.Alice)
	expectedAliceXRP := uint64(jtx.XRP(10000)) + uint64(jtx.XRP(500)) - 20 // 2 tx fees
	if aliceXRP != expectedAliceXRP {
		t.Errorf("Alice XRP: got %d, want %d (diff %d)", aliceXRP, expectedAliceXRP, int64(aliceXRP)-int64(expectedAliceXRP))
	}
}

func TestAMMBookStep_CurrencyConversionInParts(t *testing.T) {
	// Pool: XRP(10000)/USD(10000)
	// Alice sends USD(100) to get XRP(100) — but constant product means she can't get exactly 100 XRP for 100 USD.
	// Without partial payment: tecPATH_PARTIAL
	// With partial payment: succeeds, gets ~99.01 XRP
	amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		beforeXRP := env.Balance(env.Alice)
		beforeLP := env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance
		// Without partial payment — should fail
		payTx := payment.Pay(env.Alice, env.Alice, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_PARTIAL")
		require.Equal(t, beforeXRP-env.BaseFee(), env.Balance(env.Alice))
		require.Equal(t, uint64(10_000_000_000), env.AMMPoolXRP(ammAcc))
		requireAMMAmount(t, ammHolding(t, env, ammAcc, env.USD), "10000")
		requireAMMAmount(t, ammHolding(t, env, env.Alice, env.USD), "20000")

		// With partial payment — should succeed
		payTx2 := payment.Pay(env.Alice, env.Alice, uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 100)).
			PartialPayment().
			Build()
		result2 := env.Submit(payTx2)
		jtx.RequireTxSuccess(t, result2)
		env.Close()

		require.Equal(t, uint64(9_900_990_100), env.AMMPoolXRP(ammAcc))
		require.Equal(t, beforeXRP+99_009_900-2*env.BaseFee(), env.Balance(env.Alice))
		require.Equal(t, beforeLP, env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance)

		requireAMMAmount(t, ammHolding(t, env, ammAcc, env.USD), "10100")

		// Alice USD: initial 30000 - 10000(AMM) - 100(pay) = 19900
		requireAMMAmount(t, ammHolding(t, env, env.Alice, env.USD), "19900")
	})
}

func TestAMMBookStep_CrossCurrencyStartXRP(t *testing.T) {
	// Pool: XRP(10000)/USD(10100) — 100 XRP buys exactly 100 USD
	pool := [2]tx.Amount{
		amm.XRPAmount(10000),
		amm.IOUAmount(nil, "USD", 10100),
	}
	amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
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
		requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 100)
	})
}

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

func TestAMMBookStep_CrossCurrencyBridged(t *testing.T) {
	env := newCalcEnv(t)

	gw1 := jtx.NewAccount("gateway_1")
	gw2 := jtx.NewAccount("gateway_2")
	dan := jtx.NewAccount("dan")

	env.TestEnv.FundAmount(gw1, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(gw2, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(60000)))
	env.TestEnv.FundAmount(dan, uint64(jtx.XRP(60000)))
	env.Close()

	env.Trust(env.Alice, gw1, "USD", 1000)
	env.Close()
	env.Trust(env.Bob, gw2, "EUR", 1000)
	env.Close()
	env.Trust(env.Carol, gw1, "USD", 10000)
	env.Close()
	env.Trust(dan, gw2, "EUR", 1000)
	env.Close()

	env.PayIOU(gw1, env.Alice, "USD", 500)
	env.Close()
	env.PayIOU(gw1, env.Carol, "USD", 6000)
	env.PayIOU(gw2, dan, "EUR", 400)
	env.Close()

	createTx := amm.AMMCreate(env.Carol,
		amm.IOUAmount(gw1, "USD", 5000),
		amm.XRPAmount(50000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env,
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

	requireAMMAmount(t, ammHolding(t, env, ammAcc, tx.Asset{Currency: "USD", Issuer: gw1.Address}), "5030.181086519115")

	// Dan should have 1 remaining offer: TakerPays=XRP(200), TakerGets=EUR(20)
	offerbuild.RequireOfferCount(t, env.TestEnv, dan, 1)
	offerbuild.RequireIsOffer(t, env.TestEnv, dan,
		amm.XRPAmount(200),
		amm.IOUAmount(gw2, "EUR", 20))

	// Bob should have 30 EUR
	requireAMMAmount(t, ammHolding(t, env, env.Bob, tx.Asset{Currency: "EUR", Issuer: gw2.Address}), "30")
}

func TestAMMBookStep_GatewayCrossCurrency(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixAMMv1_1=%t", fixed), func(t *testing.T) {
			env := newCalcEnv(t)
			if !fixed {
				env.DisableFeature("fixAMMv1_1")
				env.DisableFeature("fixAMMv1_3")
			}
			env.Close()
			if env.Rules().Enabled(amendment.FeatureFixAMMv1_1) != fixed {
				t.Fatal("incorrect rounding amendment profile")
			}
			startingXRP := uint64(100_100_000) + env.TestEnv.ReserveBase() + env.TestEnv.ReserveIncrement() + 2*env.BaseFee()
			env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
			env.TestEnv.FundAmount(env.Alice, startingXRP)
			env.TestEnv.FundAmount(env.Bob, startingXRP)
			env.Close()
			for _, account := range []*jtx.Account{env.Alice, env.Bob} {
				for _, currency := range []string{"XTS", "XXX"} {
					env.Trust(account, env.GW, currency, 1000)
					env.PayIOU(env.GW, account, currency, 100)
				}
			}
			env.Close()
			xts := tx.Asset{Currency: "XTS", Issuer: env.GW.Address}
			xxx := tx.Asset{Currency: "XXX", Issuer: env.GW.Address}
			jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice,
				amm.IOUAmount(env.GW, "XTS", 100), amm.IOUAmount(env.GW, "XXX", 100)).Build()))
			env.Close()
			pool := amm.AMMAccount(t, env, xts, xxx)
			supply := env.ReadAMMData(xts, xxx).LPTokenBalance
			bobXRP := env.Balance(env.Bob)
			result := env.Submit(payment.PayIssued(env.Bob, env.Bob, amm.IOUAmount(env.GW, "XXX", 1)).
				SendMax(amm.IOUAmount(env.GW, "XTS", 1.5)).PathsCurrency("XXX", env.GW).
				NoDirectRipple().PartialPayment().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			poolXTS, bobXTS := "101.010101010101", "98.989898989899"
			if fixed {
				poolXTS, bobXTS = "101.0101010101011", "98.9898989898989"
			}
			requireAMMAmount(t, ammHolding(t, env, pool, xts), poolXTS)
			requireAMMAmount(t, ammHolding(t, env, pool, xxx), "99")
			requireAMMAmount(t, ammHolding(t, env, env.Bob, xts), bobXTS)
			requireAMMAmount(t, ammHolding(t, env, env.Bob, xxx), "101")
			require.Equal(t, supply, env.ReadAMMData(xts, xxx).LPTokenBalance)
			jtx.RequireBalance(t, env.TestEnv, env.Bob, bobXRP-result.Fee)
		})
	}
}

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

		createTx1 := amm.AMMCreate(env.Alice,
			amm.XRPAmount(10000),
			amm.IOUAmount(env.GW, "USD", 10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx1))
		env.Close()

		ammAlice := amm.AMMAccount(t, env, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

		createTx2 := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "EUR", 10000),
			amm.XRPAmount(10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx2))
		env.Close()

		ammBob := amm.AMMAccount(t, env,
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
		requireAMMAmount(t, ammHolding(t, env, ammBob, env.EUR), "10100")

		// Carol: USD(15100), EUR(14900)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 15100)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "EUR", 14900)
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

		createTx := amm.AMMCreate(env.Alice,
			amm.XRPAmount(10000),
			amm.IOUAmount(env.GW, "USD", 10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAlice := amm.AMMAccount(t, env, amm.XRP(),
			tx.Asset{Currency: "USD", Issuer: env.GW.Address})

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

		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 15100)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "EUR", 14900)
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

		aliceOffer := offerbuild.OfferCreate(env.Alice,
			amm.XRPAmount(100),
			amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(aliceOffer))
		env.Close()

		createTx := amm.AMMCreate(env.Bob,
			amm.IOUAmount(env.GW, "EUR", 10000),
			amm.XRPAmount(10100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammBob := amm.AMMAccount(t, env,
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
		requireAMMAmount(t, ammHolding(t, env, ammBob, env.EUR), "10100")

		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 15100)
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "EUR", 14900)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		offerbuild.RequireOfferCount(t, env.TestEnv, env.Alice, 0)
	})
}
