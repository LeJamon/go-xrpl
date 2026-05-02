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

// accountTxMock wraps mockLedgerService and overrides GetAccountTransactions
type accountTxMock struct {
	*mockLedgerService
	getAccountTransactionsFn func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error)
}

func newAccountTxMock() *accountTxMock {
	return &accountTxMock{
		mockLedgerService: newMockLedgerService(),
	}
}

func (m *accountTxMock) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	if m.getAccountTransactionsFn != nil {
		return m.getAccountTransactionsFn(account, ledgerMin, ledgerMax, limit, marker, forward)
	}
	return nil, errors.New("not implemented")
}

// newTestServicesAccountTx builds a *types.ServiceContainer wrapping the mock.
func newTestServicesAccountTx(mock *accountTxMock) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
}

// Error Validation Tests
// Based on rippled AccountTx_test.cpp testParameters()

// TestAccountTxErrorValidation tests error handling for invalid inputs
func TestAccountTxErrorValidation(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
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
			name:          "Missing account field - empty params",
			params:        map[string]interface{}{},
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing account field - nil params",
			params:        nil,
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Malformed account address - hex format",
			params: map[string]interface{}{
				"account": "0xDEADBEEF",
			},
			expectedError: "Malformed account.",
			expectedCode:  35, // actMalformed (address validation)
		},
		{
			name: "Malformed account address - bad checksum",
			params: map[string]interface{}{
				"account": "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			},
			expectedError: "Malformed account.",
			expectedCode:  35, // actMalformed (address validation)
		},
		{
			name: "Account not found - valid format but not in ledger",
			params: map[string]interface{}{
				"account": "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
			},
			expectedError: "Account not found.",
			expectedCode:  19, // actNotFound
			setupMock: func() {
				mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
					return nil, errors.New("account not found")
				}
			},
		},
		{
			name: "Invalid account type - integer",
			params: map[string]interface{}{
				"account": 12345,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - boolean",
			params: map[string]interface{}{
				"account": true,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - null",
			params: map[string]interface{}{
				"account": nil,
			},
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - object",
			params: map[string]interface{}{
				"account": map[string]interface{}{"nested": "value"},
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - array",
			params: map[string]interface{}{
				"account": []string{"value1", "value2"},
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - float",
			params: map[string]interface{}{
				"account": 1.1,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock
			mock.getAccountTransactionsFn = nil

			if tc.setupMock != nil {
				tc.setupMock()
			}

			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError,
				"Error message should contain expected text")
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

// Ledger Index Min/Max Handling Tests
// Based on rippled AccountTx_test.cpp testParameters() ledger_index_min/max sections

func TestAccountTxLedgerIndexMinMax(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Default ledger_index_min=-1 and ledger_index_max=-1", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			// With -1 defaults, the handler should pass through
			assert.Equal(t, validAccount, account)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    2,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account":          validAccount,
			"ledger_index_min": -1,
			"ledger_index_max": -1,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for default ledger index range")
		require.NotNil(t, result)
	})

	t.Run("ledger_index_min=0 and ledger_index_max=0 (omitted)", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			// When omitted, Go zero values are 0. The handler passes them through.
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    2,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error when min/max omitted")
		require.NotNil(t, result)
	})

	t.Run("Specific ledger range with transactions", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, int64(1), ledgerMin)
			assert.Equal(t, int64(3), ledgerMax)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    3,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account":          validAccount,
			"ledger_index_min": 1,
			"ledger_index_max": 3,
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

		assert.Equal(t, float64(1), resp["ledger_index_min"])
		assert.Equal(t, float64(3), resp["ledger_index_max"])
	})
}

// Binary vs JSON Mode Tests
// Based on rippled AccountTx_test.cpp binary parameter

func TestAccountTxBinaryMode(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Create a sample transaction hash
	txHash := [32]byte{}
	for i := range txHash {
		txHash[i] = byte(i + 1)
	}

	// A minimal valid serialized tx blob and meta for testing binary mode.
	// These are hex-encoded placeholders that represent raw binary data.
	txBlobBytes, _ := hex.DecodeString("1200002200000000240000000361D4838D7EA4C680000000000000000000000000005553440000000000E6C92BF47A692162751F6017CF3E40B4AE15285568400000000000000A7321ED5F5AC43F527AE97194A1B29F2E8831A2AEE056431FC596590B5F3F5769AF70774473045022100")
	metaBytes, _ := hex.DecodeString("201C00000001")

	t.Run("Binary mode returns tx_blob and meta_blob as hex (API v2)", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:   account,
				LedgerMin: 1,
				LedgerMax: 5,
				Limit:     200,
				Transactions: []types.AccountTransaction{
					{
						Hash:        txHash,
						LedgerIndex: 3,
						TxBlob:      txBlobBytes,
						Meta:        metaBytes,
					},
				},
				Validated: true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"binary":  true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error in binary mode")
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txs := resp["transactions"].([]interface{})
		require.Len(t, txs, 1)

		tx0 := txs[0].(map[string]interface{})
		// In binary mode (API v2), should have tx_blob and meta_blob as hex strings
		assert.Contains(t, tx0, "tx_blob", "Binary mode should return tx_blob")
		assert.Contains(t, tx0, "meta_blob", "Binary mode should return meta_blob")
		assert.Contains(t, tx0, "ledger_index", "Binary mode should return ledger_index")
		assert.Equal(t, true, tx0["validated"])

		// tx_blob should be uppercase hex
		txBlobStr, ok := tx0["tx_blob"].(string)
		assert.True(t, ok, "tx_blob should be a string")
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(txBlobBytes)), txBlobStr)

		// meta_blob should be uppercase hex
		metaBlobStr, ok := tx0["meta_blob"].(string)
		assert.True(t, ok, "meta_blob should be a string")
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(metaBytes)), metaBlobStr)
	})

	t.Run("JSON mode returns decoded tx and meta objects", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:   account,
				LedgerMin: 1,
				LedgerMax: 5,
				Limit:     200,
				Transactions: []types.AccountTransaction{
					{
						Hash:        txHash,
						LedgerIndex: 3,
						TxBlob:      txBlobBytes,
						Meta:        metaBytes,
					},
				},
				Validated: true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"binary":  false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error in JSON mode")
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txs := resp["transactions"].([]interface{})
		require.Len(t, txs, 1)

		tx0 := txs[0].(map[string]interface{})
		// In JSON mode, should have "tx" or "tx_blob" (if decode fails, falls back to hex)
		// Either way, hash and validated should be present
		assert.Contains(t, tx0, "hash", "JSON mode should return hash")
		assert.Equal(t, true, tx0["validated"])
	})
}

// Forward / Reverse Ordering Tests
// Based on rippled AccountTx_test.cpp forward parameter

func TestAccountTxForwardReverse(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Forward=true passes forward flag", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.True(t, forward, "forward flag should be true")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"forward": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Forward=false (default reverse ordering)", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.False(t, forward, "forward flag should be false")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"forward": false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Forward omitted defaults to false", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.False(t, forward, "forward flag should default to false")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// Marker-Based Pagination Tests
// Based on rippled AccountTx_test.cpp marker handling

func TestAccountTxMarkerPagination(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("No marker returns first page", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Nil(t, marker, "marker should be nil for first page")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        2,
				Transactions: []types.AccountTransaction{},
				Marker:       &types.AccountTxMarker{LedgerSeq: 5, TxnSeq: 1},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   2,
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

		// Response should include marker for next page
		assert.Contains(t, resp, "marker")
		markerObj := resp["marker"].(map[string]interface{})
		assert.Equal(t, float64(5), markerObj["ledger"])
		assert.Equal(t, float64(1), markerObj["seq"])
	})

	t.Run("Marker passed to service for next page", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			require.NotNil(t, marker, "marker should be provided for second page")
			assert.Equal(t, uint32(5), marker.LedgerSeq)
			assert.Equal(t, uint32(1), marker.TxnSeq)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        2,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
				// No marker means last page
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   2,
			"marker": map[string]interface{}{
				"ledger": 5,
				"seq":    1,
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

		// No marker means last page
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "Last page should not have marker")
	})
}

// Response Structure Tests
// Based on rippled AccountTx_test.cpp - validates response fields

func TestAccountTxResponseStructure(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Response contains all required fields", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
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

		// Check required top-level fields
		assert.Contains(t, resp, "account")
		assert.Contains(t, resp, "ledger_index_min")
		assert.Contains(t, resp, "ledger_index_max")
		assert.Contains(t, resp, "limit")
		assert.Contains(t, resp, "transactions")
		assert.Contains(t, resp, "validated")

		// Check field values
		assert.Equal(t, validAccount, resp["account"])
		assert.Equal(t, float64(1), resp["ledger_index_min"])
		assert.Equal(t, float64(10), resp["ledger_index_max"])
		assert.Equal(t, float64(200), resp["limit"])
		assert.Equal(t, true, resp["validated"])

		// transactions should be an array
		txs, ok := resp["transactions"].([]interface{})
		assert.True(t, ok, "transactions should be an array")
		assert.Len(t, txs, 0)
	})

	t.Run("Response with marker present", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        5,
				Transactions: []types.AccountTransaction{},
				Marker:       &types.AccountTxMarker{LedgerSeq: 7, TxnSeq: 3},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   5,
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

		assert.Contains(t, resp, "marker")
		markerObj := resp["marker"].(map[string]interface{})
		assert.Equal(t, float64(7), markerObj["ledger"])
		assert.Equal(t, float64(3), markerObj["seq"])
	})

	t.Run("Response without marker when no more results", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
				// Marker is nil
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
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

		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "No marker expected when all results returned")
	})
}

// Empty Account (No Transactions) Tests

func TestAccountTxEmptyAccount(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:      account,
			LedgerMin:    1,
			LedgerMax:    10,
			Limit:        200,
			Transactions: []types.AccountTransaction{},
			Validated:    true,
		}, nil
	}

	params := map[string]interface{}{
		"account": validAccount,
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

	txs := resp["transactions"].([]interface{})
	assert.Len(t, txs, 0, "Empty account should have no transactions")
	assert.Equal(t, validAccount, resp["account"])
	assert.Equal(t, true, resp["validated"])
}

// Multiple Transactions with Correct Hash Formatting Tests

func TestAccountTxMultipleTransactions(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Create multiple transaction hashes
	hash1 := [32]byte{}
	hash2 := [32]byte{}
	hash3 := [32]byte{}
	for i := range hash1 {
		hash1[i] = byte(i + 1)
		hash2[i] = byte(i + 0x20)
		hash3[i] = byte(i + 0x40)
	}

	txBlob := []byte{0x12, 0x00, 0x00}
	meta := []byte{0x20, 0x1C, 0x00, 0x00, 0x00, 0x01}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 10,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{Hash: hash1, LedgerIndex: 3, TxBlob: txBlob, Meta: meta},
				{Hash: hash2, LedgerIndex: 4, TxBlob: txBlob, Meta: meta},
				{Hash: hash3, LedgerIndex: 5, TxBlob: txBlob, Meta: meta},
			},
			Validated: true,
		}, nil
	}

	t.Run("Binary mode - API v2 fields", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		params := map[string]interface{}{
			"account": validAccount,
			"binary":  true,
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

		txs := resp["transactions"].([]interface{})
		require.Len(t, txs, 3, "Should return 3 transactions")

		tx0 := txs[0].(map[string]interface{})
		tx1 := txs[1].(map[string]interface{})
		tx2 := txs[2].(map[string]interface{})

		// Verify each transaction has validated=true
		assert.Equal(t, true, tx0["validated"])
		assert.Equal(t, true, tx1["validated"])
		assert.Equal(t, true, tx2["validated"])

		// Verify ledger_index in binary mode
		assert.Equal(t, float64(3), tx0["ledger_index"])
		assert.Equal(t, float64(4), tx1["ledger_index"])
		assert.Equal(t, float64(5), tx2["ledger_index"])

		// Binary mode uses meta_blob, not meta
		assert.Contains(t, tx0, "meta_blob", "Binary v2 should have meta_blob")
		assert.Contains(t, tx0, "tx_blob", "Binary v2 should have tx_blob")
	})

	t.Run("JSON mode - hash at entry level", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		params := map[string]interface{}{
			"account": validAccount,
			"binary":  false,
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

		txs := resp["transactions"].([]interface{})
		require.Len(t, txs, 3, "Should return 3 transactions")

		// Verify hash formatting - should be uppercase hex at entry level
		expectedHash1 := strings.ToUpper(hex.EncodeToString(hash1[:]))
		expectedHash2 := strings.ToUpper(hex.EncodeToString(hash2[:]))
		expectedHash3 := strings.ToUpper(hex.EncodeToString(hash3[:]))

		tx0 := txs[0].(map[string]interface{})
		tx1 := txs[1].(map[string]interface{})
		tx2 := txs[2].(map[string]interface{})

		assert.Equal(t, expectedHash1, tx0["hash"], "Hash 1 should be uppercase hex")
		assert.Equal(t, expectedHash2, tx1["hash"], "Hash 2 should be uppercase hex")
		assert.Equal(t, expectedHash3, tx2["hash"], "Hash 3 should be uppercase hex")

		// Verify each transaction has validated=true
		assert.Equal(t, true, tx0["validated"])
		assert.Equal(t, true, tx1["validated"])
		assert.Equal(t, true, tx2["validated"])

		// Verify ledger_index at entry level
		assert.Equal(t, float64(3), tx0["ledger_index"])
		assert.Equal(t, float64(4), tx1["ledger_index"])
		assert.Equal(t, float64(5), tx2["ledger_index"])
	})
}

// Service Unavailable / Nil Ledger Tests

func TestAccountTxServiceUnavailable(t *testing.T) {
	method := &handlers.AccountTxMethod{}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	t.Run("Services is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Services.Ledger is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// Transaction History Not Available Tests

func TestAccountTxTransactionHistoryNotAvailable(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return nil, errors.New("transaction history not available (no database configured)")
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, 73, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Transaction history not available")
}

// Method Metadata Tests

func TestAccountTxMethodMetadata(t *testing.T) {
	method := &handlers.AccountTxMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_tx should be accessible to guests")
	})

	t.Run("SupportedApiVersions includes v1, v2, and v3", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// Limit Parameter Tests

func TestAccountTxLimitParameter(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Custom limit is passed to service", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, uint32(10), limit, "Limit should be passed through")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        10,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   10,
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

		assert.Equal(t, float64(10), resp["limit"])
	})

	t.Run("Default limit (0) when not specified", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, uint32(0), limit, "Limit should default to 0 when not specified")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// InjectDeliveredAmount Tests

func TestAccountTxInjectDeliveredAmount(t *testing.T) {
	// Test the InjectDeliveredAmount function directly via exported function name
	// Since the function is unexported (injectDeliveredAmount), we test it
	// indirectly through the handler's JSON mode behavior.

	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// We test indirectly: when a Payment transaction is decoded in JSON mode,
	// the handler should inject DeliveredAmount into the metadata.
	// Since we can't easily construct a valid binary Payment blob in unit tests
	// without the full codec, we verify the handler doesn't crash with
	// minimal blobs and that the overall flow works.

	txBlob := []byte{0x12, 0x00, 0x00}
	meta := []byte{0x20, 0x1C}

	txHash := [32]byte{0xAA, 0xBB, 0xCC}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 5,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{
					Hash:        txHash,
					LedgerIndex: 3,
					TxBlob:      txBlob,
					Meta:        meta,
				},
			},
			Validated: true,
		}, nil
	}

	params := map[string]interface{}{
		"account": validAccount,
		"binary":  false,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	// This should not panic even with minimal/invalid tx blobs
	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Handler should not error on decode failure, should fallback")
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Verify the transactions array exists
	txs := resp["transactions"].([]interface{})
	require.Len(t, txs, 1)

	tx0 := txs[0].(map[string]interface{})
	// Hash should always be present regardless of decode success
	assert.Contains(t, tx0, "hash")
	expectedHash := strings.ToUpper(hex.EncodeToString(txHash[:]))
	assert.Equal(t, expectedHash, tx0["hash"])
}

// Service Error Propagation Tests

func TestAccountTxServiceErrors(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Generic service error", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, errors.New("database connection failed")
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Failed to get account transactions")
	})

	t.Run("Account not found error", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, errors.New("account not found")
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, 19, rpcErr.Code) // actNotFound
		assert.Contains(t, rpcErr.Message, "Account not found.")
	})

	t.Run("Transaction history not available", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, errors.New("transaction history not available (no database configured)")
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, 73, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Transaction history not available")
	})
}

// Validated Field Tests
// Based on rippled AccountTx_test.cpp - validated flag in each transaction

func TestAccountTxValidatedField(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	txHash := [32]byte{0x01}
	txBlob := []byte{0x12, 0x00}
	meta := []byte{0x20, 0x1C}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 10,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{Hash: txHash, LedgerIndex: 3, TxBlob: txBlob, Meta: meta},
			},
			Validated: true,
		}, nil
	}

	// Test in binary mode where we can check the validated flag easily
	params := map[string]interface{}{
		"account": validAccount,
		"binary":  true,
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

	// Top-level validated
	assert.Equal(t, true, resp["validated"])

	// Per-transaction validated
	txs := resp["transactions"].([]interface{})
	require.Len(t, txs, 1)
	tx0 := txs[0].(map[string]interface{})
	assert.Equal(t, true, tx0["validated"],
		"Each transaction entry should have validated=true")
}

// Account parameter passed to service correctly

func TestAccountTxAccountPassedToService(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		assert.Equal(t, validAccount, account, "Account should be passed to service")
		return &types.AccountTxResult{
			Account:      account,
			LedgerMin:    1,
			LedgerMax:    5,
			Limit:        200,
			Transactions: []types.AccountTransaction{},
			Validated:    true,
		}, nil
	}

	params := map[string]interface{}{
		"account": validAccount,
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

	assert.Equal(t, validAccount, resp["account"],
		"Response should echo back the account")
}
