package amm_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAMMDepositOverflowResultsAndRollback(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, fixture := range []struct {
			name     string
			twoAsset bool
			amount   int64
		}{
			{name: "two-asset-int64", twoAsset: true, amount: 1_000_000_000_000_000},
			{name: "two-asset-xrp-maximum", twoAsset: true, amount: 100_000_000_000},
			{name: "eprice-int64", amount: 99_999_999_999_999_999},
			{name: "eprice-xrp-maximum", amount: 1_000_000_000},
		} {
			t.Run(fmt.Sprintf("%s/cleanup=%t", fixture.name, cleanup), func(t *testing.T) {
				env := amm.NewAMMTestEnv(t)
				if cleanup {
					env.EnableFeatureNow("fixCleanup3_4_0")
				} else {
					env.DisableFeature("fixCleanup3_4_0")
					env.Close()
				}
				env.Fund()
				env.Trust(env.Alice, env.GW, "USD", 1e20)
				env.PayIOU(env.GW, env.Alice, "USD", 1e18)
				env.Close()
				jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.GW, amm.XRPAmount(10), amm.IOUAmount(env.GW, "USD", 1)).Build()))
				env.Close()

				key := coreamm.ComputeAMMKeylet(amm.XRP(), env.USD)
				poolBefore, err := env.LedgerEntry(key)
				require.NoError(t, err)
				ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
				require.NotNil(t, ammAccount)
				xrpBefore := env.AMMPoolXRP(ammAccount)
				usdBefore := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
				balanceBefore, sequenceBefore := env.Balance(env.Alice), env.Seq(env.Alice)
				ownersBefore := env.OwnerCount(env.Alice)
				iouBefore := env.IOUBalance(env.Alice, env.GW, "USD")
				deposit := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD)
				if fixture.twoAsset {
					deposit = amm.AMMDeposit(env.Alice, env.USD, amm.XRP())
					deposit.Amount(amm.IOUAmount(env.GW, "USD", float64(fixture.amount))).Amount2(amm.XRPAmount(1)).TwoAsset()
				} else {
					deposit.Amount(tx.NewXRPAmount(0)).EPrice(tx.NewXRPAmount(fixture.amount)).LimitLPToken()
				}
				result := env.Submit(deposit.Build())
				if cleanup {
					require.Equal(t, "tecAMM_FAILED", result.Code)
					require.Equal(t, balanceBefore-10, env.Balance(env.Alice))
					require.Equal(t, sequenceBefore+1, env.Seq(env.Alice))
				} else {
					require.Equal(t, "tefEXCEPTION", result.Code)
					require.Equal(t, balanceBefore, env.Balance(env.Alice))
					require.Equal(t, sequenceBefore, env.Seq(env.Alice))
				}
				poolAfter, err := env.LedgerEntry(key)
				require.NoError(t, err)
				require.Equal(t, poolBefore, poolAfter)
				xrpAfter := env.AMMPoolXRP(ammAccount)
				usdAfter := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
				require.Equal(t, xrpBefore, xrpAfter)
				require.Equal(t, usdBefore, usdAfter)
				require.Equal(t, ownersBefore, env.OwnerCount(env.Alice))
				require.Equal(t, iouBefore, env.IOUBalance(env.Alice, env.GW, "USD"))
			})
		}
	}
}

func TestAMMDepositLPTokenOverflowReturnsInternalAndClaimsFee(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for _, lpTokenOut := range []string{"1000000000000000", "100000000000000000000"} {
				t.Run(fmt.Sprintf("cleanup=%t/reverse=%t/lp-token-out=%s", cleanup, reverse, lpTokenOut), func(t *testing.T) {
					pool := [2]tx.Amount{amm.XRPAmount(100), amm.IOUAmount(nil, "USD", 100)}
					amm.TestAMM(t, &pool, 0, func(env *amm.AMMTestEnv, ammAccount *jtx.Account) {
						if cleanup {
							env.EnableFeatureNow("fixCleanup3_4_0")
						} else {
							env.DisableFeature("fixCleanup3_4_0")
							env.Close()
						}

						asset1, asset2 := amm.XRP(), env.USD
						if reverse {
							asset1, asset2 = asset2, asset1
						}
						ammData := env.ReadAMMData(asset1, asset2)
						require.NotNil(t, ammData)
						lpOut, err := state.NewIssuedAmountFromDecimalString(
							lpTokenOut, ammData.LPTokenBalance.Currency, ammData.LPTokenBalance.Issuer,
						)
						require.NoError(t, err)

						poolKey := coreamm.ComputeAMMKeylet(amm.XRP(), env.USD)
						poolBefore, err := env.LedgerEntry(poolKey)
						require.NoError(t, err)
						xrpPoolBefore := env.AMMPoolXRP(ammAccount)
						usdPoolBefore := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
						xrpDepositorBefore := env.Balance(env.Carol)
						usdDepositorBefore, heldUSD := env.LookupIOUBalance(env.Carol, env.GW, "USD")
						require.True(t, heldUSD)
						ownersBefore := env.OwnerCount(env.Carol)
						lpDepositorBefore, lpDepositorHeldBefore := env.LookupIOUBalance(env.Carol, ammAccount, ammData.LPTokenBalance.Currency)
						sequenceBefore := env.Seq(env.Carol)

						result := env.Submit(amm.AMMDeposit(env.Carol, asset1, asset2).LPTokenOut(lpOut).LPToken().Build())
						require.Equal(t, ter.TecINTERNAL.String(), result.Code)
						require.Equal(t, uint64(10), result.Fee)
						require.NotNil(t, result.Metadata)
						require.Equal(t, ter.TecINTERNAL, result.Metadata.TransactionResult)
						require.Equal(t, xrpDepositorBefore-10, env.Balance(env.Carol))
						require.Equal(t, sequenceBefore+1, env.Seq(env.Carol))

						poolAfter, err := env.LedgerEntry(poolKey)
						require.NoError(t, err)
						require.Equal(t, poolBefore, poolAfter)
						require.Equal(t, xrpPoolBefore, env.AMMPoolXRP(ammAccount))
						require.Equal(t, usdPoolBefore, env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD"))
						usdDepositorAfter, heldUSD := env.LookupIOUBalance(env.Carol, env.GW, "USD")
						require.True(t, heldUSD)
						require.Equal(t, *usdDepositorBefore, *usdDepositorAfter)
						require.Equal(t, ownersBefore, env.OwnerCount(env.Carol))
						lpDepositorAfter, lpDepositorHeldAfter := env.LookupIOUBalance(env.Carol, ammAccount, ammData.LPTokenBalance.Currency)
						require.Equal(t, lpDepositorHeldBefore, lpDepositorHeldAfter)
						if lpDepositorHeldBefore {
							require.Equal(t, *lpDepositorBefore, *lpDepositorAfter)
						}
					})
				})
			}
		}
	}
}
