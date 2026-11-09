package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/stretchr/testify/require"
)

// An arithmetic exception in a proportional tfLPToken deposit fails with
// tecINTERNAL and claims the fee, with or without fixCleanup3_4_0.
func TestAMMDepositLPTokenArithmeticExceptionIsTecInternal(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanup), func(t *testing.T) {
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
			iouBefore := env.IOUBalance(env.Alice, env.GW, "USD")

			deposit := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
				LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1e20)).
				LPToken()
			result := env.Submit(deposit.Build())
			require.Equal(t, "tecINTERNAL", result.Code)
			require.Equal(t, balanceBefore-10, env.Balance(env.Alice))
			require.Equal(t, sequenceBefore+1, env.Seq(env.Alice))

			poolAfter, err := env.LedgerEntry(key)
			require.NoError(t, err)
			require.Equal(t, poolBefore, poolAfter)
			require.Equal(t, xrpBefore, env.AMMPoolXRP(ammAccount))
			require.Equal(t, usdBefore, env.AMMPoolIOUPrecise(ammAccount, env.GW, "USD"))
			require.Equal(t, iouBefore, env.IOUBalance(env.Alice, env.GW, "USD"))
		})
	}
}
