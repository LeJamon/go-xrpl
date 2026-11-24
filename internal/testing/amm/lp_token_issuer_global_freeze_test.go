package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
)

// isLPTokenFrozen uses the Issue isFrozen overload, which reports a global
// freeze before it exempts the issuer. A pool-asset issuer that globally froze
// its currency therefore cannot send LP tokens it holds once
// fixFrozenLPTokenTransfer is enabled.
func TestFrozenLP_PoolAssetIssuerGlobalFreeze(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(label(enabled), func(t *testing.T) {
			env := lpFrozenEnv(t, enabled)
			lpAmt := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 5)

			jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.GW, env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 1000)).Build()))
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.Bob, env.GW, lpAmt).Build()))
			env.Close()

			env.EnableGlobalFreeze(env.GW)
			env.Close()

			r := env.Submit(payment.PayIssued(env.GW, env.Bob, lpAmt).Build())
			if enabled {
				if r.Code != jtx.TecPATH_DRY {
					t.Fatalf("with fix, globally frozen issuer gw->bob LP payment must be tecPATH_DRY, got %s", r.Code)
				}
			} else if !r.Success {
				t.Fatalf("without fix, gw->bob LP payment must succeed, got %s", r.Code)
			}
		})
	}
}
