package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/stretchr/testify/require"
)

// AMMClawback credits the clawed asset to the holder before clawing it back,
// so a holder balance far below the withdrawn amount is rounded away.
func TestAMMClawbackRoundsThroughHolderLine(t *testing.T) {
	env := setupClawbackEnv(t, 100000, 100000)
	env.Trust(env.Alice, env.GW, "USD", 1e30)
	env.Trust(env.Alice, env.GW, "EUR", 1e30)
	env.PayIOU(env.GW, env.Alice, "USD", 2e17)
	env.PayIOU(env.GW, env.Alice, "EUR", 1000)
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 2e17), amm.IOUAmount(env.GW, "EUR", 100)).Build()))
	env.Close()
	env.PayIOU(env.GW, env.Alice, "USD", 1.23456789)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, env.EUR).
		Amount(amm.IOUAmount(env.GW, "USD", 1e17)).Build()))
	env.Close()
	require.True(t, env.IOUBalance(env.Alice, env.GW, "USD").IsZero())
}
