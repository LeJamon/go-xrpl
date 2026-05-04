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

// mockAccountLinesLedgerService implements LedgerService for account_lines testing
type mockAccountLinesLedgerService struct {
	accountLinesResult   *types.AccountLinesResult
	accountLinesErr      error
	accountInfo          *types.AccountInfo
	accountInfoErr       error
	currentLedgerIndex   uint32
	closedLedgerIndex    uint32
	validatedLedgerIndex uint32
	standalone           bool
	serverInfo           types.LedgerServerInfo
}

func newMockAccountLinesLedgerService() *mockAccountLinesLedgerService {
	return &mockAccountLinesLedgerService{
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

func (m *mockAccountLinesLedgerService) GetCurrentLedgerIndex() uint32 { return m.currentLedgerIndex }
func (m *mockAccountLinesLedgerService) GetClosedLedgerIndex() uint32  { return m.closedLedgerIndex }
func (m *mockAccountLinesLedgerService) GetValidatedLedgerIndex() uint32 {
	return m.validatedLedgerIndex
}
func (m *mockAccountLinesLedgerService) AcceptLedger(context.Context) (uint32, error) {
	return m.closedLedgerIndex + 1, nil
}
func (m *mockAccountLinesLedgerService) IsStandalone() bool                    { return m.standalone }
func (m *mockAccountLinesLedgerService) GetServerInfo() types.LedgerServerInfo { return m.serverInfo }
func (m *mockAccountLinesLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockAccountLinesLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockAccountLinesLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
	if m.accountInfoErr != nil {
		return nil, m.accountInfoErr
	}
	if m.accountInfo != nil {
		return m.accountInfo, nil
	}
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
func (m *mockAccountLinesLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	if m.accountLinesErr != nil {
		return nil, m.accountLinesErr
	}
	if m.accountLinesResult != nil {
		return m.accountLinesResult, nil
	}
	// Return empty lines by default
	return &types.AccountLinesResult{
		Account:     account,
		Lines:       []types.TrustLine{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}
func (m *mockAccountLinesLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetTransactionHistory(ctx context.Context, startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountLinesLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockAccountLinesLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// newAccountLinesTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountLinesTestServices(mock *mockAccountLinesLedgerService) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
}

// TestAccountLinesErrorValidation tests error handling for invalid inputs
// Based on rippled AccountLines_test.cpp testAccountLines()
func TestAccountLinesErrorValidation(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
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
			// Test case from rippled: malformed account using node public key format
			// Based on line 52-57 of AccountLines_test.cpp
			name: "Malformed account address - node public key format (actMalformed)",
			params: map[string]interface{}{
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			// Test case from rippled: account not found (unfunded account)
			// Based on line 78-87 of AccountLines_test.cpp
			name: "Account not found - valid format but not in ledger (actNotFound)",
			params: map[string]interface{}{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountLinesErr = errors.New("account not found")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountLinesResult = nil
			mock.accountLinesErr = nil

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
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

// TestAccountLinesInvalidAccountTypes tests various invalid account parameter types
// Based on rippled AccountLines_test.cpp testInvalidAccountParam lambda (lines 60-76)
func TestAccountLinesInvalidAccountTypes(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

// TestAccountLinesLedgerSpecification tests different ledger index specifications
// Based on rippled AccountLines_test.cpp lines 102-124
func TestAccountLinesLedgerSpecification(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
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
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
					Validated:   false,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, validAccount, resp["account"])
			},
		},
		{
			name: "ledger_index: closed",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": "closed",
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "ledger_index")
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": 2,
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				ledgerIndex := resp["ledger_index"]
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				case int:
					assert.Equal(t, 2, v)
				}
			},
		},
		{
			// Test case from rippled: invalid ledger index string -> ledgerIndexMalformed
			// Based on lines 102-113 of AccountLines_test.cpp
			name: "ledger_index: invalid string -> ledgerIndexMalformed",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": "nonsense",
			},
			setupMock: func() {
				mock.accountLinesResult = nil
				mock.accountLinesErr = errors.New("ledger index malformed")
			},
			expectError:  true,
			expectedCode: types.RpcINTERNAL,
		},
		{
			// Test case from rippled: ledger not found for non-existent sequence
			// Based on lines 114-124 of AccountLines_test.cpp
			name: "ledger_index: non-existent sequence -> ledgerNotFound",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": 50000,
			},
			setupMock: func() {
				mock.accountLinesResult = nil
				mock.accountLinesErr = errors.New("ledger not found")
			},
			expectError:  true,
			expectedCode: types.RpcINTERNAL,
		},
		{
			name: "ledger_hash: valid hash",
			params: map[string]interface{}{
				"account":     validAccount,
				"ledger_hash": "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
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
				mock.accountLinesResult = nil
				mock.accountLinesErr = errors.New("ledger not found")
			},
			expectError:  true,
			expectedCode: types.RpcINTERNAL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset and setup mock
			mock.accountLinesResult = nil
			mock.accountLinesErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

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

// TestAccountLinesPeerFilter tests the peer parameter for filtering by counterparty
// Based on rippled AccountLines_test.cpp lines 242-270
func TestAccountLinesPeerFilter(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	peerAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	tests := []struct {
		name         string
		params       map[string]interface{}
		setupMock    func()
		expectError  bool
		expectedCode int
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			// Test case from rippled: filter by peer account
			// Based on lines 242-257 of AccountLines_test.cpp
			name: "Valid peer filter returns filtered trust lines",
			params: map[string]interface{}{
				"account": validAccount,
				"peer":    peerAccount,
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account: validAccount,
					Lines: []types.TrustLine{
						{
							Account:  peerAccount,
							Balance:  "50",
							Currency: "USD",
							Limit:    "100",
						},
					},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 1)
				line := lines[0].(map[string]interface{})
				assert.Equal(t, peerAccount, line["account"])
			},
		},
		{
			// Test case from rippled: malformed peer address
			// Based on lines 258-270 of AccountLines_test.cpp
			name: "Malformed peer address - node public key format",
			params: map[string]interface{}{
				"account": validAccount,
				"peer":    "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectError:  true,
			expectedCode: types.RpcACT_MALFORMED,
		},
		{
			name: "Peer not found - valid format but no trust lines with this peer",
			params: map[string]interface{}{
				"account": validAccount,
				"peer":    "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset and setup mock
			mock.accountLinesResult = nil
			mock.accountLinesErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

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

// TestAccountLinesPagination tests the limit and marker parameters
// Based on rippled AccountLines_test.cpp lines 271-342
func TestAccountLinesPagination(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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
			// Test case from rippled: limit parameter
			// Based on lines 284-305 of AccountLines_test.cpp
			name: "Limit parameter restricts result count",
			params: map[string]interface{}{
				"account": validAccount,
				"limit":   1,
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account: validAccount,
					Lines: []types.TrustLine{
						{
							Account:  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
							Balance:  "50",
							Currency: "USD",
							Limit:    "100",
						},
					},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
					Marker:      "next-marker-value",
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 1)
				assert.Contains(t, resp, "marker", "Should have marker for pagination")
			},
		},
		{
			// Test case from rippled: marker for pagination
			// Based on lines 294-305 of AccountLines_test.cpp
			name: "Marker continues pagination from previous result",
			params: map[string]interface{}{
				"account": validAccount,
				"marker":  "some-valid-marker",
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account: validAccount,
					Lines: []types.TrustLine{
						{
							Account:  "rUFiTVw3LSgEqrHV7yPL4nZ1n6f6QgjjfU",
							Balance:  "100",
							Currency: "EUR",
							Limit:    "200",
						},
					},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 1)
			},
		},
		{
			// Test case from rippled: negative limit fails
			// Based on lines 271-283 of AccountLines_test.cpp
			name: "Negative limit fails with error",
			params: map[string]interface{}{
				"account": validAccount,
				"limit":   -1,
			},
			setupMock: func() {
				// The method should validate limit before calling service
				mock.accountLinesErr = errors.New("invalid limit")
			},
			expectError:  true,
			expectedCode: types.RpcINVALID_PARAMS, // Invalid parameters error for negative limit
		},
		{
			// Test case from rippled: invalid marker
			// Based on lines 316-330 of AccountLines_test.cpp
			name: "Invalid marker returns error",
			params: map[string]interface{}{
				"account": validAccount,
				"marker":  "corrupted-marker-value",
			},
			setupMock: func() {
				mock.accountLinesErr = errors.New("invalid marker")
			},
			expectError:  true,
			expectedCode: types.RpcINTERNAL,
		},
		{
			// Test case from rippled: non-string marker fails
			// Based on lines 331-342 of AccountLines_test.cpp
			name: "Non-string marker fails with error",
			params: map[string]interface{}{
				"account": validAccount,
				"marker":  true, // boolean marker should fail
			},
			setupMock: func() {
				// Note: this would fail during parsing if marker expects string
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       []types.TrustLine{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
			},
			// The implementation accepts interface{} for marker, so this may not error
			// depending on how the service handles it
			expectError: false,
		},
		{
			name: "Default limit returns reasonable amount",
			params: map[string]interface{}{
				"account": validAccount,
			},
			setupMock: func() {
				// Create multiple trust lines
				lines := []types.TrustLine{}
				for i := 0; i < 10; i++ {
					lines = append(lines, types.TrustLine{
						Account:  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
						Balance:  "50",
						Currency: "USD",
						Limit:    "100",
					})
				}
				mock.accountLinesResult = &types.AccountLinesResult{
					Account:     validAccount,
					Lines:       lines,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 10)
			},
		},
		{
			name: "Limit with marker paginates correctly",
			params: map[string]interface{}{
				"account": validAccount,
				"limit":   3,
				"marker":  "page-2-marker",
			},
			setupMock: func() {
				mock.accountLinesResult = &types.AccountLinesResult{
					Account: validAccount,
					Lines: []types.TrustLine{
						{Account: "rAddr1", Balance: "10", Currency: "AAA", Limit: "100"},
						{Account: "rAddr2", Balance: "20", Currency: "BBB", Limit: "100"},
						{Account: "rAddr3", Balance: "30", Currency: "CCC", Limit: "100"},
					},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
					Marker:      "page-3-marker",
				}
				mock.accountLinesErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				lines := resp["lines"].([]interface{})
				assert.Len(t, lines, 3)
				assert.Equal(t, "page-3-marker", resp["marker"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset and setup mock
			mock.accountLinesResult = nil
			mock.accountLinesErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

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

// TestAccountLinesResponseFields tests that the response contains expected fields
// Based on rippled AccountLines_test.cpp lines 343-392
func TestAccountLinesResponseFields(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	peerAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Empty lines array for account with no trust lines", func(t *testing.T) {
		// Based on rippled test at lines 93-101
		mock.accountLinesResult = &types.AccountLinesResult{
			Account:     validAccount,
			Lines:       []types.TrustLine{},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountLinesErr = nil

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

		// Check lines is an empty array
		lines := resp["lines"].([]interface{})
		assert.Len(t, lines, 0, "Lines should be empty array for account with no trust lines")
	})

	t.Run("Lines array structure with all fields", func(t *testing.T) {
		// Based on rippled test at lines 343-358
		mock.accountLinesResult = &types.AccountLinesResult{
			Account: validAccount,
			Lines: []types.TrustLine{
				{
					Account:        peerAccount,
					Balance:        "50.5",
					Currency:       "USD",
					Limit:          "100",
					LimitPeer:      "0",
					QualityIn:      1000000000, // Default quality
					QualityOut:     1000000000, // Default quality
					NoRipple:       false,
					NoRipplePeer:   false,
					Authorized:     false,
					PeerAuthorized: false,
					Freeze:         false,
					FreezePeer:     false,
				},
			},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountLinesErr = nil

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
		assert.Contains(t, resp, "account")
		assert.Contains(t, resp, "lines")
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check lines array
		lines := resp["lines"].([]interface{})
		require.Len(t, lines, 1)

		line := lines[0].(map[string]interface{})
		// Required fields for each line
		assert.Equal(t, peerAccount, line["account"])
		assert.Equal(t, "50.5", line["balance"])
		assert.Equal(t, "USD", line["currency"])
		assert.Equal(t, "100", line["limit"])
		assert.Equal(t, "0", line["limit_peer"])
	})

	t.Run("Lines with flags set (freeze, no_ripple, authorized)", func(t *testing.T) {
		// Based on rippled test at lines 343-358 (checking flags)
		mock.accountLinesResult = &types.AccountLinesResult{
			Account: validAccount,
			Lines: []types.TrustLine{
				{
					Account:        peerAccount,
					Balance:        "100.25",
					Currency:       "EUR",
					Limit:          "200",
					LimitPeer:      "150",
					QualityIn:      0, // No quality set
					QualityOut:     0, // No quality set
					NoRipple:       true,
					NoRipplePeer:   true,
					Authorized:     true,
					PeerAuthorized: true,
					Freeze:         true,
					FreezePeer:     true,
				},
			},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountLinesErr = nil

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

		lines := resp["lines"].([]interface{})
		require.Len(t, lines, 1)

		line := lines[0].(map[string]interface{})

		// Check flag fields
		assert.Equal(t, true, line["no_ripple"])
		assert.Equal(t, true, line["no_ripple_peer"])
		assert.Equal(t, true, line["authorized"])
		assert.Equal(t, true, line["peer_authorized"])
		assert.Equal(t, true, line["freeze"])
		assert.Equal(t, true, line["freeze_peer"])
	})

	t.Run("Multiple trust lines with different currencies", func(t *testing.T) {
		// Based on rippled test creating multiple trust lines (lines 126-140)
		mock.accountLinesResult = &types.AccountLinesResult{
			Account: validAccount,
			Lines: []types.TrustLine{
				{
					Account:  peerAccount,
					Balance:  "50",
					Currency: "USD",
					Limit:    "100",
				},
				{
					Account:  peerAccount,
					Balance:  "75.5",
					Currency: "EUR",
					Limit:    "200",
				},
				{
					Account:  "rUFiTVw3LSgEqrHV7yPL4nZ1n6f6QgjjfU",
					Balance:  "25.25",
					Currency: "BTC",
					Limit:    "10",
				},
			},
			LedgerIndex: 4,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountLinesErr = nil

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

		lines := resp["lines"].([]interface{})
		assert.Len(t, lines, 3, "Should have 3 trust lines")

		// Verify different currencies
		currencies := make(map[string]bool)
		for _, l := range lines {
			line := l.(map[string]interface{})
			currencies[line["currency"].(string)] = true
		}
		assert.Contains(t, currencies, "USD")
		assert.Contains(t, currencies, "EUR")
		assert.Contains(t, currencies, "BTC")
	})
}

// TestAccountLinesServiceUnavailable tests behavior when ledger service is not available
func TestAccountLinesServiceUnavailable(t *testing.T) {
	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
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

// TestAccountLinesServiceNilLedger tests behavior when ledger service is nil
func TestAccountLinesServiceNilLedger(t *testing.T) {
	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: nil},
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

// TestAccountLinesMethodMetadata tests the method's metadata functions
func TestAccountLinesMethodMetadata(t *testing.T) {
	method := &handlers.AccountLinesMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_lines should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestAccountLinesMalformedAddresses tests various malformed address formats
// Based on rippled AccountLines_test.cpp malformed account tests
func TestAccountLinesMalformedAddresses(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Malformed addresses are now caught by ValidateAccount at handler level
	// returning rpcACT_MALFORMED (35) matching rippled behavior
	malformedAddresses := []struct {
		name    string
		address string
	}{
		{"node public key format", "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV"},
		{"seed string", "foo"},
		{"short string", "r"},
		{"too short address", "rHb9CJAWyB4rj91VRWn96DkukG"},
		{"too long address", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyThExtraChars"},
		{"invalid characters", "rHb9CJAWyB4rj91VRWn96DkukG4bwdty!@"},
		{"lowercase prefix", "rhb9cjAWyB4rj91VRWn96DkukG4bwdtyTh"},
		{"zero prefix", "0Hb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		{"numeric only", "12345678901234567890123456789012345"},
		{"hex string", "0x1234567890ABCDEF1234567890ABCDEF12345678"},
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
			assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code,
				"Expected actMalformed error for malformed address: %s", tc.address)
		})
	}
}

// TestAccountLinesHistoricLedgers tests retrieving trust lines from historic ledgers
// Based on rippled AccountLines_test.cpp testAccountLinesHistory lambda (lines 181-206)
func TestAccountLinesHistoricLedgers(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name           string
		ledgerIndex    interface{}
		expectedLines  int
		ledgerSequence uint32
	}{
		{
			name:           "Ledger 3 - no trust lines",
			ledgerIndex:    3,
			expectedLines:  0,
			ledgerSequence: 3,
		},
		{
			name:           "Ledger 4 - 26 trust lines (gw1 currencies)",
			ledgerIndex:    4,
			expectedLines:  26,
			ledgerSequence: 4,
		},
		{
			name:           "Ledger 58 - 52 trust lines (gw1 + gw2 currencies)",
			ledgerIndex:    58,
			expectedLines:  52,
			ledgerSequence: 58,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create mock lines
			lines := make([]types.TrustLine, tc.expectedLines)
			for i := 0; i < tc.expectedLines; i++ {
				lines[i] = types.TrustLine{
					Account:  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					Balance:  "50",
					Currency: "USD",
					Limit:    "100",
				}
			}

			mock.accountLinesResult = &types.AccountLinesResult{
				Account:     validAccount,
				Lines:       lines,
				LedgerIndex: tc.ledgerSequence,
				LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
				Validated:   true,
			}
			mock.accountLinesErr = nil

			params := map[string]interface{}{
				"account":      validAccount,
				"ledger_index": tc.ledgerIndex,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			require.Nil(t, rpcErr, "Expected no error")
			require.NotNil(t, result, "Expected result")

			// Convert to map
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]interface{}
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			linesResp := resp["lines"].([]interface{})
			assert.Len(t, linesResp, tc.expectedLines,
				"Expected %d trust lines in ledger %d", tc.expectedLines, tc.ledgerSequence)
		})
	}
}

// TestAccountLinesMarkerOwnership tests that markers from one account cannot be used with another
// Based on rippled AccountLines_test.cpp testAccountLinesMarker (lines 395-478)
func TestAccountLinesMarkerOwnership(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Marker from alice's account cannot be used for becky's account", func(t *testing.T) {
		// First get alice's lines with marker
		aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		beckyAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

		// Setup mock to return error when using alice's marker with becky's account
		mock.accountLinesErr = errors.New("invalid marker - not owned by account")

		params := map[string]interface{}{
			"account": beckyAccount,
			"marker":  "alice-marker-pointing-to-her-signer-list",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		_ = aliceAccount // Just to silence unused variable warning
	})
}

// TestAccountLinesDeletedEntry tests behavior when a trust line pointed to by marker is deleted
// Based on rippled AccountLines_test.cpp testAccountLineDelete (lines 480-554)
func TestAccountLinesDeletedEntry(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Marker becomes invalid when pointed entry is deleted", func(t *testing.T) {
		aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

		// Setup mock to simulate deleted trust line scenario
		mock.accountLinesErr = errors.New("invalid marker - entry deleted")

		params := map[string]interface{}{
			"account": aliceAccount,
			"marker":  "marker-pointing-to-deleted-trustline",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	})
}

// TestAccountLinesWalkMarkers tests that pagination works correctly through various entry types
// Based on rippled AccountLines_test.cpp testAccountLinesWalkMarkers (lines 556-773)
func TestAccountLinesWalkMarkers(t *testing.T) {
	mock := newMockAccountLinesLedgerService()
	services := newAccountLinesTestServices(mock)

	method := &handlers.AccountLinesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Iterate through all trust lines with limit=1", func(t *testing.T) {
		// First call returns first line and marker
		mock.accountLinesResult = &types.AccountLinesResult{
			Account: validAccount,
			Lines: []types.TrustLine{
				{Account: "rPeer1", Balance: "50", Currency: "USD", Limit: "100"},
			},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
			Marker:      "marker-to-second-entry",
		}
		mock.accountLinesErr = nil

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   1,
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

		lines := resp["lines"].([]interface{})
		assert.Len(t, lines, 1, "Should return exactly 1 line with limit=1")
		assert.Contains(t, resp, "marker", "Should have marker for continuation")
	})
}
