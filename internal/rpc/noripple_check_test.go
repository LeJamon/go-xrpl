package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNoRippleCheckLedgerService implements LedgerService for noripple_check testing
type mockNoRippleCheckLedgerService struct {
	*mockLedgerService
	noRippleCheckResult *types.NoRippleCheckResult
	noRippleCheckErr    error
}

func newMockNoRippleCheckLedgerService() *mockNoRippleCheckLedgerService {
	return &mockNoRippleCheckLedgerService{mockLedgerService: newMockLedgerService()}
}

func (m *mockNoRippleCheckLedgerService) GetNoRippleCheck(_ context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	if m.noRippleCheckErr != nil {
		return nil, m.noRippleCheckErr
	}
	if m.noRippleCheckResult != nil {
		return m.noRippleCheckResult, nil
	}
	return &types.NoRippleCheckResult{
		Problems:    []string{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}

// newNoRippleCheckTestServices builds a per-test ServiceContainer wrapping mock.
func newNoRippleCheckTestServices(mock *mockNoRippleCheckLedgerService) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
	})
}

// Error Validation Tests

// TestNoRippleCheckErrorValidation tests error handling for invalid inputs
// Based on rippled NoRippleCheck_test.cpp testBadInput()
func TestNoRippleCheckErrorValidation(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name          string
		params        map[string]any
		setupMock     func()
		expectError   bool
		expectedError string
	}{
		{
			name:          "Missing account field",
			params:        map[string]any{},
			expectError:   true,
			expectedError: "Missing field 'account'.",
		},
		{
			name: "Missing role field",
			params: map[string]any{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectError:   true,
			expectedError: "Missing field 'role'.",
		},
		{
			name: "Invalid role field",
			params: map[string]any{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"role":    "not_a_role",
			},
			expectError:   true,
			expectedError: "Invalid field 'role'.",
		},
		{
			name: "Account not found",
			params: map[string]any{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"role":    "user",
			},
			setupMock: func() {
				mock.noRippleCheckErr = svcerr.ErrAccountNotFound
			},
			expectError:   true,
			expectedError: "Account not found.",
		},
		{
			name: "Malformed account",
			params: map[string]any{
				"account": "invalid_account_address",
				"role":    "user",
			},
			// ValidateAccount catches this before the service call
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

// User Role Tests - No Problems

// TestNoRippleCheckUserRoleNoProblems tests user role with properly configured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=true, problems=false)
func TestNoRippleCheckUserRoleNoProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// User with no problems: DefaultRipple not set, NoRipple set on trust lines
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{}, // No problems
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
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

// User Role Tests - With Problems

// TestNoRippleCheckUserRoleWithProblems tests user role with misconfigured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=true, problems=true)
func TestNoRippleCheckUserRoleWithProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	// Verify problems array has expected problems
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Len(t, problems, 2, "Expected 2 problems for misconfigured user")

	// Check problem messages
	assert.Contains(t, problems[0], "default ripple flag")
	assert.Contains(t, problems[1], "set the no ripple flag")
}

// Gateway Role Tests - No Problems

// TestNoRippleCheckGatewayRoleNoProblems tests gateway role with properly configured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=false, problems=false)
func TestNoRippleCheckGatewayRoleNoProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Gateway with no problems: DefaultRipple set, NoRipple not set on trust lines
	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{}, // No problems
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	// Verify problems array is empty
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Empty(t, problems, "Expected no problems for properly configured gateway")
}

// Gateway Role Tests - With Problems

// TestNoRippleCheckGatewayRoleWithProblems tests gateway role with misconfigured account
// Based on rippled NoRippleCheck_test.cpp testBasic(user=false, problems=true)
func TestNoRippleCheckGatewayRoleWithProblems(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"ledger_index": "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	// Verify problems array has expected problems
	problems, ok := respMap["problems"].([]string)
	require.True(t, ok, "problems should be a string array")
	assert.Len(t, problems, 2, "Expected 2 problems for misconfigured gateway")

	// Check problem messages
	assert.Contains(t, problems[0], "immediately set your default ripple flag")
	assert.Contains(t, problems[1], "clear the no ripple flag")
}

// Transaction Generation Tests

// TestNoRippleCheckWithTransactionsUser tests transaction generation for user role
// Based on rippled NoRippleCheck_test.cpp testBasic with transactions=true
func TestNoRippleCheckWithTransactionsUser(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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
				LimitAmount: map[string]any{
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

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "user",
		"transactions": true,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	// Verify transactions array exists
	transactions, ok := respMap["transactions"].([]map[string]any)
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
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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
				LimitAmount: map[string]any{
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

	params := map[string]any{
		"account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":         "gateway",
		"transactions": true,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	// Verify transactions array exists
	transactions, ok := respMap["transactions"].([]map[string]any)
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

// API Version Tests

// TestNoRippleCheckTransactionsFieldValidationAPIv2 tests that API v2+ validates transactions field is boolean
// Based on rippled NoRippleCheck.cpp API version check
func TestNoRippleCheckTransactionsFieldValidationAPIv2(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   services,
	}

	params := `{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "role": "user", "transactions": "true"}`

	resp, err := method.Handle(ctx, []byte(params))

	require.NotNil(t, err, "Expected error for non-boolean transactions in API v2")
	assert.Equal(t, "Invalid field 'transactions'.", err.Message)
	assert.Nil(t, resp)
}

// TestNoRippleCheckTransactionsFieldAPIv1 tests that API v1 accepts transactions as any truthy value
func TestNoRippleCheckTransactionsFieldAPIv1(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

// Service Unavailable Tests

// TestNoRippleCheckServiceUnavailable tests response when ledger service is unavailable
func TestNoRippleCheckServiceUnavailable(t *testing.T) {
	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":    "user",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.NotNil(t, err)
	assert.Equal(t, "Internal error.", err.Message)
	assert.Nil(t, resp)
}

// Limit Parameter Tests

// TestNoRippleCheckWithLimit tests the limit parameter
func TestNoRippleCheckWithLimit(t *testing.T) {
	mock := newMockNoRippleCheckLedgerService()
	services := newNoRippleCheckTestServices(mock)

	method := &handlers.NoRippleCheckMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.noRippleCheckResult = &types.NoRippleCheckResult{
		Problems:    []string{},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"role":    "user",
		"limit":   10,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)
}

// Method Metadata Tests

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
