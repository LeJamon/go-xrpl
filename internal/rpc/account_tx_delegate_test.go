package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountTxDelegateValidation(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	tests := []struct {
		name     string
		delegate any
		code     int
		message  string
	}{
		{name: "non-object", delegate: "actor", code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'delegate'."},
		{name: "null", delegate: nil, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'delegate'."},
		{name: "missing filter", delegate: map[string]any{}, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'delegate_filter'."},
		{name: "non-string filter", delegate: map[string]any{"delegate_filter": 1}, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'delegate_filter'."},
		{name: "unknown filter", delegate: map[string]any{"delegate_filter": "other"}, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'delegate_filter'."},
		{name: "non-string counterparty", delegate: map[string]any{"delegate_filter": "actor", "counter_party": 1}, code: rpcerrors.RpcINVALID_PARAMS, message: "Invalid field 'counter_party'."},
		{name: "malformed counterparty", delegate: map[string]any{"delegate_filter": "actor", "counter_party": "not-an-account"}, code: rpcerrors.RpcACT_MALFORMED, message: "Account malformed."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{"account": account, "delegate": test.delegate})
			require.NoError(t, err)
			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RpcContext{
				Context:    context.Background(),
				ApiVersion: types.ApiVersion3,
				Services:   newTestServicesAccountTx(newAccountTxMock()),
			}, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
			assert.Equal(t, test.message, rpcErr.Message)
		})
	}
}

func TestAccountTxDelegateMarkerConvention(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const mismatch = "Do not mix delegate and non-delegate pagination markers in account_tx; repeat the same `delegate` object when using a delegate marker."

	for _, test := range []struct {
		name   string
		params map[string]any
	}{
		{
			name: "delegate marker without filter",
			params: map[string]any{
				"account": account,
				"marker":  map[string]any{"ledger": 2, "seq": 1, "delegate": true},
			},
		},
		{
			name: "ordinary marker with filter",
			params: map[string]any{
				"account":  account,
				"marker":   map[string]any{"ledger": 2, "seq": 1},
				"delegate": map[string]any{"delegate_filter": "actor"},
			},
		},
		{
			name: "false flag remains ordinary",
			params: map[string]any{
				"account":  account,
				"marker":   map[string]any{"ledger": 2, "seq": 1, "delegate": false},
				"delegate": map[string]any{"delegate_filter": "actor"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(test.params)
			require.NoError(t, err)
			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RpcContext{
				Context:    context.Background(),
				ApiVersion: types.ApiVersion3,
				Services:   newTestServicesAccountTx(newAccountTxMock()),
			}, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, mismatch, rpcErr.Message)
		})
	}
}

func TestAccountTxDelegateForwardingAndMarker(t *testing.T) {
	const (
		account      = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		counterparty = "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv"
	)
	mock := newAccountTxMock()
	mock.getAccountTransactionsWithDelegateFn = func(_ context.Context, gotAccount string, minLedger, maxLedger int64, limit uint32, marker *types.AccountTxMarker, forward bool, delegate *types.AccountTxDelegateFilter) (*types.AccountTxResult, error) {
		assert.Equal(t, account, gotAccount)
		assert.Equal(t, int64(1), minLedger)
		assert.Equal(t, int64(2), maxLedger)
		assert.Equal(t, uint32(10), limit)
		assert.Nil(t, marker)
		assert.True(t, forward)
		require.NotNil(t, delegate)
		assert.Equal(t, types.AccountTxDelegateActor, delegate.Role)
		assert.Equal(t, counterparty, delegate.Counterparty)
		return &types.AccountTxResult{
			Account:   gotAccount,
			LedgerMin: 1,
			LedgerMax: 2,
			Limit:     limit,
			Marker:    &types.AccountTxMarker{LedgerSeq: 2, TxnSeq: 3},
			Validated: true,
		}, nil
	}

	params, err := json.Marshal(map[string]any{
		"account": account,
		"binary":  true,
		"forward": true,
		"limit":   10,
		"delegate": map[string]any{
			"delegate_filter": "actor",
			"counter_party":   counterparty,
		},
	})
	require.NoError(t, err)
	result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion3,
		Services:   newTestServicesAccountTx(mock),
	}, params)
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	marker := response["marker"].(map[string]any)
	assert.Equal(t, uint32(2), marker["ledger"])
	assert.Equal(t, uint32(3), marker["seq"])
	assert.Equal(t, true, marker["delegate"])
}

func TestAccountTxMarkerShapePrecedesDelegateValidation(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion3,
		Services:   newTestServicesAccountTx(newAccountTxMock()),
	}, json.RawMessage(`{"account":"`+account+`","marker":{},"delegate":"bad"}`))
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalid marker. Provide ledger index via ledger field, and transaction sequence number via seq field", rpcErr.Message)
}
