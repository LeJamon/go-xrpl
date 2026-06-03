package paychan

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

// fixIncludeKeyletFields stores the creating tx's sfSequence (the value used in
// keylet::payChannel) on the PayChannel SLE. Reference: rippled PayChan.cpp:294-297.
func TestPayChanCreate_IncludeKeyletFields_Sequence(t *testing.T) {
	create := func(env *jtx.TestEnv) (uint32, *state.PayChannelData) {
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		seq := env.Seq(alice)
		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		data, err := env.LedgerEntry(chanKeylet(alice, bob, seq))
		require.NoError(t, err)
		ch, err := state.ParsePayChannel(data)
		require.NoError(t, err)
		return seq, ch
	}

	t.Run("enabled stores sfSequence", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		seq, ch := create(env)
		require.True(t, ch.HasSequence, "Sequence must be stored when fixIncludeKeyletFields is enabled")
		require.Equal(t, seq, ch.Sequence)
	})

	t.Run("disabled omits sfSequence", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixIncludeKeyletFields")
		_, ch := create(env)
		require.False(t, ch.HasSequence, "Sequence must be absent without fixIncludeKeyletFields")
	})
}
