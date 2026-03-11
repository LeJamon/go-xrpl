package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock helpers for tx_history tests
// =============================================================================

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

func (m *mockLedgerServiceTxHistory) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
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

func setupTestServicesTxHistory(mock *mockLedgerServiceTxHistory) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// =============================================================================
// Tests
// =============================================================================

// TestTxHistoryBasicRequest tests basic request handling with start parameter.
// Based on rippled TransactionHistory_test.cpp testRequest.
func TestTxHistoryBasicRequest(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	cleanup := setupTestServicesTxHistory(mock)
	defer cleanup()

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("start=0", func(t *testing.T) {
		mock.txHistoryResult = &types.TxHistoryResult{
			Index:        0,
			Transactions: []types.AccountTransaction{},
		}

		params := map[string]interface{}{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr, "Expected no error for start=0")
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(respJSON, &resp)

		assert.Equal(t, float64(0), resp["index"], "index should match start value")
		assert.Contains(t, resp, "txs", "Response must contain txs array")
	})

	t.Run("start=10", func(t *testing.T) {
		mock.txHistoryResult = &types.TxHistoryResult{
			Index:        10,
			Transactions: []types.AccountTransaction{},
		}

		params := map[string]interface{}{
			"start": 10,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(respJSON, &resp)

		assert.Equal(t, float64(10), resp["index"])
	})

	t.Run("no params defaults to start=0", func(t *testing.T) {
		mock.txHistoryResult = &types.TxHistoryResult{
			Index:        0,
			Transactions: []types.AccountTransaction{},
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr, "Expected no error with nil params")
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(respJSON, &resp)

		assert.Equal(t, float64(0), resp["index"])
	})
}

// TestTxHistoryEmptyResult tests the response when there are no transactions.
// Based on rippled TransactionHistory_test.cpp empty history scenario.
func TestTxHistoryEmptyResult(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	cleanup := setupTestServicesTxHistory(mock)
	defer cleanup()

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.txHistoryResult = &types.TxHistoryResult{
		Index:        0,
		Transactions: []types.AccountTransaction{},
	}

	params := map[string]interface{}{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Empty history should not be an error")
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	json.Unmarshal(respJSON, &resp)

	txs, ok := resp["txs"].([]interface{})
	require.True(t, ok, "txs must be an array")
	assert.Empty(t, txs, "txs should be empty when no transactions exist")
}

// TestTxHistoryResponseStructure tests that the response contains expected fields.
// Based on rippled TransactionHistory_test.cpp response validation.
func TestTxHistoryResponseStructure(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	cleanup := setupTestServicesTxHistory(mock)
	defer cleanup()

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.txHistoryResult = &types.TxHistoryResult{
		Index:        5,
		Transactions: []types.AccountTransaction{},
	}

	params := map[string]interface{}{
		"start": 5,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	err := json.Unmarshal(respJSON, &resp)
	require.NoError(t, err)

	// Required fields per rippled response
	assert.Contains(t, resp, "index", "Response must contain 'index' field")
	assert.Contains(t, resp, "txs", "Response must contain 'txs' field")

	// Validate index is correct
	assert.Equal(t, float64(5), resp["index"])

	// Validate txs is an array
	_, ok := resp["txs"].([]interface{})
	assert.True(t, ok, "txs must be an array")
}

// TestTxHistoryDatabaseNotConfigured tests the error when the database is not configured.
// Based on rippled TransactionHistory_test.cpp - tx_history requires a database.
func TestTxHistoryDatabaseNotConfigured(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	cleanup := setupTestServicesTxHistory(mock)
	defer cleanup()

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.txHistoryErr = errors.New("transaction history not available (no database configured)")

	params := map[string]interface{}{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, 73, rpcErr.Code, "Should return error code 73 for no database")
}

// TestTxHistoryServiceUnavailable tests behavior when the ledger service is not available.
func TestTxHistoryServiceUnavailable(t *testing.T) {
	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Services is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

		params := map[string]interface{}{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Services.Ledger is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

		params := map[string]interface{}{
			"start": 0,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// TestTxHistoryInternalError tests behavior when the service returns a generic error.
func TestTxHistoryInternalError(t *testing.T) {
	mock := newMockLedgerServiceTxHistory()
	cleanup := setupTestServicesTxHistory(mock)
	defer cleanup()

	method := &handlers.TxHistoryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.txHistoryErr = errors.New("internal database failure")

	params := map[string]interface{}{
		"start": 0,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

// TestTxHistoryMethodMetadata tests the method's metadata functions.
func TestTxHistoryMethodMetadata(t *testing.T) {
	method := &handlers.TxHistoryMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"tx_history should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
