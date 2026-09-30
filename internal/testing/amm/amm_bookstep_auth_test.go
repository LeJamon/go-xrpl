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
	"github.com/stretchr/testify/require"
)

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

	ammAcc := amm.AMMAccount(t, env,
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
	requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 0)
}

func TestAMMBookStep_RequireAuthRejectsUnauthorizedSyntheticOffer(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(400000)))
	env.Close()

	env.TestEnv.EnableRequireAuth(env.GW)
	env.Close()

	env.TestEnv.AuthorizeTrustLine(env.GW, env.Alice, "USD")
	env.Trust(env.Alice, env.GW, "USD", 2000)
	env.TestEnv.AuthorizeTrustLine(env.GW, env.Bob, "USD")
	env.Trust(env.Bob, env.GW, "USD", 100)
	env.PayIOU(env.GW, env.Alice, "USD", 1000)
	env.PayIOU(env.GW, env.Bob, "USD", 50)
	env.Close()

	createTx := amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 1000),
		amm.XRPAmount(1050)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env,
		tx.Asset{Currency: "USD", Issuer: env.GW.Address},
		amm.XRP())

	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(50),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1050)), env.GW, "USD", 1000)
	requireAMMIOUBalance(t, env.TestEnv, env.Bob, env.GW, "USD", 50)
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Bob, 1)
	offerbuild.RequireIsOffer(t, env.TestEnv, env.Bob,
		amm.XRPAmount(50),
		amm.IOUAmount(env.GW, "USD", 50))
}

func TestAMMBookStep_Payment(t *testing.T) {
	// rippled setup:
	//   fund(env, gw, {alice, becky}, XRP(5000))
	//   trust(alice, USD(1000)); trust(becky, USD(1000))
	//   pay(gw, alice, USD(500))
	//   AMM ammAlice(env, alice, XRP(100), USD(140))
	//   pay(becky, becky, USD(10)), path(~USD), sendmax(XRP(10))
	// Expected: AMM XRP(107692308 drops), USD(130)
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)
	lpBefore := env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance

	// becky pays herself USD(10) via AMM, path(~USD), sendmax(XRP(10))
	payTx := payment.PayIssued(becky, becky, amm.IOUAmount(env.GW, "USD", 10)).
		SendMax(amm.XRPAmount(10)).
		PathsCurrency("USD", env.GW).
		Build()
	result := env.Submit(payTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// AMM: XRP should be exactly 107692308 drops, USD should be exactly 130.
	ammXRP := env.AMMPoolXRP(ammAcc)
	require.Equal(t, uint64(107_692_308), ammXRP)
	ammUSD := env.AMMPoolIOUPrecise(ammAcc, env.GW, "USD")
	require.Equal(t, state.NewIssuedAmountFromValue(130, 0, "USD", env.GW.Address), ammUSD)
	require.Equal(t, lpBefore, env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance)

	// DepositAuth must not prevent a self-payment through the AMM.
	env.TestEnv.EnableDepositAuth(becky)
	env.Close()
	secondPayment := payment.PayIssued(becky, becky, amm.IOUAmount(env.GW, "USD", 10)).
		SendMax(amm.XRPAmount(10)).
		PathsCurrency("USD", env.GW).
		Build()
	jtx.RequireTxSuccess(t, env.Submit(secondPayment))
	env.Close()
}

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

func TestAMMBookStep_RippleState(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	g1 := jtx.NewAccount("G1")

	env.TestEnv.FundAmount(g1, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(1000)))
	env.Close()

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
		amm.IOUAmount(g1, "USD", 105)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
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
		amm.IOUAmount(g1, "USD", 5),
		amm.XRPAmount(25)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// AMM: XRP(525), USD(100)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(525)), g1, "USD", 100)

	// After freeze: bob cannot sell from that line
	offerTx2 := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(1),
		amm.IOUAmount(g1, "USD", 5)).Build()
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

func TestAMMBookStep_OffersWhenFrozen(t *testing.T) {
	env := newCalcEnv(t)
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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
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

	ammXRP := env.AMMPoolXRP(ammAcc)
	ammUSD := ammHolding(t, env, ammAcc, tx.Asset{Currency: "USD", Issuer: g1.Address})
	require.Equal(t, uint64(jtx.XRP(1000)), ammXRP)
	requireAMMAmount(t, ammUSD, "1001")

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
	ammUSD2 := ammHolding(t, env, ammAcc, tx.Asset{Currency: "USD", Issuer: g1.Address})
	if ammXRP2 != ammXRP {
		t.Errorf("AMM XRP changed after freeze: before %d, after %d", ammXRP, ammXRP2)
	}
	require.Equal(t, ammUSD, ammUSD2)
	env.TestEnv.FreezeTrustLine(g1, a4, "USD")
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(a2, g1USD(999), amm.XRPAmount(999)).Build()))
	env.Close()
	offerbuild.RequireOfferCount(t, env.TestEnv, a4, 0)
	require.Equal(t, ammXRP, env.AMMPoolXRP(ammAcc))
	require.Equal(t, ammUSD, ammHolding(t, env, ammAcc, tx.Asset{Currency: "USD", Issuer: g1.Address}))
}
