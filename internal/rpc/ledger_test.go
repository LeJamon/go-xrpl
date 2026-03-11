package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerReader implements types.LedgerReader for testing
type mockLedgerReader struct {
	seq                 uint32
	hash                [32]byte
	parentHash          [32]byte
	txMapHash           [32]byte
	stateMapHash        [32]byte
	closed              bool
	validated           bool
	totalDrops          uint64
	closeTime           int64
	closeTimeResolution uint32
	closeFlags          uint8
	parentCloseTime     int64
	transactions        []struct {
		hash [32]byte
		data []byte
	}
}

func (m *mockLedgerReader) Sequence() uint32                 { return m.seq }
func (m *mockLedgerReader) Hash() [32]byte                   { return m.hash }
func (m *mockLedgerReader) ParentHash() [32]byte             { return m.parentHash }
func (m *mockLedgerReader) IsClosed() bool                   { return m.closed }
func (m *mockLedgerReader) IsValidated() bool                { return m.validated }
func (m *mockLedgerReader) TotalDrops() uint64               { return m.totalDrops }
func (m *mockLedgerReader) CloseTime() int64                 { return m.closeTime }
func (m *mockLedgerReader) CloseTimeResolution() uint32      { return m.closeTimeResolution }
func (m *mockLedgerReader) CloseFlags() uint8                { return m.closeFlags }
func (m *mockLedgerReader) ParentCloseTime() int64           { return m.parentCloseTime }
func (m *mockLedgerReader) TxMapHash() [32]byte              { return m.txMapHash }
func (m *mockLedgerReader) StateMapHash() [32]byte           { return m.stateMapHash }
func (m *mockLedgerReader) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	for _, tx := range m.transactions {
		if !fn(tx.hash, tx.data) {
			break
		}
	}
	return nil
}

// ledgerMock wraps mockLedgerService and overrides GetLedgerBySequence/GetLedgerByHash
type ledgerMock struct {
	*mockLedgerService
	getLedgerBySequenceFn func(seq uint32) (types.LedgerReader, error)
	getLedgerByHashFn     func(hash [32]byte) (types.LedgerReader, error)
}

func (m *ledgerMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

func (m *ledgerMock) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if m.getLedgerByHashFn != nil {
		return m.getLedgerByHashFn(hash)
	}
	return m.mockLedgerService.GetLedgerByHash(hash)
}

// newDefaultLedgerReader creates a default mockLedgerReader with typical values
func newDefaultLedgerReader(seq uint32, validated bool) *mockLedgerReader {
	var hash [32]byte
	hash[0] = byte(seq)
	hash[31] = 0xAA

	var parentHash [32]byte
	if seq > 1 {
		parentHash[0] = byte(seq - 1)
		parentHash[31] = 0xAA
	}

	return &mockLedgerReader{
		seq:                 seq,
		hash:                hash,
		parentHash:          parentHash,
		closed:              validated || seq < 3,
		validated:           validated,
		totalDrops:          99999999999999980,
		closeTime:           776000030,
		closeTimeResolution: 10,
		closeFlags:          0,
		parentCloseTime:     776000020,
	}
}

// TestLedgerBasicRequest tests basic ledger request with default params
// Based on rippled LedgerRPC_test.cpp testLedgerRequest()
func TestLedgerBasicRequest(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Default params returns validated ledger", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")
		assert.Equal(t, true, resp["validated"])
	})

	t.Run("Numeric ledger_index", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": 2,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Equal(t, true, ledger["closed"])
		assert.Equal(t, "2", ledger["ledger_index"])
	})

	t.Run("String numeric ledger_index", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "2",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Equal(t, true, ledger["closed"])
		assert.Equal(t, "2", ledger["ledger_index"])
	})
}

// TestLedgerBadInput tests bad input handling for ledger method
// Based on rippled LedgerRPC_test.cpp testBadInput()
func TestLedgerBadInput(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq <= 2 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name        string
		params      interface{}
		expectError bool
	}{
		{
			name:        "Invalid string ledger_index (potato)",
			params:      map[string]interface{}{"ledger_index": "potato"},
			expectError: true,
		},
		{
			name:        "Non-existent ledger_index",
			params:      map[string]interface{}{"ledger_index": 10},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr, "Expected RPC error")
			}
		})
	}
}

// TestLedgerCurrentRequest tests ledger_index "current" requests
// Based on rippled LedgerRPC_test.cpp testLedgerCurrent() and testLedgerRequest()
func TestLedgerCurrentRequest(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	currentReader := newDefaultLedgerReader(3, false)
	currentReader.closed = false
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 3 {
			return currentReader, nil
		}
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "current",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
	require.NotNil(t, result)

	resp := resultToMap(t, result)
	ledger := resp["ledger"].(map[string]interface{})
	assert.Equal(t, false, ledger["closed"])
	assert.Equal(t, "3", ledger["ledger_index"])
	// Current ledger should not be validated
	assert.Equal(t, false, resp["validated"])
}

// TestLedgerFullOption tests the full option with transactions and expand
// Based on rippled LedgerRPC_test.cpp testLedgerFull()
func TestLedgerFullOption(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	// Add some mock transactions
	var txHash1, txHash2 [32]byte
	txHash1[0] = 0x01
	txHash2[0] = 0x02
	reader.transactions = []struct {
		hash [32]byte
		data []byte
	}{
		{hash: txHash1, data: []byte{0x01, 0x02, 0x03}},
		{hash: txHash2, data: []byte{0x04, 0x05, 0x06}},
	}

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Transactions true returns tx hashes", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": 2,
			"transactions": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Contains(t, ledger, "transactions")
		txs := ledger["transactions"].([]interface{})
		assert.Equal(t, 2, len(txs))
		// Without expand, should be hash strings
		_, isString := txs[0].(string)
		assert.True(t, isString, "Without expand, transactions should be hash strings")
	})

	t.Run("Transactions true with expand returns objects", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": 2,
			"transactions": true,
			"expand":       true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Contains(t, ledger, "transactions")
		txs := ledger["transactions"].([]interface{})
		assert.Equal(t, 2, len(txs))
		// With expand, should be objects with hash field
		txObj, isMap := txs[0].(map[string]interface{})
		assert.True(t, isMap, "With expand, transactions should be objects")
		assert.Contains(t, txObj, "hash")
	})
}

// TestLedgerAccountsOption tests the accounts option
// Based on rippled LedgerRPC_test.cpp testLedgerAccounts()
func TestLedgerAccountsOption(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// The accounts option requests account state. Even without account state
	// implementation, the handler should not error.
	params := map[string]interface{}{
		"ledger_index": 2,
		"accounts":     true,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
	require.NotNil(t, result)

	resp := resultToMap(t, result)
	assert.Contains(t, resp, "ledger")
}

// TestLedgerLookupByHash tests ledger lookup by hash
// Based on rippled LedgerRPC_test.cpp testLookupLedger() hash section
func TestLedgerLookupByHash(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	var expectedHash [32]byte
	expectedHash[0] = 0x4B
	expectedHash[1] = 0xC5
	reader.hash = expectedHash

	mock.getLedgerByHashFn = func(hash [32]byte) (types.LedgerReader, error) {
		if hash == expectedHash {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	hashStr := hex.EncodeToString(expectedHash[:])

	t.Run("Valid hash lookup", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_hash": hashStr,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("Invalid hash - too long", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_hash": "DEADBEEF" + hashStr,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Invalid hash - non-hex characters", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_hash": "2E81FC6EC0DD943197EGC7E3FBE9AE307F2775F2F7485BB37307984C3C0F2340",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Valid hash format but not found", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_hash": "8C3EEDB3124D92E49E75D81A8826A2E65A75FD71FC3FD6F36FEB803C5F1D812D",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, -1, rpcErr.Code)
	})
}

// TestLedgerResponseStructure tests that the response contains all expected fields
// Based on rippled LedgerRPC_test.cpp testLookupLedger() verifying response shape
func TestLedgerResponseStructure(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "validated",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMap(t, result)

	// Top-level fields
	assert.Contains(t, resp, "ledger")
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "validated")

	// Ledger object fields
	ledger := resp["ledger"].(map[string]interface{})
	assert.Contains(t, ledger, "accepted")
	assert.Contains(t, ledger, "account_hash")
	assert.Contains(t, ledger, "close_flags")
	assert.Contains(t, ledger, "close_time")
	assert.Contains(t, ledger, "close_time_human")
	assert.Contains(t, ledger, "close_time_iso")
	assert.Contains(t, ledger, "close_time_resolution")
	assert.Contains(t, ledger, "closed")
	assert.Contains(t, ledger, "hash")
	assert.Contains(t, ledger, "ledger_hash")
	assert.Contains(t, ledger, "ledger_index")
	assert.Contains(t, ledger, "parent_close_time")
	assert.Contains(t, ledger, "parent_hash")
	assert.Contains(t, ledger, "seqNum")
	assert.Contains(t, ledger, "totalCoins")
	assert.Contains(t, ledger, "total_coins")
	assert.Contains(t, ledger, "transaction_hash")

	// ledger_hash should be 64-char uppercase hex
	ledgerHash, ok := resp["ledger_hash"].(string)
	assert.True(t, ok, "ledger_hash should be a string")
	assert.Equal(t, 64, len(ledgerHash), "ledger_hash should be 64 characters")

	// validated should be true for validated ledger
	assert.Equal(t, true, resp["validated"])

	// closed should be true for validated ledger
	assert.Equal(t, true, ledger["closed"])

	// ledger_index inside ledger should be string representation
	ledgerIndex, ok := ledger["ledger_index"].(string)
	assert.True(t, ok, "ledger.ledger_index should be a string")
	assert.Equal(t, "2", ledgerIndex)
}

// TestLedgerServiceUnavailable tests behavior when ledger service is not available
func TestLedgerServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Nil services", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Nil ledger in services", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// TestLedgerNilLedgerReturned tests behavior when GetLedgerBySequence returns nil
func TestLedgerNilLedgerReturned(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr, "Expected error when no ledger is found")
}

// TestLedgerMethodMetadata tests the method's metadata
func TestLedgerMethodMetadata(t *testing.T) {
	method := &handlers.LedgerMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"ledger should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestLedgerLookupByIndex tests ledger lookup by various ledger_index values
// Based on rippled LedgerRPC_test.cpp testLookupLedger() ledger_index section
func TestLedgerLookupByIndex(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}

	readers := map[uint32]*mockLedgerReader{
		1: newDefaultLedgerReader(1, true),
		2: newDefaultLedgerReader(2, true),
		3: newDefaultLedgerReader(3, false),
	}
	readers[3].closed = false

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if r, ok := readers[seq]; ok {
			return r, nil
		}
		return nil, errors.New("not found")
	}
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("closed keyword", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": "closed"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("validated keyword", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": "validated"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		assert.Contains(t, resp, "ledger")
		assert.Contains(t, resp, "ledger_hash")
	})

	t.Run("current keyword", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": "current"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Equal(t, "3", ledger["ledger_index"])
	})

	t.Run("invalid keyword", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": "invalid"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Numeric index 1", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": 1}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMap(t, result)
		ledger := resp["ledger"].(map[string]interface{})
		assert.Equal(t, "1", ledger["ledger_index"])
	})

	t.Run("Numeric index out of range", func(t *testing.T) {
		params := map[string]interface{}{"ledger_index": 7}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, -1, rpcErr.Code, "Should return lgrNotFound error")
	})
}

// resultToMap is a test helper that converts a handler result to map[string]interface{}
func resultToMap(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}
