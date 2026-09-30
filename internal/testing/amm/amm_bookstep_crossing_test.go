// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

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
			amm.ExpectTER(t, result, "tecKILLED")
			env.Close()

			// AMM unchanged
			env.ExpectAMMBalances(t, ammAcc,
				uint64(jtx.XRP(10100)), env.GW, "USD", 10000)
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30000)
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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 29900)
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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 29900)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})

	// Passive: offer stays on books without crossing AMM.
	// Reference: rippled AMMExtended_test.cpp testFillModes
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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
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
	requireAMMAmount(t, ammHolding(t, env, env.Bob, env.USD), "1")

	// Bob XRP = 200000 - 3000 - baseFee = 196999999990
	bobXRP := env.TestEnv.Balance(env.Bob)
	expectedBobXRP := uint64(jtx.XRP(200000)) - uint64(jtx.XRP(3000)) - 10
	if bobXRP != expectedBobXRP {
		t.Errorf("Bob XRP: got %d, want %d", bobXRP, expectedBobXRP)
	}
}

func TestAMMBookStep_OfferCreateThenCross(t *testing.T) {
	// This is AMMExtended_test.cpp's transfer-rate fixture. The pool starts at
	// USD(150)/XRP(150100), then Bob's USD(0.1) for XRP(100) offer crosses it.
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(200_000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(200_000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(200_000)))
	env.Close()

	// rate(gw, 1.005): rippled's JTX conversion is uint32(1.005*1e9).
	env.SetTransferRate(env.GW, 1_004_999_999)
	env.Close()
	env.Trust(env.Alice, env.GW, "USD", 1_000)
	env.Trust(env.Bob, env.GW, "USD", 1_000)
	env.Close()
	env.PayIOU(env.GW, env.Bob, "USD", 1)
	env.PayIOU(env.GW, env.Alice, "USD", 200)
	env.Close()

	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 150),
		amm.XRPAmount(150_100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()
	ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)

	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(100),
		amm.IOUAmount(env.GW, "USD", 0.1)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	expectedUSD, err := state.NewIssuedAmountFromDecimalString("150.1", "USD", env.GW.Address)
	require.NoError(t, err)
	env.ExpectAMMBalancesExact(t, ammAcc, uint64(jtx.XRP(150_000)), expectedUSD)

	bobUSD, ok := env.LookupIOUBalance(env.Bob, env.GW, "USD")
	require.True(t, ok, "Bob USD trust line must remain present")
	expectedBobUSD, err := state.NewIssuedAmountFromDecimalString("0.8995000001", "USD", env.GW.Address)
	require.NoError(t, err)
	require.Equal(t, 0, bobUSD.Compare(expectedBobUSD),
		"Bob USD balance must include the gateway transfer rate")
}

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
		requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30101)

		// Carol XRP: 30000 - 100 XRP - 10 drops trust fee - 10 drops offer fee = 29899999980
		carolXRP := env.TestEnv.Balance(env.Carol)
		expectedCarol := uint64(jtx.XRP(30000)) - uint64(jtx.XRP(100)) - 20
		if carolXRP != expectedCarol {
			t.Errorf("Carol XRP: got %d, want %d", carolXRP, expectedCarol)
		}
	})
}

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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

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
	requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 200)

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
		amm.ExpectTER(t, result, "tecKILLED")
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

		ammAcc := amm.AMMAccount(t, env, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

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

		ammAcc := amm.AMMAccount(t, env, amm.XRP(), tx.Asset{Currency: "USD", Issuer: env.GW.Address})

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

func TestAMMBookStep_StepLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping step limit test in short mode (creates 2000 offers)")
	}

	for _, fixAMMv1_1 := range []bool{false, true} {
		label := "PreFix"
		if fixAMMv1_1 {
			label = "PostFix"
		}
		t.Run(label, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			dan := jtx.NewAccount("dan")
			ed := jtx.NewAccount("ed")
			if !fixAMMv1_1 {
				env.DisableFeature("fixAMMv1_1")
				env.DisableFeature("fixAMMv1_3")
			}
			env.Close()

			// Fund accounts with large XRP amounts.
			for _, account := range []*jtx.Account{env.GW, ed, env.Alice, env.Bob, env.Carol, dan} {
				env.TestEnv.FundAmount(account, uint64(jtx.XRP(100_000_000)))
			}
			env.Close()

			// Trust lines and initial USD balances for Ed, Bob, and Dan.
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

			// Bob's offers after the first are unfunded and are removed when the
			// payment engine reaches them.
			env.NOffers(2_000, env.Bob, tx.NewXRPAmount(1_000_000), amm.IOUAmount(env.GW, "USD", 1))
			env.NOffers(1, dan, tx.NewXRPAmount(1_000_000), amm.IOUAmount(env.GW, "USD", 1))

			ammCreateTx := amm.AMMCreate(ed,
				tx.NewXRPAmount(9_000_000),
				amm.IOUAmount(env.GW, "USD", 11)).TradingFee(0).Build()
			jtx.RequireTxSuccess(t, env.Submit(ammCreateTx))
			env.Close()

			// Alice takes Bob's first offer, grooms the unfunded offers until the
			// step limit, and then receives the AMM's remaining liquidity.
			aliceOfferTx := offerbuild.OfferCreate(env.Alice,
				amm.IOUAmount(env.GW, "USD", 1_000),
				tx.NewXRPAmount(1_000_000_000)).Build()
			jtx.RequireTxSuccess(t, env.Submit(aliceOfferTx))
			env.Close()

			aliceUSD, ok := env.LookupIOUBalance(env.Alice, env.GW, "USD")
			require.True(t, ok, "Alice USD trust line must remain present")
			wantAliceUSD := state.NewIssuedAmountFromValue(2_050_126_257_867_561, -15, "USD", env.GW.Address)
			if fixAMMv1_1 {
				wantAliceUSD = state.NewIssuedAmountFromValue(2_050_125_257_867_587, -15, "USD", env.GW.Address)
			}
			require.Equal(t, 0, aliceUSD.Compare(wantAliceUSD),
				"Alice USD balance must match the source step-limit vector")
			require.Equal(t, uint32(2), env.TestEnv.OwnerCount(env.Alice),
				"Alice owner count after the first step-limit offer")

			bobUSD, ok := env.LookupIOUBalance(env.Bob, env.GW, "USD")
			require.True(t, ok, "Bob USD trust line must remain present")
			require.Equal(t, 0, bobUSD.Compare(state.NewIssuedAmountFromValue(0, 0, "USD", env.GW.Address)),
				"Bob USD balance after the first step-limit offer")
			require.Equal(t, uint32(1_001), env.TestEnv.OwnerCount(env.Bob),
				"Bob owner count after the first step-limit offer")

			danUSD, ok := env.LookupIOUBalance(dan, env.GW, "USD")
			require.True(t, ok, "Dan USD trust line must remain present")
			require.Equal(t, 0, danUSD.Compare(state.NewIssuedAmountFromValue(1, 0, "USD", env.GW.Address)),
				"Dan USD balance after the first step-limit offer")
			require.Equal(t, uint32(2), env.TestEnv.OwnerCount(dan),
				"Dan owner count after the first step-limit offer")

			// Carol's offer reaches the next 1000 unfunded Bob offers. The source
			// checks that her offer remains while Bob's directory is fully groomed.
			carolOfferTx := offerbuild.OfferCreate(env.Carol,
				amm.IOUAmount(env.GW, "USD", 1_000),
				tx.NewXRPAmount(1_000_000_000)).Build()
			jtx.RequireTxSuccess(t, env.Submit(carolOfferTx))
			env.Close()
			_, ok = env.LookupIOUBalance(env.Carol, env.GW, "USD")
			require.False(t, ok, "Carol must not gain a USD trust line from the dry offer")
			require.Equal(t, uint32(1), env.TestEnv.OwnerCount(env.Carol),
				"Carol owner count after the second step-limit offer")
			require.Len(t, env.AccountOffers(env.Carol), 1,
				"Carol's unfilled offer must remain on the book")
			require.Equal(t, uint32(1), env.TestEnv.OwnerCount(env.Bob),
				"Bob owner count after Carol grooms the remaining offers")
			bobUSD, ok = env.LookupIOUBalance(env.Bob, env.GW, "USD")
			require.True(t, ok, "Bob USD trust line must remain present after Carol")
			require.Equal(t, 0, bobUSD.Compare(state.NewIssuedAmountFromValue(0, 0, "USD", env.GW.Address)),
				"Bob USD balance after Carol's step-limit offer")
			require.Equal(t, uint32(2), env.TestEnv.OwnerCount(dan),
				"Dan owner count after Carol's step-limit offer")
		})
	}
}
