package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

type ledgerExtensionsMock struct {
	*mockLedgerService
	acceptCalls int
	rangeResult *types.LedgerRangeResult
}

func (m *ledgerExtensionsMock) AcceptLedger(context.Context) (uint32, error) {
	m.acceptCalls++
	return 8, nil
}

func (m *ledgerExtensionsMock) GetLedgerRange(context.Context, uint32, uint32) (*types.LedgerRangeResult, error) {
	return m.rangeResult, nil
}

func TestLedgerAcceptDoesNotExposeCloseTimeControl(t *testing.T) {
	ledger := &ledgerExtensionsMock{mockLedgerService: newMockLedgerService()}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		Services: &types.ServiceContainer{Ledger: ledger},
	}

	result, rpcErr := (&handlers.LedgerAcceptMethod{}).Handle(ctx, json.RawMessage(`{"close_time":1}`))
	require.Nil(t, rpcErr)
	require.Equal(t, 1, ledger.acceptCalls)
	require.Equal(t, map[string]any{"ledger_current_index": uint32(9)}, result)
}

func TestLedgerRangeSortsLedgersBySequence(t *testing.T) {
	hashes := map[uint32][32]byte{
		9: {0x99},
		7: {0x77},
		8: {0x88},
	}
	ledger := &ledgerExtensionsMock{
		mockLedgerService: newMockLedgerService(),
		rangeResult: &types.LedgerRangeResult{
			LedgerFirst: 7,
			LedgerLast:  9,
			Hashes:      hashes,
		},
	}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		Services: &types.ServiceContainer{Ledger: ledger},
	}

	for range 20 {
		result, rpcErr := (&handlers.LedgerRangeMethod{}).Handle(ctx, json.RawMessage(`{"start_ledger":7,"stop_ledger":9}`))
		require.Nil(t, rpcErr)
		response := result.(map[string]any)
		ledgers := response["ledgers"].([]map[string]any)
		require.Equal(t, []uint32{7, 8, 9}, []uint32{
			ledgers[0]["ledger_index"].(uint32),
			ledgers[1]["ledger_index"].(uint32),
			ledgers[2]["ledger_index"].(uint32),
		})
	}
}
