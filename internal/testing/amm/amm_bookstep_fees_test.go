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
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "EUR", 28990)
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 29000)
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30000)

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
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "EUR", 29000)
			requireAMMIOUBalance(t, env.TestEnv, env.Alice, env.GW, "USD", 29000)
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 29990)

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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30000)
			// rippled: STAmount{EUR, 989'8989898989899, -13}.
			bobEUR, ok := env.TestEnv.LookupIOUBalance(env.Bob, env.GW, "EUR")
			require.True(t, ok, "Bob EUR trust line must remain present")
			require.Equal(t, 0, bobEUR.Compare(tx.NewIssuedAmount(
				9_898_989_898_989_899, -13, "EUR", env.GW.Address)),
				"Bob EUR balance must match the exact 1%-fee result")
			bobUSD, ok := env.TestEnv.LookupIOUBalance(env.Bob, env.GW, "USD")
			require.True(t, ok, "Bob USD trust line must remain present")
			require.Equal(t, 0, bobUSD.Compare(tx.NewIssuedAmount(1000, 0, "USD", env.GW.Address)),
				"Bob USD balance must remain unchanged while he sends EUR")
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

			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 29990)
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "EUR", 30010)

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
			carolUSD, ok := env.TestEnv.LookupIOUBalance(env.Carol, env.GW, "USD")
			require.True(t, ok, "Carol USD trust line must remain present")
			carolEUR, ok := env.TestEnv.LookupIOUBalance(env.Carol, env.GW, "EUR")
			require.True(t, ok, "Carol EUR trust line must remain present")
			// The first two offers cancel out. The third leaves the exact 0.5%
			// trading-fee result in both balances.
			require.Equal(t, 0, carolUSD.Compare(tx.NewIssuedAmount(
				2_999_502_512_562_814, -11, "USD", env.GW.Address)),
				"Carol USD balance must include the exact AMM fee")
			require.Equal(t, 0, carolEUR.Compare(tx.NewIssuedAmount(
				3_000_497_487_437_186, -11, "EUR", env.GW.Address)),
				"Carol EUR balance must include the exact AMM fee")
		})
	})
}

func TestAMMBookStep_OfferFeesConsumeFunds(t *testing.T) {
	env := newCalcEnv(t)

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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
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
	requireAMMAmount(t, ammHolding(t, env, ammAcc, tx.Asset{Currency: "USD", Issuer: gw1.Address}), "1090.909090909091")

	// Alice got ~109.09 USD
	requireAMMAmount(t, ammHolding(t, env, env.Alice, tx.Asset{Currency: "USD", Issuer: gw1.Address}), "109.090909090909")

	// Alice XRP should be reserve(3) = reserveBase + 3*increment (after offer consumed)
	aliceXRP := env.TestEnv.Balance(env.Alice)
	if aliceXRP != reserve3 {
		t.Errorf("Alice XRP: got %d, want %d (reserve(3))", aliceXRP, reserve3)
	}
}

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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 30100)
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
			requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 29875)
			offerbuild.RequireOfferCount(t, env.TestEnv, env.Carol, 0)
		})
	})
}

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

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
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
	requireAMMAmount(t, ammHolding(t, env, env.Alice, tx.Asset{Currency: "USD", Issuer: env.Bob.Address}), "100")
}

func TestAMMBookStep_TransferRateNoOwnerFee(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixAMMv1_1 bool
		fixAMMv1_3 bool
		poolUSD    int64
		carolUSD   int64
	}{
		{
			name:       "fixAMMv1_1",
			fixAMMv1_1: true,
			fixAMMv1_3: true,
			poolUSD:    8928571428571429,
			carolUSD:   1085714285714286,
		},
		{
			name:       "legacy",
			fixAMMv1_1: false,
			fixAMMv1_3: false,
			poolUSD:    8928571428571428,
			carolUSD:   1085714285714286,
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
			ammAcc := amm.AMMAccount(t, env, env.GBP, env.USD)
			lpBefore := env.ReadAMMData(env.GBP, env.USD).LPTokenBalance

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
			requireAMMAmount(t, ammHolding(t, env, env.Alice, env.GBP), "850")

			require.Equal(t,
				state.NewIssuedAmountFromValue(1120, 0, "GBP", env.GW.Address),
				env.AMMPoolIOUPrecise(ammAcc, env.GW, "GBP"))
			require.Equal(t,
				state.NewIssuedAmountFromValue(tc.poolUSD, -13, "USD", env.GW.Address),
				env.AMMPoolIOUPrecise(ammAcc, env.GW, "USD"))
			require.Equal(t,
				state.NewIssuedAmountFromValue(tc.carolUSD, -12, "USD", env.GW.Address),
				ammHolding(t, env, env.Carol, env.USD))
			require.Equal(t, lpBefore, env.ReadAMMData(env.GBP, env.USD).LPTokenBalance)
		})
	}
}
