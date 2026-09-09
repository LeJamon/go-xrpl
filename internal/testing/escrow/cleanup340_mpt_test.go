package escrow_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestMPTEscrowCleanupMaximumFee(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("TokenEscrow")
			if enabled {
				env.EnableFeature("fixCleanup3_4_0")
			} else {
				env.DisableFeature("fixCleanup3_4_0")
			}
			issuer, alice, bob := jtx.NewAccount("issuer"), jtx.NewAccount("alice"), jtx.NewAccount("bob")
			token := mpt.NewMPTTester(t, env, issuer, mpt.MPTInit{Holders: []*jtx.Account{alice, bob}})
			token.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer, TransferFee: mpt.PtrUint16(100)})
			token.Authorize(mpt.AuthorizeOpts{Account: alice})
			token.Authorize(mpt.AuthorizeOpts{Account: bob})
			token.Pay(issuer, alice, math.MaxInt64)
			env.Close()
			seq := env.Seq(alice)
			jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(token.MPTAmount(math.MaxInt64)).FinishTime(env.Now().Add(time.Second)).Build()))
			env.Close()
			balance, sequence := env.Balance(bob), env.Seq(bob)
			result := env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
			if !enabled {
				jtx.RequireTxFail(t, result, "tefEXCEPTION")
				jtx.RequireBalance(t, env, bob, balance)
				jtx.RequireSequence(t, env, bob, sequence)
				require.True(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)))
				token.RequireMPTokenAmount(bob, 0)
				require.Equal(t, uint64(math.MaxInt64), token.HolderLockedAmount(alice))
				require.Equal(t, uint64(math.MaxInt64), token.IssuanceLockedAmount())
				require.Equal(t, uint64(math.MaxInt64), token.IssuanceOutstandingAmount())
				return
			}
			jtx.RequireTxSuccess(t, result)
			jtx.RequireBalance(t, env, bob, balance-env.BaseFee())
			jtx.RequireSequence(t, env, bob, sequence+1)
			require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)))
			token.RequireMPTokenAmount(alice, 0)
			token.RequireMPTokenAmount(bob, 9_214_157_878_975_800_006)
			require.Zero(t, token.HolderLockedAmount(alice))
			require.Zero(t, token.IssuanceLockedAmount())
			require.Equal(t, uint64(9_214_157_878_975_800_006), token.IssuanceOutstandingAmount())
			jtx.RequireOwnerCount(t, env, alice, 1)
		})
	}
}
