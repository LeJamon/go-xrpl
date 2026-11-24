package rpcenv

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestLedgerRequestValidatedAgeUsesEnvClock(t *testing.T) {
	env := New(t)
	env.Close()
	seq := env.LastClosedLedger().Sequence()
	env.Close()
	params := map[string]any{"ledger_index": seq}
	env.SetTime(env.LastClosedLedger().CloseTime().Add(120 * time.Second))
	result, rpcErr := env.RPCAs("ledger_request", params, types.RoleAdmin, types.ApiVersion2)
	require.Nil(t, rpcErr)
	require.Equal(t, seq, result.(map[string]any)["ledger_index"])

	env.AdvanceTime(time.Second)
	_, rpcErr = env.RPCAs("ledger_request", params, types.RoleAdmin, types.ApiVersion2)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpcerrors.RpcNOT_SYNCED, rpcErr.Code)
}
