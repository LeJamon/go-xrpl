package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// txTablesOffLedger disables the tx-tables capability on top of
// mockLedgerService, simulating a node without a transaction database.
type txTablesOffLedger struct{ *mockLedgerService }

func (m *txTablesOffLedger) UseTxTables() bool { return false }

func newTxTablesOffContext() *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: &txTablesOffLedger{newMockLedgerService()}},
	}
}

func assertNotEnabled(t *testing.T, result any, rpcErr *types.RpcError) {
	t.Helper()
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
	assert.Equal(t, "notEnabled", rpcErr.ErrorString)
	assert.Equal(t, "Not enabled in configuration.", rpcErr.Message)
}

// Without a transaction database rippled answers rpcNOT_ENABLED before any
// parameter validation (the useTxTables() gate is the first statement of
// doAccountTxJson, doTxHistory and doTxJson). These tests pin that
// precedence: even malformed or missing parameters must yield notEnabled.

func TestAccountTxNotEnabledPrecedesParamValidation(t *testing.T) {
	method := &handlers.AccountTxMethod{}
	ctx := newTxTablesOffContext()

	for name, params := range map[string]map[string]any{
		"malformed account": {"account": "0xDEADBEEF"},
		"missing account":   {},
	} {
		t.Run(name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			assertNotEnabled(t, result, rpcErr)
		})
	}
}

func TestTxHistoryNotEnabledPrecedesParamValidation(t *testing.T) {
	method := &handlers.TxHistoryMethod{}
	ctx := newTxTablesOffContext()

	paramsJSON, err := json.Marshal(map[string]any{"start": "not-a-number"})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	assertNotEnabled(t, result, rpcErr)
}

func TestTxNotEnabledPrecedesParamValidation(t *testing.T) {
	method := &handlers.TxMethod{}
	ctx := newTxTablesOffContext()

	for name, params := range map[string]map[string]any{
		"missing transaction": {},
		"invalid ctid":        {"ctid": "not-a-ctid"},
	} {
		t.Run(name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			assertNotEnabled(t, result, rpcErr)
		})
	}
}
