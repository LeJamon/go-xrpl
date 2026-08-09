package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceSubmit extends mockLedgerService with submit-specific behavior
type mockLedgerServiceSubmit struct {
	*mockLedgerService
	submitResult     *types.SubmitResult
	submitError      error
	storedTxs        map[string][]byte
	submitCalls      int
	lastTxBlob       string
	currentFeesCalls int
	accountInfoCalls int
}

func newMockLedgerServiceSubmit() *mockLedgerServiceSubmit {
	return &mockLedgerServiceSubmit{
		mockLedgerService: newMockLedgerService(),
		storedTxs:         make(map[string][]byte),
		submitResult: &types.SubmitResult{
			EngineResult:        "tesSUCCESS",
			EngineResultCode:    0,
			EngineResultMessage: "The transaction was applied. Only final in a validated ledger.",
			Applied:             true,
			Fee:                 10,
			CurrentLedger:       3,
			ValidatedLedger:     2,
		},
	}
}

func (m *mockLedgerServiceSubmit) SubmitTransaction(txJSON []byte, txBlobHex string) (*types.SubmitResult, error) {
	m.submitCalls++
	m.lastTxBlob = txBlobHex
	if m.submitError != nil {
		return nil, m.submitError
	}
	return m.submitResult, nil
}

func (m *mockLedgerServiceSubmit) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	m.currentFeesCalls++
	return 10, 10000000, 2000000
}

func (m *mockLedgerServiceSubmit) GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*types.AccountInfo, error) {
	m.accountInfoCalls++
	return m.mockLedgerService.GetAccountInfo(ctx, account, ledgerIndex)
}

func TestSubmitMethodRawBlobPresenceAndProjection(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}

	rawMap := map[string]any{
		"TransactionType": "Payment",
		"Account":         validAccountAddress,
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Memos":           []map[string]any{},
		"Sequence":        1,
	}
	rawBlob := rawSubmitBlob(t, rawMap)
	params, err := json.Marshal(map[string]any{
		"tx_blob": strings.ToLower(rawBlob),
		"tx_json": []any{},
		"secret":  map[string]any{},
	})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, strings.ToUpper(rawBlob), response["tx_blob"])
	assert.Empty(t, response["deprecated"])
	assert.NotContains(t, response, "hash", "raw submit retains legacy nested-hash shape")
	assert.Equal(t, strings.ToUpper(rawBlob), mock.lastTxBlob)

	txJSON, ok := response["tx_json"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Payment", txJSON["TransactionType"])
	assert.Equal(t, "1000000", txJSON["Amount"])
	assert.NotEmpty(t, txJSON["SigningPubKey"])
	assert.NotEmpty(t, txJSON["TxnSignature"])
	assert.Empty(t, txJSON["Memos"])
	assert.Equal(t, handlers.CalculateTxHash(rawBlob), txJSON["hash"])
	assert.NotContains(t, txJSON, "DeliverMax", "raw submit does not apply DeliverMax projection")
	assert.Equal(t, 1, mock.submitCalls)
}

func TestSubmitMethodRawBlobInvalidTransactionEnvelope(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}
	params, err := json.Marshal(map[string]any{"tx_blob": "00"})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidTransaction", rpcErr.ErrorString)
	assert.NotEmpty(t, rpcErr.ErrorException)
	assert.Zero(t, mock.submitCalls)
}

func TestSubmitMethodRawBlobRejectsBadSignatureBeforeSubmission(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	mock.standalone = false
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}
	blob := rawSubmitBlob(t, map[string]any{
		"TransactionType": "AccountSet",
		"Account":         validAccountAddress,
		"Fee":             "10",
		"Sequence":        1,
	})
	decoded, err := binarycodec.Decode(blob)
	require.NoError(t, err)
	signature, ok := decoded["TxnSignature"].(string)
	require.True(t, ok)
	decoded["TxnSignature"] = strings.Repeat("00", len(signature)/2)
	badBlob, err := binarycodec.Encode(decoded)
	require.NoError(t, err)
	params, err := json.Marshal(map[string]any{"tx_blob": badBlob})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, "invalidTransaction", rpcErr.ErrorString)
	assert.Equal(t, "fails local checks: Invalid signature.", rpcErr.ErrorException)
	assert.Zero(t, mock.submitCalls)
}

func TestSubmitMethodRawBlobCanonicalizesFieldOrder(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}
	canonicalBlob := rawSubmitBlob(t, map[string]any{
		"TransactionType": "AccountSet",
		"Account":         validAccountAddress,
		"Fee":             "10",
		"Sequence":        1,
	})
	canonicalBytes, err := hex.DecodeString(canonicalBlob)
	require.NoError(t, err)
	accountOffset := bytes.LastIndex(canonicalBytes, []byte{0x81, 0x14})
	require.GreaterOrEqual(t, accountOffset, 0)
	require.GreaterOrEqual(t, len(canonicalBytes), accountOffset+22)
	accountField := append([]byte(nil), canonicalBytes[accountOffset:accountOffset+22]...)
	nonCanonical := append(accountField, canonicalBytes[:accountOffset]...)
	nonCanonical = append(nonCanonical, canonicalBytes[accountOffset+22:]...)
	require.NotEqual(t, canonicalBytes, nonCanonical)
	params, err := json.Marshal(map[string]any{"tx_blob": hex.EncodeToString(nonCanonical)})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, canonicalBlob, response["tx_blob"])
	assert.Equal(t, canonicalBlob, mock.lastTxBlob)
	txJSON, ok := response["tx_json"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, handlers.CalculateTxHash(canonicalBlob), txJSON["hash"])
}

func TestSubmitMethodAbsentBlobRequiresSigningCapability(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}
	params, err := json.Marshal(map[string]any{
		"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         validAccountAddress,
		},
	})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcNOT_SUPPORTED, rpcErr.Code)
	assert.Equal(t, "Signing is not supported by this server.", rpcErr.Message)
	assert.NotContains(t, rpcErr.Extra, "deprecated")
	assert.Zero(t, mock.submitCalls)
}

func TestSubmitMethodAbsentBlobValidatesFailHardBeforeSigningCapability(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services:   newSubmitTestServices(mock),
	}
	params, err := json.Marshal(map[string]any{
		"tx_json":   map[string]any{},
		"fail_hard": "true",
	})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "Invalid field 'fail_hard', not boolean.", rpcErr.Message)
	assert.NotContains(t, rpcErr.Extra, "deprecated")
	assert.Zero(t, mock.submitCalls)
}

func (m *mockLedgerServiceSubmit) StoreTransaction(txHash [32]byte, txData []byte) error {
	// Store the transaction for verification
	hashStr := string(txHash[:])
	m.storedTxs[hashStr] = txData
	return nil
}

// newSubmitTestServices builds a per-test ServiceContainer wrapping mock.
func newSubmitTestServices(mock *mockLedgerServiceSubmit) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
	})
}

func newSubmitSigningTestServices(mock *mockLedgerServiceSubmit) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
		Capabilities: types.RPCCapabilities{
			SigningEnabled: true,
		},
	})
}

func rawSubmitParams(t *testing.T, txJSON map[string]any, extra map[string]any) json.RawMessage {
	t.Helper()
	txBlob := rawSubmitBlob(t, txJSON)
	params := map[string]any{"tx_blob": txBlob}
	maps.Copy(params, extra)
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	return paramsJSON
}

func rawSubmitBlob(t *testing.T, txJSON map[string]any) string {
	t.Helper()
	entropy := make([]byte, 16)
	for i := range entropy {
		entropy[i] = 0x11
	}
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair(entropy, false)
	require.NoError(t, err)

	wireJSON := maps.Clone(txJSON)
	wireJSON["SigningPubKey"] = publicKey
	delete(wireJSON, "TxnSignature")
	encodedJSON, err := json.Marshal(wireJSON)
	require.NoError(t, err)
	transaction, err := tx.ParseJSON(encodedJSON)
	require.NoError(t, err)
	signature, err := txsign.SignTransaction(transaction, privateKey)
	require.NoError(t, err)
	wireJSON["TxnSignature"] = signature
	blob, err := binarycodec.Encode(wireJSON)
	require.NoError(t, err)
	return blob
}

func signedSubmitParams(t *testing.T, txJSON map[string]any, extra map[string]any) json.RawMessage {
	t.Helper()
	params := map[string]any{
		"tx_json":  txJSON,
		"seed_hex": "DEDCE9CE67B451D852FD4E846FCDE31C",
		"key_type": "secp256k1",
	}
	maps.Copy(params, extra)
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)
	return paramsJSON
}

// TestSubmitMethodErrorValidation tests error handling for invalid inputs
func TestSubmitMethodErrorValidation(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
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
			name:          "Missing tx_blob and tx_json - empty params",
			params:        map[string]any{},
			expectedError: "Signing is not supported by this server.",
			expectedCode:  rpcerrors.RpcNOT_SUPPORTED,
		},
		{
			name:          "Missing tx_blob and tx_json - nil params",
			params:        nil,
			expectedError: "Signing is not supported by this server.",
			expectedCode:  rpcerrors.RpcNOT_SUPPORTED,
		},
		{
			name: "Empty tx_blob",
			params: map[string]any{
				"tx_blob": "",
			},
			expectedError: "Invalid parameters.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - integer",
			params: map[string]any{
				"tx_blob": 12345,
			},
			expectedError: "Invalid parameters",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - boolean",
			params: map[string]any{
				"tx_blob": true,
			},
			expectedError: "Invalid parameters",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - array",
			params: map[string]any{
				"tx_blob": []string{"hex1", "hex2"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "tx_blob invalid hex",
			params: map[string]any{
				"tx_blob": "ZZZZ",
			},
			expectedError: "Invalid parameters.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.submitError = nil
			mock.submitResult = &types.SubmitResult{
				EngineResult:        "tesSUCCESS",
				EngineResultCode:    0,
				EngineResultMessage: "The transaction was applied.",
				Applied:             true,
			}

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

// TestSubmitMethodValidTxJson tests valid tx_json submission
func TestSubmitMethodValidTxJson(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name         string
		txJson       map[string]any
		mockResult   *types.SubmitResult
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name: "Valid Payment transaction",
			txJson: map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
				"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
				"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
			},
			mockResult: &types.SubmitResult{
				EngineResult:        "tesSUCCESS",
				EngineResultCode:    0,
				EngineResultMessage: "The transaction was applied. Only final in a validated ledger.",
				Applied:             true,
				Fee:                 10,
				CurrentLedger:       3,
				ValidatedLedger:     2,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
				assert.Equal(t, float64(0), resp["engine_result_code"])
				assert.Equal(t, true, resp["applied"])
				assert.Equal(t, true, resp["accepted"])
				assert.Contains(t, resp, "tx_json")
			},
		},
		{
			name: "Valid AccountSet transaction",
			txJson: map[string]any{
				"TransactionType": "AccountSet",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Fee":             "12",
				"Sequence":        5,
				"SetFlag":         8,
			},
			mockResult: &types.SubmitResult{
				EngineResult:        "tesSUCCESS",
				EngineResultCode:    0,
				EngineResultMessage: "The transaction was applied.",
				Applied:             true,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
				assert.Equal(t, true, resp["applied"])
			},
		},
		{
			name: "Valid TrustSet transaction",
			txJson: map[string]any{
				"TransactionType": "TrustSet",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"LimitAmount": map[string]any{
					"currency": "USD",
					"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"value":    "100",
				},
				"Fee":      "12",
				"Sequence": 10,
			},
			mockResult: &types.SubmitResult{
				EngineResult:     "tesSUCCESS",
				EngineResultCode: 0,
				Applied:          true,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock result
			mock.submitResult = tc.mockResult
			mock.submitError = nil

			paramsJSON := rawSubmitParams(t, tc.txJson, nil)

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

func TestSubmitMethodDoesNotPersistSyntheticMetadata(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   newSubmitTestServices(mock),
	}
	params := rawSubmitParams(t, validStoredPaymentTransaction(), nil)

	_, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	require.Empty(t, mock.storedTxs)
	assert.Zero(t, mock.currentFeesCalls)
	assert.Zero(t, mock.accountInfoCalls)
}

// TestSubmitMethodResponseFields tests that response contains expected fields
func TestSubmitMethodResponseFields(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.submitResult = &types.SubmitResult{
		EngineResult:        "tesSUCCESS",
		EngineResultCode:    0,
		EngineResultMessage: "The transaction was applied. Only final in a validated ledger.",
		Applied:             true,
		Fee:                 10,
		CurrentLedger:       3,
		ValidatedLedger:     2,
		CurrentLedgerState: &types.SubmitLedgerState{
			ValidatedLedgerIndex:     2,
			OpenLedgerCost:           12,
			AccountSequenceNext:      7,
			AccountSequenceAvailable: 9,
		},
	}

	t.Run("Response contains all required fields", func(t *testing.T) {
		paramsJSON := rawSubmitParams(t, map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        1,
		}, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check required response fields
		assert.Contains(t, resp, "engine_result")
		assert.Contains(t, resp, "engine_result_code")
		assert.Contains(t, resp, "engine_result_message")
		assert.Contains(t, resp, "tx_json")
		assert.Contains(t, resp, "accepted")
		assert.Contains(t, resp, "applied")
		assert.Contains(t, resp, "broadcast")
		assert.Contains(t, resp, "kept")
		assert.Contains(t, resp, "queued")
		assert.Contains(t, resp, "validated_ledger_index")
		assert.Contains(t, resp, "tx_blob")
		assert.Contains(t, resp, "account_sequence_next")
		assert.Contains(t, resp, "account_sequence_available")
		assert.Contains(t, resp, "open_ledger_cost")

		// Verify field values for successful submission
		assert.Equal(t, "tesSUCCESS", resp["engine_result"])
		assert.Equal(t, float64(0), resp["engine_result_code"])
		assert.Equal(t, "The transaction was applied. Only final in a validated ledger.", resp["engine_result_message"])
		assert.Equal(t, true, resp["accepted"])
		assert.Equal(t, true, resp["applied"])
		assert.Equal(t, false, resp["queued"])
		assert.Equal(t, float64(2), resp["validated_ledger_index"])
		assert.Equal(t, float64(7), resp["account_sequence_next"])
		assert.Equal(t, float64(9), resp["account_sequence_available"])
		assert.Equal(t, "12", resp["open_ledger_cost"])
	})

	t.Run("tx_json is included in response", func(t *testing.T) {
		paramsJSON := rawSubmitParams(t, map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        1,
		}, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Verify tx_json content
		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok, "tx_json should be a map")
		assert.Equal(t, "Payment", txJson["TransactionType"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", txJson["Account"])
	})
}

func TestSubmitMethodOmitsLedgerStateFieldsWithoutSnapshot(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	mock.submitResult = &types.SubmitResult{
		EngineResult:        "tesSUCCESS",
		EngineResultCode:    0,
		EngineResultMessage: "The transaction was applied.",
		Applied:             true,
		ValidatedLedger:     99,
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   newSubmitTestServices(mock),
	}
	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, rawSubmitParams(t, validStoredPaymentTransaction(), nil))
	require.Nil(t, rpcErr)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(resultJSON, &response))
	for _, field := range []string{
		"account_sequence_next",
		"account_sequence_available",
		"open_ledger_cost",
		"validated_ledger_index",
	} {
		assert.NotContains(t, response, field, "field %q must be absent without a snapshot", field)
	}
}

// TestSubmitMethodEngineResults tests various engine result codes
func TestSubmitMethodEngineResults(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	baseTxJson := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	tests := []struct {
		name             string
		engineResult     string
		engineResultCode int
		engineResultMsg  string
		applied          bool
		expectedStatus   string
		validateResp     func(t *testing.T, resp map[string]any)
	}{
		{
			name:             "tesSUCCESS",
			engineResult:     "tesSUCCESS",
			engineResultCode: 0,
			engineResultMsg:  "The transaction was applied. Only final in a validated ledger.",
			applied:          true,
			expectedStatus:   "success",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, true, resp["applied"])
			},
		},
		{
			name:             "tecCLAIM - Claimed cost only",
			engineResult:     "tecCLAIM",
			engineResultCode: 100,
			engineResultMsg:  "Fee claimed. Sequence used. No action.",
			applied:          true,
			expectedStatus:   "success", // tec codes are still "successful"
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tecCLAIM", resp["engine_result"])
				assert.Equal(t, float64(100), resp["engine_result_code"])
			},
		},
		{
			name:             "tecUNFUNDED_PAYMENT",
			engineResult:     "tecUNFUNDED_PAYMENT",
			engineResultCode: 104,
			engineResultMsg:  "Insufficient XRP balance to send.",
			applied:          true,
			expectedStatus:   "success",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tecUNFUNDED_PAYMENT", resp["engine_result"])
				assert.Equal(t, float64(104), resp["engine_result_code"])
			},
		},
		{
			name:             "tecPATH_DRY",
			engineResult:     "tecPATH_DRY",
			engineResultCode: 128,
			engineResultMsg:  "Path could not send partial amount.",
			applied:          true,
			expectedStatus:   "success",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tecPATH_DRY", resp["engine_result"])
			},
		},
		{
			name:             "tefPAST_SEQ - Past sequence number",
			engineResult:     "tefPAST_SEQ",
			engineResultCode: -190,
			engineResultMsg:  "This sequence number has already passed.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tefPAST_SEQ", resp["engine_result"])
				assert.Equal(t, false, resp["applied"])
			},
		},
		{
			name:             "tefMAX_LEDGER - Max ledger exceeded",
			engineResult:     "tefMAX_LEDGER",
			engineResultCode: -186,
			engineResultMsg:  "Ledger sequence too high.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tefMAX_LEDGER", resp["engine_result"])
			},
		},
		{
			name:             "temBAD_AMOUNT - Invalid amount",
			engineResult:     "temBAD_AMOUNT",
			engineResultCode: -298,
			engineResultMsg:  "Malformed: Bad amount.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "temBAD_AMOUNT", resp["engine_result"])
				assert.Equal(t, false, resp["applied"])
			},
		},
		{
			name:             "temBAD_FEE - Invalid fee",
			engineResult:     "temBAD_FEE",
			engineResultCode: -299,
			engineResultMsg:  "Invalid fee value.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "temBAD_FEE", resp["engine_result"])
			},
		},
		{
			name:             "terRETRY - Retry transaction",
			engineResult:     "terRETRY",
			engineResultCode: -99,
			engineResultMsg:  "Retry transaction.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "terRETRY", resp["engine_result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.submitResult = &types.SubmitResult{
				EngineResult:        tc.engineResult,
				EngineResultCode:    tc.engineResultCode,
				EngineResultMessage: tc.engineResultMsg,
				Applied:             tc.applied,
				CurrentLedger:       3,
				ValidatedLedger:     2,
			}

			paramsJSON := rawSubmitParams(t, baseTxJson, nil)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Submit should not return RPC error even for transaction failures")
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]any
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			// Common assertions
			assert.Equal(t, tc.engineResult, resp["engine_result"])
			assert.Equal(t, float64(tc.engineResultCode), resp["engine_result_code"])
			assert.Equal(t, tc.engineResultMsg, resp["engine_result_message"])

			// Test-specific validations
			tc.validateResp(t, resp)
		})
	}
}

// TestSubmitMethodMalformedTransaction tests malformed transaction handling.
// Without tx_blob, submit is a signed path and capability admission precedes
// tx_json validation. These fixtures verify the disabled-signing response.
func TestSubmitMethodMalformedTransaction(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name        string
		txJson      any
		expectError bool
		errorMsg    string
		description string
	}{
		{
			name:        "String tx_json - rejected",
			txJson:      "not a valid json object",
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "A JSON string is not a transaction object",
		},
		{
			name:        "Number tx_json - rejected",
			txJson:      12345,
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "A JSON number is not a transaction object",
		},
		{
			name:        "Boolean tx_json - rejected",
			txJson:      true,
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "A JSON boolean is not a transaction object",
		},
		{
			name:        "Array tx_json - rejected",
			txJson:      []any{1, 2, 3},
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "A JSON array is not a transaction object",
		},
		{
			name:        "Empty tx_json object - accepted",
			txJson:      map[string]any{},
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "Empty object is valid, ledger service validates content",
		},
		{
			name: "Valid minimal transaction",
			txJson: map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectError: true,
			errorMsg:    "Signing is not supported by this server.",
			description: "Minimal valid transaction structure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Description: %s", tc.description)

			params := map[string]any{
				"tx_json": tc.txJson,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result, "Expected nil result for error case")
				require.NotNil(t, rpcErr, "Expected RPC error")
				assert.Equal(t, rpcerrors.RpcNOT_SUPPORTED, rpcErr.Code)
				assert.Contains(t, rpcErr.Message, tc.errorMsg)
			} else {
				require.Nil(t, rpcErr, "Expected no error - validation in ledger service")
				require.NotNil(t, result)
			}
		})
	}
}

// TestSubmitMethodServiceUnavailable tests behavior when ledger service is not available
func TestSubmitMethodServiceUnavailable(t *testing.T) {
	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	paramsJSON := rawSubmitParams(t, map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}, nil)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestSubmitMethodServiceNilLedger tests behavior when ledger service is nil
func TestSubmitMethodServiceNilLedger(t *testing.T) {
	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: nil}),
	}

	paramsJSON := rawSubmitParams(t, map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}, nil)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestSubmitMethodSubmitError tests handling of ledger service errors
func TestSubmitMethodSubmitError(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name        string
		submitError error
	}{
		{
			name:        "Internal error",
			submitError: errors.New("internal ledger error"),
		},
		{
			name:        "Network error",
			submitError: errors.New("network unavailable"),
		},
		{
			name:        "Validation error",
			submitError: errors.New("transaction validation failed"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.submitError = tc.submitError

			paramsJSON := rawSubmitParams(t, map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
			}, nil)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
			assert.Equal(t, "Exception occurred during transaction submission.", rpcErr.Message)
			assert.NotContains(t, rpcErr.Message, tc.submitError.Error())
		})
	}
}

func TestSubmitMethodTxBlobSubmitErrorIsSanitized(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	submitErr := errors.New("private ledger backend failure")
	mock.submitError = submitErr
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   newSubmitTestServices(mock),
	}
	txBlob, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	})
	require.NoError(t, err)
	params, err := json.Marshal(map[string]any{"tx_blob": txBlob})
	require.NoError(t, err)

	result, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "internal", rpcErr.ErrorString)
	assert.Equal(t, "internal", rpcErr.Type)
	assert.Equal(t, "Exception occurred during transaction submission.", rpcErr.Message)
	assert.NotContains(t, rpcErr.Message, submitErr.Error())
}

// TestSubmitMethodMetadata tests the method's metadata functions
func TestSubmitMethodMetadata(t *testing.T) {
	method := &handlers.SubmitMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleUser, method.RequiredRole(),
			"submit method should require user role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestSubmitMethodOptionalParams tests optional parameters
func TestSubmitMethodOptionalParams(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	baseTxJson := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	tests := []struct {
		name         string
		extraParams  map[string]any
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name: "fail_hard parameter",
			extraParams: map[string]any{
				"fail_hard": true,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				// fail_hard is accepted but doesn't change success response
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "offline parameter",
			extraParams: map[string]any{
				"offline": true,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "build_path parameter",
			extraParams: map[string]any{
				"build_path": true,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "fee_mult_max parameter",
			extraParams: map[string]any{
				"fee_mult_max": 10,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "fee_div_max parameter",
			extraParams: map[string]any{
				"fee_div_max": 1,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "multiple optional parameters",
			extraParams: map[string]any{
				"fail_hard":    true,
				"offline":      false,
				"fee_mult_max": 10,
				"fee_div_max":  1,
			},
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON := rawSubmitParams(t, baseTxJson, tc.extraParams)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error")
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]any
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			tc.validateResp(t, resp)
		})
	}
}

// TestSubmitMethodSigningCredentials tests the sign-and-submit path:
// when tx_json + signing credentials are provided, the handler derives
// the key, signs the transaction, and submits the signed blob.
func TestSubmitMethodSigningCredentials(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitSigningTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name         string
		signingParam string
		signingValue string
		keyType      string
		account      string
		description  string
	}{
		{
			name:         "secret parameter",
			signingParam: "secret",
			signingValue: "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9",
			account:      "rMCcNuTcajgw7YTgBy1sys3b89QqjUrMpH",
			description:  "Traditional secret format for signing",
		},
		{
			name:         "seed parameter",
			signingParam: "seed",
			signingValue: "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9",
			keyType:      "secp256k1",
			account:      "rMCcNuTcajgw7YTgBy1sys3b89QqjUrMpH",
			description:  "Seed format for signing",
		},
		{
			name:         "seed_hex parameter",
			signingParam: "seed_hex",
			signingValue: "DEDCE9CE67B451D852FD4E846FCDE31C",
			keyType:      "secp256k1",
			account:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			description:  "Hex-encoded seed for signing",
		},
		{
			name:         "passphrase parameter",
			signingParam: "passphrase",
			signingValue: "masterpassphrase",
			keyType:      "secp256k1",
			account:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			description:  "Passphrase-based key derivation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Parameter: %s, Description: %s", tc.signingParam, tc.description)

			params := map[string]any{
				"tx_json": map[string]any{
					"TransactionType": "Payment",
					"Account":         tc.account,
					"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"Amount":          "1000000",
				},
				tc.signingParam: tc.signingValue,
			}
			if tc.keyType != "" {
				params["key_type"] = tc.keyType
			}

			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			require.Nil(t, rpcErr, "sign-and-submit should succeed")
			require.NotNil(t, result)

			// Convert result to map for field inspection
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]any
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			// The response must contain the deprecated warning
			assert.Equal(t,
				"Signing support in the 'submit' command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool.",
				resp["deprecated"],
			)

			// The tx_json in the response must contain a signature
			txJson, ok := resp["tx_json"].(map[string]any)
			require.True(t, ok, "tx_json should be a map")
			assert.Contains(t, txJson, "TxnSignature",
				"signed transaction must have TxnSignature")
			assert.Contains(t, txJson, "SigningPubKey",
				"signed transaction must have SigningPubKey")
			assert.Contains(t, txJson, "Account",
				"signed transaction must have Account auto-filled")

			// tx_blob must be present (hex-encoded signed blob)
			assert.NotEmpty(t, resp["tx_blob"],
				"tx_blob must be present for signed transaction")

			// Engine result should reflect the mock
			assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			assert.Equal(t, true, resp["applied"])
		})
	}
}

func TestSubmitMethodEmptyCredentialIsPresent(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitSigningTestServices(mock)
	params := json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000"
		},
		"secret": ""
	}`)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	_, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcBAD_SEED, rpcErr.Code)
	assert.Equal(t, "Invalid field 'secret'.", rpcErr.Message)
	assert.Equal(t,
		"Signing support in the 'submit' command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool.",
		rpcErr.Extra["deprecated"],
	)
}

func TestSubmitMethodMissingTxJSONPreservesCredentialPrecedence(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitSigningTestServices(mock)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name    string
		secret  string
		code    int
		message string
	}{
		{
			name:    "valid credential",
			secret:  "masterpassphrase",
			code:    rpcerrors.RpcINVALID_PARAMS,
			message: "Missing field 'tx_json'.",
		},
		{
			name:    "invalid credential",
			secret:  "",
			code:    rpcerrors.RpcBAD_SEED,
			message: "Invalid field 'secret'.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{"secret": test.secret})
			require.NoError(t, err)
			_, rpcErr := (&handlers.SubmitMethod{}).Handle(ctx, params)
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
			assert.Equal(t, test.message, rpcErr.Message)
		})
	}
	assert.Zero(t, mock.submitCalls)
}

// TestSubmitMethodApiV2Response tests API v2 specific response formatting.
// API v2 should include "hash" at the root level of the response.
func TestSubmitMethodApiV2Response(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitSigningTestServices(mock)

	method := &handlers.SubmitMethod{}

	baseTxJson := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	t.Run("API v1 does not have hash at root", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// API v1: no hash at root level
		_, hasRootHash := resp["hash"]
		assert.False(t, hasRootHash, "API v1 should NOT have hash at root level")

		// hash should still be present inside tx_json
		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)
		assert.NotEmpty(t, txJson["hash"], "hash should be inside tx_json")
	})

	t.Run("API v2 has hash at root", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// API v2: hash at root level
		rootHash, hasRootHash := resp["hash"].(string)
		assert.True(t, hasRootHash, "API v2 should have hash at root level")
		assert.NotEmpty(t, rootHash)

		// API v2+: hash moves to the response root; it is not a serialized field.
		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, txJson, "hash")
	})

	t.Run("API v3 has hash at root", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion3,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// API v3: also has hash at root
		rootHash, hasRootHash := resp["hash"].(string)
		assert.True(t, hasRootHash, "API v3 should have hash at root level")
		assert.NotEmpty(t, rootHash)
	})
}

// TestSubmitMethodDeliverMax tests DeliverMax injection for Payment transactions.
// For API v1: Amount is kept, DeliverMax is added.
// For API v2+: Amount is removed, DeliverMax replaces it.
func TestSubmitMethodDeliverMax(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitSigningTestServices(mock)

	method := &handlers.SubmitMethod{}

	t.Run("API v1 Payment - Amount kept, DeliverMax added", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        1,
		}, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)

		// API v1: Amount is kept
		assert.Equal(t, "1000000", txJson["Amount"],
			"API v1 should keep Amount in tx_json")
		// DeliverMax is added
		assert.Equal(t, "1000000", txJson["DeliverMax"],
			"API v1 should add DeliverMax for Payment")
	})

	t.Run("API v2 Payment - Amount removed, DeliverMax added", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
			"Fee":             "10",
			"Sequence":        1,
		}, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)

		// API v2: Amount is removed
		_, hasAmount := txJson["Amount"]
		assert.False(t, hasAmount,
			"API v2 should remove Amount from tx_json for Payment")
		// DeliverMax replaces it
		assert.Equal(t, "1000000", txJson["DeliverMax"],
			"API v2 should have DeliverMax in tx_json for Payment")
	})

	t.Run("Non-Payment tx - no DeliverMax regardless of API version", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		paramsJSON := signedSubmitParams(t, map[string]any{
			"TransactionType": "AccountSet",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Fee":             "12",
			"Sequence":        5,
			"SetFlag":         8,
		}, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)

		// Non-Payment: no DeliverMax added
		_, hasDeliverMax := txJson["DeliverMax"]
		assert.False(t, hasDeliverMax,
			"Non-Payment tx should not have DeliverMax")
	})
}

// TestSubmitMethodIndependentBooleans tests that the boolean response fields
// (accepted, applied, broadcast, queued, kept) can be set independently,
// matching rippled's Transaction::SubmitResult struct.
func TestSubmitMethodIndependentBooleans(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	baseTxJson := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	t.Run("Applied=true implies accepted=true, broadcast=true, kept=true", func(t *testing.T) {
		mock.submitResult = &types.SubmitResult{
			EngineResult:        "tesSUCCESS",
			EngineResultCode:    0,
			EngineResultMessage: "The transaction was applied.",
			Applied:             true,
			Broadcast:           true,
			Kept:                true,
			Queued:              false,
			ValidatedLedger:     2,
		}

		paramsJSON := rawSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)

		assert.Equal(t, true, resp["applied"])
		assert.Equal(t, true, resp["broadcast"])
		assert.Equal(t, true, resp["kept"])
		assert.Equal(t, false, resp["queued"])
		assert.Equal(t, true, resp["accepted"],
			"accepted should be true when applied is true (any() = true)")
	})

	t.Run("Not applied, not broadcast - accepted=false", func(t *testing.T) {
		mock.submitResult = &types.SubmitResult{
			EngineResult:        "tefPAST_SEQ",
			EngineResultCode:    -190,
			EngineResultMessage: "This sequence number has already passed.",
			Applied:             false,
			Broadcast:           false,
			Kept:                false,
			Queued:              false,
			ValidatedLedger:     2,
		}

		paramsJSON := rawSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)

		assert.Equal(t, false, resp["applied"])
		assert.Equal(t, false, resp["broadcast"])
		assert.Equal(t, false, resp["kept"])
		assert.Equal(t, false, resp["queued"])
		assert.Equal(t, false, resp["accepted"],
			"accepted should be false when nothing is true")
	})

	t.Run("Queued only - accepted=true, applied=false", func(t *testing.T) {
		mock.submitResult = &types.SubmitResult{
			EngineResult:        "terQUEUED",
			EngineResultCode:    -89,
			EngineResultMessage: "Held until escalated fee drops.",
			Applied:             false,
			Broadcast:           false,
			Kept:                false,
			Queued:              true,
			ValidatedLedger:     2,
		}

		paramsJSON := rawSubmitParams(t, baseTxJson, nil)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)

		assert.Equal(t, false, resp["applied"])
		assert.Equal(t, false, resp["broadcast"])
		assert.Equal(t, false, resp["kept"])
		assert.Equal(t, true, resp["queued"])
		assert.Equal(t, true, resp["accepted"],
			"accepted should be true when queued is true (any() = true)")
	})
}
