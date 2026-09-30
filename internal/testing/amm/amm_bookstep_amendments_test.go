// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"fmt"
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

func TestAMMBookStep_FixDefaultInnerObj(t *testing.T) {
	cases := []struct {
		name        string
		tradingFee  uint16
		closeLedger bool
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

func TestAMMBookStep_FixOverflowOffer(t *testing.T) {
	type inputSet struct {
		name       string
		poolUsdBIT float64
		poolUsdGH  float64
		sendMax    float64 // usdBIT
		sendUsdGH  float64 // desired amount
		// Expected AMM balances after payment with fixAMMv1_1 enabled.
		goodUsdGHMant  int64
		goodUsdGHExp   int
		goodUsdBITMant int64
		goodUsdBITExp  int
		// Expected AMM balances with the legacy fixAMMv1_1 arithmetic.
		legacyUsdGHMant  int64
		legacyUsdGHExp   int
		legacyUsdBITMant int64
		legacyUsdBITExp  int
		lptMant          int64
		lptAltMant       int64
		lptExp           int
		// CLOB offer parameters
		offer1BtcGH float64
		offer2BtcGH float64
		offer2UsdGH float64
		// Transfer rates (0 = none)
		rateBIT float64
		rateGH  float64
	}

	// Values are selected by fixAMMv1_1 below and normalized to the Go Amount
	// mantissa range.
	tests := []inputSet{
		{
			name: "Test Fix Overflow Offer", poolUsdBIT: 3, poolUsdGH: 273,
			sendMax: 50, sendUsdGH: 272.455089820359,
			// rippled goodr: {967543114222965, -13} → normalized: {9675431142229650, -14}
			goodUsdGHMant: 9675431142229650, goodUsdGHExp: -14,
			goodUsdBITMant: 8464739069098152, goodUsdBITExp: -15,
			legacyUsdGHMant: 9675431142203820, legacyUsdGHExp: -14,
			legacyUsdBITMant: 8464739069120721, legacyUsdBITExp: -15,
			lptMant: 2861817604250837, lptAltMant: 2861817604250836, lptExp: -14,
			offer1BtcGH: 0.1, offer2BtcGH: 0.1, offer2UsdGH: 1,
			rateBIT: 1.15, rateGH: 1.2,
		},
		{
			name: "Overflow test {1, 100, 1.00}", poolUsdBIT: 1, poolUsdGH: 100,
			sendMax: 1.00, sendUsdGH: 100,
			goodUsdGHMant: 5294379354424135, goodUsdGHExp: -14,
			legacyUsdGHMant: 5294379354424079, legacyUsdGHExp: -14,
			// rippled: {2, 0} → normalized: {2000000000000000, -15}
			goodUsdBITMant: 2000000000000000, goodUsdBITExp: -15,
			legacyUsdBITMant: 2000000000000000, legacyUsdBITExp: -15,
			lptMant: 1000000000000000, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
		{
			name: "Overflow test {1, 100, 0.111}", poolUsdBIT: 1, poolUsdGH: 100,
			sendMax: 0.111, sendUsdGH: 100,
			goodUsdGHMant: 9004347888284201, goodUsdGHExp: -14,
			legacyUsdGHMant: 9004347888284115, legacyUsdGHExp: -14,
			goodUsdBITMant: 1111000000000000, goodUsdBITExp: -15,
			legacyUsdBITMant: 1111000000000000, legacyUsdBITExp: -15,
			lptMant: 1000000000000000, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
		},
		{
			name: "Overflow test {1, 100, 4.6432}", poolUsdBIT: 1, poolUsdGH: 100,
			sendMax: 4.6432, sendUsdGH: 100,
			goodUsdGHMant: 3544113971506987, goodUsdGHExp: -14,
			legacyUsdGHMant: 3544113971506987, legacyUsdGHExp: -14,
			goodUsdBITMant: 2821579689703954, goodUsdBITExp: -15,
			legacyUsdBITMant: 2821579689703915, legacyUsdBITExp: -15,
			lptMant: 1000000000000000, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
		},
		{
			name: "Overflow test {1, 100, 10}", poolUsdBIT: 1, poolUsdGH: 100,
			sendMax: 10, sendUsdGH: 100,
			goodUsdGHMant: 3544113971506987, goodUsdGHExp: -14,
			legacyUsdGHMant: 3544113971506987, legacyUsdGHExp: -14,
			goodUsdBITMant: 2821579689703954, goodUsdBITExp: -15,
			legacyUsdBITMant: 2821579689703915, legacyUsdBITExp: -15,
			lptMant: 1000000000000000, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
		},
		{
			name: "Overflow test {50, 100, 50.00}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 50.00, sendUsdGH: 100,
			goodUsdGHMant: 5294379354424092, goodUsdGHExp: -14,
			legacyUsdGHMant: 5294379354424081, legacyUsdGHExp: -14,
			// rippled: {100, 0} → normalized: {1000000000000000, -13}
			goodUsdBITMant: 1000000000000000, goodUsdBITExp: -13,
			legacyUsdBITMant: 1000000000000000, legacyUsdBITExp: -13,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
		{
			name: "Overflow test {50, 100, 5.55}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 5.55, sendUsdGH: 100,
			// rippled goodr: {900434788828413, -13} → normalized: {9004347888284130, -14}
			goodUsdGHMant: 9004347888284130, goodUsdGHExp: -14,
			legacyUsdGHMant: 9004347888284113, legacyUsdGHExp: -14,
			// rippled: {5555, -2} → normalized: {5555000000000000, -14}
			goodUsdBITMant: 5555000000000000, goodUsdBITExp: -14,
			legacyUsdBITMant: 5555000000000000, legacyUsdBITExp: -14,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
			rateBIT: 0, rateGH: 0,
		},
		{
			name: "Overflow test {50, 100, 232.16}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 232.16, sendUsdGH: 100,
			goodUsdGHMant: 3544113971506987, goodUsdGHExp: -14,
			legacyUsdGHMant: 3544113971506987, legacyUsdGHExp: -14,
			goodUsdBITMant: 1410789844851962, goodUsdBITExp: -13,
			legacyUsdBITMant: 1410789844851958, legacyUsdBITExp: -13,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
		},
		{
			name: "Overflow test {50, 100, 500}", poolUsdBIT: 50, poolUsdGH: 100,
			sendMax: 500, sendUsdGH: 100,
			goodUsdGHMant: 3544113971506987, goodUsdGHExp: -14,
			legacyUsdGHMant: 3544113971506987, legacyUsdGHExp: -14,
			goodUsdBITMant: 1410789844851962, goodUsdBITExp: -13,
			legacyUsdBITMant: 1410789844851958, legacyUsdBITExp: -13,
			lptMant: 7071067811865475, lptExp: -14,
			offer1BtcGH: 1e-5, offer2BtcGH: 1, offer2UsdGH: 1e-5,
		},
	}

	fixes := []struct{ rounding, lpTokens bool }{{false, false}, {false, true}, {true, false}, {true, true}}
	for _, tc := range tests {
		for _, fix := range fixes {
			t.Run(fmt.Sprintf("%s/fixAMMv1_1=%t/fixAMMv1_3=%t", tc.name, fix.rounding, fix.lpTokens), func(t *testing.T) {
				env := amm.NewAMMTestEnv(t)
				env.DisableFeature("fixAMMOverflowOffer")
				env.DisableFeature("SingleAssetVault")
				env.DisableFeature("LendingProtocol")
				if !fix.rounding {
					env.DisableFeature("fixAMMv1_1")
				}
				if !fix.lpTokens {
					env.DisableFeature("fixAMMv1_3")
				}
				env.Close()
				if env.FeatureEnabled("fixAMMOverflowOffer") {
					t.Fatal("retired overflow amendment must be absent from these rules")
				}
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
				ammAcc := amm.AMMAccount(t, env, usdGHAsset, usdBITAsset)
				ammDataBefore := env.ReadAMMData(usdGHAsset, usdBITAsset)
				if ammDataBefore == nil {
					t.Fatal("AMM data is nil before payment")
				}

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
				balanceBefore, sequenceBefore := env.Balance(trader), env.Seq(trader)
				jtx.RequireTxSuccess(t, env.Submit(payTx))
				env.Close()
				if got := balanceBefore - env.Balance(trader); got != env.BaseFee() {
					t.Errorf("trader fee = %d drops, want %d", got, env.BaseFee())
				}
				if got := env.Seq(trader); got != sequenceBefore+1 {
					t.Errorf("trader sequence = %d, want %d", got, sequenceBefore+1)
				}

				// Check AMM balances (precise mantissa/exponent comparison)
				ammUsdGH := env.TestEnv.IOUBalance(ammAcc, gatehub, "USD")
				ammUsdBIT := env.TestEnv.IOUBalance(ammAcc, bitstamp, "USD")

				if ammUsdGH == nil {
					t.Fatal("AMM usdGH balance is nil")
				}
				if ammUsdBIT == nil {
					t.Fatal("AMM usdBIT balance is nil")
				}

				wantGHMant, wantGHExp := tc.goodUsdGHMant, tc.goodUsdGHExp
				wantBITMant, wantBITExp := tc.goodUsdBITMant, tc.goodUsdBITExp
				if !fix.rounding {
					wantGHMant, wantGHExp = tc.legacyUsdGHMant, tc.legacyUsdGHExp
					wantBITMant, wantBITExp = tc.legacyUsdBITMant, tc.legacyUsdBITExp
				}

				if ammUsdGH.Mantissa() != wantGHMant || ammUsdGH.Exponent() != wantGHExp {
					t.Errorf("AMM usdGH balance mismatch: got {%d, %d}, expected {%d, %d} (got %g, diff=%d)",
						ammUsdGH.Mantissa(), ammUsdGH.Exponent(), wantGHMant, wantGHExp, ammUsdGH.Float64(), ammUsdGH.Mantissa()-wantGHMant)
				}
				if ammUsdBIT.Mantissa() != wantBITMant || ammUsdBIT.Exponent() != wantBITExp {
					t.Errorf("AMM usdBIT balance mismatch: got {%d, %d}, expected {%d, %d} (got %g, diff=%d)",
						ammUsdBIT.Mantissa(), ammUsdBIT.Exponent(), wantBITMant, wantBITExp, ammUsdBIT.Float64(), ammUsdBIT.Mantissa()-wantBITMant)
				}

				ammDataAfter := env.ReadAMMData(usdGHAsset, usdBITAsset)
				if ammDataAfter == nil {
					t.Fatal("AMM data is nil after payment")
				}
				if got, want := ammDataAfter.LPTokenBalance, ammDataBefore.LPTokenBalance; got.Mantissa() != want.Mantissa() || got.Exponent() != want.Exponent() {
					t.Errorf("AMM LP token balance changed: got {%d, %d}, want {%d, %d}",
						got.Mantissa(), got.Exponent(), want.Mantissa(), want.Exponent())
				}
				wantLPTMant := tc.lptMant
				if fix.lpTokens && tc.lptAltMant != 0 {
					wantLPTMant = tc.lptAltMant
				}
				if got := ammDataAfter.LPTokenBalance; got.Mantissa() != wantLPTMant || got.Exponent() != tc.lptExp {
					t.Errorf("AMM LP token balance = {%d, %d}, want {%d, %d}",
						got.Mantissa(), got.Exponent(), wantLPTMant, tc.lptExp)
				}
				numbers := state.NewNumberContext(state.MantissaScaleSmall, true)
				product := numbers.FromAmount(*ammUsdGH, state.RoundToNearest).
					Mul(numbers.FromAmount(*ammUsdBIT, state.RoundToNearest))
				// The pool-product bound allows 1e-14 of representation error.
				bound := product.Root2().Add(numbers.Number(1, -14, state.RoundToNearest))
				if bound.Cmp(numbers.Number(tc.lptMant, tc.lptExp, state.RoundToNearest)) < 0 {
					t.Fatalf("pool product no longer backs the LP token balance: %s", product.String())
				}
			})
		}
	}
}

func TestAMMBookStep_FixAMMOfferBlockedByLOB(t *testing.T) {
	for _, receiveXRP := range []bool{false, true} {
		for _, blockingOffer := range []bool{false, true} {
			for _, fixAMMv1_1 := range []bool{false, true} {
				for _, mptTokensV2 := range []bool{false, true} {
					name := fmt.Sprintf("receiveXRP=%t/blocker=%t/fixAMMv1_1=%t/MPTokensV2=%t", receiveXRP, blockingOffer, fixAMMv1_1, mptTokensV2)
					t.Run(name, func(t *testing.T) {
						env := amm.NewAMMTestEnv(t)
						env.DisableFeature("fixAMMOverflowOffer")
						if fixAMMv1_1 {
							env.EnableFeature("fixAMMv1_1")
						} else {
							env.DisableFeature("fixAMMv1_1")
						}
						if mptTokensV2 {
							env.EnableFeature("MPTokensV2")
						} else {
							env.DisableFeature("MPTokensV2")
						}
						env.Close()
						require.False(t, env.FeatureEnabled("fixAMMOverflowOffer"))
						require.Equal(t, fixAMMv1_1, env.FeatureEnabled("fixAMMv1_1"))
						require.Equal(t, mptTokensV2, env.FeatureEnabled("MPTokensV2"))

						usd := func(mantissa int64, exponent int) tx.Amount {
							return state.NewIssuedAmountFromValue(mantissa, exponent, "USD", env.GW.Address)
						}
						checkAmount := func(want, got tx.Amount) {
							t.Helper()
							require.Equal(t, want.IsNative(), got.IsNative())
							require.Equal(t, want.Value(), got.Value())
							if !want.IsNative() {
								require.Equal(t, want.Currency, got.Currency)
								require.Equal(t, want.Issuer, got.Issuer)
							}
						}
						checkOffer := func(account *jtx.Account, pays, gets tx.Amount) {
							t.Helper()
							offers := env.AccountOffers(account)
							require.Len(t, offers, 1)
							checkAmount(pays, offers[0].TakerPays)
							checkAmount(gets, offers[0].TakerGets)
						}

						fundXRP, fundUSD := int64(1_000_000), float64(1_000_000)
						creator, blocker := env.GW, env.Alice
						poolXRP, poolUSD := uint64(200_000_000_000), usd(100_000, 0)
						blockerPays, blockerGets := tx.NewXRPAmount(1_000_000), usd(1, -2)
						carolPays, carolGets := usd(49, -2), tx.NewXRPAmount(1_000_000)
						if receiveXRP {
							fundXRP, fundUSD = 10_000, 1_000
							creator, blocker = env.Alice, env.Bob
							poolXRP, poolUSD = 1_000_000_000, usd(500, 0)
							blockerPays, blockerGets = usd(1, 0), tx.NewXRPAmount(500)
							carolPays, carolGets = tx.NewXRPAmount(100_000_000), usd(55, 0)
						}
						for _, account := range []*jtx.Account{env.GW, env.Alice, env.Carol, env.Bob} {
							env.TestEnv.FundAmount(account, uint64(fundXRP*1_000_000))
						}
						env.Close()
						for _, account := range []*jtx.Account{env.Alice, env.Carol, env.Bob} {
							env.Trust(account, env.GW, "USD", fundUSD)
						}
						env.Close()
						for _, account := range []*jtx.Account{env.Alice, env.Carol, env.Bob} {
							env.PayIOU(env.GW, account, "USD", fundUSD)
						}
						env.Close()
						if blockingOffer {
							jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(blocker, blockerPays, blockerGets).Build()))
							env.Close()
						}
						jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(creator, tx.NewXRPAmount(int64(poolXRP)), poolUSD).TradingFee(0).Build()))
						env.Close()
						ammAcc := amm.AMMAccount(t, env, amm.XRP(), env.USD)
						lpBefore := env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance
						carolXRPBefore, carolSeqBefore := env.Balance(env.Carol), env.Seq(env.Carol)
						jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(env.Carol, carolPays, carolGets).Build()))
						env.Close()

						wantXRP, wantUSD := poolXRP, poolUSD
						if blockingOffer && !fixAMMv1_1 {
							checkOffer(env.Carol, carolPays, carolGets)
						} else if !receiveXRP {
							wantXRP, wantUSD = 200_000_980_005, usd(9_999_951, -2)
							require.Empty(t, env.AccountOffers(env.Carol))
						} else if !mptTokensV2 {
							wantXRP, wantUSD = 909_090_909, usd(550_000_000_055, -9)
							checkOffer(env.Carol, tx.NewXRPAmount(9_090_909), usd(499_999_995, -8))
						} else {
							wantXRP, wantUSD = 909_090_910, usd(54_999_999_945, -8)
							checkOffer(env.Carol, tx.NewXRPAmount(9_090_910), usd(50_000_005, -7))
						}
						if blockingOffer {
							checkOffer(blocker, blockerPays, blockerGets)
						} else {
							require.Empty(t, env.AccountOffers(blocker))
						}
						require.Equal(t, wantXRP, env.AMMPoolXRP(ammAcc))
						checkAmount(wantUSD, env.AMMPoolIOUPrecise(ammAcc, env.GW, "USD"))
						checkAmount(lpBefore, env.ReadAMMData(amm.XRP(), env.USD).LPTokenBalance)
						require.Equal(t, carolXRPBefore+poolXRP-wantXRP-env.BaseFee(), env.Balance(env.Carol))
						require.Equal(t, carolSeqBefore+1, env.Seq(env.Carol))
					})
				}
			}
		}
	}
}
