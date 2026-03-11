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

// mockNoRippleCheckLedgerService implements LedgerService for noripple_check testing
type mockNoRippleCheckLedgerService struct {
	noRippleCheckResult  *types.NoRippleCheckResult
	noRippleCheckErr     error
	accountInfo          *types.AccountInfo
	accountInfoErr       error
	currentLedgerIndex   uint32
	closedLedgerIndex    uint32
	validatedLedgerIndex uint32
	standalone           bool
	serverInfo           types.LedgerServerInfo
}

func newMockNoRippleCheckLedgerService() *mockNoRippleCheckLedgerService {
	return &mockNoRippleCheckLedgerService{
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

func (m *mockNoRippleCheckLedgerService) GetCurrentLedgerIndex() uint32   { return m.currentLedgerIndex }
func (m *mockNoRippleCheckLedgerService) GetClosedLedgerIndex() uint32    { return m.closedLedgerIndex }
func (m *mockNoRippleCheckLedgerService) GetValidatedLedgerIndex() uint32 { return m.validatedLedgerIndex }
func (m *mockNoRippleCheckLedgerService) AcceptLedger() (uint32, error)   { return m.closedLedgerIndex + 1, nil }
func (m *mockNoRippleCheckLedgerService) IsStandalone() bool              { return m.standalone }
func (m *mockNoRippleCheckLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockNoRippleCheckLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockNoRippleCheckLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockNoRippleCheckLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
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
func (m *mockNoRippleCheckLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	if m.noRippleCheckErr != nil {
		return nil, m.noRippleCheckErr
	}
	if m.noRippleCheckResult != nil {
		return m.noRippleCheckResult, nil
	}
	// Return empty result by default
	return &types.NoRippleCheckResult{
		Problems:    []string{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}
func (m *mockNoRippleCheckLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNoRippleCheckLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}

// setupNoRippleCheckTestServices initializes the Services singleton with a mock for testing
func setupNoRippleCheckTestServices(mock *mockNoRippleCheckLedgerService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// =============================================================================
// Error Validation Tests
// =============================================================================

// TestNoRippleCheckErrorValidation tests error handling for invalid inputs
// Based on rippled NoRippleCheck_test.cpp testBadInput()
func TestNoRippleCheckErrorValidation(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
	}{
		{
			name:          "Missing account field",
			params:        map[string]interface{}{},
			expectError:   true,
			expectedError: "Missing required parameter: account",
		},
		{
			name: "Missing role field",
			params: map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectError:   true,
			expectedError: "Missing required parameter: role",
		},
		{
			name: "Invalid role field",
			params: map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"role":    "not_a_role",
			},
			expectError:   true,
			expectedError: "Invalid field 'role'.",
		},
		{
			name: "Account not found",
			params: map[string]interface{}{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"role":    "user",
			},
			setupMock: func() {
				mock.noRippleCheckErr = errors.New("account not found")
			},
			expectError:   true,
			expectedError: "Account not found.",
		},
		{
			name: "Malformed account",
			params: map[string]interface{}{
				"account": "invalid_account_address",
				"role":    "user",
			},
			setupMock: func() {
				mock.noRippleCheckErr = errors.New("invalid account address: bad checksum")
			},
			expectError:   true,
			expectedError: "Account malformed.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock state
			mock.noRippleCheckErr = nil
			mock.noRippleCheckResult = nil

			if tt.setupMock != nil {
				tt.setupMock()
			}

			paramsJSON, _ := json.Marshal(tt.params)
			resp, err := method.Handle(ctx, paramsJSON)

			if tt.expectError {
				require.NotNil(t, err, "Expected an error but got none")
				assert.Contains(t, err.Message, tt.expectedError)
				assert.Nil(t, resp)
			} else {
				require.Nil(t, err, "Unexpected error: %v", err)
				require.NotNil(t, resp)
			}
		})
	}
}

// =============================================================================
// User Role Tests - No Problems
// =============================================================================

// TestNoRippleCheckUserRoleNoProblems tests user role with properly configured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=true, problems=false)
func TestNoRippleCheckUserRoleNoProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// User with no problems: DefaultRipple not set, NoRipple set on trust lines
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{}, // No problems
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify problems array is empty
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Empty(t, problems, "Expected no problems for properly configured user")

	// Verify other response fields
	assert.Contains(t, respMap, "ledger_index")
	assert.Contains(t, respMap, "ledger_hash")
	assert.Contains(t, respMap, "validated")
}

// =============================================================================
// User Role Tests - With Problems
// =============================================================================

// TestNoRippleCheckUserRoleWithProblems tests user role with misconfigured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=true, problems=true)
func TestNoRippleCheckUserRoleWithProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// User with problems: DefaultRipple set (bad), NoRipple not set on trust lines (bad)
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems: []string{
			"You appear to have set your default ripple flag even though you are not a gateway. This is not recommended unless you are experimenting",
			"You should probably set the no ripple flag on your USD line to rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify problems array has expected problems
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Len(t, problems, 2, "Expected 2 problems for misconfigured user")

	// Check problem messages
	assert.Contains(t, problems[0], "default ripple flag")
	assert.Contains(t, problems[1], "set the no ripple flag")
}

// =============================================================================
// Gateway Role Tests - No Problems
// =============================================================================

// TestNoRippleCheckGatewayRoleNoProblems tests gateway role with properly configured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=false, problems=false)
func TestNoRippleCheckGatewayRoleNoProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Gateway with no problems: DefaultRipple set, NoRipple not set on trust lines
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{}, // No problems
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify problems array is empty
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Empty(t, problems, "Expected no problems for properly configured gateway")
}

// =============================================================================
// Gateway Role Tests - With Problems
// =============================================================================

// TestNoRippleCheckGatewayRoleWithProblems tests gateway role with misconfigured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=false, problems=true)
func TestNoRippleCheckGatewayRoleWithProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Gateway with problems: DefaultRipple not set (bad), NoRipple set on trust lines (bad)
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems: []string{
			"You should immediately set your default ripple flag",
			"You should clear the no ripple flag on your USD line to rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify problems array has expected problems
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Len(t, problems, 2, "Expected 2 problems for misconfigured gateway")

	// Check problem messages
	assert.Contains(t, problems[0], "immediately set your default ripple flag")
	assert.Contains(t, problems[1], "clear the no ripple flag")
}

// =============================================================================
// Transaction Generation Tests
// =============================================================================

// TestNoRippleCheckWithTransactionsUser tests transaction generation for user role
// Based on rippled NoRippleCheck_test.cpp testBasic with transactions=true
func TestNoRippleCheckWithTransactionsUser(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// User with problems requesting transactions (only TrustSet, no AccountSet since DefaultRipple should not be set)
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems: []string{
			"You should probably set the no ripple flag on your USD line to rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		},
		Transactions: []types.SuggestedTransaction{
			{
				TransactionType: "TrustSet",
				Account:         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				Fee:             "10",
				Sequence:        1,
				Flags:           131072, // tfSetNoRipple
				LimitAmount: map[string]interface{}{
					"currency": "USD",
					"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"value":    "100",
				},
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"transactions": true,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify transactions array exists
	transactions, ok := respMap["transactions"].([]map[string]interface{})
	require.True(t, ok, "transactions should be present")
	require.Len(t, transactions, 1, "Expected 1 transaction for user")

	// Verify TrustSet transaction
	assert.Equal(t, "TrustSet", transactions[0]["TransactionType"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", transactions[0]["Account"])
	assert.Contains(t, transactions[0], "LimitAmount")
}

// TestNoRippleCheckWithTransactionsGateway tests transaction generation for gateway role
// Based on rippled NoRippleCheck_test.cpp testBasic with transactions=true
func TestNoRippleCheckWithTransactionsGateway(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Gateway with problems requesting transactions (AccountSet + TrustSet)
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems: []string{
			"You should immediately set your default ripple flag",
			"You should clear the no ripple flag on your USD line to rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		},
		Transactions: []types.SuggestedTransaction{
			{
				TransactionType: "AccountSet",
				Account:         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				Fee:             "10",
				Sequence:        1,
				SetFlag:         8, // asfDefaultRipple
			},
			{
				TransactionType: "TrustSet",
				Account:         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				Fee:             "10",
				Sequence:        2,
				Flags:           262144, // tfClearNoRipple
				LimitAmount: map[string]interface{}{
					"currency": "USD",
					"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"value":    "100",
				},
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"transactions": true,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	// Verify transactions array exists
	transactions, ok := respMap["transactions"].([]map[string]interface{})
	require.True(t, ok, "transactions should be present")
	require.Len(t, transactions, 2, "Expected 2 transactions for gateway")

	// Verify AccountSet transaction
	assert.Equal(t, "AccountSet", transactions[0]["TransactionType"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", transactions[0]["Account"])
	assert.Equal(t, uint32(8), transactions[0]["SetFlag"])

	// Verify TrustSet transaction
	assert.Equal(t, "TrustSet", transactions[1]["TransactionType"])
	assert.Contains(t, transactions[1], "LimitAmount")
}

// =============================================================================
// API Version Tests
// =============================================================================

// TestNoRippleCheckTransactionsFieldValidationAPIv2 tests that API v2+ validates transactions field is boolean
// Based on rippled NoRippleCheck.cpp API version check
func TestNoRippleCheckTransactionsFieldValidationAPIv2(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
	}

	// In API v2, transactions must be a boolean, not a string
	// Note: Go's JSON unmarshaling catches this type mismatch during parsing
	params := `{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "role": "user", "transactions": "true"}`

	resp, err := method.Handle(ctx, []byte(params))

	require.NotNil(t, err, "Expected error for non-boolean transactions in API v2")
	// JSON unmarshal catches the type mismatch before our custom validation
	assert.Contains(t, err.Message, "cannot unmarshal string into Go struct field")
	assert.Nil(t, resp)
}

// TestNoRippleCheckTransactionsFieldAPIv1 tests that API v1 accepts transactions as any truthy value
func TestNoRippleCheckTransactionsFieldAPIv1(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	// In API v1, any truthy value should work
	params := `{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "role": "user", "transactions": true}`

	resp, err := method.Handle(ctx, []byte(params))

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)
}

// =============================================================================
// Service Unavailable Tests
// =============================================================================

// TestNoRippleCheckServiceUnavailable tests response when ledger service is unavailable
func TestNoRippleCheckServiceUnavailable(t *testing.T) {
	// Set Services to nil
	oldServices := types.Services
	types.Services = nil
	defer func() {
		types.Services = oldServices
	}()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":    "user",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Ledger service not available")
	assert.Nil(t, resp)
}

// =============================================================================
// Limit Parameter Tests
// =============================================================================

// TestNoRippleCheckWithLimit tests the limit parameter
func TestNoRippleCheckWithLimit(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	cleanup := setupNoRippleCheckTestServices(mock)
	defer cleanup()

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":    "user",
		"limit":   10,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)
}

// =============================================================================
// Method Metadata Tests
// =============================================================================

// TestNoRippleCheckMethodMetadata tests method metadata (role, API versions)
func TestNoRippleCheckMethodMetadata(t *testing.T) {
	method := &handlers.NoRippleCheckMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
