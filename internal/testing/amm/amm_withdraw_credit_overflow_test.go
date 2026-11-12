package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/stretchr/testify/require"
)

const maxIOULimit = 9.999999999999999e95

// A withdrawal whose credit overflows the LP's IOU balance fails with a
// fee-claiming tec instead of a non-applied tefEXCEPTION.
func TestAMMWithdrawCreditOverflow(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			build      func(env *amm.AMMTestEnv) tx.Transaction
			withFix    string
			withoutFix string
		}{
			{
				name: "single-asset",
				build: func(env *amm.AMMTestEnv) tx.Transaction {
					return amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
						Amount(amm.IOUAmount(env.GW, "USD", 1e95)).SingleAsset().Build()
				},
				withFix:    "tecAMM_FAILED",
				withoutFix: "tefEXCEPTION",
			},
			{
				name: "withdraw-all",
				build: func(env *amm.AMMTestEnv) tx.Transaction {
					return amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).WithdrawAll().Build()
				},
				withFix:    "tecINTERNAL",
				withoutFix: "tecINTERNAL",
			},
			{
				name: "lp-token",
				build: func(env *amm.AMMTestEnv) tx.Transaction {
					return amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
						LPTokenIn(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1e51)).LPToken().Build()
				},
				withFix:    "tecINTERNAL",
				withoutFix: "tecINTERNAL",
			},
		} {
			t.Run(fmt.Sprintf("%s/cleanup=%t", tc.name, cleanup), func(t *testing.T) {
				env := amm.NewAMMTestEnv(t)
				if cleanup {
					env.EnableFeatureNow("fixCleanup3_4_0")
				} else {
					env.DisableFeature("fixCleanup3_4_0")
					env.Close()
				}
				env.Fund()
				env.Trust(env.Alice, env.GW, "USD", maxIOULimit)
				env.PayIOU(env.GW, env.Alice, "USD", 9e95)
				env.Close()
				jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, amm.XRPAmount(100), amm.IOUAmount(env.GW, "USD", 5e95)).Build()))
				env.Close()
				env.PayIOU(env.GW, env.Alice, "USD", 5.5e95)
				env.Close()

				key := coreamm.ComputeAMMKeylet(amm.XRP(), env.USD)
				poolBefore, err := env.LedgerEntry(key)
				require.NoError(t, err)
				ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
				require.NotNil(t, ammAccount)
				xrpBefore := env.AMMPoolXRP(ammAccount)
				usdBefore := env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD")
				balanceBefore, sequenceBefore := env.Balance(env.Alice), env.Seq(env.Alice)
				iouBefore := env.IOUBalance(env.Alice, env.GW, "USD")

				result := env.Submit(tc.build(env))
				want := tc.withoutFix
				if cleanup {
					want = tc.withFix
				}
				require.Equal(t, want, result.Code)
				if want == "tefEXCEPTION" {
					require.Equal(t, balanceBefore, env.Balance(env.Alice))
					require.Equal(t, sequenceBefore, env.Seq(env.Alice))
				} else {
					require.Equal(t, balanceBefore-10, env.Balance(env.Alice))
					require.Equal(t, sequenceBefore+1, env.Seq(env.Alice))
				}
				poolAfter, err := env.LedgerEntry(key)
				require.NoError(t, err)
				require.Equal(t, poolBefore, poolAfter)
				require.Equal(t, xrpBefore, env.AMMPoolXRP(ammAccount))
				require.Equal(t, usdBefore, env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD"))
				require.Equal(t, iouBefore, env.IOUBalance(env.Alice, env.GW, "USD"))
			})
		}
	}
}
