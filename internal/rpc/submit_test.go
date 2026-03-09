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

func (m *mockLedgerServiceSubmit) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
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

// setupTestServicesSubmit initializes the Services singleton with a submit mock for testing
func setupTestServicesSubmit(mock *mockLedgerServiceSubmit) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// TestSubmitMethodErrorValidation tests error handling for invalid inputs
func TestSubmitMethodErrorValidation(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        interface{}
		expectedError string
		expectedCode  int
		setupMock     func()
	}{
		{
			name:          "Missing tx_blob and tx_json - empty params",
			params:        map[string]interface{}{},
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
			params: map[string]interface{}{
				"tx_blob": "",
			},
			expectedError: "Either tx_blob or tx_json must be provided",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - integer",
			params: map[string]interface{}{
				"tx_blob": 12345,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - boolean",
			params: map[string]interface{}{
				"tx_blob": true,
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid tx_blob type - array",
			params: map[string]interface{}{
				"tx_blob": []string{"hex1", "hex2"},
			},
			expectedError: "Invalid parameters",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "tx_blob submission not implemented",
			params: map[string]interface{}{
				"tx_blob": "1200002200000000240000000161D4838D7EA4C6800000000000000000000000000055534400000000000000000000000000000000000000000000000168400000000000000A732103AB40A0490F9B7ED8DF29D246BF2D6269820A0EE7742ACDD457BEA7C7D0931EDB74473045022100",
			},
			expectedError: "tx_blob submission not yet implemented",
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

			// Setup mock if needed
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

			// Call the method
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
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name         string
		txJson       map[string]interface{}
		mockResult   *types.SubmitResult
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Valid Payment transaction",
			txJson: map[string]interface{}{
				"TransactionType": "Payment",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Amount":          "1000000",
				"Fee":             "10",
				"Sequence":        1,
				"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
				"TxnSignature":    "3045022100...",
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
				assert.Equal(t, float64(0), resp["engine_result_code"])
				assert.Equal(t, true, resp["applied"])
				assert.Equal(t, true, resp["accepted"])
				assert.Contains(t, resp, "tx_json")
			},
		},
		{
			name: "Valid AccountSet transaction",
			txJson: map[string]interface{}{
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
				assert.Equal(t, true, resp["applied"])
			},
		},
		{
			name: "Valid TrustSet transaction",
			txJson: map[string]interface{}{
				"TransactionType": "TrustSet",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"LimitAmount": map[string]interface{}{
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock result
			mock.submitResult = tc.mockResult
			mock.submitError = nil

			params := map[string]interface{}{
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
			var respMap map[string]interface{}
			err = json.Unmarshal(resultJSON, &respMap)
			require.NoError(t, err)

			tc.validateResp(t, respMap)
		})
	}
}

// TestSubmitMethodResponseFields tests that response contains expected fields
func TestSubmitMethodResponseFields(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
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
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
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
		var resp map[string]interface{}
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
		assert.Contains(t, resp, "current_ledger_index")

		// Verify field values for successful submission
		assert.Equal(t, "tesSUCCESS", resp["engine_result"])
		assert.Equal(t, float64(0), resp["engine_result_code"])
		assert.Equal(t, "The transaction was applied. Only final in a validated ledger.", resp["engine_result_message"])
		assert.Equal(t, true, resp["accepted"])
		assert.Equal(t, true, resp["applied"])
		assert.Equal(t, false, resp["queued"])
	})

	t.Run("tx_json is included in response", func(t *testing.T) {
		params := map[string]interface{}{
			"tx_json": map[string]interface{}{
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
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Verify tx_json content
		txJson, ok := resp["tx_json"].(map[string]interface{})
		require.True(t, ok, "tx_json should be a map")
		assert.Equal(t, "Payment", txJson["TransactionType"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", txJson["Account"])
	})
}

// TestSubmitMethodEngineResults tests various engine result codes
func TestSubmitMethodEngineResults(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	baseTxJson := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	tests := []struct {
		name               string
		engineResult       string
		engineResultCode   int
		engineResultMsg    string
		applied            bool
		expectedStatus     string
		validateResp       func(t *testing.T, resp map[string]interface{})
	}{
		{
			name:             "tesSUCCESS",
			engineResult:     "tesSUCCESS",
			engineResultCode: 0,
			engineResultMsg:  "The transaction was applied. Only final in a validated ledger.",
			applied:          true,
			expectedStatus:   "success",
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, true, resp["applied"])
				assert.Equal(t, "success", resp["status"])
			},
		},
		{
			name:             "tecCLAIM - Claimed cost only",
			engineResult:     "tecCLAIM",
			engineResultCode: 100,
			engineResultMsg:  "Fee claimed. No action.",
			applied:          true,
			expectedStatus:   "success", // tec codes are still "successful"
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tefPAST_SEQ", resp["engine_result"])
				assert.Equal(t, false, resp["applied"])
				assert.Equal(t, "error", resp["status"])
			},
		},
		{
			name:             "tefMAX_LEDGER - Max ledger exceeded",
			engineResult:     "tefMAX_LEDGER",
			engineResultCode: -186,
			engineResultMsg:  "Ledger sequence too high.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tefMAX_LEDGER", resp["engine_result"])
			},
		},
		{
			name:             "temBAD_AMOUNT - Invalid amount",
			engineResult:     "temBAD_AMOUNT",
			engineResultCode: -298,
			engineResultMsg:  "Can only send positive amounts.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "temBAD_AMOUNT", resp["engine_result"])
				assert.Equal(t, "error", resp["status"])
			},
		},
		{
			name:             "temBAD_FEE - Invalid fee",
			engineResult:     "temBAD_FEE",
			engineResultCode: -299,
			engineResultMsg:  "Invalid fee value.",
			applied:          false,
			expectedStatus:   "error",
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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

			params := map[string]interface{}{
				"tx_json": baseTxJson,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Submit should not return RPC error even for transaction failures")
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]interface{}
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

// TestSubmitMethodMalformedTransaction tests malformed transaction handling
// Note: The current implementation accepts tx_json as raw JSON and passes it to
// the ledger service for validation. Type checking of tx_json content happens
// during the unmarshal to map[string]interface{} in the method itself.
func TestSubmitMethodMalformedTransaction(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	// Note: The current implementation uses json.RawMessage for tx_json,
	// which means it accepts any valid JSON. The tests below document
	// the actual behavior, where validation happens in the ledger service.
	tests := []struct {
		name        string
		txJson      interface{}
		expectError bool
		errorMsg    string
		description string
	}{
		{
			name:        "String tx_json - passed to ledger service",
			txJson:      "not a valid json object",
			expectError: false, // Current impl accepts, validates in ledger service
			description: "String is valid JSON, passed to ledger service",
		},
		{
			name:        "Number tx_json - passed to ledger service",
			txJson:      12345,
			expectError: false, // Current impl accepts, validates in ledger service
			description: "Number is valid JSON, passed to ledger service",
		},
		{
			name:        "Boolean tx_json - passed to ledger service",
			txJson:      true,
			expectError: false, // Current impl accepts, validates in ledger service
			description: "Boolean is valid JSON, passed to ledger service",
		},
		{
			name:        "Array tx_json - passed to ledger service",
			txJson:      []interface{}{1, 2, 3},
			expectError: false, // Current impl accepts, validates in ledger service
			description: "Array is valid JSON, passed to ledger service",
		},
		{
			name:        "Empty tx_json object - accepted",
			txJson:      map[string]interface{}{},
			expectError: false,
			description: "Empty object is valid, ledger service validates content",
		},
		{
			name: "Valid minimal transaction",
			txJson: map[string]interface{}{
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

			params := map[string]interface{}{
				"tx_json": tc.txJson,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result, "Expected nil result for error case")
				require.NotNil(t, rpcErr, "Expected RPC error")
				assert.Contains(t, rpcErr.Message, tc.errorMsg)
			} else {
				// Current implementation accepts any JSON and passes to ledger service
				// This documents the actual behavior
				require.Nil(t, rpcErr, "Expected no error - validation in ledger service")
				require.NotNil(t, result)
			}
		})
	}
}

// TestSubmitMethodServiceUnavailable tests behavior when ledger service is not available
func TestSubmitMethodServiceUnavailable(t *testing.T) {
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
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
	oldServices := types.Services
	types.Services = &types.ServiceContainer{Ledger: nil}
	defer func() { types.Services = oldServices }()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"tx_json": map[string]interface{}{
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
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
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

			params := map[string]interface{}{
				"tx_json": map[string]interface{}{
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
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	baseTxJson := map[string]interface{}{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
	}

	tests := []struct {
		name         string
		extraParams  map[string]interface{}
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "fail_hard parameter",
			extraParams: map[string]interface{}{
				"fail_hard": true,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// fail_hard is accepted but doesn't change success response
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "offline parameter",
			extraParams: map[string]interface{}{
				"offline": true,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "build_path parameter",
			extraParams: map[string]interface{}{
				"build_path": true,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "fee_mult_max parameter",
			extraParams: map[string]interface{}{
				"fee_mult_max": 10,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "fee_div_max parameter",
			extraParams: map[string]interface{}{
				"fee_div_max": 1,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
		{
			name: "multiple optional parameters",
			extraParams: map[string]interface{}{
				"fail_hard":    true,
				"offline":      false,
				"fee_mult_max": 10,
				"fee_div_max":  1,
			},
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, "tesSUCCESS", resp["engine_result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"tx_json": baseTxJson,
			}
			// Add extra params
			for k, v := range tc.extraParams {
				params[k] = v
			}

			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error")
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]interface{}
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			tc.validateResp(t, resp)
		})
	}
}

// TestSubmitMethodSigningCredentials documents signing credential handling
// Note: The current implementation has TODO markers for signing
func TestSubmitMethodSigningCredentials(t *testing.T) {
	mock := newMockLedgerServiceSubmit()
	cleanup := setupTestServicesSubmit(mock)
	defer cleanup()

	method := &handlers.SubmitMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion1,
	}

	// These tests document expected behavior for signing credentials
	// The current implementation passes through tx_json without signing

	tests := []struct {
		name          string
		signingParam  string
		signingValue  string
		description   string
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
			// Document expected behavior
			t.Logf("Parameter: %s, Description: %s", tc.signingParam, tc.description)

			// The current implementation accepts these parameters without error
			// but doesn't perform actual signing
			params := map[string]interface{}{
				"tx_json": map[string]interface{}{
					"TransactionType": "Payment",
					"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
					"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"Amount":          "1000000",
				},
				tc.signingParam: tc.signingValue,
			}

			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			// Currently the method succeeds (signing is TODO)
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
		})
	}
}
