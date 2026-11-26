package escrow_test

import (
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestIOUEscrow_BadCurrencySignedBinary(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	const badCurrencyHex = "0000000000000000000000005852500000000000"
	seq, balance := env.Seq(alice), env.Balance(alice)
	result := env.SubmitSignedBinary(
		escrow.EscrowCreate(alice, bob, 0).
			IOUAmount(tx.NewIssuedAmountFromFloat64(1000, badCurrencyHex, gw.Address)).
			FinishTime(env.Now().Add(time.Second)).
			CancelTime(env.Now().Add(time.Hour)).
			Build())

	jtx.RequireTxFail(t, result, jtx.TemBAD_CURRENCY)
	require.False(t, result.Applied)
	require.Equal(t, seq, env.Seq(alice))
	require.Equal(t, balance, env.Balance(alice))
}
