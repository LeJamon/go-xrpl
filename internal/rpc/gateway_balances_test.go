package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGatewayBalancesLedgerService implements LedgerService for gateway_balances testing
type mockGatewayBalancesLedgerService struct {
	*mockLedgerService
	gatewayBalancesResult *types.GatewayBalancesResult
	gatewayBalancesErr    error
}

func newMockGatewayBalancesLedgerService() *mockGatewayBalancesLedgerService {
	return &mockGatewayBalancesLedgerService{mockLedgerService: newMockLedgerService()}
}

func (m *mockGatewayBalancesLedgerService) GetGatewayBalances(_ context.Context, account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	if m.gatewayBalancesErr != nil {
		return nil, m.gatewayBalancesErr
	}
	if m.gatewayBalancesResult != nil {
		return m.gatewayBalancesResult, nil
	}
	return &types.GatewayBalancesResult{
		Account:     account,
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}

// newGatewayBalancesTestServices builds a per-test ServiceContainer wrapping mock.
func newGatewayBalancesTestServices(mock *mockGatewayBalancesLedgerService) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger: mock,
	})
}

// TestGatewayBalancesErrorValidation tests error handling for invalid inputs
// Based on rippled GatewayBalances_test.cpp
func TestGatewayBalancesErrorValidation(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	services := newGatewayBalancesTestServices(mock)

	method := &handlers.GatewayBalancesMethod{}
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
			name:          "Missing account field - empty params",
			params:        map[string]any{},
			expectedError: "Missing field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing account field - nil params",
			params:        nil,
			expectedError: "Missing field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Account not found",
			params: map[string]any{
				// Use a valid r-address so it passes ValidateAccount; mock returns "account not found"
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  rpcerrors.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.gatewayBalancesErr = svcerr.ErrAccountNotFound
			},
		},
		{
			name: "Malformed account address",
			params: map[string]any{
				// n-prefix address is not a valid account address -- caught by ValidateAccount
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.gatewayBalancesResult = nil
			mock.gatewayBalancesErr = nil

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

// TestGatewayBalancesInvalidHotwallet tests invalid hotwallet parameter handling
// Based on rippled GatewayBalances_test.cpp testGWBApiVersions
func TestGatewayBalancesInvalidHotwallet(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	services := newGatewayBalancesTestServices(mock)

	method := &handlers.GatewayBalancesMethod{}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Invalid hotwallet - api version 1 returns invalidHotwallet error", func(t *testing.T) {
		mock.gatewayBalancesErr = fmt.Errorf("%w: asdf", svcerr.ErrInvalidHotWallet)

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := map[string]any{
			"account":   aliceAccount,
			"hotwallet": "asdf",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_HOTWALLET, rpcErr.Code)
		assert.Equal(t, "invalidHotWallet", rpcErr.ErrorString)
		assert.Contains(t, rpcErr.Message, "Invalid hotwallet")
	})

	t.Run("Invalid hotwallet - api version 2 returns invalidParams error", func(t *testing.T) {
		mock.gatewayBalancesErr = fmt.Errorf("%w: asdf", svcerr.ErrInvalidHotWallet)

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		params := map[string]any{
			"account":   aliceAccount,
			"hotwallet": "asdf",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	})

	// rippled rejects an empty-string hotwallet (parseBase58 failure) but
	// treats JSON null as a valid empty hotwallet set.
	t.Run("Empty-string hotwallet - api version 1 returns invalidHotwallet error", func(t *testing.T) {
		mock.gatewayBalancesErr = nil

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		paramsJSON := json.RawMessage(`{"account": "` + aliceAccount + `", "hotwallet": ""}`)
		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_HOTWALLET, rpcErr.Code)
		assert.Equal(t, "invalidHotWallet", rpcErr.ErrorString)
		assert.Equal(t, "Invalid hotwallet.", rpcErr.Message)
	})

	t.Run("Empty-string hotwallet - api version 2 returns invalidParams error", func(t *testing.T) {
		mock.gatewayBalancesErr = nil

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		paramsJSON := json.RawMessage(`{"account": "` + aliceAccount + `", "hotwallet": ""}`)
		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Null hotwallet is a valid empty hotwallet set", func(t *testing.T) {
		mock.gatewayBalancesErr = nil

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		paramsJSON := json.RawMessage(`{"account": "` + aliceAccount + `", "hotwallet": null}`)
		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		assert.NotNil(t, result)
	})
}

// TestGatewayBalancesBasic tests basic gateway balance functionality
// Based on rippled GatewayBalances_test.cpp testGWB
func TestGatewayBalancesBasic(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	services := newGatewayBalancesTestServices(mock)

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	hwAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	bobAccount := "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9"
	charleyAccount := "rDxCU1KjMmGcjuVa5PxNccTQF3kN5CWUid"
	daveAccount := "rPu2ffWSxEXMHZgsCWdQnpL5fYMKGfx4JH"

	t.Run("Gateway with no issued currency returns empty obligations", func(t *testing.T) {
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account:     aliceAccount,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

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

		assert.Equal(t, aliceAccount, resp["account"])
		// rippled omits obligations/balances/assets entirely when empty
		// (GatewayBalances.cpp:241-288).
		_, hasObligations := resp["obligations"]
		assert.False(t, hasObligations, "obligations should be omitted when empty")
		_, hasBalances := resp["balances"]
		assert.False(t, hasBalances, "balances should be omitted when empty")
		_, hasAssets := resp["assets"]
		assert.False(t, hasAssets, "assets should be omitted when empty")
	})

	t.Run("Gateway with obligations returns obligations by currency", func(t *testing.T) {
		// Based on rippled test: gateway issues USD, CNY, JPY to clients
		// bob: USD 50
		// charley: CNY 250, JPY 250
		// dave: CNY 30 (frozen)
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account: aliceAccount,
			Obligations: map[string]string{
				"CNY": "250", // charley only (dave is frozen)
				"JPY": "250",
				"USD": "50",
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

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

		obligations := resp["obligations"].(map[string]any)
		assert.Equal(t, "250", obligations["CNY"])
		assert.Equal(t, "250", obligations["JPY"])
		assert.Equal(t, "50", obligations["USD"])
	})

	t.Run("Gateway with hotwallet returns balances", func(t *testing.T) {
		// Based on rippled test: hotwallet (hw) holds USD 5000 and JPY 5000
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account: aliceAccount,
			Balances: map[string][]types.CurrencyBalance{
				hwAccount: {
					{Currency: "USD", Value: "5000"},
					{Currency: "JPY", Value: "5000"},
				},
			},
			Obligations: map[string]string{
				"CNY": "250",
				"JPY": "250",
				"USD": "50",
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

		params := map[string]any{
			"account":   aliceAccount,
			"hotwallet": hwAccount,
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

		// Check balances
		balances := resp["balances"].(map[string]any)
		hwBalances := balances[hwAccount].([]any)
		assert.Len(t, hwBalances, 2)

		// Check that both USD and JPY are present
		currencies := make(map[string]string)
		for _, b := range hwBalances {
			bal := b.(map[string]any)
			currencies[bal["currency"].(string)] = bal["value"].(string)
		}
		assert.Equal(t, "5000", currencies["USD"])
		assert.Equal(t, "5000", currencies["JPY"])
	})

	t.Run("Gateway with frozen balances returns frozen_balances", func(t *testing.T) {
		// Based on rippled test: dave's trust line is frozen, CNY 30
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account: aliceAccount,
			FrozenBalances: map[string][]types.CurrencyBalance{
				daveAccount: {
					{Currency: "CNY", Value: "30"},
				},
			},
			Obligations: map[string]string{
				"CNY": "250",
				"JPY": "250",
				"USD": "50",
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

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

		// Check frozen_balances
		frozenBalances := resp["frozen_balances"].(map[string]any)
		daveFrozen := frozenBalances[daveAccount].([]any)
		assert.Len(t, daveFrozen, 1)
		daveBal := daveFrozen[0].(map[string]any)
		assert.Equal(t, "CNY", daveBal["currency"])
		assert.Equal(t, "30", daveBal["value"])
	})

	t.Run("Gateway with assets returns assets", func(t *testing.T) {
		// Based on rippled test: charley sent USD 10 to alice (unusual case)
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account: aliceAccount,
			Assets: map[string][]types.CurrencyBalance{
				charleyAccount: {
					{Currency: "USD", Value: "10"},
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

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

		// Check assets
		assets := resp["assets"].(map[string]any)
		charleyAssets := assets[charleyAccount].([]any)
		assert.Len(t, charleyAssets, 1)
		charleyBal := charleyAssets[0].(map[string]any)
		assert.Equal(t, "USD", charleyBal["currency"])
		assert.Equal(t, "10", charleyBal["value"])
	})

	// Test for variable not used warning
	_ = bobAccount
}

// TestGatewayBalancesHotwalletFormats tests different hotwallet parameter formats
func TestGatewayBalancesHotwalletFormats(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	services := newGatewayBalancesTestServices(mock)

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	hwAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Single hotwallet as string", func(t *testing.T) {
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account:     aliceAccount,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

		params := map[string]any{
			"account":   aliceAccount,
			"hotwallet": hwAccount, // Single string
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Multiple hotwallets as array", func(t *testing.T) {
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account:     aliceAccount,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

		params := map[string]any{
			"account": aliceAccount,
			"hotwallet": []string{
				hwAccount,
				"rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Empty hotwallet array", func(t *testing.T) {
		mock.gatewayBalancesResult = &types.GatewayBalancesResult{
			Account:     aliceAccount,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.gatewayBalancesErr = nil

		params := map[string]any{
			"account":   aliceAccount,
			"hotwallet": []string{},
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// TestGatewayBalancesServiceUnavailable tests behavior when ledger service is not available
func TestGatewayBalancesServiceUnavailable(t *testing.T) {
	method := &handlers.GatewayBalancesMethod{}
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

// TestGatewayBalancesMethodMetadata tests the method's metadata functions
func TestGatewayBalancesMethodMetadata(t *testing.T) {
	method := &handlers.GatewayBalancesMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"gateway_balances should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestGatewayBalancesResponseFields tests that all required fields are present
func TestGatewayBalancesResponseFields(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	services := newGatewayBalancesTestServices(mock)

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.gatewayBalancesResult = &types.GatewayBalancesResult{
		Account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		Obligations: map[string]string{"USD": "100"},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
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

	// Verify all required top-level fields are present. A bare query targets
	// the open ledger, so lookupLedger emits only ledger_current_index.
	assert.Contains(t, resp, "account")
	assert.Contains(t, resp, "ledger_current_index")
	assert.NotContains(t, resp, "ledger_hash")
	assert.NotContains(t, resp, "ledger_index")
	assert.Contains(t, resp, "validated")
}
