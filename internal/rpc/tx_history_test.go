package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock helpers for tx_history tests

// mockLedgerServiceTxHistory extends mockLedgerService with tx_history-specific behavior.
type mockLedgerServiceTxHistory struct {
	*mockLedgerService
	txHistoryResult *types.TxHistoryResult
	txHistoryErr    error
}

func newMockLedgerServiceTxHistory() *mockLedgerServiceTxHistory {
	return &mockLedgerServiceTxHistory{
		mockLedgerService: newMockLedgerService(),
	}
}

func (m *mockLedgerServiceTxHistory) GetTransactionHistory(ctx context.Context, startIndex uint32) (*types.TxHistoryResult, error) {
	if m.txHistoryErr != nil {
		return nil, m.txHistoryErr
	}
	if m.txHistoryResult != nil {
		return m.txHistoryResult, nil
	}
	return &types.TxHistoryResult{
		Index:        startIndex,
		Transactions: []types.AccountTransaction{},
	}, nil
}

func newTxHistoryTestServices(mock *mockLedgerServiceTxHistory) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
	})
}

// Tests

// TestTxHistoryBasicRequest tests basic request handling with start parameter.
// Based on rippled TransactionHistory_test.cpp testRequest.
func TestTxHistoryBasicRequest(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	services := newTxHistoryTestServices(mock)

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("start=0", func(t *testing.T) {
		mock.txHistoryResult = &types.TxHistoryResult{
			Index:        0,
			Transactions: []types.AccountTransaction{},
		}

		params := map[string]any{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr, "Expected no error for start=0")
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(respJSON, &resp)

		assert.Equal(t, float64(0), resp["index"], "index should match start value")
		assert.Contains(t, resp, "txs", "Response must contain txs array")
	})

	t.Run("start=10", func(t *testing.T) {
		mock.txHistoryResult = &types.TxHistoryResult{
			Index:        10,
			Transactions: []types.AccountTransaction{},
		}

		params := map[string]any{
			"start": 10,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(respJSON, &resp)

		assert.Equal(t, float64(10), resp["index"])
	})

	// rippled TransactionHistory_test.cpp testBadInput: start is required.
	t.Run("missing start is invalidParams", func(t *testing.T) {
		for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
			result, rpcErr := method.Handle(ctx, params)
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, "invalidParams", rpcErr.ErrorString)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
		}
	})

	// rippled TransactionHistory_test.cpp testBadInput: start above 10000
	// needs an unlimited role.
	t.Run("start above 10000 needs an unlimited role", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"start":10001}`))
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "noPermission", rpcErr.ErrorString)

		admin := *ctx
		admin.Role = types.RoleAdmin
		mock.txHistoryResult = &types.TxHistoryResult{Index: 10001}
		_, rpcErr = method.Handle(&admin, json.RawMessage(`{"start":10001}`))
		require.Nil(t, rpcErr)
	})
}

// TestTxHistoryEmptyResult tests the response when there are no transactions.
// Based on rippled TransactionHistory_test.cpp empty history scenario.
func TestTxHistoryEmptyResult(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	services := newTxHistoryTestServices(mock)

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.txHistoryResult = &types.TxHistoryResult{
		Index:        0,
		Transactions: []types.AccountTransaction{},
	}

	params := map[string]any{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Empty history should not be an error")
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]any
	json.Unmarshal(respJSON, &resp)

	// rippled appends nothing to obj[txs], so it serializes as null.
	txs, present := resp["txs"]
	require.True(t, present, "txs must be present")
	assert.Nil(t, txs, "txs should be null when no transactions exist")
}

func TestTxHistoryMalformedTransactionReturnsInternal(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	mock.txHistoryResult = &types.TxHistoryResult{
		Index: 5,
		Transactions: []types.AccountTransaction{{
			Hash:        [32]byte{1},
			LedgerIndex: 5,
			TxBlob:      []byte{0xff},
		}},
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   newTxHistoryTestServices(mock),
	}

	result, rpcErr := (&handlers.TxHistoryMethod{}).Handle(ctx, json.RawMessage(`{"start":5}`))
	require.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, 73, rpcErr.Code)
	assert.Equal(t, "internal", rpcErr.ErrorString)
}

// TestTxHistoryResponseStructure tests that the response contains expected fields.
// Based on rippled TransactionHistory_test.cpp response validation.
func TestTxHistoryResponseStructure(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	services := newTxHistoryTestServices(mock)

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.txHistoryResult = &types.TxHistoryResult{
		Index:        5,
		Transactions: []types.AccountTransaction{},
	}

	params := map[string]any{
		"start": 5,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]any
	err := json.Unmarshal(respJSON, &resp)
	require.NoError(t, err)

	// Required fields per rippled response
	assert.Contains(t, resp, "index", "Response must contain 'index' field")
	assert.Contains(t, resp, "txs", "Response must contain 'txs' field")

	// Validate index is correct
	assert.Equal(t, float64(5), resp["index"])

	// An empty page leaves txs null, as rippled does.
	assert.Nil(t, resp["txs"], "empty txs must be null")
}

// TestTxHistoryDatabaseNotConfigured tests the error when the database is not configured.
// Based on rippled TransactionHistory_test.cpp - tx_history requires a database.
func TestTxHistoryDatabaseNotConfigured(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	services := newTxHistoryTestServices(mock)

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.txHistoryErr = svcerr.ErrTxHistoryUnavailable

	params := map[string]any{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcNOT_ENABLED, rpcErr.Code, "Should return notEnabled (12) for no database")
	assert.Equal(t, "notEnabled", rpcErr.ErrorString)
	assert.Equal(t, "Not enabled in configuration.", rpcErr.Message)
}

// TestTxHistoryServiceUnavailable tests behavior when the ledger service is not available.
func TestTxHistoryServiceUnavailable(t *testing.T) {
	method := &handlers.TxHistoryMethod{}

	t.Run("Services is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		params := map[string]any{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
		assert.Equal(t, "Internal error.", rpcErr.Message)
	})

	t.Run("Services.Ledger is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: nil}),
		}

		params := map[string]any{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
		assert.Equal(t, "Internal error.", rpcErr.Message)
	})
}

// TestTxHistoryInternalError tests behavior when the service returns a generic error.
func TestTxHistoryInternalError(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	services := newTxHistoryTestServices(mock)

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.txHistoryErr = errors.New("internal database failure")

	params := map[string]any{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
}

// TestTxHistoryMethodMetadata tests the method's metadata functions.
func TestTxHistoryMethodMetadata(t *testing.T) {
	method := &handlers.TxHistoryMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, method.RequiredRole(),
			"tx_history requires Role::USER per rippled Handler.cpp")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		// tx_history is deprecated and only supports v1
		assert.NotContains(t, versions, types.ApiVersion2)
		assert.NotContains(t, versions, types.ApiVersion3)
	})
}

// Transaction::getJson adds inLedger next to ledger_index before API v2,
// and tx_history only exists in API v1.
func TestTxHistoryTransactionCarriesInLedger(t *testing.T) {
	encoded, err := binarycodec.Encode(map[string]any{
		"TransactionType": "AccountSet",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
	})
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)

	mock := newMockLedgerServiceTxHistory()
	mock.txHistoryResult = &types.TxHistoryResult{
		Index: 0,
		Transactions: []types.AccountTransaction{{
			Hash:        [32]byte{1},
			LedgerIndex: 7,
			TxBlob:      blob,
		}},
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   newTxHistoryTestServices(mock),
	}

	result, rpcErr := (&handlers.TxHistoryMethod{}).Handle(ctx, json.RawMessage(`{"start":0}`))
	require.Nil(t, rpcErr)
	respJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Txs []map[string]any `json:"txs"`
	}
	require.NoError(t, json.Unmarshal(respJSON, &resp))
	require.Len(t, resp.Txs, 1)
	assert.Equal(t, float64(7), resp.Txs[0]["inLedger"])
	assert.Equal(t, float64(7), resp.Txs[0]["ledger_index"])
}
