package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock helpers for book_changes tests
// =============================================================================

// mockLedgerReaderBC implements types.LedgerReader for book_changes tests.
type mockLedgerReaderBC struct {
	seq        uint32
	hash       [32]byte
	parentHash [32]byte
	closed     bool
	validated  bool
	totalDrops uint64
	closeTime  int64
	closeRes   uint32
	closeFlags uint8
	pCloseTime int64
	txMapHash  [32]byte
	stateMap   [32]byte
	txs        map[[32]byte][]byte
}

func newMockLedgerReaderBC(seq uint32) *mockLedgerReaderBC {
	var h [32]byte
	h[0] = byte(seq >> 24)
	h[1] = byte(seq >> 16)
	h[2] = byte(seq >> 8)
	h[3] = byte(seq)
	return &mockLedgerReaderBC{
		seq:        seq,
		hash:       h,
		closed:     true,
		validated:  true,
		totalDrops: 100000000000,
		closeTime:  10,
		closeRes:   10,
		txs:        make(map[[32]byte][]byte),
	}
}

func (m *mockLedgerReaderBC) Sequence() uint32                                         { return m.seq }
func (m *mockLedgerReaderBC) Hash() [32]byte                                           { return m.hash }
func (m *mockLedgerReaderBC) ParentHash() [32]byte                                     { return m.parentHash }
func (m *mockLedgerReaderBC) IsClosed() bool                                           { return m.closed }
func (m *mockLedgerReaderBC) IsValidated() bool                                        { return m.validated }
func (m *mockLedgerReaderBC) TotalDrops() uint64                                       { return m.totalDrops }
func (m *mockLedgerReaderBC) CloseTime() int64                                         { return m.closeTime }
func (m *mockLedgerReaderBC) CloseTimeResolution() uint32                              { return m.closeRes }
func (m *mockLedgerReaderBC) CloseFlags() uint8                                        { return m.closeFlags }
func (m *mockLedgerReaderBC) ParentCloseTime() int64                                   { return m.pCloseTime }
func (m *mockLedgerReaderBC) TxMapHash() [32]byte                                      { return m.txMapHash }
func (m *mockLedgerReaderBC) StateMapHash() [32]byte                                   { return m.stateMap }
func (m *mockLedgerReaderBC) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	for h, d := range m.txs {
		if !fn(h, d) {
			break
		}
	}
	return nil
}

// mockLedgerServiceBC extends mockLedgerService with book_changes-specific behavior.
type mockLedgerServiceBC struct {
	*mockLedgerService
	ledgers      map[uint32]*mockLedgerReaderBC
	ledgersByHash map[[32]byte]*mockLedgerReaderBC
}

func newMockLedgerServiceBC() *mockLedgerServiceBC {
	return &mockLedgerServiceBC{
		mockLedgerService: newMockLedgerService(),
		ledgers:           make(map[uint32]*mockLedgerReaderBC),
		ledgersByHash:     make(map[[32]byte]*mockLedgerReaderBC),
	}
}

func (m *mockLedgerServiceBC) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if l, ok := m.ledgers[seq]; ok {
		return l, nil
	}
	return nil, errors.New("ledger not found")
}

func (m *mockLedgerServiceBC) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if l, ok := m.ledgersByHash[hash]; ok {
		return l, nil
	}
	return nil, errors.New("ledger not found")
}

func (m *mockLedgerServiceBC) addLedger(lr *mockLedgerReaderBC) {
	m.ledgers[lr.seq] = lr
	m.ledgersByHash[lr.hash] = lr
}

func setupTestServicesBC(mock *mockLedgerServiceBC) func() {
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

// TestBookChangesValidLedgerIndexVariants tests with "validated", "current", and "closed".
// Based on rippled BookChanges_test.cpp testConventionalLedgerInputStrings.
func TestBookChangesValidLedgerIndexVariants(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	// Add ledgers for validated (2), current (3), closed (2)
	ledger2 := newMockLedgerReaderBC(2)
	ledger2.validated = true
	mock.addLedger(ledger2)

	ledger3 := newMockLedgerReaderBC(3)
	ledger3.validated = false
	mock.addLedger(ledger3)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name        string
		ledgerIndex interface{}
		expectValid bool
	}{
		{
			name:        "validated",
			ledgerIndex: "validated",
			expectValid: true,
		},
		{
			name:        "current",
			ledgerIndex: "current",
			expectValid: false,
		},
		{
			name:        "closed",
			ledgerIndex: "closed",
			expectValid: true,
		},
		{
			name:        "integer index",
			ledgerIndex: 2,
			expectValid: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"ledger_index": tc.ledgerIndex,
			}
			paramsJSON, _ := json.Marshal(params)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			require.Nil(t, rpcErr, "Expected no error for ledger_index=%v", tc.ledgerIndex)
			require.NotNil(t, result)

			respJSON, _ := json.Marshal(result)
			var resp map[string]interface{}
			json.Unmarshal(respJSON, &resp)

			if tc.expectValid {
				assert.Equal(t, true, resp["validated"])
			} else {
				assert.Equal(t, false, resp["validated"])
			}
		})
	}
}

// TestBookChangesInvalidLedger tests that a non-existent ledger returns an error.
// Based on rippled BookChanges_test.cpp testConventionalLedgerInputStrings
// (non_conventional_ledger_input case).
func TestBookChangesInvalidLedger(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	// Add only ledger 2 - ledger 999 does not exist
	ledger2 := newMockLedgerReaderBC(2)
	mock.addLedger(ledger2)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("non-existent ledger index", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": 999,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr, "Expected error for non-existent ledger")
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})
}

// TestBookChangesEmptyChanges tests that a ledger with no offer modifications
// returns an empty changes array.
// Based on rippled BookChanges_test.cpp testLedgerInputDefaultBehavior (ledger with no offers).
func TestBookChangesEmptyChanges(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	// Add a ledger with no transactions (hence no offer changes)
	ledger2 := newMockLedgerReaderBC(2)
	mock.addLedger(ledger2)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "validated",
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error for empty ledger")
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	json.Unmarshal(respJSON, &resp)

	changes, ok := resp["changes"].([]interface{})
	require.True(t, ok, "changes must be an array")
	assert.Empty(t, changes, "changes should be empty when no offers modified")
}

// TestBookChangesResponseStructure tests that the response contains the expected fields:
// type, ledger_index, ledger_hash, ledger_time, validated, and changes.
// Based on rippled BookChanges_test.cpp expected response format.
func TestBookChangesResponseStructure(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	ledger2 := newMockLedgerReaderBC(2)
	ledger2.closeTime = 42
	mock.addLedger(ledger2)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "validated",
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	err := json.Unmarshal(respJSON, &resp)
	require.NoError(t, err)

	// Required fields per rippled book_changes response
	assert.Contains(t, resp, "type", "Response must contain 'type'")
	assert.Contains(t, resp, "ledger_index", "Response must contain 'ledger_index'")
	assert.Contains(t, resp, "ledger_hash", "Response must contain 'ledger_hash'")
	assert.Contains(t, resp, "ledger_time", "Response must contain 'ledger_time'")
	assert.Contains(t, resp, "validated", "Response must contain 'validated'")
	assert.Contains(t, resp, "changes", "Response must contain 'changes'")

	// Verify type is "bookChanges"
	assert.Equal(t, "bookChanges", resp["type"])

	// Verify ledger_index
	assert.Equal(t, float64(2), resp["ledger_index"])

	// Verify ledger_hash is a non-empty hex string
	ledgerHash, ok := resp["ledger_hash"].(string)
	assert.True(t, ok)
	assert.Len(t, ledgerHash, 64, "ledger_hash should be 64 hex chars")
	assert.Equal(t, strings.ToUpper(ledgerHash), ledgerHash, "ledger_hash should be uppercase")

	// Verify ledger_hash decodes as valid hex
	_, err = hex.DecodeString(ledgerHash)
	assert.NoError(t, err, "ledger_hash must be valid hex")

	// Verify ledger_time
	assert.Equal(t, float64(42), resp["ledger_time"])

	// Verify validated
	assert.Equal(t, true, resp["validated"])

	// Verify changes is an array
	_, ok = resp["changes"].([]interface{})
	assert.True(t, ok, "changes must be an array")
}

// TestBookChangesDefaultLedger tests that when no ledger_index is specified,
// the handler defaults to the validated ledger.
// Based on rippled BookChanges_test.cpp testLedgerInputDefaultBehavior.
func TestBookChangesDefaultLedger(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	ledger2 := newMockLedgerReaderBC(2)
	ledger2.validated = true
	mock.addLedger(ledger2)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Empty params: should default to validated ledger (index 2)
	params := map[string]interface{}{}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error when no ledger specified")
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	json.Unmarshal(respJSON, &resp)

	assert.Equal(t, float64(2), resp["ledger_index"],
		"Default should resolve to validated ledger index")
}

// TestBookChangesServiceUnavailable tests behavior when the ledger service is not available.
func TestBookChangesServiceUnavailable(t *testing.T) {
	method := &handlers.BookChangesMethod{}
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
			"ledger_index": "validated",
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
			"ledger_index": "validated",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// TestBookChangesMethodMetadata tests the method's metadata functions.
func TestBookChangesMethodMetadata(t *testing.T) {
	method := &handlers.BookChangesMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"book_changes should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestBookChangesNilParams tests that nil params are handled gracefully.
func TestBookChangesNilParams(t *testing.T) {
	mock := newMockLedgerServiceBC()
	cleanup := setupTestServicesBC(mock)
	defer cleanup()

	ledger2 := newMockLedgerReaderBC(2)
	mock.addLedger(ledger2)

	method := &handlers.BookChangesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error with nil params")
	require.NotNil(t, result)
}
