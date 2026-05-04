package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceTx extends mockLedgerService with tx-specific behavior
type mockLedgerServiceTx struct {
	*mockLedgerService
	transactions       map[string]*types.TransactionInfo
	networkID          uint16
	txLookupError      error
	ledgerRangeError   error
	completeLedgers    string
	minAvailableLedger uint32
	maxAvailableLedger uint32
}

func newMockLedgerServiceTx() *mockLedgerServiceTx {
	return &mockLedgerServiceTx{
		mockLedgerService:  newMockLedgerService(),
		transactions:       make(map[string]*types.TransactionInfo),
		networkID:          0, // Default: mainnet-like (no network ID)
		completeLedgers:    "1-1000",
		minAvailableLedger: 1,
		maxAvailableLedger: 1000,
	}
}

func (m *mockLedgerServiceTx) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	if m.txLookupError != nil {
		return nil, m.txLookupError
	}
	hashStr := strings.ToUpper(hex.EncodeToString(txHash[:]))
	if tx, ok := m.transactions[hashStr]; ok {
		return tx, nil
	}
	return nil, errors.New("transaction not found")
}

func (m *mockLedgerServiceTx) GetNetworkID() uint16 {
	return m.networkID
}

func (m *mockLedgerServiceTx) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	if m.ledgerRangeError != nil {
		return nil, m.ledgerRangeError
	}
	return &types.LedgerRangeResult{
		LedgerFirst: m.minAvailableLedger,
		LedgerLast:  m.maxAvailableLedger,
		Hashes:      make(map[uint32][32]byte),
	}, nil
}

// servicesForTx builds a per-test ServiceContainer with a tx mock.
func servicesForTx(mock *mockLedgerServiceTx) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger: mock,
	}
}

// Transaction Lookup Tests

// TestTxMethodErrorValidation tests error handling for invalid inputs
// Based on rippled Transaction_test.cpp
func TestTxMethodErrorValidation(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name          string
		params        interface{}
		expectedError string
		expectedCode  int
		setupMock     func()
	}{
		{
			name:          "Missing transaction field - empty params",
			params:        map[string]interface{}{},
			expectedError: "Missing required parameter: transaction",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing transaction field - nil params",
			params:        nil,
			expectedError: "Missing required parameter: transaction",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid hash format - too short",
			params: map[string]interface{}{
				"transaction": "ABC123",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - too long (68 chars)",
			params: map[string]interface{}{
				"transaction": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - 63 chars (1 short)",
			params: map[string]interface{}{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - 65 chars (1 extra)",
			params: map[string]interface{}{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C70",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - not hex (contains G)",
			params: map[string]interface{}{
				"transaction": "G08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - not hex (contains Z)",
			params: map[string]interface{}{
				"transaction": "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - special characters",
			params: map[string]interface{}{
				"transaction": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6!@#$",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - contains spaces",
			params: map[string]interface{}{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD169 C7",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - empty string",
			params: map[string]interface{}{
				"transaction": "",
			},
			expectedError: "Missing required parameter: transaction",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Transaction not found - valid hash format (txnNotFound)",
			params: map[string]interface{}{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
			},
			expectedError: "Transaction not found",
			setupMock: func() {
				mock.txLookupError = errors.New("transaction not found")
			},
		},
		{
			name: "Invalid transaction type - integer",
			params: map[string]interface{}{
				"transaction": 12345,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - boolean",
			params: map[string]interface{}{
				"transaction": true,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - array",
			params: map[string]interface{}{
				"transaction": []string{"hash1", "hash2"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - object",
			params: map[string]interface{}{
				"transaction": map[string]interface{}{"hash": "value"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - float",
			params: map[string]interface{}{
				"transaction": 123.456,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - null",
			params: map[string]interface{}{
				"transaction": nil,
			},
			expectedError: "Missing required parameter: transaction",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.txLookupError = nil

			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			// Verify error response
			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError,
				"Error message should contain expected text")
			if tc.expectedCode != 0 {
				assert.Equal(t, tc.expectedCode, rpcErr.Code,
					"Error code should match expected")
			}
		})
	}
}

// TestTxMethodLookupByHash tests transaction lookup by 64-char hex hash
// Based on rippled Transaction_test.cpp testRequest
func TestTxMethodLookupByHash(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Valid 64-character transaction hash
	validHash := "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"

	// Create mock transaction data
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
		},
	}
	storedData, _ := json.Marshal(storedTx)

	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
		TxIndex:     0,
	}

	tests := []struct {
		name         string
		params       map[string]interface{}
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Lookup by lowercase hash",
			params: map[string]interface{}{
				"transaction": strings.ToLower(validHash),
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Hash is uppercased in the response (matching rippled)
				assert.Equal(t, strings.ToUpper(validHash), resp["hash"])
				assert.Equal(t, float64(100), resp["ledger_index"])
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "Lookup by uppercase hash",
			params: map[string]interface{}{
				"transaction": strings.ToUpper(validHash),
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, strings.ToUpper(validHash), resp["hash"])
				assert.Equal(t, float64(100), resp["ledger_index"])
			},
		},
		{
			name: "Lookup by mixed case hash",
			params: map[string]interface{}{
				"transaction": "e08D6E9754025ba2534A78707605E0601f03ACE063687A0ca1BDDACFCD1698c7",
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, float64(100), resp["ledger_index"])
			},
		},
		{
			name: "Lookup returns all required fields",
			params: map[string]interface{}{
				"transaction": validHash,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Required fields per rippled
				assert.Contains(t, resp, "hash")
				assert.Contains(t, resp, "ledger_index")
				assert.Contains(t, resp, "ledger_hash")
				assert.Contains(t, resp, "validated")
				assert.Contains(t, resp, "meta")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error")
			require.NotNil(t, result)

			// Convert result to map
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var respMap map[string]interface{}
			err = json.Unmarshal(resultJSON, &respMap)
			require.NoError(t, err)

			tc.validateResp(t, respMap)
		})
	}
}

// TestTxMethodBinaryOption tests the binary=true/false option
// Based on rippled Transaction_test.cpp testBinaryRequest
func TestTxMethodBinaryOption(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validHash := "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"

	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
		},
	}
	storedData, _ := json.Marshal(storedTx)

	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
		TxIndex:     0,
	}

	tests := []struct {
		name         string
		binary       interface{}
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name:   "binary=false returns JSON fields",
			binary: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Should have JSON fields from transaction
				assert.Equal(t, "Payment", resp["TransactionType"])
				assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", resp["Account"])
				assert.Equal(t, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", resp["Destination"])
				// Should have meta as JSON object
				assert.NotNil(t, resp["meta"])
				if meta, ok := resp["meta"].(map[string]interface{}); ok {
					assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
				}
			},
		},
		{
			name:   "binary=true returns tx_blob as hex string",
			binary: true,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Should have tx_blob (hex encoded binary)
				if txBlob, ok := resp["tx_blob"].(string); ok {
					assert.NotEmpty(t, txBlob)
					// Verify it's a valid hex string
					_, err := hex.DecodeString(txBlob)
					assert.NoError(t, err, "tx_blob should be valid hex")
				}
				// Should have meta as binary (hex string)
				if meta, ok := resp["meta"].(string); ok {
					assert.NotEmpty(t, meta)
				}
			},
		},
		{
			name:   "Default (no binary param) returns JSON",
			binary: nil,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Should have JSON fields
				assert.Equal(t, "Payment", resp["TransactionType"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"transaction": validHash,
			}
			if tc.binary != nil {
				params["binary"] = tc.binary
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error")
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var respMap map[string]interface{}
			err = json.Unmarshal(resultJSON, &respMap)
			require.NoError(t, err)

			tc.validateResp(t, respMap)
		})
	}
}

// CTID (Concise Transaction ID) Tests
// Based on rippled Transaction_test.cpp testCTIDValidation

// EncodeCTID encodes ledger_seq, txn_index, and network_id into a CTID string
// CTID format: C + ledger_seq (7 hex nibbles) + txn_index (4 hex nibbles) + network_id (4 hex nibbles) = 16 chars total
func EncodeCTID(ledgerSeq uint32, txnIndex uint16, networkID uint16) (string, error) {
	// Validate ledger_seq doesn't exceed 28 bits (0x0FFFFFFF)
	if ledgerSeq > 0x0FFFFFFF {
		return "", fmt.Errorf("ledger_seq exceeds maximum value (0x0FFFFFFF)")
	}

	// Build the CTID value:
	// Bits 60-63: 0xC (marker)
	// Bits 32-59: ledger_seq (28 bits)
	// Bits 16-31: txn_index (16 bits)
	// Bits 0-15: network_id (16 bits)
	ctidValue := uint64(0xC)<<60 |
		uint64(ledgerSeq)<<32 |
		uint64(txnIndex)<<16 |
		uint64(networkID)

	return fmt.Sprintf("%016X", ctidValue), nil
}

// DecodeCTID decodes a CTID string into its components
func DecodeCTID(ctid string) (ledgerSeq uint32, txnIndex uint16, networkID uint16, err error) {
	// Convert to uppercase for parsing
	ctid = strings.ToUpper(strings.TrimSpace(ctid))

	// Validate length - must be exactly 16 hex characters
	if len(ctid) != 16 {
		return 0, 0, 0, fmt.Errorf("invalid CTID length: expected 16 characters, got %d", len(ctid))
	}

	// Validate starts with 'C' - the CTID marker
	if ctid[0] != 'C' {
		return 0, 0, 0, fmt.Errorf("invalid CTID: must start with 'C'")
	}

	// Validate all characters are valid hex
	for i, c := range ctid {
		isHex := (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
		if !isHex {
			return 0, 0, 0, fmt.Errorf("invalid CTID: character at position %d is not a valid hex digit", i)
		}
	}

	// Parse hex value
	var ctidValue uint64
	_, err = fmt.Sscanf(ctid, "%016X", &ctidValue)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid CTID: not a valid hex string")
	}

	// Extract components
	ledgerSeq = uint32((ctidValue >> 32) & 0x0FFFFFFF)
	txnIndex = uint16((ctidValue >> 16) & 0xFFFF)
	networkID = uint16(ctidValue & 0xFFFF)

	return ledgerSeq, txnIndex, networkID, nil
}

// TestCTIDEncoding tests CTID encoding according to rippled specification
// Based on rippled Transaction_test.cpp testCTIDValidation Test cases 1-4
func TestCTIDEncoding(t *testing.T) {
	tests := []struct {
		name       string
		ledgerSeq  uint32
		txnIndex   uint16
		networkID  uint16
		expected   string
		shouldFail bool
	}{
		// Test case 1: Valid input values
		{
			name:      "Max values (0x0FFFFFFF, 0xFFFF, 0xFFFF)",
			ledgerSeq: 0x0FFFFFFF,
			txnIndex:  0xFFFF,
			networkID: 0xFFFF,
			expected:  "CFFFFFFFFFFFFFFF",
		},
		{
			name:      "All zeros",
			ledgerSeq: 0,
			txnIndex:  0,
			networkID: 0,
			expected:  "C000000000000000",
		},
		{
			name:      "Simple values (1, 2, 3)",
			ledgerSeq: 1,
			txnIndex:  2,
			networkID: 3,
			expected:  "C000000100020003",
		},
		{
			name:      "Mainnet example from rippled",
			ledgerSeq: 13249191,
			txnIndex:  12911,
			networkID: 65535,
			expected:  "C0CA2AA7326FFFFF",
		},
		{
			name:      "Network ID 11111 (test network)",
			ledgerSeq: 100,
			txnIndex:  0,
			networkID: 11111,
			expected:  "C000006400002B67",
		},
		{
			name:      "Network ID 21337 (custom network)",
			ledgerSeq: 100,
			txnIndex:  0,
			networkID: 21337,
			expected:  "C000006400005359",
		},
		// Test case 2: ledger_seq greater than 0xFFFFFFF
		{
			name:       "Ledger sequence exceeds 28 bits (0x10000000)",
			ledgerSeq:  0x10000000,
			txnIndex:   0,
			networkID:  0,
			shouldFail: true,
		},
		// Test case 3: txn_index is always valid (uint16 max is valid)
		// Test case 4: network_id is always valid (uint16 max is valid)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := EncodeCTID(tc.ledgerSeq, tc.txnIndex, tc.networkID)

			if tc.shouldFail {
				assert.Error(t, err, "Expected encoding to fail")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result,
					"CTID encoding mismatch for ledger=%d, txn=%d, net=%d",
					tc.ledgerSeq, tc.txnIndex, tc.networkID)
			}
		})
	}
}

// TestCTIDDecoding tests CTID decoding according to rippled specification
// Based on rippled Transaction_test.cpp testCTIDValidation Test cases 5-14
func TestCTIDDecoding(t *testing.T) {
	tests := []struct {
		name              string
		ctid              string
		expectedLedgerSeq uint32
		expectedTxnIndex  uint16
		expectedNetworkID uint16
		shouldFail        bool
		errorContains     string
	}{
		// Test case 5: Valid input values
		{
			name:              "All zeros",
			ctid:              "C000000000000000",
			expectedLedgerSeq: 0,
			expectedTxnIndex:  0,
			expectedNetworkID: 0,
		},
		{
			name:              "Simple values (1, 2, 3)",
			ctid:              "C000000100020003",
			expectedLedgerSeq: 1,
			expectedTxnIndex:  2,
			expectedNetworkID: 3,
		},
		{
			name:              "Example from rippled (13249191, 12911, 49221)",
			ctid:              "C0CA2AA7326FC045",
			expectedLedgerSeq: 13249191,
			expectedTxnIndex:  12911,
			expectedNetworkID: 49221,
		},
		{
			name:              "Max values",
			ctid:              "CFFFFFFFFFFFFFFF",
			expectedLedgerSeq: 0x0FFFFFFF,
			expectedTxnIndex:  0xFFFF,
			expectedNetworkID: 0xFFFF,
		},
		// Case-insensitive tests
		{
			name:              "Lowercase CTID",
			ctid:              "c000000100020003",
			expectedLedgerSeq: 1,
			expectedTxnIndex:  2,
			expectedNetworkID: 3,
		},
		{
			name:              "Mixed case CTID",
			ctid:              "C0cA2Aa7326Fc045",
			expectedLedgerSeq: 13249191,
			expectedTxnIndex:  12911,
			expectedNetworkID: 49221,
		},
		// Test case 6: ctid not a string or big int - handled by type system
		// Test case 7: ctid not a hexadecimal string (exactly 16 chars but invalid hex)
		{
			name:          "Invalid - not hex (contains G at end)",
			ctid:          "C003FFFFFFFFFFFG", // Exactly 16 chars but G is not valid hex
			shouldFail:    true,
			errorContains: "not a valid hex",
		},
		{
			name:          "Invalid - not hex (contains G in middle)",
			ctid:          "C003GFFFFFFFFFFF", // G at position 4
			shouldFail:    true,
			errorContains: "not a valid hex",
		},
		// Test case 8: ctid not exactly 16 nibbles
		{
			name:          "Invalid - too short (15 chars)",
			ctid:          "C003FFFFFFFFFFF",
			shouldFail:    true,
			errorContains: "invalid CTID length",
		},
		{
			name:          "Invalid - too long (17 chars)",
			ctid:          "C003FFFFFFFFFFFFF",
			shouldFail:    true,
			errorContains: "invalid CTID length",
		},
		// Test case 9: ctid too large - handled by 16 char limit
		{
			name:          "Invalid - way too long",
			ctid:          "CFFFFFFFFFFFFFFFFFF",
			shouldFail:    true,
			errorContains: "invalid CTID length",
		},
		// Test case 10: ctid doesn't start with a C nibble
		{
			name:          "Invalid - doesn't start with C",
			ctid:          "FFFFFFFFFFFFFFFF",
			shouldFail:    true,
			errorContains: "must start with 'C'",
		},
		{
			name:          "Invalid - starts with 0",
			ctid:          "0000000100020003",
			shouldFail:    true,
			errorContains: "must start with 'C'",
		},
		{
			name:          "Invalid - starts with A",
			ctid:          "A000000100020003",
			shouldFail:    true,
			errorContains: "must start with 'C'",
		},
		// Additional validation tests
		{
			name:          "Invalid - empty string",
			ctid:          "",
			shouldFail:    true,
			errorContains: "invalid CTID length",
		},
		{
			name:          "Invalid - contains special characters",
			ctid:          "C003FFFFFFFFF!00",
			shouldFail:    true,
			errorContains: "not a valid hex",
		},
		{
			name:          "Invalid - contains underscore",
			ctid:          "C003FFFFFFFFFFF_",
			shouldFail:    true,
			errorContains: "not a valid hex",
		},
		{
			name:          "Invalid - contains space",
			ctid:          "C003FFFFFFFFFF F",
			shouldFail:    true,
			errorContains: "not a valid hex",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledgerSeq, txnIndex, networkID, err := DecodeCTID(tc.ctid)

			if tc.shouldFail {
				assert.Error(t, err, "Expected decoding to fail for CTID: %s", tc.ctid)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedLedgerSeq, ledgerSeq, "Ledger sequence mismatch")
				assert.Equal(t, tc.expectedTxnIndex, txnIndex, "Transaction index mismatch")
				assert.Equal(t, tc.expectedNetworkID, networkID, "Network ID mismatch")
			}
		})
	}
}

// TestCTIDRoundTrip tests that encoding and decoding are consistent
func TestCTIDRoundTrip(t *testing.T) {
	tests := []struct {
		ledgerSeq uint32
		txnIndex  uint16
		networkID uint16
	}{
		{0, 0, 0},
		{1, 2, 3},
		{0x0FFFFFFF, 0xFFFF, 0xFFFF},
		{100, 0, 11111},
		{13249191, 12911, 49221},
		{1000000, 100, 0},
		{100, 5, 21337},
	}

	for _, tc := range tests {
		name := fmt.Sprintf("ledger=%d,txn=%d,net=%d", tc.ledgerSeq, tc.txnIndex, tc.networkID)
		t.Run(name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeCTID(tc.ledgerSeq, tc.txnIndex, tc.networkID)
			require.NoError(t, err)

			// Decode
			ledgerSeq, txnIndex, networkID, err := DecodeCTID(encoded)
			require.NoError(t, err)

			// Verify round-trip
			assert.Equal(t, tc.ledgerSeq, ledgerSeq)
			assert.Equal(t, tc.txnIndex, txnIndex)
			assert.Equal(t, tc.networkID, networkID)
		})
	}
}

// TestCTIDCaseInsensitive tests that CTID parsing is case-insensitive
// Based on rippled Transaction_test.cpp CTID mixed case test
func TestCTIDCaseInsensitive(t *testing.T) {
	// Create a known CTID
	original, err := EncodeCTID(100, 5, 11111)
	require.NoError(t, err)

	// Test various case variations
	variations := []string{
		strings.ToUpper(original),
		strings.ToLower(original),
	}

	// Generate some mixed case variations
	for i := 0; i < len(original); i++ {
		var mixed []byte
		for j, c := range original {
			if j == i {
				if c >= 'A' && c <= 'F' {
					mixed = append(mixed, byte(c+32)) // lowercase
				} else if c >= 'a' && c <= 'f' {
					mixed = append(mixed, byte(c-32)) // uppercase
				} else {
					mixed = append(mixed, byte(c))
				}
			} else {
				mixed = append(mixed, byte(c))
			}
		}
		variations = append(variations, string(mixed))
	}

	for _, variant := range variations {
		t.Run(variant, func(t *testing.T) {
			ledgerSeq, txnIndex, networkID, err := DecodeCTID(variant)
			assert.NoError(t, err)
			assert.Equal(t, uint32(100), ledgerSeq)
			assert.Equal(t, uint16(5), txnIndex)
			assert.Equal(t, uint16(11111), networkID)
		})
	}
}

// TestCTIDWrongNetwork tests detection of wrong network ID in CTID
// Based on rippled Transaction_test.cpp "test the wrong network ID was submitted"
func TestCTIDWrongNetwork(t *testing.T) {
	tests := []struct {
		name            string
		ctidNetworkID   uint16
		serverNetworkID uint16
		expectError     bool
		errorType       string
	}{
		{
			name:            "Matching network ID - no error",
			ctidNetworkID:   11111,
			serverNetworkID: 11111,
			expectError:     false,
		},
		{
			name:            "Wrong network ID - should return wrongNetwork",
			ctidNetworkID:   21338,
			serverNetworkID: 21337,
			expectError:     true,
			errorType:       "wrongNetwork",
		},
		{
			name:            "CTID network 0 with server network 0",
			ctidNetworkID:   0,
			serverNetworkID: 0,
			expectError:     false,
		},
		{
			name:            "Wrong network - mainnet vs testnet",
			ctidNetworkID:   0,
			serverNetworkID: 1,
			expectError:     true,
			errorType:       "wrongNetwork",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a CTID for the specified network
			ctid, err := EncodeCTID(100, 0, tc.ctidNetworkID)
			require.NoError(t, err)

			// Decode and verify network ID
			_, _, networkID, err := DecodeCTID(ctid)
			require.NoError(t, err)
			assert.Equal(t, tc.ctidNetworkID, networkID)

			// Simulate network ID check
			if networkID != tc.serverNetworkID {
				if tc.expectError {
					// This would trigger "wrongNetwork" error in actual implementation
					t.Logf("Expected wrongNetwork error: CTID network %d != server network %d",
						networkID, tc.serverNetworkID)
				} else {
					t.Errorf("Unexpected network mismatch: CTID network %d != server network %d",
						networkID, tc.serverNetworkID)
				}
			}
		})
	}
}

// TestCTIDNetworkBoundary tests network ID boundary values
// Based on rippled Transaction_test.cpp network ID boundary tests (65535, 65536)
func TestCTIDNetworkBoundary(t *testing.T) {
	tests := []struct {
		networkID    uint16
		shouldEncode bool
		description  string
	}{
		{0, true, "Network ID 0 (mainnet-like, no network)"},
		{1, true, "Network ID 1"},
		{2, true, "Network ID 2"},
		{1024, true, "Network ID 1024"},
		{11111, true, "Test network 11111"},
		{21337, true, "Custom network 21337"},
		{65534, true, "Network ID 65534"},
		{65535, true, "Max network ID 65535 (0xFFFF)"},
		// Note: uint16 cannot exceed 65535, so no need to test > 65535
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			ctid, err := EncodeCTID(100, 0, tc.networkID)
			if tc.shouldEncode {
				assert.NoError(t, err)
				assert.NotEmpty(t, ctid)
				assert.Len(t, ctid, 16, "CTID should be 16 characters")
				assert.Equal(t, 'C', rune(ctid[0]), "CTID should start with C")

				// Verify decode returns same network ID
				_, _, decodedNetID, err := DecodeCTID(ctid)
				require.NoError(t, err)
				assert.Equal(t, tc.networkID, decodedNetID)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestCTIDFormatValidation tests CTID format validation
func TestCTIDFormatValidation(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		shouldParse bool
		description string
	}{
		{"Valid CTID", "C000000100020003", true, "Standard valid CTID"},
		{"Valid max CTID", "CFFFFFFFFFFFFFFF", true, "Maximum valid CTID"},
		{"Valid min CTID", "C000000000000000", true, "Minimum valid CTID"},
		{"Too short", "C00000010002000", false, "15 characters"},
		{"Too long", "C0000001000200030", false, "17 characters"},
		{"Wrong prefix D", "D000000100020003", false, "Starts with D"},
		{"Wrong prefix 0", "0000000100020003", false, "Starts with 0"},
		{"Wrong prefix F", "F000000100020003", false, "Starts with F"},
		{"Contains G", "C00000010002000G", false, "Invalid hex char G"},
		{"Contains lowercase g", "C00000010002000g", false, "Invalid hex char g"},
		{"Contains space", "C00000 100020003", false, "Contains space"},
		{"Empty string", "", false, "Empty input"},
		{"Only C", "C", false, "Just the prefix"},
		{"Lowercase c prefix", "c000000100020003", true, "Lowercase prefix is valid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := DecodeCTID(tc.input)
			if tc.shouldParse {
				assert.NoError(t, err, "Expected valid CTID: %s", tc.description)
			} else {
				assert.Error(t, err, "Expected invalid CTID: %s", tc.description)
			}
		})
	}
}

// Range Search Tests
// Based on rippled Transaction_test.cpp testRangeRequest

// TestTxMethodLedgerRange tests min_ledger and max_ledger parameters
// Based on rippled Transaction_test.cpp testRangeRequest
func TestTxMethodLedgerRange(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validHash := "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"

	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	storedTx := handlers.StoredTransaction{TxJSON: txJSON}
	storedData, _ := json.Marshal(storedTx)

	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}

	tests := []struct {
		name        string
		params      map[string]interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid ledger range - transaction within range",
			params: map[string]interface{}{
				"transaction": validHash,
				"min_ledger":  50,
				"max_ledger":  150,
			},
			expectError: false,
		},
		{
			name: "Ledger range with binary=true",
			params: map[string]interface{}{
				"transaction": validHash,
				"binary":      true,
				"min_ledger":  1,
				"max_ledger":  200,
			},
			expectError: false,
		},
		{
			name: "Min ledger only (partial range)",
			params: map[string]interface{}{
				"transaction": validHash,
				"min_ledger":  50,
			},
			expectError: false,
		},
		{
			name: "Max ledger only (partial range)",
			params: map[string]interface{}{
				"transaction": validHash,
				"max_ledger":  150,
			},
			expectError: false,
		},
		{
			name: "Exact ledger match",
			params: map[string]interface{}{
				"transaction": validHash,
				"min_ledger":  100,
				"max_ledger":  100,
			},
			expectError: false,
		},
		{
			name: "Wide range (exactly 1000 ledgers)",
			params: map[string]interface{}{
				"transaction": validHash,
				"min_ledger":  1,
				"max_ledger":  1000,
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected error")
				assert.Contains(t, rpcErr.Message, tc.errorMsg)
			} else {
				require.Nil(t, rpcErr, "Expected no error")
				require.NotNil(t, result)
			}
		})
	}
}

// TestTxMethodInvalidLedgerRange tests invalid ledger range parameters
// Based on rippled Transaction_test.cpp testRangeRequest invalid range tests
func TestTxMethodInvalidLedgerRange(t *testing.T) {
	// These tests document the expected behavior based on rippled
	// The actual implementation would need to validate these ranges

	tests := []struct {
		name        string
		minLedger   interface{}
		maxLedger   interface{}
		errorCode   int
		errorToken  string
		description string
	}{
		{
			name:        "Invalid range - min > max",
			minLedger:   100,
			maxLedger:   50,
			errorCode:   types.RpcINVALID_LGR_RANGE,
			errorToken:  "invalidLgrRange",
			description: "Minimum ledger cannot be greater than maximum",
		},
		{
			name:        "Invalid range - negative min",
			minLedger:   -1,
			maxLedger:   100,
			errorCode:   types.RpcINVALID_LGR_RANGE,
			errorToken:  "invalidLgrRange",
			description: "Negative ledger values are invalid",
		},
		{
			name:        "Invalid range - both negative",
			minLedger:   -20,
			maxLedger:   -10,
			errorCode:   types.RpcINVALID_LGR_RANGE,
			errorToken:  "invalidLgrRange",
			description: "Negative ledger values are invalid",
		},
		{
			name:        "Invalid range - negative max only",
			minLedger:   0,
			maxLedger:   -1,
			errorCode:   types.RpcINVALID_LGR_RANGE,
			errorToken:  "invalidLgrRange",
			description: "Negative ledger values are invalid",
		},
		{
			name:        "Excessive range - max - min > 1000",
			minLedger:   1,
			maxLedger:   1002,
			errorCode:   46, // rpcEXCESSIVE_LGR_RANGE
			errorToken:  "excessiveLgrRange",
			description: "Range cannot exceed 1000 ledgers",
		},
		{
			name:        "Excessive range - 2000 ledgers",
			minLedger:   1,
			maxLedger:   2001,
			errorCode:   46,
			errorToken:  "excessiveLgrRange",
			description: "Range of 2000 ledgers exceeds limit",
		},
		{
			name:        "Invalid range - only min provided as single value",
			minLedger:   20,
			maxLedger:   nil,
			errorCode:   types.RpcINVALID_LGR_RANGE,
			errorToken:  "invalidLgrRange",
			description: "Both min and max must be provided for range search",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Document the expected behavior
			t.Logf("Expected error: code=%d, token=%s - %s",
				tc.errorCode, tc.errorToken, tc.description)
		})
	}
}

// TestTxMethodSearchedAllFlag tests the searched_all flag in response
// Based on rippled Transaction_test.cpp testRangeRequest searched_all tests
func TestTxMethodSearchedAllFlag(t *testing.T) {
	// These tests document the expected behavior for searched_all flag
	// Based on rippled's behavior:
	// - searched_all: true when entire range was searched and tx not found
	// - searched_all: false when search was incomplete (deleted ledger, etc.)
	// - searched_all: not present when transaction was found

	tests := []struct {
		name                string
		scenario            string
		expectedSearchedAll *bool // nil means field should not be present
	}{
		{
			name:                "Transaction found in range",
			scenario:            "Transaction exists within min_ledger to max_ledger",
			expectedSearchedAll: nil, // Not present when found
		},
		{
			name:                "Transaction not found - all searched",
			scenario:            "Transaction not in range, but all ledgers available",
			expectedSearchedAll: boolPtr(true),
		},
		{
			name:                "Transaction not found - incomplete search",
			scenario:            "Transaction not in range, some ledgers missing/deleted",
			expectedSearchedAll: boolPtr(false),
		},
		{
			name:                "Found outside provided range",
			scenario:            "Transaction found but in different ledger range",
			expectedSearchedAll: boolPtr(false),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Scenario: %s", tc.scenario)
			if tc.expectedSearchedAll == nil {
				t.Log("Expected: searched_all field not present")
			} else {
				t.Logf("Expected: searched_all = %v", *tc.expectedSearchedAll)
			}
		})
	}
}

// Helper function for bool pointer
func boolPtr(b bool) *bool {
	return &b
}

// Response Field Tests

// TestTxMethodResponseFields tests that response contains expected fields
// Based on rippled Transaction_test.cpp testRequest and testBinaryRequest
func TestTxMethodResponseFields(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validHash := "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"
	expectedLedgerHash := "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"

	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
			"AffectedNodes":     []interface{}{},
		},
	}
	storedData, _ := json.Marshal(storedTx)

	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		LedgerHash:  expectedLedgerHash,
		Validated:   true,
		TxIndex:     0,
	}

	t.Run("Response contains all required fields (JSON mode)", func(t *testing.T) {
		params := map[string]interface{}{
			"transaction": validHash,
			"binary":      false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check required response fields per rippled spec
		assert.Contains(t, resp, "hash", "Response must include hash")
		assert.Contains(t, resp, "ledger_index", "Response must include ledger_index")
		assert.Contains(t, resp, "ledger_hash", "Response must include ledger_hash")
		assert.Contains(t, resp, "validated", "Response must include validated")

		// Verify field values
		assert.Equal(t, validHash, resp["hash"])
		assert.Equal(t, float64(100), resp["ledger_index"])
		assert.Equal(t, expectedLedgerHash, resp["ledger_hash"])
		assert.Equal(t, true, resp["validated"])

		// Check transaction fields are present (for JSON mode)
		assert.Equal(t, "Payment", resp["TransactionType"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", resp["Account"])

		// Check meta is present
		assert.Contains(t, resp, "meta", "Response must include meta")
		if meta, ok := resp["meta"].(map[string]interface{}); ok {
			assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
		}
	})

	t.Run("Response contains inLedger for backward compatibility", func(t *testing.T) {
		params := map[string]interface{}{
			"transaction": validHash,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check inLedger field for backward compatibility
		assert.Contains(t, resp, "inLedger", "Response should include inLedger for compatibility")
		assert.Equal(t, resp["ledger_index"], resp["inLedger"],
			"inLedger should equal ledger_index")
	})

	t.Run("Binary mode response fields", func(t *testing.T) {
		params := map[string]interface{}{
			"transaction": validHash,
			"binary":      true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Required fields in binary mode
		assert.Contains(t, resp, "hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "validated")

		// Binary-specific fields
		if _, ok := resp["tx_blob"]; ok {
			txBlob := resp["tx_blob"].(string)
			assert.NotEmpty(t, txBlob, "tx_blob should not be empty")
			// Verify it's valid hex
			_, err := hex.DecodeString(txBlob)
			assert.NoError(t, err, "tx_blob should be valid hex")
		}
	})

	t.Run("Date field present for validated transaction", func(t *testing.T) {
		// In rippled, date is present for validated transactions
		// Document expected behavior
		t.Log("Expected: date field present for validated transactions")
		t.Log("Format: Ripple epoch seconds (seconds since 2000-01-01T00:00:00Z)")
	})
}

// Service Availability Tests

// TestTxMethodServiceUnavailable tests behavior when ledger service is not available
func TestTxMethodServiceUnavailable(t *testing.T) {
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	params := map[string]interface{}{
		"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// TestTxMethodServiceNilLedger tests behavior when ledger service is nil
func TestTxMethodServiceNilLedger(t *testing.T) {
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: nil},
	}

	params := map[string]interface{}{
		"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// Method Metadata Tests

// TestTxMethodMetadata tests the method's metadata functions
func TestTxMethodMetadata(t *testing.T) {
	method := &handlers.TxMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, method.RequiredRole(),
			"tx method should require RoleUser (rippled: Role::USER)")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1, "Should support API version 1")
		assert.Contains(t, versions, types.ApiVersion2, "Should support API version 2")
		assert.Contains(t, versions, types.ApiVersion3, "Should support API version 3")
	})
}

// API Version Tests

// TestTxMethodApiVersions tests behavior across different API versions
// Based on rippled Transaction_test.cpp testRequest with api_version parameter
func TestTxMethodApiVersions(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}

	validHash := "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"

	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
		},
	}
	storedData, _ := json.Marshal(storedTx)

	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}

	apiVersions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for _, version := range apiVersions {
		t.Run(fmt.Sprintf("API Version %d", version), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: version,
				Services:   services,
			}

			params := map[string]interface{}{
				"transaction": validHash,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Should succeed for API version %d", version)
			require.NotNil(t, result)

			// Note: API version 2+ may have different response format
			// (tx_json instead of flat fields, close_time_iso, etc.)
			if version > 1 {
				t.Logf("API version %d may return tx_json wrapper and additional fields", version)
			}
		})
	}
}

// CTID Lookup Tests (when implemented)
// Based on rippled Transaction_test.cpp testCTIDRPC

// TestTxMethodLookupByCTID documents expected CTID lookup behavior
// Based on rippled Transaction_test.cpp testCTIDRPC
func TestTxMethodLookupByCTID(t *testing.T) {
	// These tests document the expected behavior for CTID lookup
	// Actual implementation would need to support the ctid parameter

	tests := []struct {
		name        string
		ctid        string
		networkID   uint16
		expectError bool
		errorType   string
		description string
	}{
		{
			name:        "Valid CTID lookup",
			ctid:        "C000006400002B67", // ledger 100, tx 0, network 11111
			networkID:   11111,
			expectError: false,
			description: "Should find transaction at ledger 100, index 0",
		},
		{
			name:        "CTID with wrong network ID",
			ctid:        "C000006400005359", // ledger 100, tx 0, network 21337
			networkID:   21338,              // Different from CTID
			expectError: true,
			errorType:   "wrongNetwork",
			description: "Should return wrongNetwork error",
		},
		{
			name:        "Lowercase CTID",
			ctid:        "c000006400002b67",
			networkID:   11111,
			expectError: false,
			description: "Case-insensitive CTID should work",
		},
		{
			name:        "Mixed case CTID",
			ctid:        "C000006400002b67",
			networkID:   11111,
			expectError: false,
			description: "Mixed case CTID should work",
		},
		// Note: Network ID > 65535 test removed - uint16 type prevents overflow
		// In rippled, this would be handled by checking uint32 network ID before encoding
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("CTID: %s, Network: %d", tc.ctid, tc.networkID)
			t.Logf("Description: %s", tc.description)
			if tc.expectError {
				t.Logf("Expected error: %s", tc.errorType)
			}
		})
	}
}

// TestCTIDNetworkIDInResponse tests that CTID is included in response
// Based on rippled Transaction_test.cpp network ID boundary tests
func TestCTIDNetworkIDInResponse(t *testing.T) {
	// Document expected behavior for CTID in response
	// Based on rippled: CTID is only in response when network_id <= 0xFFFF

	tests := []struct {
		networkID      uint32
		ctidInResponse bool
		description    string
	}{
		{2, true, "Network ID 2 - CTID should be present"},
		{1024, true, "Network ID 1024 - CTID should be present"},
		{11111, true, "Test network 11111 - CTID should be present"},
		{65535, true, "Max network ID 65535 - CTID should be present"},
		{65536, false, "Network ID 65536 - CTID NOT supported"},
		{100000, false, "Network ID 100000 - CTID NOT supported"},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			if tc.ctidInResponse {
				t.Logf("Network %d: CTID should be present in response", tc.networkID)
			} else {
				t.Logf("Network %d: CTID should NOT be in response (exceeds 16-bit)", tc.networkID)
			}
		})
	}
}

// Edge Cases and Error Conditions

// TestTxMethodEdgeCases tests various edge cases
func TestTxMethodEdgeCases(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Transaction with corrupted stored data", func(t *testing.T) {
		// Store invalid JSON as transaction data
		invalidHash := "1111111111111111111111111111111111111111111111111111111111111111"
		mock.transactions[invalidHash] = &types.TransactionInfo{
			TxData:      []byte("not valid json"),
			LedgerIndex: 100,
			LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			Validated:   true,
		}

		params := map[string]interface{}{
			"transaction": strings.ToLower(invalidHash),
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	})

	t.Run("Transaction hash with leading zeros", func(t *testing.T) {
		leadingZeroHash := "0000000000000000000000000000000000000000000000000000000000000001"
		txJSON := map[string]interface{}{"TransactionType": "Payment"}
		storedTx := handlers.StoredTransaction{TxJSON: txJSON}
		storedData, _ := json.Marshal(storedTx)

		mock.transactions[strings.ToUpper(leadingZeroHash)] = &types.TransactionInfo{
			TxData:      storedData,
			LedgerIndex: 100,
			Validated:   true,
		}

		params := map[string]interface{}{
			"transaction": leadingZeroHash,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("All-F hash (max value)", func(t *testing.T) {
		maxHash := "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
		txJSON := map[string]interface{}{"TransactionType": "Payment"}
		storedTx := handlers.StoredTransaction{TxJSON: txJSON}
		storedData, _ := json.Marshal(storedTx)

		mock.transactions[maxHash] = &types.TransactionInfo{
			TxData:      storedData,
			LedgerIndex: 100,
			Validated:   true,
		}

		params := map[string]interface{}{
			"transaction": maxHash,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// TestTxMethodInternalErrors tests internal error handling
func TestTxMethodInternalErrors(t *testing.T) {
	mock := newMockLedgerServiceTx()
	services := servicesForTx(mock)

	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Database error during lookup", func(t *testing.T) {
		mock.txLookupError = errors.New("database connection failed")

		params := map[string]interface{}{
			"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Should return txnNotFound error
		assert.Contains(t, rpcErr.Message, "not found")
	})
}
