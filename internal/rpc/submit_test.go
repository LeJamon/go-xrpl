package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLedgerServiceSubmit extends mockLedgerService with submit-specific behavior
type mockLedgerServiceSubmit struct {
	*mockLedgerService
	submitResult *types.SubmitResult
	submitError  error
	storedTxs    map[string][]byte
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

func (m *mockLedgerServiceSubmit) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	if m.submitError != nil {
		return nil, m.submitError
	}
	return m.submitResult, nil
}

func (m *mockLedgerServiceSubmit) StoreTransaction(txHash [32]byte, txData []byte) error {
	// Store the transaction for verification
	hashStr := string(txHash[:])
	m.storedTxs[hashStr] = txData
	return nil
}

// newSubmitTestServices builds a per-test ServiceContainer wrapping mock.
func newSubmitTestServices(mock *mockLedgerServiceSubmit) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger: mock,
	}
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
			expectedError: "Either tx_blob or tx_json must be provided",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing tx_blob and tx_json - nil params",
			params:        nil,
			expectedError: "Either tx_blob or tx_json must be provided",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Empty tx_blob",
			params: map[string]any{
				"tx_blob": "",
			},
			expectedError: "Either tx_blob or tx_json must be provided",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - integer",
			params: map[string]any{
				"tx_blob": 12345,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - boolean",
			params: map[string]any{
				"tx_blob": true,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - array",
			params: map[string]any{
				"tx_blob": []string{"hex1", "hex2"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "tx_blob invalid hex",
			params: map[string]any{
				"tx_blob": "ZZZZ",
			},
			expectedError: "Invalid tx_blob",
			expectedCode:  types.RpcINVALID_PARAMS,
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

			params := map[string]any{
				"tx_json": tc.txJson,
			}
			paramsJSON, err := json.Marshal(params)
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
	}

	t.Run("Response contains all required fields", func(t *testing.T) {
		params := map[string]any{
			"tx_json": map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
			},
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

		// Verify field values for successful submission
		assert.Equal(t, "tesSUCCESS", resp["engine_result"])
		assert.Equal(t, float64(0), resp["engine_result_code"])
		assert.Equal(t, "The transaction was applied. Only final in a validated ledger.", resp["engine_result_message"])
		assert.Equal(t, true, resp["accepted"])
		assert.Equal(t, true, resp["applied"])
		assert.Equal(t, false, resp["queued"])
	})

	t.Run("tx_json is included in response", func(t *testing.T) {
		params := map[string]any{
			"tx_json": map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
			},
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

		// Verify tx_json content
		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok, "tx_json should be a map")
		assert.Equal(t, "Payment", txJson["TransactionType"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", txJson["Account"])
	})
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

			params := map[string]any{
				"tx_json": baseTxJson,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

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
// A tx_json that is not a JSON object cannot be unmarshalled into a transaction
// map, so the handler rejects it with invalidParams rather than silently
// proceeding with an empty map. Object-shaped tx_json is accepted and validated
// downstream by the ledger service.
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
			errorMsg:    "Invalid field 'tx_json'",
			description: "A JSON string is not a transaction object",
		},
		{
			name:        "Number tx_json - rejected",
			txJson:      12345,
			expectError: true,
			errorMsg:    "Invalid field 'tx_json'",
			description: "A JSON number is not a transaction object",
		},
		{
			name:        "Boolean tx_json - rejected",
			txJson:      true,
			expectError: true,
			errorMsg:    "Invalid field 'tx_json'",
			description: "A JSON boolean is not a transaction object",
		},
		{
			name:        "Array tx_json - rejected",
			txJson:      []any{1, 2, 3},
			expectError: true,
			errorMsg:    "Invalid field 'tx_json'",
			description: "A JSON array is not a transaction object",
		},
		{
			name:        "Empty tx_json object - accepted",
			txJson:      map[string]any{},
			expectError: false,
			description: "Empty object is valid, ledger service validates content",
		},
		{
			name: "Valid minimal transaction",
			txJson: map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectError: false,
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
				assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
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

	params := map[string]any{
		"tx_json": map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// TestSubmitMethodServiceNilLedger tests behavior when ledger service is nil
func TestSubmitMethodServiceNilLedger(t *testing.T) {
	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: nil},
	}

	params := map[string]any{
		"tx_json": map[string]any{
			"TransactionType": "Payment",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount":          "1000000",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
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
		name          string
		submitError   error
		expectedError string
	}{
		{
			name:          "Internal error",
			submitError:   errors.New("internal ledger error"),
			expectedError: "Failed to submit transaction",
		},
		{
			name:          "Network error",
			submitError:   errors.New("network unavailable"),
			expectedError: "Failed to submit transaction",
		},
		{
			name:          "Validation error",
			submitError:   errors.New("transaction validation failed"),
			expectedError: "Failed to submit transaction",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.submitError = tc.submitError

			params := map[string]any{
				"tx_json": map[string]any{
					"TransactionType": "Payment",
					"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
					"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"Amount":          "1000000",
				},
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Contains(t, rpcErr.Message, tc.expectedError)
		})
	}
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
			params := map[string]any{
				"tx_json": baseTxJson,
			}
			// Add extra params
			maps.Copy(params, tc.extraParams)

			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

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
		signingParam string
		signingValue string
		description  string
	}{
		{
			name:         "secret parameter",
			signingParam: "secret",
			signingValue: "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9",
			description:  "Traditional secret format for signing",
		},
		{
			name:         "seed parameter",
			signingParam: "seed",
			signingValue: "sn3nxiW7v8KXzPzAqzyHXbSSKNuN9",
			description:  "Seed format for signing",
		},
		{
			name:         "seed_hex parameter",
			signingParam: "seed_hex",
			signingValue: "DEDCE9CE67B451D852FD4E846FCDE31C",
			description:  "Hex-encoded seed for signing",
		},
		{
			name:         "passphrase parameter",
			signingParam: "passphrase",
			signingValue: "masterpassphrase",
			description:  "Passphrase-based key derivation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Parameter: %s, Description: %s", tc.signingParam, tc.description)

			// Omit Account so the signing helper auto-fills it from the
			// derived key. This avoids account mismatch errors.
			params := map[string]any{
				"tx_json": map[string]any{
					"TransactionType": "Payment",
					"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"Amount":          "1000000",
				},
				tc.signingParam: tc.signingValue,
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
			assert.Contains(t, resp, "deprecated",
				"sign-and-submit response must include deprecation warning")

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

// TestSubmitMethodApiV2Response tests API v2 specific response formatting.
// API v2 should include "hash" at the root level of the response.
func TestSubmitMethodApiV2Response(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	services := newSubmitTestServices(mock)

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

		params := map[string]any{
			"tx_json": baseTxJson,
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

		params := map[string]any{
			"tx_json": baseTxJson,
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

		// API v2: hash at root level
		rootHash, hasRootHash := resp["hash"].(string)
		assert.True(t, hasRootHash, "API v2 should have hash at root level")
		assert.NotEmpty(t, rootHash)

		// Also in tx_json
		txJson, ok := resp["tx_json"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, rootHash, txJson["hash"], "root hash and tx_json hash should match")
	})

	t.Run("API v3 has hash at root", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion3,
			Services:   services,
		}

		params := map[string]any{
			"tx_json": baseTxJson,
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
	services := newSubmitTestServices(mock)

	method := &handlers.SubmitMethod{}

	t.Run("API v1 Payment - Amount kept, DeliverMax added", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := map[string]any{
			"tx_json": map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
			},
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

		params := map[string]any{
			"tx_json": map[string]any{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
			},
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

		params := map[string]any{
			"tx_json": map[string]any{
				"TransactionType": "AccountSet",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Fee":             "12",
				"Sequence":        5,
				"SetFlag":         8,
			},
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

		params := map[string]any{"tx_json": baseTxJson}
		paramsJSON, _ := json.Marshal(params)

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

		params := map[string]any{"tx_json": baseTxJson}
		paramsJSON, _ := json.Marshal(params)

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

		params := map[string]any{"tx_json": baseTxJson}
		paramsJSON, _ := json.Marshal(params)

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
