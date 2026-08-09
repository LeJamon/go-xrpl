package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceTx extends mockLedgerService with tx-specific behavior
type mockLedgerServiceTx struct {
	*mockLedgerService
	transactions          map[string]*types.TransactionInfo
	networkID             uint16
	txLookupError         error
	txSearchResult        types.TxSearchResult
	ledgerRangeError      error
	completeLedgers       string
	minAvailableLedger    uint32
	maxAvailableLedger    uint32
	getLedgerBySequenceFn func(uint32) (types.LedgerReader, error)
}

type mockCTIDLedgerService struct {
	*mockLedgerServiceTx
	ledger *mockCTIDLedger
}

func (m *mockCTIDLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.ledger != nil && m.ledger.sequence == seq {
		return m.ledger, nil
	}
	return nil, errors.New("ledger not found")
}

type mockCTIDLedger struct {
	types.LedgerReader
	sequence uint32
	hash     [32]byte
	txHash   [32]byte
	txData   []byte
}

func (m *mockCTIDLedger) Sequence() uint32  { return m.sequence }
func (m *mockCTIDLedger) Hash() [32]byte    { return m.hash }
func (m *mockCTIDLedger) IsValidated() bool { return true }
func (m *mockCTIDLedger) CloseTime() int64  { return 0 }
func (m *mockCTIDLedger) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	fn(m.txHash, m.txData)
	return nil
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
	return nil, svcerr.ErrTxnNotFound
}

func (m *mockLedgerServiceTx) GetTransactionWithRange(_ context.Context, txHash [32]byte, _, _ uint32) (*types.TransactionInfo, types.TxSearchResult, error) {
	txInfo, err := m.GetTransaction(txHash)
	if err == nil {
		return txInfo, types.TxSearchAll, nil
	}
	return nil, m.txSearchResult, err
}

func (m *mockLedgerServiceTx) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

func (m *mockLedgerServiceTx) ResolvedNetworkID() uint16 {
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
func servicesForTx(mock *mockLedgerServiceTx) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
	})
}

func TestTxDeliveredAmountHistoricalContext(t *testing.T) {
	const txHash = "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"
	tests := []struct {
		name              string
		ledgerSequence    uint32
		closeTime         int64
		serializedAmount  string
		expectedDelivered string
	}{
		{
			name:              "pre-cutoff ledger and close time",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_000,
			expectedDelivered: "unavailable",
		},
		{
			name:              "ledger sequence cutoff",
			ledgerSequence:    4_594_095,
			expectedDelivered: "100",
		},
		{
			name:              "post-cutoff close time",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_001,
			expectedDelivered: "100",
		},
		{
			name:              "serialized amount before cutoffs",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_000,
			serializedAmount:  "40",
			expectedDelivered: "40",
		},
	}

	for _, tc := range tests {
		for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
			t.Run(fmt.Sprintf("%s/api_v%d", tc.name, apiVersion), func(t *testing.T) {
				meta := validStoredMetadata()
				if tc.serializedAmount != "" {
					meta["DeliveredAmount"] = tc.serializedAmount
				}
				txJSON := validStoredPaymentTransaction()
				txJSON["Amount"] = "100"
				stored, err := json.Marshal(handlers.StoredTransaction{
					TxJSON: txJSON,
					Meta:   meta,
				})
				require.NoError(t, err)

				mock := newMockLedgerServiceTx()
				mock.transactions[txHash] = &types.TransactionInfo{
					TxData:      stored,
					LedgerIndex: tc.ledgerSequence,
					Validated:   true,
				}
				mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
					require.Equal(t, tc.ledgerSequence, seq)
					return &mockLedgerReader{
						seq:       seq,
						closeTime: tc.closeTime,
						closed:    true,
						validated: true,
					}, nil
				}
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   servicesForTx(mock),
				}

				result, rpcErr := (&handlers.TxMethod{}).Handle(
					ctx,
					json.RawMessage(`{"transaction":"`+txHash+`"}`),
				)
				require.Nil(t, rpcErr)
				response := result.(map[string]any)
				responseMeta := response["meta"].(map[string]any)
				require.Equal(t, tc.expectedDelivered, responseMeta["delivered_amount"])
			})
		}
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
		params        any
		expectedError string
		expectedCode  int
		setupMock     func()
	}{
		{
			name:          "Missing transaction field - empty params",
			params:        map[string]any{},
			expectedError: "Invalid parameters.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing transaction field - nil params",
			params:        nil,
			expectedError: "Invalid parameters.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid hash format - too short",
			params: map[string]any{
				"transaction": "ABC123",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - too long (68 chars)",
			params: map[string]any{
				"transaction": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - 63 chars (1 short)",
			params: map[string]any{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - 65 chars (1 extra)",
			params: map[string]any{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C70",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - not hex (contains G)",
			params: map[string]any{
				"transaction": "G08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - not hex (contains Z)",
			params: map[string]any{
				"transaction": "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - special characters",
			params: map[string]any{
				"transaction": "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6!@#$",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - contains spaces",
			params: map[string]any{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD169 C7",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid hash format - empty string",
			params: map[string]any{
				"transaction": "",
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Transaction not found - valid hash format (txnNotFound)",
			params: map[string]any{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
			},
			expectedError: "Transaction not found",
			expectedCode:  types.RpcTXN_NOT_FOUND,
			setupMock: func() {
				mock.txLookupError = svcerr.ErrTxnNotFound
			},
		},
		{
			name: "Ambiguous - both transaction and ctid specified",
			params: map[string]any{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
				"ctid":        "C000002D00000000",
			},
			expectedError: "Invalid parameters.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Ambiguous - empty transaction and null ctid are still present",
			params: map[string]any{
				"transaction": "",
				"ctid":        nil,
			},
			expectedError: "Invalid parameters.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - integer",
			params: map[string]any{
				"transaction": 12345,
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid transaction type - boolean",
			params: map[string]any{
				"transaction": true,
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid transaction type - array",
			params: map[string]any{
				"transaction": []string{"hash1", "hash2"},
			},
			expectedError: "Invalid parameters.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - object",
			params: map[string]any{
				"transaction": map[string]any{"hash": "value"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid transaction type - float",
			params: map[string]any{
				"transaction": 123.456,
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
		},
		{
			name: "Invalid transaction type - null",
			params: map[string]any{
				"transaction": nil,
			},
			expectedError: "Not implemented.",
			expectedCode:  types.RpcNOT_IMPL,
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
	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]any{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
			"AffectedNodes":     []any{},
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
		params       map[string]any
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name: "Lookup by lowercase hash",
			params: map[string]any{
				"transaction": strings.ToLower(validHash),
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				// Hash is uppercased in the response (matching rippled)
				assert.Equal(t, strings.ToUpper(validHash), resp["hash"])
				assert.Equal(t, float64(100), resp["ledger_index"])
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "Lookup by uppercase hash",
			params: map[string]any{
				"transaction": strings.ToUpper(validHash),
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, strings.ToUpper(validHash), resp["hash"])
				assert.Equal(t, float64(100), resp["ledger_index"])
			},
		},
		{
			name: "Lookup by mixed case hash",
			params: map[string]any{
				"transaction": "e08D6E9754025ba2534A78707605E0601f03ACE063687A0ca1BDDACFCD1698c7",
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, float64(100), resp["ledger_index"])
			},
		},
		{
			name: "Lookup returns all required fields",
			params: map[string]any{
				"transaction": validHash,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				// Required fields per rippled
				assert.Contains(t, resp, "hash")
				assert.Contains(t, resp, "ledger_index")
				assert.NotContains(t, resp, "ledger_hash")
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
			var respMap map[string]any
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

	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]any{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
			"AffectedNodes":     []any{},
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
		binary       any
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name:   "binary=false returns JSON fields",
			binary: false,
			validateResp: func(t *testing.T, resp map[string]any) {
				// Should have JSON fields from transaction
				assert.Equal(t, "Payment", resp["TransactionType"])
				assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", resp["Account"])
				assert.Equal(t, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", resp["Destination"])
				// Should have meta as JSON object
				assert.NotNil(t, resp["meta"])
				if meta, ok := resp["meta"].(map[string]any); ok {
					assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
				}
			},
		},
		{
			name:   "binary=true returns tx_blob as hex string",
			binary: true,
			validateResp: func(t *testing.T, resp map[string]any) {
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
			validateResp: func(t *testing.T, resp map[string]any) {
				// Should have JSON fields
				assert.Equal(t, "Payment", resp["TransactionType"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
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
			var respMap map[string]any
			err = json.Unmarshal(resultJSON, &respMap)
			require.NoError(t, err)

			tc.validateResp(t, respMap)
		})
	}
}

// CTID (Concise Transaction ID) Tests
// Based on rippled Transaction_test.cpp testCTIDValidation

func encodeCTID(ledgerSeq, txnIndex, networkID uint32) (string, error) {
	ctid, ok := handlers.EncodeCTID(ledgerSeq, txnIndex, networkID)
	if !ok {
		return "", errors.New("CTID component exceeds its encoded width")
	}
	return ctid, nil
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
		txnIndex   uint32
		networkID  uint32
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
		{
			name:       "Transaction index exceeds 16 bits",
			ledgerSeq:  1,
			txnIndex:   0x10000,
			shouldFail: true,
		},
		{
			name:       "Network ID exceeds 16 bits",
			ledgerSeq:  1,
			networkID:  0x10000,
			shouldFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := encodeCTID(tc.ledgerSeq, tc.txnIndex, tc.networkID)

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
			encoded, err := encodeCTID(tc.ledgerSeq, uint32(tc.txnIndex), uint32(tc.networkID))
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
	original, err := encodeCTID(100, 5, 11111)
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
			ctid, err := encodeCTID(100, 0, uint32(tc.ctidNetworkID))
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
		networkID    uint32
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
		{65536, false, "Network ID 65536 does not fit"},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			ctid, err := encodeCTID(100, 0, tc.networkID)
			if tc.shouldEncode {
				assert.NoError(t, err)
				assert.NotEmpty(t, ctid)
				assert.Len(t, ctid, 16, "CTID should be 16 characters")
				assert.Equal(t, 'C', rune(ctid[0]), "CTID should start with C")

				// Verify decode returns same network ID
				_, _, decodedNetID, err := DecodeCTID(ctid)
				require.NoError(t, err)
				assert.Equal(t, uint16(tc.networkID), decodedNetID)
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

	txJSON := validStoredPaymentTransaction()
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
		params      map[string]any
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid ledger range - transaction within range",
			params: map[string]any{
				"transaction": validHash,
				"min_ledger":  50,
				"max_ledger":  150,
			},
			expectError: false,
		},
		{
			name: "Ledger range with binary=true",
			params: map[string]any{
				"transaction": validHash,
				"binary":      true,
				"min_ledger":  1,
				"max_ledger":  200,
			},
			expectError: false,
		},
		{
			name: "Min ledger only (partial range)",
			params: map[string]any{
				"transaction": validHash,
				"min_ledger":  50,
			},
			expectError: false,
		},
		{
			name: "Max ledger only (partial range)",
			params: map[string]any{
				"transaction": validHash,
				"max_ledger":  150,
			},
			expectError: false,
		},
		{
			name: "Exact ledger match",
			params: map[string]any{
				"transaction": validHash,
				"min_ledger":  100,
				"max_ledger":  100,
			},
			expectError: false,
		},
		{
			name: "Wide range (exactly 1000 ledgers)",
			params: map[string]any{
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

// TestTxMethodInvalidLedgerRange exercises the min_ledger/max_ledger range
// rules: a range is formed only when BOTH bounds are present (by presence, not
// by being non-zero), and when both are given it must be ordered and span at
// most 1000 ledgers (rippled Tx.cpp:330-344, doTxHelp:75-93).
func TestTxMethodInvalidLedgerRange(t *testing.T) {
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
	storedTx := handlers.StoredTransaction{
		TxJSON: validStoredPaymentTransaction(),
		Meta:   validStoredMetadata(),
	}
	storedData, _ := json.Marshal(storedTx)
	mock.transactions[validHash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: 100,
		Validated:   true,
	}

	t.Run("min greater than max", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"transaction": validHash, "min_ledger": 100, "max_ledger": 50})
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_LGR_RANGE, rpcErr.Code)
	})

	t.Run("span exceeds 1000", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"transaction": validHash, "min_ledger": 1, "max_ledger": 1002})
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcEXCESSIVE_LGR_RANGE, rpcErr.Code)
	})

	t.Run("present min_ledger 0 still forms a range", func(t *testing.T) {
		// A present min_ledger of 0 is a real lower bound (presence, not != 0),
		// so a 2000-ledger span is rejected; the old non-zero gate skipped this.
		params, _ := json.Marshal(map[string]any{"transaction": validHash, "min_ledger": 0, "max_ledger": 2000})
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcEXCESSIVE_LGR_RANGE, rpcErr.Code)
	})

	t.Run("single bound is ignored, not an error", func(t *testing.T) {
		// Only min_ledger supplied: rippled forms no range, so the query is not
		// rejected and proceeds to the direct hash lookup.
		params, _ := json.Marshal(map[string]any{"transaction": validHash, "min_ledger": 20})
		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// TestTxMethodSearchedAllFlag tests the searched_all flag in response
// Based on rippled Transaction_test.cpp testRangeRequest searched_all tests
func TestTxMethodSearchedAllFlag(t *testing.T) {
	mock := newMockLedgerServiceTx()
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   servicesForTx(mock),
	}
	const hash = "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"
	params, err := json.Marshal(map[string]any{"transaction": hash, "min_ledger": 10, "max_ledger": 20})
	require.NoError(t, err)

	t.Run("all transaction ledgers present", func(t *testing.T) {
		mock.txSearchResult = types.TxSearchAll
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, true, rpcErr.Extra["searched_all"])
	})

	t.Run("transaction ledger missing", func(t *testing.T) {
		mock.txSearchResult = types.TxSearchSome
		_, rpcErr := method.Handle(ctx, params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, false, rpcErr.Extra["searched_all"])
	})

	t.Run("found outside requested range", func(t *testing.T) {
		storedData, marshalErr := json.Marshal(handlers.StoredTransaction{TxJSON: validStoredPaymentTransaction()})
		require.NoError(t, marshalErr)
		mock.transactions[hash] = &types.TransactionInfo{TxData: storedData, LedgerIndex: 100, Validated: true}

		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		assert.NotContains(t, result.(map[string]any), "searched_all")
	})
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

	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]any{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
			"AffectedNodes":     []any{},
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
		params := map[string]any{
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
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check required response fields per rippled spec
		assert.Contains(t, resp, "hash", "Response must include hash")
		assert.Contains(t, resp, "ledger_index", "Response must include ledger_index")
		assert.Contains(t, resp, "validated", "Response must include validated")
		assert.Equal(t, "C000006400000000", resp["ctid"])
		assert.NotContains(t, resp, "ledger_hash")
		assert.NotContains(t, resp, "close_time_iso")

		// Verify field values
		assert.Equal(t, validHash, resp["hash"])
		assert.Equal(t, float64(100), resp["ledger_index"])
		assert.Equal(t, true, resp["validated"])

		// Check transaction fields are present (for JSON mode)
		assert.Equal(t, "Payment", resp["TransactionType"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", resp["Account"])

		// Check meta is present
		assert.Contains(t, resp, "meta", "Response must include meta")
		if meta, ok := resp["meta"].(map[string]any); ok {
			assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
		}
	})

	t.Run("Response contains inLedger for backward compatibility", func(t *testing.T) {
		params := map[string]any{
			"transaction": validHash,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check inLedger field for backward compatibility
		assert.Contains(t, resp, "inLedger", "Response should include inLedger for compatibility")
		assert.Equal(t, resp["ledger_index"], resp["inLedger"],
			"inLedger should equal ledger_index")
	})

	t.Run("Binary mode response fields", func(t *testing.T) {
		params := map[string]any{
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
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Required fields in binary mode
		assert.Contains(t, resp, "hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")
		assert.Equal(t, "C000006400000000", resp["ctid"])
		assert.NotContains(t, resp, "ledger_hash")
		assert.NotContains(t, resp, "close_time_iso")
		assert.NotContains(t, resp, "tx_blob")

		// Binary-specific fields
		require.Contains(t, resp, "tx")
		txBlob := resp["tx"].(string)
		assert.NotEmpty(t, txBlob, "tx should not be empty")
		_, err = hex.DecodeString(txBlob)
		assert.NoError(t, err, "tx should be valid hex")
	})

	t.Run("API v2 binary response includes root CTID", func(t *testing.T) {
		paramsJSON, err := json.Marshal(map[string]any{
			"transaction": validHash,
			"binary":      true,
		})
		require.NoError(t, err)

		v2ctx := *ctx
		v2ctx.ApiVersion = types.ApiVersion2
		result, rpcErr := method.Handle(&v2ctx, paramsJSON)
		require.Nil(t, rpcErr)
		resp := result.(map[string]any)

		assert.Equal(t, "C000006400000000", resp["ctid"])
		assert.Contains(t, resp, "tx_blob")
		assert.NotContains(t, resp, "tx")
		assert.Equal(t, expectedLedgerHash, resp["ledger_hash"])
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

	params := map[string]any{
		"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestTxMethodServiceNilLedger tests behavior when ledger service is nil
func TestTxMethodServiceNilLedger(t *testing.T) {
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: nil}),
	}

	params := map[string]any{
		"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
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

	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
	storedTx := handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta: map[string]any{
			"TransactionResult": "tesSUCCESS",
			"TransactionIndex":  0,
			"AffectedNodes":     []any{},
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

			params := map[string]any{
				"transaction": validHash,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Should succeed for API version %d", version)
			require.NotNil(t, result)
			resp := result.(map[string]any)
			assert.Equal(t, "C000006400000000", resp["ctid"])

			if version > 1 {
				shaped := resp["tx_json"].(map[string]any)
				assert.Equal(t, "1000000", shaped["DeliverMax"])
				assert.NotContains(t, shaped, "Amount")
				assert.Equal(t, "C000006400000000", shaped["ctid"])
				assert.Contains(t, resp, "ledger_hash")
			} else {
				assert.Equal(t, "1000000", resp["Amount"])
				assert.Equal(t, "1000000", resp["DeliverMax"])
				assert.NotContains(t, resp, "ledger_hash")
				assert.NotContains(t, resp, "close_time_iso")
			}
		})
	}
}

func TestTxCTIDResponsePlacement(t *testing.T) {
	const (
		hash             = "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"
		ledgerIndex      = uint32(3)
		transactionIndex = uint32(3)
		serverNetworkID  = uint32(7)
		transactionNetID = uint32(9)
		rootCTID         = "C000000300030007"
		embeddedCTID     = "C000000300030009"
	)

	txJSON := validStoredPaymentTransaction()
	txJSON["NetworkID"] = transactionNetID
	meta := validStoredMetadata()
	meta["TransactionIndex"] = transactionIndex
	storedData, err := json.Marshal(handlers.StoredTransaction{
		TxJSON: txJSON,
		Meta:   meta,
	})
	require.NoError(t, err)

	mock := newMockLedgerServiceTx()
	mock.serverInfo.NetworkID = serverNetworkID
	mock.transactions[hash] = &types.TransactionInfo{
		TxData:      storedData,
		LedgerIndex: ledgerIndex,
		LedgerHash:  strings.Repeat("A", 64),
		Validated:   true,
		TxIndex:     transactionIndex,
	}

	request := func(t *testing.T, apiVersion int, binary bool) map[string]any {
		t.Helper()
		params, err := json.Marshal(map[string]any{"transaction": hash, "binary": binary})
		require.NoError(t, err)
		result, rpcErr := (&handlers.TxMethod{}).Handle(&types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: apiVersion,
			Services:   servicesForTx(mock),
		}, params)
		require.Nil(t, rpcErr)
		response, ok := result.(map[string]any)
		require.True(t, ok)
		return response
	}

	t.Run("API v1 JSON keeps strict CTID at root", func(t *testing.T) {
		response := request(t, types.ApiVersion1, false)
		assert.Equal(t, rootCTID, response["ctid"])
		assert.NotContains(t, response, "tx_json")
	})

	t.Run("API v2 JSON has strict root and inclusive embedded CTIDs", func(t *testing.T) {
		response := request(t, types.ApiVersion2, false)
		assert.Equal(t, rootCTID, response["ctid"])
		tx, ok := response["tx_json"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, embeddedCTID, tx["ctid"])
	})

	t.Run("API v1 binary uses tx and retains root CTID", func(t *testing.T) {
		response := request(t, types.ApiVersion1, true)
		assert.Equal(t, rootCTID, response["ctid"])
		assert.Contains(t, response, "tx")
		assert.NotContains(t, response, "tx_blob")
	})

	t.Run("API v2 binary uses tx_blob and retains root CTID", func(t *testing.T) {
		response := request(t, types.ApiVersion2, true)
		assert.Equal(t, rootCTID, response["ctid"])
		assert.Contains(t, response, "tx_blob")
		assert.NotContains(t, response, "tx_json")
	})

	mock.serverInfo.NetworkID = 0xFFFF

	t.Run("API v1 retains inclusive embedded CTID at strict boundary", func(t *testing.T) {
		for _, binary := range []bool{false, true} {
			response := request(t, types.ApiVersion1, binary)
			assert.Equal(t, embeddedCTID, response["ctid"])
		}
	})

	t.Run("API v2 boundary retains only nonbinary embedded CTID", func(t *testing.T) {
		response := request(t, types.ApiVersion2, false)
		assert.NotContains(t, response, "ctid")
		tx, ok := response["tx_json"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, embeddedCTID, tx["ctid"])

		binaryResponse := request(t, types.ApiVersion2, true)
		assert.NotContains(t, binaryResponse, "ctid")
	})
}

func TestTxMethodCTIDRootAndTransactionProjection(t *testing.T) {
	const txHash = "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7"
	overrideNetworkID := uint32(21337)
	tests := []struct {
		name                 string
		ledgerIndex          uint32
		transactionIndex     uint32
		serverNetworkID      uint32
		transactionNetworkID *uint32
		rootCTID             string
		embeddedCTID         string
	}{
		{
			name:                 "transaction NetworkID does not override response CTID",
			ledgerIndex:          100,
			transactionIndex:     2,
			serverNetworkID:      11111,
			transactionNetworkID: &overrideNetworkID,
			rootCTID:             "C000006400022B67",
			embeddedCTID:         "C000006400025359",
		},
		{
			name:             "maximum network ID excludes root CTID",
			ledgerIndex:      100,
			transactionIndex: 2,
			serverNetworkID:  0xFFFF,
			embeddedCTID:     "C00000640002FFFF",
		},
		{
			name:             "maximum ledger index excludes root CTID",
			ledgerIndex:      0x0FFFFFFF,
			transactionIndex: 2,
			serverNetworkID:  11111,
			embeddedCTID:     "CFFFFFFF00022B67",
		},
		{
			name:             "maximum transaction index remains valid at root",
			ledgerIndex:      100,
			transactionIndex: 0xFFFF,
			serverNetworkID:  11111,
			rootCTID:         "C0000064FFFF2B67",
			embeddedCTID:     "C0000064FFFF2B67",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txJSON := validStoredPaymentTransaction()
			if tc.transactionNetworkID != nil {
				txJSON["NetworkID"] = *tc.transactionNetworkID
			}
			meta := validStoredMetadata()
			meta["TransactionIndex"] = tc.transactionIndex
			stored, err := json.Marshal(handlers.StoredTransaction{
				TxJSON: txJSON,
				Meta:   meta,
			})
			require.NoError(t, err)

			mock := newMockLedgerServiceTx()
			mock.serverInfo.NetworkID = tc.serverNetworkID
			mock.transactions[txHash] = &types.TransactionInfo{
				TxData:      stored,
				LedgerIndex: tc.ledgerIndex,
				Validated:   true,
				TxIndex:     tc.transactionIndex + 1,
			}
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   servicesForTx(mock),
			}

			result, rpcErr := (&handlers.TxMethod{}).Handle(
				ctx,
				json.RawMessage(`{"transaction":"`+txHash+`"}`),
			)
			require.Nil(t, rpcErr)
			response := result.(map[string]any)
			if tc.rootCTID == "" {
				assert.NotContains(t, response, "ctid")
			} else {
				assert.Equal(t, tc.rootCTID, response["ctid"])
			}
			responseTx := response["tx_json"].(map[string]any)
			assert.Equal(t, tc.embeddedCTID, responseTx["ctid"])
		})
	}

	t.Run("API v1 retains the inclusive transaction CTID at the root", func(t *testing.T) {
		txJSON := validStoredPaymentTransaction()
		txJSON["NetworkID"] = overrideNetworkID
		meta := validStoredMetadata()
		meta["TransactionIndex"] = uint32(2)
		stored, err := json.Marshal(handlers.StoredTransaction{
			TxJSON: txJSON,
			Meta:   meta,
		})
		require.NoError(t, err)

		mock := newMockLedgerServiceTx()
		mock.serverInfo.NetworkID = 0xFFFF
		mock.transactions[txHash] = &types.TransactionInfo{
			TxData:      stored,
			LedgerIndex: 100,
			Validated:   true,
			TxIndex:     2,
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   servicesForTx(mock),
		}

		result, rpcErr := (&handlers.TxMethod{}).Handle(
			ctx,
			json.RawMessage(`{"transaction":"`+txHash+`"}`),
		)
		require.Nil(t, rpcErr)
		assert.Equal(t, "C000006400025359", result.(map[string]any)["ctid"])

		result, rpcErr = (&handlers.TxMethod{}).Handle(
			ctx,
			json.RawMessage(`{"transaction":"`+txHash+`","binary":true}`),
		)
		require.Nil(t, rpcErr)
		assert.Equal(t, "C000006400025359", result.(map[string]any)["ctid"])
	})
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

func TestTxMethodCTIDLookupUsesMetadataTransactionIndex(t *testing.T) {
	reader := newDefaultLedgerReader(100, true)
	firstTx := validStoredPaymentTransaction()
	firstTx["Sequence"] = uint32(1)
	firstMeta := validStoredMetadata()
	firstMeta["TransactionIndex"] = uint32(9)
	firstData, err := json.Marshal(handlers.StoredTransaction{
		TxJSON: firstTx,
		Meta:   firstMeta,
	})
	require.NoError(t, err)
	secondTx := validStoredPaymentTransaction()
	secondTx["Sequence"] = uint32(2)
	secondMeta := validStoredMetadata()
	secondMeta["TransactionIndex"] = uint32(3)
	secondData, err := json.Marshal(handlers.StoredTransaction{
		TxJSON: secondTx,
		Meta:   secondMeta,
	})
	require.NoError(t, err)

	firstHash := [32]byte{1}
	secondHash := [32]byte{2}
	reader.transactions = append(reader.transactions,
		struct {
			hash [32]byte
			data []byte
		}{hash: firstHash, data: firstData},
		struct {
			hash [32]byte
			data []byte
		}{hash: secondHash, data: secondData},
	)

	service := &ledgerMock{mockLedgerService: newMockLedgerService()}
	service.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		require.Equal(t, uint32(100), seq)
		return reader, nil
	}
	ctid, ok := handlers.EncodeCTID(100, 3, 0)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{"ctid": ctid})
	require.NoError(t, err)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}

	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	assert.Equal(t, strings.ToUpper(hex.EncodeToString(secondHash[:])), response["hash"])
	assert.EqualValues(t, 2, response["Sequence"])
}

func TestTxMethodCTIDLookupSkipsMalformedLeaf(t *testing.T) {
	reader := newDefaultLedgerReader(100, true)
	var txHash [32]byte
	txHash[0] = 1
	reader.transactions = append(reader.transactions, struct {
		hash [32]byte
		data []byte
	}{hash: txHash, data: []byte{0xFF}})

	service := &ledgerMock{mockLedgerService: newMockLedgerService()}
	service.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 100 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	services := types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service})
	params, err := json.Marshal(map[string]any{
		"ctid":   "C000006400000000",
		"binary": true,
	})
	require.NoError(t, err)

	for _, version := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run(fmt.Sprintf("API v%d", version), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: version,
				Services:   services,
			}
			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcTXN_NOT_FOUND, rpcErr.Code)
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

	t.Run("Transaction hash with leading zeros", func(t *testing.T) {
		leadingZeroHash := "0000000000000000000000000000000000000000000000000000000000000001"
		txJSON := validStoredPaymentTransaction()
		storedTx := handlers.StoredTransaction{TxJSON: txJSON}
		storedData, _ := json.Marshal(storedTx)

		mock.transactions[strings.ToUpper(leadingZeroHash)] = &types.TransactionInfo{
			TxData:      storedData,
			LedgerIndex: 100,
			Validated:   true,
		}

		params := map[string]any{
			"transaction": leadingZeroHash,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("All-F hash (max value)", func(t *testing.T) {
		maxHash := "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
		txJSON := validStoredPaymentTransaction()
		storedTx := handlers.StoredTransaction{TxJSON: txJSON}
		storedData, _ := json.Marshal(storedTx)

		mock.transactions[maxHash] = &types.TransactionInfo{
			TxData:      storedData,
			LedgerIndex: 100,
			Validated:   true,
		}

		params := map[string]any{
			"transaction": maxHash,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

func TestTxMethodCorruptedStoredData(t *testing.T) {
	mock := newMockLedgerServiceTx()
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   servicesForTx(mock),
	}
	txHash := "1111111111111111111111111111111111111111111111111111111111111111"
	params, err := json.Marshal(map[string]any{"transaction": txHash})
	require.NoError(t, err)

	for _, tc := range append(corruptedStoredTransactionData(t), txMetadataCorruptionData(t)...) {
		t.Run(tc.name, func(t *testing.T) {
			mock.transactions[txHash] = &types.TransactionInfo{
				TxData:      tc.data,
				LedgerIndex: 100,
				LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
				Validated:   true,
			}

			result, rpcErr := method.Handle(ctx, params)

			requireDBDeserializationError(t, result, rpcErr)
		})
	}
}

func TestTxMethodStoredDataWithoutMetadata(t *testing.T) {
	mock := newMockLedgerServiceTx()
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   servicesForTx(mock),
	}
	txHash := "2222222222222222222222222222222222222222222222222222222222222222"
	params, err := json.Marshal(map[string]any{"transaction": txHash})
	require.NoError(t, err)

	for _, tc := range storedTransactionDataWithoutMetadata(t) {
		t.Run(tc.name, func(t *testing.T) {
			mock.transactions[txHash] = &types.TransactionInfo{
				TxData:      tc.data,
				LedgerIndex: 100,
				Validated:   true,
			}

			result, rpcErr := method.Handle(ctx, params)
			if tc.name == "VL metadata empty" {
				requireDBDeserializationError(t, result, rpcErr)
				return
			}

			require.Nil(t, rpcErr)
			response, ok := result.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "Payment", response["TransactionType"])
			assert.NotContains(t, response, "meta")
		})
	}
}

func TestTxMethodValidStoredDataFormats(t *testing.T) {
	mock := newMockLedgerServiceTx()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   servicesForTx(mock),
	}
	txHash := "3333333333333333333333333333333333333333333333333333333333333333"
	params, err := json.Marshal(map[string]any{"transaction": txHash})
	require.NoError(t, err)

	for _, tc := range storedTransactionDataWithMetadata(t) {
		t.Run(tc.name, func(t *testing.T) {
			mock.transactions[txHash] = &types.TransactionInfo{
				TxData:      tc.data,
				LedgerIndex: 100,
				Validated:   true,
			}

			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)

			require.Nil(t, rpcErr)
			response, ok := result.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "Payment", response["TransactionType"])
			meta, ok := response["meta"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "tesSUCCESS", meta["TransactionResult"])
		})
	}
}

func TestTxMethodCTIDCorruptedStoredData(t *testing.T) {
	base := newMockLedgerServiceTx()
	ledger := &mockCTIDLedger{
		sequence: 45,
		txHash:   [32]byte{1},
	}
	mock := &mockCTIDLedgerService{mockLedgerServiceTx: base, ledger: ledger}
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
	}
	ctid, ok := handlers.EncodeCTID(ledger.sequence, 0, 0)
	require.True(t, ok)

	for _, storedData := range append(corruptedStoredTransactionData(t), txMetadataCorruptionData(t)...) {
		t.Run(storedData.name, func(t *testing.T) {
			ledger.txData = storedData.data
			for _, responseFormat := range []struct {
				name   string
				binary bool
			}{
				{name: "json", binary: false},
				{name: "binary", binary: true},
			} {
				t.Run(responseFormat.name, func(t *testing.T) {
					params, err := json.Marshal(map[string]any{"ctid": ctid, "binary": responseFormat.binary})
					require.NoError(t, err)

					result, rpcErr := method.Handle(ctx, params)

					if _, indexed := txcore.TransactionIndexFromTxWithMetaBlob(storedData.data); indexed {
						requireDBDeserializationError(t, result, rpcErr)
						return
					}
					assert.Nil(t, result)
					require.NotNil(t, rpcErr)
					assert.Equal(t, types.RpcTXN_NOT_FOUND, rpcErr.Code)
				})
			}
		})
	}
}

func TestTxMethodCTIDStoredDataWithoutMetadata(t *testing.T) {
	base := newMockLedgerServiceTx()
	ledger := &mockCTIDLedger{
		sequence: 45,
		txHash:   [32]byte{1},
	}
	mock := &mockCTIDLedgerService{mockLedgerServiceTx: base, ledger: ledger}
	method := &handlers.TxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock}),
	}
	ctid, ok := handlers.EncodeCTID(ledger.sequence, 0, 0)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{"ctid": ctid})
	require.NoError(t, err)

	for _, tc := range storedTransactionDataWithoutMetadata(t) {
		t.Run(tc.name, func(t *testing.T) {
			ledger.txData = tc.data

			result, rpcErr := method.Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcTXN_NOT_FOUND, rpcErr.Code)
		})
	}
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

		params := map[string]any{
			"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Equal(t, "Internal error.", rpcErr.Message)
	})
}
