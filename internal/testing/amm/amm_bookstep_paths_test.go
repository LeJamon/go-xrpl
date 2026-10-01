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

	payDanBob := payment.PayIssued(dan, bob, danBUX(100)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payDanBob))
	env.Close()

	payAnnBob := payment.PayIssued(ann, bob, annBUX(72)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payAnnBob))
	env.Close()

	createTx := amm.AMMCreate(bob, annBUX(30), danBUX(30)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

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

func TestAMMBookStep_DirectToDirectPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixAMMv1_1 bool
		fixAMMv1_3 bool
		aPool      int64
		bPool      int64
		offer      int64
	}{
		{
			name:       "fixAMMv1_1",
			fixAMMv1_1: true,
			fixAMMv1_3: true,
			aPool:      3093541659651604,
			bPool:      3200215509984419,
			offer:      200215509984419,
		},
		{
			name:       "legacy",
			fixAMMv1_1: false,
			fixAMMv1_3: false,
			aPool:      3093541659651605,
			bPool:      3200215509984417,
			offer:      200215509984417,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := amm.NewAMMTestEnv(t)
			env.DisableFeature("SingleAssetVault")
			env.DisableFeature("LendingProtocol")
			if !tc.fixAMMv1_1 {
				env.DisableFeature("fixAMMv1_1")
			}
			if !tc.fixAMMv1_3 {
				env.DisableFeature("fixAMMv1_3")
			}
			env.Close()

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

			annBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", ann.Address) }
			bobBUX := func(amt float64) tx.Amount { return tx.NewIssuedAmountFromFloat64(amt, "BUX", bob.Address) }

			env.Trust(ann, bob, "BUX", 40)
			env.Trust(cam, ann, "BUX", 40)
			env.Trust(bob, ann, "BUX", 30)
			env.Trust(cam, bob, "BUX", 40)
			env.Trust(carol, bob, "BUX", 400)
			env.Trust(carol, ann, "BUX", 400)
			env.Close()

			payTx1 := payment.PayIssued(ann, cam, annBUX(35)).Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx1))
			payTx2 := payment.PayIssued(bob, cam, bobBUX(35)).Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx2))
			payTx3 := payment.PayIssued(bob, carol, bobBUX(400)).Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx3))
			payTx4 := payment.PayIssued(ann, carol, annBUX(400)).Build()
			jtx.RequireTxSuccess(t, env.Submit(payTx4))
			env.Close()

			createTx := amm.AMMCreate(carol, annBUX(300), bobBUX(330)).Build()
			jtx.RequireTxSuccess(t, env.Submit(createTx))
			env.Close()
			assetA := tx.Asset{Currency: "BUX", Issuer: ann.Address}
			assetB := tx.Asset{Currency: "BUX", Issuer: bob.Address}
			lpBefore := env.ReadAMMData(assetA, assetB).LPTokenBalance

			// cam creates passive offer: buy A_BUX(29), sell B_BUX(30)
			offerTx1 := offerbuild.OfferCreate(cam, annBUX(29), bobBUX(30)).Passive().Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx1))
			env.Close()

			// cam: A_BUX(35), B_BUX(35), 1 offer
			requireAMMIOUBalance(t, env.TestEnv, cam, ann, "BUX", 35)
			requireAMMIOUBalance(t, env.TestEnv, cam, bob, "BUX", 35)
			offerbuild.RequireOfferCount(t, env.TestEnv, cam, 1)

			// cam's offer: buy B_BUX(30), sell A_BUX(30) — this used to cause assert
			offerTx2 := offerbuild.OfferCreate(cam, bobBUX(30), annBUX(30)).Build()
			jtx.RequireTxSuccess(t, env.Submit(offerTx2))
			env.Close()

			// Verify AMM and the remaining offer against both rippled feature profiles.
			ammAcc := amm.AMMAccount(t, env, assetA, assetB)
			expectAmount := func(mantissa int64) tx.Amount {
				return state.NewIssuedAmountFromValue(mantissa, -13, "BUX", ann.Address)
			}
			require.Equal(t, expectAmount(tc.aPool), env.AMMPoolIOUPrecise(ammAcc, ann, "BUX"))
			require.Equal(t, state.NewIssuedAmountFromValue(tc.bPool, -13, "BUX", bob.Address), env.AMMPoolIOUPrecise(ammAcc, bob, "BUX"))
			require.Equal(t, lpBefore, env.ReadAMMData(assetA, assetB).LPTokenBalance)
			offerbuild.RequireOfferCount(t, env.TestEnv, cam, 1)
			offers := env.AccountOffers(cam)
			require.Equal(t, state.NewIssuedAmountFromValue(tc.offer, -13, "BUX", bob.Address), offers[0].TakerPays)
			require.Equal(t, state.NewIssuedAmountFromValue(tc.offer, -13, "BUX", ann.Address), offers[0].TakerGets)
		})
	}
}

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

	createTx1 := amm.AMMCreate(env.Bob, amm.XRPAmount(1000), amm.IOUAmount(env.GW, "USD", 1000)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx1))
	env.Close()

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
