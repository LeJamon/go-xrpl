package amm_test

import (
	"math/big"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	pay "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMCalc_LPTokensOnCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		xrp  int64
		usd  float64
		lp   string
	}{
		{"EqualAmounts", 10000, 10000, "10000000"},
		{"UnequalAmounts", 4, 1, "2000"},
		{"LargeAmounts", 20000, 20000, "20000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCalcEnv(t)
			e.FundWithIOUs(30000, 0)
			before := e.Balance(e.Alice)
			create := amm.AMMCreate(e.Alice, amm.XRPAmount(tc.xrp), amm.IOUAmount(e.GW, "USD", tc.usd)).Build()
			jtx.RequireTxSuccess(t, e.Submit(create))
			calcRequireXRPPool(t, e, uint64(tc.xrp)*1000000, create.Amount2.Value(), tc.lp)
			calcRequireLP(t, e, e.Alice, amm.XRP(), e.USD, tc.lp)
			require.Equal(t, uint64(tc.xrp)*1000000+e.ReserveIncrement(), before-e.Balance(e.Alice))
			want, err := calcAmount(t, "30000", e.USD).Sub(create.Amount2)
			require.NoError(t, err)
			require.Equal(t, want, calcHolding(t, e, e.Alice, e.USD))
		})
	}
	t.Run("IOU_IOU", func(t *testing.T) {
		e := newCalcEnv(t)
		e.FundWithIOUs(30000, 1)
		before := e.Balance(e.Alice)
		jtx.RequireTxSuccess(t, e.Submit(amm.AMMCreate(e.Alice, amm.IOUAmount(e.GW, "USD", 20000), amm.IOUAmount(e.GW, "BTC", 0.5)).Build()))
		pool := e.ReadAMMAccount(e.USD, e.BTC)
		require.NotNil(t, pool)
		calcRequireAmount(t, calcHolding(t, e, pool, e.USD), "20000")
		calcRequireAmount(t, calcHolding(t, e, pool, e.BTC), "0.5")
		calcRequireAmount(t, e.ReadAMMData(e.USD, e.BTC).LPTokenBalance, "100")
		calcRequireLP(t, e, e.Alice, e.USD, e.BTC, "100")
		calcRequireAmount(t, calcHolding(t, e, e.Alice, e.USD), "10000")
		calcRequireAmount(t, calcHolding(t, e, e.Alice, e.BTC), "0.5")
		require.Equal(t, e.ReserveIncrement(), before-e.Balance(e.Alice))
	})
}

func TestAMMCalc_Deposit(t *testing.T) {
	for _, tc := range []struct {
		name                string
		xrp                 uint64
		usd, supply, minted string
	}{
		{"SingleXRP", 11000000000, "10000", "10488088.48170151", "488088.48170151"},
		{"SingleUSD", 10000000000, "10999.99999999999", "10488088.48170151", "488088.48170151"},
		{"Proportional", 11000000000, "11000", "11000000", "1000000"},
		{"DisproportionateLimit", 11000000000, "11000", "11000000", "1000000"},
		{"ByLPTokens", 11000000000, "11000", "11000000", "1000000"},
		{"OneAssetLPTokens", 10201000000, "10000", "10100000", "100000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := calcXRPPool(t, 10000)
			before := e.Balance(e.Carol)
			b := amm.AMMDeposit(e.Carol, amm.XRP(), e.USD)
			switch tc.name {
			case "SingleXRP":
				b.Amount(amm.XRPAmount(1000)).SingleAsset()
			case "SingleUSD":
				b.Amount(amm.IOUAmount(e.GW, "USD", 1000)).SingleAsset()
			case "Proportional":
				b.Amount(amm.XRPAmount(1000)).Amount2(amm.IOUAmount(e.GW, "USD", 1000)).TwoAsset()
			case "DisproportionateLimit":
				b.Amount(amm.XRPAmount(2000)).Amount2(amm.IOUAmount(e.GW, "USD", 1000)).TwoAsset()
			case "ByLPTokens":
				b.LPTokenOut(amm.LPTokenAmount(e, amm.XRP(), e.USD, 1000000)).LPToken()
			case "OneAssetLPTokens":
				b.Amount(amm.XRPAmount(205)).LPTokenOut(amm.LPTokenAmount(e, amm.XRP(), e.USD, 100000)).OneAssetLPToken()
			default:
				t.Fatal("unknown deposit vector")
			}
			jtx.RequireTxSuccess(t, e.Submit(b.Build()))
			calcRequireXRPPool(t, e, tc.xrp, tc.usd, tc.supply)
			calcRequireLP(t, e, e.Carol, amm.XRP(), e.USD, tc.minted)
			calcRequireLP(t, e, e.Alice, amm.XRP(), e.USD, "10000000")
			require.Equal(t, tc.xrp-10000000000+10, before-e.Balance(e.Carol))
			want, err := calcAmount(t, "40000", e.USD).Sub(calcAmount(t, tc.usd, e.USD))
			require.NoError(t, err)
			require.Equal(t, want, calcHolding(t, e, e.Carol, e.USD))
		})
	}
}

func TestAMMCalc_Withdraw(t *testing.T) {
	for _, tc := range []struct {
		name        string
		xrp         uint64
		usd, supply string
	}{
		{"SingleXRP", 9000000001, "10000", "9486832.98050514"},
		{"ProportionalLPTokens", 9000000000, "9000", "9000000"},
		{"OneAssetLPTokens", 10000000000, "9980.01", "9990000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := calcXRPPool(t, 10000)
			before := e.Balance(e.Alice)
			b := amm.AMMWithdraw(e.Alice, amm.XRP(), e.USD)
			switch tc.name {
			case "SingleXRP":
				b.Amount(amm.XRPAmount(1000)).SingleAsset()
			case "ProportionalLPTokens":
				b.LPTokenIn(amm.LPTokenAmount(e, amm.XRP(), e.USD, 1000000)).LPToken()
			case "OneAssetLPTokens":
				b.Amount(amm.IOUAmount(e.GW, "USD", 0)).LPTokenIn(amm.LPTokenAmount(e, amm.XRP(), e.USD, 10000)).OneAssetLPToken()
			default:
				t.Fatal("unknown withdrawal vector")
			}
			jtx.RequireTxSuccess(t, e.Submit(b.Build()))
			calcRequireXRPPool(t, e, tc.xrp, tc.usd, tc.supply)
			calcRequireLP(t, e, e.Alice, amm.XRP(), e.USD, tc.supply)
			require.Equal(t, before+10000000000-tc.xrp-10, e.Balance(e.Alice))
			want, err := calcAmount(t, "30000", e.USD).Sub(calcAmount(t, tc.usd, e.USD))
			require.NoError(t, err)
			require.Equal(t, want, calcHolding(t, e, e.Alice, e.USD))
		})
	}
}

func TestAMMCalc_DepositWithdrawRoundTrip(t *testing.T) {
	e := calcXRPPool(t, 10000)
	before := e.Balance(e.Carol)
	jtx.RequireTxSuccess(t, e.Submit(amm.AMMDeposit(e.Carol, amm.XRP(), e.USD).LPTokenOut(amm.LPTokenAmount(e, amm.XRP(), e.USD, 1000000)).LPToken().Build()))
	calcRequireXRPPool(t, e, 11000000000, "11000", "11000000")
	calcRequireLP(t, e, e.Carol, amm.XRP(), e.USD, "1000000")
	calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), "29000")
	require.Equal(t, before-1000000010, e.Balance(e.Carol))
	e.Close()
	jtx.RequireTxSuccess(t, e.Submit(amm.AMMWithdraw(e.Carol, amm.XRP(), e.USD).WithdrawAll().Build()))
	calcRequireXRPPool(t, e, 10000000000, "10000", "10000000")
	calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), "30000")
	require.Equal(t, before-20, e.Balance(e.Carol))
	pool := e.ReadAMMAccount(amm.XRP(), e.USD)
	supply := e.ReadAMMData(amm.XRP(), e.USD).LPTokenBalance
	line, err := e.LedgerEntry(keylet.Line(e.Carol.ID, pool.ID, supply.Currency))
	require.NoError(t, err)
	require.Empty(t, line, "withdraw-all removes the zero LP trust line")
}

func TestAMMCalc_SwapAndQuality(t *testing.T) {
	for _, tc := range []struct {
		name      string
		poolUSD   float64
		delivered int64
		limited   bool
	}{
		{"ConstantProduct", 10100, 100, false}, {"LimitQuality", 10010, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := calcXRPPool(t, tc.poolUSD)
			e.TestEnv.FundAmount(e.Bob, uint64(jtx.XRP(30000)))
			e.Close()
			before := e.Balance(e.Bob)
			lp := e.ReadAMMData(amm.XRP(), e.USD).LPTokenBalance
			pool := e.ReadAMMAccount(amm.XRP(), e.USD)
			product := func() *big.Rat {
				usd, ok := new(big.Rat).SetString(calcHolding(t, e, pool, e.USD).Value())
				require.True(t, ok)
				return new(big.Rat).Mul(new(big.Rat).SetInt(new(big.Int).SetUint64(e.Balance(pool))), usd)
			}
			kBefore := product()
			p := pay.PayIssued(e.Bob, e.Carol, amm.IOUAmount(e.GW, "USD", 100)).SendMax(amm.XRPAmount(100)).PathsCurrency("USD", e.GW).NoDirectRipple()
			if tc.limited {
				p.PartialPayment().LimitQuality()
			}
			jtx.RequireTxSuccess(t, e.Submit(p.Build()))
			calcRequireXRPPool(t, e, uint64(10000+tc.delivered)*1000000, "10000", lp.Value())
			calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), big.NewInt(30000+tc.delivered).String())
			require.Equal(t, uint64(tc.delivered)*1000000+10, before-e.Balance(e.Bob))
			require.Zero(t, kBefore.Cmp(product()))
			if tc.limited {
				jtx.RequireTxClaimed(t, e.Submit(p.Build()), ter.TecPATH_DRY.String())
				calcRequireXRPPool(t, e, 10_010_000_000, "10000", lp.Value())
				calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), "30010")
				require.Equal(t, before-10_000_020, e.Balance(e.Bob))
			}
		})
	}
}

func TestAMMCalc_TradingFee(t *testing.T) {
	for _, feeOnDeposit := range []bool{false, true} {
		name := "Withdraw"
		if feeOnDeposit {
			name = "Deposit"
		}
		t.Run(name, func(t *testing.T) {
			e := newCalcEnv(t)
			e.FundWithIOUs(30000, 0)
			e.Trust(e.Alice, e.GW, "EUR", 100000)
			e.PayIOU(e.GW, e.Alice, "EUR", 30000)
			e.Close()
			create := amm.AMMCreate(e.Alice, amm.IOUAmount(e.GW, "USD", 1000), amm.IOUAmount(e.GW, "EUR", 1000))
			if feeOnDeposit {
				create.TradingFee(1000)
			}
			jtx.RequireTxSuccess(t, e.Submit(create.Build()))
			e.Close()
			beforeXRP := e.Balance(e.Carol)
			jtx.RequireTxSuccess(t, e.Submit(amm.AMMDeposit(e.Carol, e.USD, e.EUR).
				Amount(amm.IOUAmount(e.GW, "USD", 3000)).SingleAsset().Build()))
			pool := e.ReadAMMAccount(e.USD, e.EUR)
			require.NotNil(t, pool)
			wantUSD := "4000"
			if feeOnDeposit {
				wantUSD = "3999.999999999999"
			}
			calcRequireAmount(t, calcHolding(t, e, pool, e.USD), wantUSD)
			calcRequireAmount(t, calcHolding(t, e, pool, e.EUR), "1000")
			calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), "27000")
			require.Equal(t, beforeXRP-10, e.Balance(e.Carol))
			if feeOnDeposit {
				calcRequireLP(t, e, e.Carol, e.USD, e.EUR, "994.981155689671")
				calcRequireAmount(t, e.ReadAMMData(e.USD, e.EUR).LPTokenBalance, "1994.981155689671")
				return
			}
			calcRequireLP(t, e, e.Carol, e.USD, e.EUR, "1000")
			calcRequireAmount(t, e.ReadAMMData(e.USD, e.EUR).LPTokenBalance, "2000")
			jtx.RequireTxSuccess(t, e.Submit(amm.AMMVote(e.Alice, e.USD, e.EUR, 1000).Build()))
			require.Equal(t, uint16(1000), e.ReadAMMData(e.USD, e.EUR).TradingFee)
			jtx.RequireTxSuccess(t, e.Submit(amm.AMMWithdraw(e.Carol, e.USD, e.EUR).
				Amount(amm.IOUAmount(e.GW, "USD", 0)).OneAssetWithdrawAll().Build()))
			calcRequireAmount(t, calcHolding(t, e, pool, e.USD), "1005.025125628141")
			calcRequireAmount(t, calcHolding(t, e, pool, e.EUR), "1000")
			calcRequireAmount(t, e.ReadAMMData(e.USD, e.EUR).LPTokenBalance, "1000")
			calcRequireLP(t, e, e.Alice, e.USD, e.EUR, "1000")
			calcRequireAmount(t, calcHolding(t, e, e.Carol, e.USD), "29994.97487437186")
			require.Equal(t, beforeXRP-20, e.Balance(e.Carol))
			line, err := e.LedgerEntry(keylet.Line(e.Carol.ID, pool.ID, e.ReadAMMData(e.USD, e.EUR).LPTokenBalance.Currency))
			require.NoError(t, err)
			require.Empty(t, line)
		})
	}
}

func newCalcEnv(t *testing.T) *amm.AMMTestEnv {
	t.Helper()
	e := amm.NewAMMTestEnv(t)
	// These oracle vectors use the small-mantissa arithmetic profile.
	e.DisableFeature("SingleAssetVault")
	e.DisableFeature("LendingProtocol")
	return e
}

func calcXRPPool(t *testing.T, usd float64) *amm.AMMTestEnv {
	t.Helper()
	e := newCalcEnv(t)
	e.FundWithIOUs(30000, 0)
	jtx.RequireTxSuccess(t, e.Submit(amm.AMMCreate(e.Alice, amm.XRPAmount(10000), amm.IOUAmount(e.GW, "USD", usd)).Build()))
	e.Close()
	return e
}
func calcRequireXRPPool(t *testing.T, e *amm.AMMTestEnv, xrp uint64, usd, lp string) {
	t.Helper()
	pool := e.ReadAMMAccount(amm.XRP(), e.USD)
	require.NotNil(t, pool)
	require.Equal(t, xrp, e.Balance(pool))
	calcRequireAmount(t, calcHolding(t, e, pool, e.USD), usd)
	data := e.ReadAMMData(amm.XRP(), e.USD)
	require.NotNil(t, data)
	calcRequireAmount(t, data.LPTokenBalance, lp)
}
func calcRequireLP(t *testing.T, e *amm.AMMTestEnv, holder *jtx.Account, a, b tx.Asset, value string) {
	t.Helper()
	pool := e.ReadAMMAccount(a, b)
	require.NotNil(t, pool)
	supply := e.ReadAMMData(a, b).LPTokenBalance
	calcRequireAmount(t, calcHolding(t, e, holder, tx.Asset{Currency: supply.Currency, Issuer: pool.Address}), value)
}
func calcHolding(t *testing.T, e *amm.AMMTestEnv, holder *jtx.Account, asset tx.Asset) tx.Amount {
	t.Helper()
	issuer, err := state.DecodeAccountID(asset.Issuer)
	require.NoError(t, err)
	data, err := e.LedgerEntry(keylet.Line(holder.ID, issuer, asset.Currency))
	require.NoError(t, err)
	require.NotEmpty(t, data, "holding trust line must exist")
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	amount := line.Balance
	if !keylet.IsLowAccount(holder.ID, issuer) {
		amount = amount.Negate()
	}
	require.Equal(t, asset.Currency, amount.Currency)
	amount.Issuer = asset.Issuer
	return amount
}
func calcAmount(t *testing.T, value string, asset tx.Asset) tx.Amount {
	t.Helper()
	amount, err := state.NewIssuedAmountFromDecimalString(value, asset.Currency, asset.Issuer)
	require.NoError(t, err)
	return amount
}
func calcRequireAmount(t *testing.T, got tx.Amount, value string) {
	t.Helper()
	require.Equal(t, calcAmount(t, value, tx.Asset{Currency: got.Currency, Issuer: got.Issuer}), got)
}
