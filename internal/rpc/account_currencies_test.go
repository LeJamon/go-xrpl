package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccountCurrenciesLedgerService implements LedgerService for account_currencies testing
type mockAccountCurrenciesLedgerService struct {
	*mockLedgerService
	accountCurrenciesResult *types.AccountCurrenciesResult
	accountCurrenciesErr    error
}

func newMockAccountCurrenciesLedgerService() *mockAccountCurrenciesLedgerService {
	return &mockAccountCurrenciesLedgerService{mockLedgerService: newMockLedgerService()}
}

func (m *mockAccountCurrenciesLedgerService) GetAccountCurrencies(_ context.Context, account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	if m.accountCurrenciesErr != nil {
		return nil, m.accountCurrenciesErr
	}
	if m.accountCurrenciesResult != nil {
		return m.accountCurrenciesResult, nil
	}
	return &types.AccountCurrenciesResult{
		ReceiveCurrencies: []string{},
		SendCurrencies:    []string{},
		LedgerIndex:       m.validatedLedgerIndex,
		LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:         true,
	}, nil
}

// newAccountCurrenciesTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountCurrenciesTestServices(mock *mockAccountCurrenciesLedgerService) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock})
}

// TestAccountCurrenciesBadInput tests error handling for invalid inputs
// Based on rippled AccountCurrencies_test.cpp testBadInput()
func TestAccountCurrenciesBadInput(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
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
		expectedToken string
		setupMock     func()
	}{
		{
			// missing account field
			name:          "Missing account field - empty params",
			params:        map[string]any{},
			expectedError: "Missing field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (integer)
			name: "Invalid account type - integer",
			params: map[string]any{
				"account": 1,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (float)
			name: "Invalid account type - float",
			params: map[string]any{
				"account": 1.1,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (boolean)
			name: "Invalid account type - boolean",
			params: map[string]any{
				"account": true,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// invalid base58 characters (llIIOO)
			// rippled returns rpcACT_MALFORMED for malformed addresses
			name: "Malformed account - invalid base58 characters",
			params: map[string]any{
				"account": "llIIOO",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
		},
		{
			// Cannot use a seed as account
			// rippled returns rpcACT_MALFORMED
			name: "Malformed account - seed format (actMalformed)",
			params: map[string]any{
				"account": "Bob",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
		},
		{
			// ask for nonexistent account (actNotFound)
			name: "Account not found - valid format but not in ledger",
			params: map[string]any{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  rpcerrors.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountCurrenciesErr = svcerr.ErrAccountNotFound
			},
		},
		{
			name: "Malformed account - service rejects address (actMalformed token)",
			params: map[string]any{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
			expectedToken: "actMalformed",
			setupMock: func() {
				mock.accountCurrenciesErr = svcerr.ErrAccountMalformed
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountCurrenciesResult = nil
			mock.accountCurrenciesErr = nil

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
			if tc.expectedToken != "" {
				assert.Equal(t, tc.expectedToken, rpcErr.ErrorString,
					"Error token should match expected")
			}
			if tc.expectedCode == rpcerrors.RpcACT_NOT_FOUND {
				assert.Equal(t, map[string]any{
					"error":         "actNotFound",
					"error_code":    rpcerrors.RpcACT_NOT_FOUND,
					"error_message": "Account not found.",
				}, rpcErr.ResponseFields())
			}
		})
	}
}

// TestAccountCurrenciesBasic tests basic functionality
// Based on rippled AccountCurrencies_test.cpp testBasic()
func TestAccountCurrenciesBasic(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Account with no trust lines returns empty arrays", func(t *testing.T) {
		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: []string{},
			SendCurrencies:    []string{},
			LedgerIndex:       2,
			LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

		params := map[string]any{
			"account": aliceAccount,
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

		receiveCurrencies := resp["receive_currencies"].([]any)
		sendCurrencies := resp["send_currencies"].([]any)
		assert.Len(t, receiveCurrencies, 0, "Should have no receive currencies")
		assert.Len(t, sendCurrencies, 0, "Should have no send currencies")
	})

	t.Run("Account with trust lines but no balance - can receive", func(t *testing.T) {
		// Based on rippled test: after setting up 26 trust lines (USA - USZ)
		// receive_currencies should contain all, send_currencies should be empty
		currencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: currencies,
			SendCurrencies:    []string{},
			LedgerIndex:       3,
			LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

		params := map[string]any{
			"account": aliceAccount,
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

		receiveCurrencies := resp["receive_currencies"].([]any)
		sendCurrencies := resp["send_currencies"].([]any)
		assert.Len(t, receiveCurrencies, 26, "Should have 26 receive currencies")
		assert.Len(t, sendCurrencies, 0, "Should have no send currencies (no balance)")
	})

	t.Run("Account with trust lines and balance - can send and receive", func(t *testing.T) {
		// After payment, alice has balance, so can both send and receive
		currencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: currencies,
			SendCurrencies:    currencies,
			LedgerIndex:       4,
			LedgerHash:        [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

		params := map[string]any{
			"account": aliceAccount,
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

		receiveCurrencies := resp["receive_currencies"].([]any)
		sendCurrencies := resp["send_currencies"].([]any)
		assert.Len(t, receiveCurrencies, 26, "Should have 26 receive currencies")
		assert.Len(t, sendCurrencies, 26, "Should have 26 send currencies")
	})

	t.Run("Exhausted trust line removes from receive_currencies", func(t *testing.T) {
		// When balance == limit, cannot receive more
		receiveCurrencies := []string{"USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"} // USA missing
		sendCurrencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: receiveCurrencies,
			SendCurrencies:    sendCurrencies,
			LedgerIndex:       5,
			LedgerHash:        [32]byte{0x6B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

		params := map[string]any{
			"account": aliceAccount,
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

		rcvCurrencies := resp["receive_currencies"].([]any)
		sndCurrencies := resp["send_currencies"].([]any)
		assert.Len(t, rcvCurrencies, 25, "Should have 25 receive currencies (USA exhausted)")
		assert.Len(t, sndCurrencies, 26, "Should still have 26 send currencies")

		// Verify USA is not in receive_currencies
		for _, c := range rcvCurrencies {
			assert.NotEqual(t, "USA", c.(string))
		}
	})

	t.Run("Zero balance removes from send_currencies", func(t *testing.T) {
		// When balance == 0, cannot send
		receiveCurrencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}
		sendCurrencies := []string{"USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"} // USA missing

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: receiveCurrencies,
			SendCurrencies:    sendCurrencies,
			LedgerIndex:       6,
			LedgerHash:        [32]byte{0x7B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

		params := map[string]any{
			"account": aliceAccount,
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

		rcvCurrencies := resp["receive_currencies"].([]any)
		sndCurrencies := resp["send_currencies"].([]any)
		assert.Len(t, rcvCurrencies, 26, "Should have all 26 receive currencies")
		assert.Len(t, sndCurrencies, 25, "Should have 25 send currencies (USA has zero balance)")

		// Verify USA is not in send_currencies
		for _, c := range sndCurrencies {
			assert.NotEqual(t, "USA", c.(string))
		}
	})
}

// TestAccountCurrenciesResponseFields tests that all required fields are present
func TestAccountCurrenciesResponseFields(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
		ReceiveCurrencies: []string{"USD", "EUR"},
		SendCurrencies:    []string{"USD"},
		LedgerIndex:       2,
		LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:         true,
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
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

	assert.Contains(t, resp, "ledger_current_index")
	assert.NotContains(t, resp, "ledger_hash")
	assert.NotContains(t, resp, "ledger_index")
	assert.Contains(t, resp, "receive_currencies")
	assert.Contains(t, resp, "send_currencies")
	assert.Contains(t, resp, "validated")
}

// TestAccountCurrenciesServiceUnavailable tests behavior when ledger service is not available
func TestAccountCurrenciesServiceUnavailable(t *testing.T) {
	method := &handlers.AccountCurrenciesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestAccountCurrenciesMethodMetadata tests the method's metadata functions
func TestAccountCurrenciesMethodMetadata(t *testing.T) {
	method := &handlers.AccountCurrenciesMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_currencies should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
