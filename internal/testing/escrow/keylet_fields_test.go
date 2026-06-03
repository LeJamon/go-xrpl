package escrow_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// fixIncludeKeyletFields stores the creating tx's sfSequence (the value used in
// keylet::escrow) on the Escrow SLE. Reference: rippled Escrow.cpp:542-545.
func TestEscrowCreate_IncludeKeyletFields_Sequence(t *testing.T) {
	create := func(env *jtx.TestEnv) (uint32, *state.EscrowData) {
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		fund5000(env, alice, bob)

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, xrp(1000)).
				FinishTime(env.Now().Add(1 * time.Second)).
				Build())
		jtx.RequireTxSuccess(t, result)

		data, err := env.LedgerEntry(keylet.Escrow(alice.ID, seq))
		require.NoError(t, err)
		e, err := state.ParseEscrow(data)
		require.NoError(t, err)
		return seq, e
	}

	t.Run("enabled stores sfSequence", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		seq, e := create(env)
		require.True(t, e.HasSequence, "Sequence must be stored when fixIncludeKeyletFields is enabled")
		require.Equal(t, seq, e.Sequence)
	})

	t.Run("disabled omits sfSequence", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixIncludeKeyletFields")
		_, e := create(env)
		require.False(t, e.HasSequence, "Sequence must be absent without fixIncludeKeyletFields")
	})
}
