package offer

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx/account"

	"github.com/stretchr/testify/require"
)

func TestOfferCleanup340DisallowIncomingTrustline(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/existing=%t", enabled, existing), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if enabled {
					env.EnableFeature("fixCleanup3_4_0")
				}
				issuer, alice := jtx.NewAccount("issuer"), jtx.NewAccount("alice")
				env.FundAmount(issuer, uint64(jtx.XRP(1000)))
				env.FundAmount(alice, uint64(jtx.XRP(1000)))
				env.Close()
				if existing {
					env.Trust(alice, jtx.USD(issuer, 100))
				}
				jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).SetFlag(account.AccountSetFlagDisallowIncomingTrustline).Build()))
				before, seq := env.Balance(alice), env.Seq(alice)
				result := env.Submit(OfferCreate(alice, jtx.USD(issuer, 10), jtx.XRPTxAmount(10_000_000)).Build())
				if enabled && !existing {
					jtx.RequireTxClaimed(t, result, "tecNO_LINE")
					RequireOfferCount(t, env, alice, 0)
				} else {
					jtx.RequireTxSuccess(t, result)
					RequireOfferCount(t, env, alice, 1)
				}
				require.Equal(t, before-10, env.Balance(alice))
				require.Equal(t, seq+1, env.Seq(alice))
				jtx.RequireTxSuccess(t, env.Submit(OfferCreate(issuer, jtx.USD(issuer, 10), jtx.XRPTxAmount(10_000_000)).Build()))
			})
		}
	}
}
