package escrow_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestIOUEscrow_CancelRecreatesTrustLine exercises the fixCleanup3_2_0 token-escrow
// cancel fix: the refund uses the creator's account ledger entry, so cancelling an
// IOU escrow after the creator deleted their trust line succeeds and re-creates the
// line. Pre-amendment the refund used the (erased) escrow entry and the cancel fails.
// Reference: rippled EscrowToken_test.cpp testIOUCancelDoApply (PR 6171).
func TestIOUEscrow_CancelRecreatesTrustLine(t *testing.T) {
	run := func(t *testing.T, fixOn bool) {
		env := jtx.NewTestEnv(t)
		if !fixOn {
			env.DisableFeature("fixCleanup3_2_0")
		}
		env.EnableFeature("TokenEscrow")

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, gw, alice, bob)

		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build()))
		env.Close()
		env.Trust(alice, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Trust(bob, tx.NewIssuedAmountFromFloat64(100000, "USD", gw.Address))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, alice, usd(10000, gw)).Build()))
		env.Close()

		// alice escrows 1000 USD to bob.
		seq := env.Seq(alice)
		jtx.RequireTxSuccess(t, env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				IOUAmount(usd(1000, gw)).
				FinishTime(env.Now().Add(1*time.Second)).
				CancelTime(env.Now().Add(2*time.Second)).
				Build()))
		env.Close()
		require.InDelta(t, 9000, env.BalanceIOU(alice, "USD", gw), 0.001)

		// alice pays away the rest and deletes her USD trust line.
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(alice, gw, usd(9000, gw)).Build()))
		env.Close()
		env.Trust(alice, tx.NewIssuedAmountFromFloat64(0, "USD", gw.Address))
		env.Close()
		require.False(t, env.TrustLineExists(alice, gw, "USD"), "trust line should be deleted")

		// Advance past the cancel time and cancel the escrow.
		env.SetTime(env.Now().Add(10 * time.Second))
		env.Close()
		res := env.Submit(escrow.EscrowCancel(alice, alice, seq).Build())

		if fixOn {
			jtx.RequireTxSuccess(t, res)
			require.True(t, env.TrustLineExists(alice, gw, "USD"), "cancel must re-create the trust line")
			require.InDelta(t, 1000, env.BalanceIOU(alice, "USD", gw), 0.001, "creator must get the escrowed IOU back")
		} else {
			require.Equal(t, "tefEXCEPTION", res.Code, "pre-fix cancel must fail reading the escrow SLE owner count")
		}
	}

	t.Run("fixOn", func(t *testing.T) { run(t, true) })
	t.Run("fixOff", func(t *testing.T) { run(t, false) })
}
