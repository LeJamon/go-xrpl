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

// mockLedgerService implements LedgerService for testing
type mockLedgerService struct {
	accountInfo          *types.AccountInfo
	accountInfoErr       error
	currentLedgerIndex   uint32
	closedLedgerIndex    uint32
	validatedLedgerIndex uint32
	standalone           bool
	serverInfo           types.LedgerServerInfo
	amendmentBlocked     bool
}

func newMockLedgerService() *mockLedgerService {
	return &mockLedgerService{
		currentLedgerIndex:   3,
		closedLedgerIndex:    2,
		validatedLedgerIndex: 2,
		standalone:           true,
		serverInfo: types.LedgerServerInfo{
			Standalone:         true,
			OpenLedgerSeq:      3,
			ClosedLedgerSeq:    2,
			ValidatedLedgerSeq: 2,
			CompleteLedgers:    "1-2",
		},
	}
}

func (m *mockLedgerService) GetCurrentLedgerIndex() uint32         { return m.currentLedgerIndex }
func (m *mockLedgerService) GetClosedLedgerIndex() uint32          { return m.closedLedgerIndex }
func (m *mockLedgerService) GetValidatedLedgerIndex() uint32       { return m.validatedLedgerIndex }
func (m *mockLedgerService) AcceptLedger() (uint32, error)         { return m.closedLedgerIndex + 1, nil }
func (m *mockLedgerService) IsStandalone() bool                    { return m.standalone }
func (m *mockLedgerService) GetServerInfo() types.LedgerServerInfo { return m.serverInfo }
func (m *mockLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
	if m.accountInfoErr != nil {
		return nil, m.accountInfoErr
	}
	if m.accountInfo != nil {
		return m.accountInfo, nil
	}
	// Default account info for valid accounts
	return &types.AccountInfo{
		Account:     account,
		Balance:     "100000000",
		Flags:       0,
		OwnerCount:  0,
		Sequence:    1,
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}, nil
}
func (m *mockLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockLedgerService) IsAmendmentBlocked() bool { return m.amendmentBlocked }
func (m *mockLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// setupTestServices initializes the Services singleton with a mock for testing.
// Accepts any type that implements types.LedgerService.
func setupTestServices(mock types.LedgerService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// TestAccountInfoErrorValidation tests error handling for invalid inputs
// Based on rippled AccountInfo_test.cpp testErrors()
func TestAccountInfoErrorValidation(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
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
			name: "Invalid account type - integer",
			params: map[string]interface{}{
				"account": 12345,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - float",
			params: map[string]interface{}{
				"account": 1.5,
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
			// Note: JSON null gets unmarshaled as empty string in Go, triggering missing parameter error
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
			name: "Malformed account address - node public key format",
			params: map[string]interface{}{
				"account": "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "Malformed account address - seed format",
			params: map[string]interface{}{
				"account": "foo",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "Account not found - valid format but not in ledger",
			params: map[string]interface{}{
				"account": "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
			},
			expectedError: "Account not found.",
			expectedCode:  19, // actNotFound
			setupMock: func() {
				mock.accountInfoErr = errors.New("account not found")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountInfo = nil
			mock.accountInfoErr = nil

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
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

// TestAccountInfoLedgerSpecification tests different ledger index specifications
// Based on rippled's ledger specification behavior
func TestAccountInfoLedgerSpecification(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name         string
		params       map[string]interface{}
		setupMock    func()
		expectError  bool
		expectedCode int
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "ledger_index: validated",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.accountInfo = &types.AccountInfo{
					Account:     validAccount,
					Balance:     "100000000000",
					Flags:       0,
					OwnerCount:  0,
					Sequence:    1,
					LedgerIndex: 2,
					LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
					Validated:   true,
				}
				mock.accountInfoErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "ledger_index: current",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": "current",
			},
			setupMock: func() {
				mock.accountInfo = &types.AccountInfo{
					Account:     validAccount,
					Balance:     "100000000000",
					Flags:       0,
					OwnerCount:  0,
					Sequence:    1,
					LedgerIndex: 3,
					LedgerHash:  "5BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
					Validated:   false,
				}
				mock.accountInfoErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Current ledger may not be validated
				accountData := resp["account_data"].(map[string]interface{})
				assert.Equal(t, validAccount, accountData["Account"])
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": 2,
			},
			setupMock: func() {
				mock.accountInfo = &types.AccountInfo{
					Account:     validAccount,
					Balance:     "100000000000",
					Flags:       0,
					OwnerCount:  0,
					Sequence:    1,
					LedgerIndex: 2,
					LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
					Validated:   true,
				}
				mock.accountInfoErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				ledgerIndex := resp["ledger_index"]
				// Convert to float64 since JSON unmarshals numbers as float64
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				case int:
					assert.Equal(t, 2, v)
				default:
					t.Logf("ledger_index type: %T, value: %v", ledgerIndex, ledgerIndex)
				}
			},
		},
		{
			name: "ledger_index: invalid string",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": "invalid_ledger",
			},
			setupMock: func() {
				// The implementation should handle invalid ledger index
				mock.accountInfo = nil
				mock.accountInfoErr = errors.New("ledger index malformed")
			},
			expectError:  true,
			expectedCode: -32603, // Internal error for ledger not found
		},
		{
			name: "ledger_hash: valid hash",
			params: map[string]interface{}{
				"account":     validAccount,
				"ledger_hash": "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			},
			setupMock: func() {
				mock.accountInfo = &types.AccountInfo{
					Account:     validAccount,
					Balance:     "100000000000",
					Flags:       0,
					OwnerCount:  0,
					Sequence:    1,
					LedgerIndex: 2,
					LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
					Validated:   true,
				}
				mock.accountInfoErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "ledger_hash")
			},
		},
		{
			name: "ledger_hash: invalid hash - not found",
			params: map[string]interface{}{
				"account":     validAccount,
				"ledger_hash": "0000000000000000000000000000000000000000000000000000000000000000",
			},
			setupMock: func() {
				mock.accountInfo = nil
				mock.accountInfoErr = errors.New("ledger not found")
			},
			expectError:  true,
			expectedCode: -32603, // Internal error
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset and setup mock
			mock.accountInfo = nil
			mock.accountInfoErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			// Call the method
			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result, "Expected nil result for error case")
				require.NotNil(t, rpcErr, "Expected RPC error")
				if tc.expectedCode != 0 {
					assert.Equal(t, tc.expectedCode, rpcErr.Code)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
				require.NotNil(t, result, "Expected result")

				// Convert result to map for validation
				resultJSON, err := json.Marshal(result)
				require.NoError(t, err)
				var respMap map[string]interface{}
				err = json.Unmarshal(resultJSON, &respMap)
				require.NoError(t, err)

				if tc.validateResp != nil {
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// TestAccountInfoResponseFields tests that the response contains expected fields
// Based on rippled account_info response structure
func TestAccountInfoResponseFields(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Basic account_data fields", func(t *testing.T) {
		mock.accountInfo = &types.AccountInfo{
			Account:     validAccount,
			Balance:     "100000000000",
			Flags:       131072, // lsfDefaultRipple
			OwnerCount:  5,
			Sequence:    42,
			LedgerIndex: 2,
			LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			Validated:   true,
		}
		mock.accountInfoErr = nil

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		// Convert to map
		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check top-level fields
		assert.Contains(t, resp, "account_data")
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check account_data fields
		accountData := resp["account_data"].(map[string]interface{})
		assert.Equal(t, validAccount, accountData["Account"])
		assert.Equal(t, "100000000000", accountData["Balance"])
		assert.Equal(t, float64(131072), accountData["Flags"])
		assert.Equal(t, float64(5), accountData["OwnerCount"])
		assert.Equal(t, float64(42), accountData["Sequence"])
		assert.Equal(t, "AccountRoot", accountData["LedgerEntryType"])
	})

	t.Run("Optional fields present when set", func(t *testing.T) {
		mock.accountInfo = &types.AccountInfo{
			Account:      validAccount,
			Balance:      "100000000000",
			Flags:        0,
			OwnerCount:   0,
			Sequence:     1,
			RegularKey:   "rrrrrrrrrrrrrrrrrrrrBZbvji",
			Domain:       "6578616D706C652E636F6D", // "example.com" in hex
			EmailHash:    "98b8a86c8e1f7e89c04ab4ad8ecb8621",
			TransferRate: 1002000000,
			TickSize:     5,
			LedgerIndex:  2,
			LedgerHash:   "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			Validated:    true,
		}
		mock.accountInfoErr = nil

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		// Convert to map
		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		accountData := resp["account_data"].(map[string]interface{})
		assert.Equal(t, "rrrrrrrrrrrrrrrrrrrrBZbvji", accountData["RegularKey"])
		assert.Equal(t, "6578616D706C652E636F6D", accountData["Domain"])
		assert.Equal(t, "98b8a86c8e1f7e89c04ab4ad8ecb8621", accountData["EmailHash"])
		assert.Equal(t, float64(1002000000), accountData["TransferRate"])
		assert.Equal(t, float64(5), accountData["TickSize"])
	})

	t.Run("queue_data when queue=true and current ledger", func(t *testing.T) {
		mock.accountInfo = &types.AccountInfo{
			Account:     validAccount,
			Balance:     "100000000000",
			Flags:       0,
			OwnerCount:  0,
			Sequence:    1,
			LedgerIndex: 3,
			LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			Validated:   false,
		}
		mock.accountInfoErr = nil

		params := map[string]interface{}{
			"account":      validAccount,
			"queue":        true,
			"ledger_index": "current",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		// Convert to map
		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check queue_data is present
		assert.Contains(t, resp, "queue_data")
		queueData := resp["queue_data"].(map[string]interface{})
		assert.Contains(t, queueData, "auth_change_queued")
		assert.Contains(t, queueData, "highest_sequence")
		assert.Contains(t, queueData, "lowest_sequence")
		assert.Contains(t, queueData, "max_spend_drops_total")
		assert.Contains(t, queueData, "transactions")
		assert.Contains(t, queueData, "txn_count")
	})

	t.Run("signer_lists when signer_lists=true", func(t *testing.T) {
		mock.accountInfo = &types.AccountInfo{
			Account:     validAccount,
			Balance:     "100000000000",
			Flags:       0,
			OwnerCount:  0,
			Sequence:    1,
			LedgerIndex: 2,
			LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			Validated:   true,
		}
		mock.accountInfoErr = nil

		params := map[string]interface{}{
			"account":      validAccount,
			"signer_lists": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		// Convert to map
		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check signer_lists is present under account_data (API v1 behavior)
		accountData := resp["account_data"].(map[string]interface{})
		assert.Contains(t, accountData, "signer_lists")
		signerLists := accountData["signer_lists"].([]interface{})
		assert.NotNil(t, signerLists)
	})
}

// TestAccountInfoInvalidAccountTypes tests various invalid account parameter types
// Based on rippled AccountInfo_test.cpp testInvalidAccountParam
func TestAccountInfoInvalidAccountTypes(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// These test cases mirror rippled's testInvalidAccountParam lambda
	invalidParams := []struct {
		name  string
		value interface{}
	}{
		{"integer", 1},
		{"float", 1.1},
		{"boolean true", true},
		{"boolean false", false},
		{"null", nil},
		{"empty object", map[string]interface{}{}},
		{"non-empty object", map[string]interface{}{"key": "value"}},
		{"empty array", []interface{}{}},
		{"non-empty array", []interface{}{"value1", "value2"}},
		{"negative integer", -1},
		{"zero", 0},
		{"large integer", 9999999999999},
	}

	for _, tc := range invalidParams {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"account": tc.value,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for invalid account type")
			require.NotNil(t, rpcErr, "Expected RPC error for invalid account type")
			// Should return invalid params error
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code,
				"Expected invalidParams error code for type: %s", tc.name)
		})
	}
}

// TestAccountInfoMalformedAddresses tests various malformed address formats
// Based on rippled AccountInfo_test.cpp malformed account tests
func TestAccountInfoMalformedAddresses(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	malformedAddresses := []struct {
		name         string
		address      string
		expectedCode int
	}{
		{"node public key format", "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV", types.RpcACT_MALFORMED},
		{"seed string", "foo", types.RpcACT_MALFORMED},
		{"short string", "r", types.RpcACT_MALFORMED},
		{"empty string", "", types.RpcINVALID_PARAMS},
		{"too short address", "rHb9CJAWyB4rj91VRWn96DkukG", types.RpcACT_MALFORMED},
		{"too long address", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyThExtraChars", types.RpcACT_MALFORMED},
		{"invalid characters", "rHb9CJAWyB4rj91VRWn96DkukG4bwdty!@", types.RpcACT_MALFORMED},
		{"lowercase prefix", "rhb9cjAWyB4rj91VRWn96DkukG4bwdtyTh", types.RpcACT_MALFORMED},
		{"zero prefix", "0Hb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", types.RpcACT_MALFORMED},
		{"numeric only", "12345678901234567890123456789012345", types.RpcACT_MALFORMED},
		{"hex string", "0x1234567890ABCDEF1234567890ABCDEF12345678", types.RpcACT_MALFORMED},
	}

	for _, tc := range malformedAddresses {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"account": tc.address,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for malformed address")
			require.NotNil(t, rpcErr, "Expected RPC error for malformed address")
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Expected correct error code for malformed address: %s", tc.address)
		})
	}
}

// TestAccountInfoServiceUnavailable tests behavior when ledger service is not available
func TestAccountInfoServiceUnavailable(t *testing.T) {
	// Temporarily set Services to nil
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// TestAccountInfoServiceNilLedger tests behavior when ledger service is nil
func TestAccountInfoServiceNilLedger(t *testing.T) {
	// Set Services with nil Ledger
	oldServices := types.Services
	types.Services = &types.ServiceContainer{Ledger: nil}
	defer func() { types.Services = oldServices }()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// TestAccountInfoMethodMetadata tests the method's metadata functions
func TestAccountInfoMethodMetadata(t *testing.T) {
	method := &handlers.AccountInfoMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_info should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestAccountInfoStrictMode tests the strict parameter behavior
func TestAccountInfoStrictMode(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.accountInfo = &types.AccountInfo{
		Account:     validAccount,
		Balance:     "100000000000",
		Flags:       0,
		OwnerCount:  0,
		Sequence:    1,
		LedgerIndex: 2,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}
	mock.accountInfoErr = nil

	tests := []struct {
		name   string
		strict bool
	}{
		{"strict=true", true},
		{"strict=false", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"account": validAccount,
				"strict":  tc.strict,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			// Both should succeed with valid account
			require.Nil(t, rpcErr, "Expected no error with strict=%v", tc.strict)
			require.NotNil(t, result, "Expected result with strict=%v", tc.strict)
		})
	}
}

// TestAccountInfoLedgerIndexFormats tests different ledger_index format handling
func TestAccountInfoLedgerIndexFormats(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.AccountInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.accountInfo = &types.AccountInfo{
		Account:     validAccount,
		Balance:     "100000000000",
		Flags:       0,
		OwnerCount:  0,
		Sequence:    1,
		LedgerIndex: 2,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}
	mock.accountInfoErr = nil

	tests := []struct {
		name        string
		ledgerIndex interface{}
		shouldWork  bool
	}{
		{"string validated", "validated", true},
		{"string current", "current", true},
		{"string closed", "closed", true},
		{"integer 1", 1, true},
		{"integer 2", 2, true},
		{"integer 100", 100, true},
		{"string integer", "2", true},
		{"float 2.0", 2.0, true}, // JSON numbers are floats
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"account":      validAccount,
				"ledger_index": tc.ledgerIndex,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.shouldWork {
				require.Nil(t, rpcErr, "Expected no error for ledger_index=%v", tc.ledgerIndex)
				require.NotNil(t, result, "Expected result for ledger_index=%v", tc.ledgerIndex)
			} else {
				require.NotNil(t, rpcErr, "Expected error for ledger_index=%v", tc.ledgerIndex)
			}
		})
	}
}
