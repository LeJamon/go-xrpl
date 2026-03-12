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
// Mock helpers for transaction_entry tests
// =============================================================================

// mockLedgerReaderTE implements types.LedgerReader for transaction_entry tests.
type mockLedgerReaderTE struct {
	seq         uint32
	hash        [32]byte
	parentHash  [32]byte
	closed      bool
	validated   bool
	totalDrops  uint64
	closeTime   int64
	closeRes    uint32
	closeFlags  uint8
	pCloseTime  int64
	txMapHash   [32]byte
	stateMap    [32]byte
	txs         map[[32]byte][]byte
}

func newMockLedgerReaderTE(seq uint32) *mockLedgerReaderTE {
	var h [32]byte
	// Fill hash with sequence for uniqueness
	h[0] = byte(seq >> 24)
	h[1] = byte(seq >> 16)
	h[2] = byte(seq >> 8)
	h[3] = byte(seq)
	return &mockLedgerReaderTE{
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

func (m *mockLedgerReaderTE) Sequence() uint32                                         { return m.seq }
func (m *mockLedgerReaderTE) Hash() [32]byte                                           { return m.hash }
func (m *mockLedgerReaderTE) ParentHash() [32]byte                                     { return m.parentHash }
func (m *mockLedgerReaderTE) IsClosed() bool                                           { return m.closed }
func (m *mockLedgerReaderTE) IsValidated() bool                                        { return m.validated }
func (m *mockLedgerReaderTE) TotalDrops() uint64                                       { return m.totalDrops }
func (m *mockLedgerReaderTE) CloseTime() int64                                         { return m.closeTime }
func (m *mockLedgerReaderTE) CloseTimeResolution() uint32                              { return m.closeRes }
func (m *mockLedgerReaderTE) CloseFlags() uint8                                        { return m.closeFlags }
func (m *mockLedgerReaderTE) ParentCloseTime() int64                                   { return m.pCloseTime }
func (m *mockLedgerReaderTE) TxMapHash() [32]byte                                      { return m.txMapHash }
func (m *mockLedgerReaderTE) StateMapHash() [32]byte                                   { return m.stateMap }
func (m *mockLedgerReaderTE) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	for h, d := range m.txs {
		if !fn(h, d) {
			break
		}
	}
	return nil
}

// mockLedgerServiceTE extends mockLedgerService with transaction_entry-specific behavior
type mockLedgerServiceTE struct {
	*mockLedgerService
	transactions map[string]*types.TransactionInfo
	ledgers      map[uint32]*mockLedgerReaderTE
	ledgersByHash map[[32]byte]*mockLedgerReaderTE
}

func newMockLedgerServiceTE() *mockLedgerServiceTE {
	return &mockLedgerServiceTE{
		mockLedgerService: newMockLedgerService(),
		transactions:      make(map[string]*types.TransactionInfo),
		ledgers:           make(map[uint32]*mockLedgerReaderTE),
		ledgersByHash:     make(map[[32]byte]*mockLedgerReaderTE),
	}
}

func (m *mockLedgerServiceTE) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	hashStr := strings.ToUpper(hex.EncodeToString(txHash[:]))
	if tx, ok := m.transactions[hashStr]; ok {
		return tx, nil
	}
	return nil, errors.New("transaction not found")
}

func (m *mockLedgerServiceTE) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if l, ok := m.ledgers[seq]; ok {
		return l, nil
	}
	return nil, errors.New("ledger not found")
}

func (m *mockLedgerServiceTE) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if l, ok := m.ledgersByHash[hash]; ok {
		return l, nil
	}
	return nil, errors.New("ledger not found")
}

func (m *mockLedgerServiceTE) addLedger(lr *mockLedgerReaderTE) {
	m.ledgers[lr.seq] = lr
	m.ledgersByHash[lr.hash] = lr
}

func setupTestServicesTE(mock *mockLedgerServiceTE) func() {
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

// TestTransactionEntryMissingTxHash tests that missing tx_hash returns an error.
// Based on rippled TransactionEntry_test.cpp testBadInput (no params case).
func TestTransactionEntryMissingTxHash(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name   string
		params interface{}
	}{
		{
			name:   "empty params",
			params: map[string]interface{}{},
		},
		{
			name:   "nil params",
			params: nil,
		},
		{
			name: "tx_hash is empty string",
			params: map[string]interface{}{
				"tx_hash": "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result when tx_hash is missing")
			require.NotNil(t, rpcErr, "Expected RPC error when tx_hash is missing")
			assert.Contains(t, rpcErr.Message, "tx_hash",
				"Error should reference tx_hash parameter")
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		})
	}
}

// TestTransactionEntryInvalidTxHash tests that malformed tx_hash values return an error.
// Based on rippled TransactionEntry_test.cpp (DEADBEEF case and too-short/too-long cases).
func TestTransactionEntryInvalidTxHash(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name   string
		txHash string
	}{
		{
			name:   "too short - DEADBEEF",
			txHash: "DEADBEEF",
		},
		{
			name:   "63 chars - one short",
			txHash: "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C",
		},
		{
			name:   "65 chars - one too many",
			txHash: "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C70",
		},
		{
			name:   "not hex (contains G)",
			txHash: "G08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
		},
		{
			name:   "contains spaces",
			txHash: "E08D 6E97 5402 5BA2 534A 7870 7605 E060 1F03 ACE0 6368 7A0C A1BD DAFC FD16 98C7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"tx_hash":      tc.txHash,
				"ledger_index": "validated",
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for invalid tx_hash")
			require.NotNil(t, rpcErr, "Expected RPC error for invalid tx_hash: %s", tc.txHash)
		})
	}
}

// TestTransactionEntryLedgerResolution tests resolving the target ledger
// by hash, by index, and by named shortcuts (current, validated, closed).
// Based on rippled TransactionEntry_test.cpp testRequest (ledger_index and ledger_hash lookups).
func TestTransactionEntryLedgerResolution(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	// Add a ledger at sequence 2
	ledger2 := newMockLedgerReaderTE(2)
	ledger2.closeTime = 10
	mock.addLedger(ledger2)

	// Valid 64-char hex tx hash
	txHashStr := "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"
	txHashBytes, _ := hex.DecodeString(txHashStr)
	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	storedTx := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"TransactionType": "Payment",
			"Fee":             "10",
		},
		"meta": map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
		},
	}
	txData, _ := json.Marshal(storedTx)

	mock.transactions[txHashStr] = &types.TransactionInfo{
		TxData:      txData,
		LedgerIndex: 2,
		LedgerHash:  strings.ToUpper(hex.EncodeToString(ledger2.hash[:])),
		Validated:   true,
	}

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("by ledger_index integer", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash":      txHashStr,
			"ledger_index": 2,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for ledger_index=2")
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(respJSON, &resp)
		assert.Equal(t, float64(2), resp["ledger_index"])
	})

	t.Run("by ledger_index validated", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash":      txHashStr,
			"ledger_index": "validated",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for ledger_index=validated")
		require.NotNil(t, result)
	})

	t.Run("by ledger_index current", func(t *testing.T) {
		// Transaction is in ledger 2; current ledger index is 3 by default in mock,
		// so tx won't be found in ledger 3.
		params := map[string]interface{}{
			"tx_hash":      txHashStr,
			"ledger_index": "current",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		// The tx is in ledger 2 not 3, so it should fail with txnNotFound
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "not found")
	})

	t.Run("by ledger_index closed", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash":      txHashStr,
			"ledger_index": "closed",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for ledger_index=closed (closed=2)")
		require.NotNil(t, result)
	})

	t.Run("by ledger_hash", func(t *testing.T) {
		ledgerHashStr := strings.ToUpper(hex.EncodeToString(ledger2.hash[:]))
		params := map[string]interface{}{
			"tx_hash":     txHashStr,
			"ledger_hash": ledgerHashStr,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for ledger_hash lookup")
		require.NotNil(t, result)

		respJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(respJSON, &resp)
		assert.Equal(t, float64(2), resp["ledger_index"])
	})

	t.Run("default to validated when no ledger specified", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash": txHashStr,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		// validated ledger index defaults to 2, which matches our tx
		require.Nil(t, rpcErr, "Expected no error when defaulting to validated")
		require.NotNil(t, result)
	})
}

// TestTransactionEntryTxNotFound tests that a valid hash not in the ledger returns txnNotFound.
// Based on rippled TransactionEntry_test.cpp (valid structure but tx not found case).
func TestTransactionEntryTxNotFound(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	// Add a ledger but no transactions
	ledger2 := newMockLedgerReaderTE(2)
	mock.addLedger(ledger2)

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	txHashStr := "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"
	params := map[string]interface{}{
		"tx_hash":      txHashStr,
		"ledger_index": "validated",
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result, "Expected nil result when tx is not found")
	require.NotNil(t, rpcErr, "Expected RPC error when tx is not found")
	assert.Contains(t, rpcErr.Message, "not found")
}

// TestTransactionEntryTxNotInRequestedLedger tests that a transaction found in a different
// ledger than requested returns an error.
func TestTransactionEntryTxNotInRequestedLedger(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	// Add two ledgers
	ledger2 := newMockLedgerReaderTE(2)
	ledger3 := newMockLedgerReaderTE(3)
	mock.addLedger(ledger2)
	mock.addLedger(ledger3)
	mock.currentLedgerIndex = 4
	mock.closedLedgerIndex = 3
	mock.validatedLedgerIndex = 3

	txHashStr := "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"

	storedTx := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"TransactionType": "Payment",
		},
		"meta": map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
		},
	}
	txData, _ := json.Marshal(storedTx)

	// Transaction is in ledger 2
	mock.transactions[txHashStr] = &types.TransactionInfo{
		TxData:      txData,
		LedgerIndex: 2,
		LedgerHash:  strings.ToUpper(hex.EncodeToString(ledger2.hash[:])),
		Validated:   true,
	}

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Request with ledger 3, but tx is in ledger 2
	params := map[string]interface{}{
		"tx_hash":      txHashStr,
		"ledger_index": 3,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result, "Expected nil result when tx is in a different ledger")
	require.NotNil(t, rpcErr, "Expected RPC error when tx is not in requested ledger")
	assert.Contains(t, rpcErr.Message, "not found")
}

// TestTransactionEntryResponseStructure tests that a successful response
// contains the expected fields: tx_json, metadata, ledger_index, ledger_hash, validated.
// Based on rippled TransactionEntry_test.cpp testRequest (checking response members).
func TestTransactionEntryResponseStructure(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	ledger2 := newMockLedgerReaderTE(2)
	ledger2.closeTime = 10
	mock.addLedger(ledger2)

	txHashStr := "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"

	storedTx := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"TransactionType": "Payment",
			"Fee":             "10",
			"Sequence":        float64(3),
		},
		"meta": map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
		},
	}
	txData, _ := json.Marshal(storedTx)

	mock.transactions[txHashStr] = &types.TransactionInfo{
		TxData:      txData,
		LedgerIndex: 2,
		LedgerHash:  strings.ToUpper(hex.EncodeToString(ledger2.hash[:])),
		Validated:   true,
	}

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"tx_hash":      txHashStr,
		"ledger_index": 2,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no error")
	require.NotNil(t, result)

	respJSON, _ := json.Marshal(result)
	var resp map[string]interface{}
	err := json.Unmarshal(respJSON, &resp)
	require.NoError(t, err)

	// Required response fields per rippled
	assert.Contains(t, resp, "tx_json", "Response must contain tx_json")
	assert.Contains(t, resp, "metadata", "Response must contain metadata")
	assert.Contains(t, resp, "ledger_index", "Response must contain ledger_index")
	assert.Contains(t, resp, "ledger_hash", "Response must contain ledger_hash")
	assert.Contains(t, resp, "validated", "Response must contain validated")

	// Validate specific values
	assert.Equal(t, float64(2), resp["ledger_index"])
	assert.Equal(t, true, resp["validated"])

	// Validate tx_json content
	txJSON, ok := resp["tx_json"].(map[string]interface{})
	require.True(t, ok, "tx_json must be an object")
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", txJSON["Account"])
	assert.Equal(t, "Payment", txJSON["TransactionType"])

	// Validate metadata content
	meta, ok := resp["metadata"].(map[string]interface{})
	require.True(t, ok, "metadata must be an object")
	assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
}

// TestTransactionEntryServiceUnavailable tests behavior when ledger service is not available.
func TestTransactionEntryServiceUnavailable(t *testing.T) {
	method := &handlers.TransactionEntryMethod{}
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
			"tx_hash":      "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05",
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
			"tx_hash":      "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05",
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

// TestTransactionEntryMethodMetadata tests the method's metadata functions.
func TestTransactionEntryMethodMetadata(t *testing.T) {
	method := &handlers.TransactionEntryMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, method.RequiredRole(),
			"transaction_entry requires RoleUser (rippled: Role::USER)")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestTransactionEntryInvalidLedgerHash tests that an invalid ledger_hash returns an error.
func TestTransactionEntryInvalidLedgerHash(t *testing.T) {
	mock := newMockLedgerServiceTE()
	cleanup := setupTestServicesTE(mock)
	defer cleanup()

	method := &handlers.TransactionEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	txHashStr := "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"

	storedTx := map[string]interface{}{
		"tx_json": map[string]interface{}{
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
		"meta": map[string]interface{}{},
	}
	txData, _ := json.Marshal(storedTx)
	mock.transactions[txHashStr] = &types.TransactionInfo{
		TxData:      txData,
		LedgerIndex: 2,
		Validated:   true,
	}

	t.Run("ledger_hash not found", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash":     txHashStr,
			"ledger_hash": "0000000000000000000000000000000000000000000000000000000000000000",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("ledger_hash malformed - too short", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_hash":     txHashStr,
			"ledger_hash": "DEADBEEF",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})
}
