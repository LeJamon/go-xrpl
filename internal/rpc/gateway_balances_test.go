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

// mockGatewayBalancesLedgerService implements LedgerService for gateway_balances testing
type mockGatewayBalancesLedgerService struct {
	gatewayBalancesResult *types.GatewayBalancesResult
	gatewayBalancesErr    error
	accountInfo           *types.AccountInfo
	accountInfoErr        error
	currentLedgerIndex    uint32
	closedLedgerIndex     uint32
	validatedLedgerIndex  uint32
	standalone            bool
	serverInfo            types.LedgerServerInfo
}

func newMockGatewayBalancesLedgerService() *mockGatewayBalancesLedgerService {
	return &mockGatewayBalancesLedgerService{
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

func (m *mockGatewayBalancesLedgerService) GetCurrentLedgerIndex() uint32   { return m.currentLedgerIndex }
func (m *mockGatewayBalancesLedgerService) GetClosedLedgerIndex() uint32    { return m.closedLedgerIndex }
func (m *mockGatewayBalancesLedgerService) GetValidatedLedgerIndex() uint32 { return m.validatedLedgerIndex }
func (m *mockGatewayBalancesLedgerService) AcceptLedger() (uint32, error)   { return m.closedLedgerIndex + 1, nil }
func (m *mockGatewayBalancesLedgerService) IsStandalone() bool              { return m.standalone }
func (m *mockGatewayBalancesLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockGatewayBalancesLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockGatewayBalancesLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockGatewayBalancesLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
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
func (m *mockGatewayBalancesLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	if m.gatewayBalancesErr != nil {
		return nil, m.gatewayBalancesErr
	}
	if m.gatewayBalancesResult != nil {
		return m.gatewayBalancesResult, nil
	}
	// Return empty result by default
	return &types.GatewayBalancesResult{
		Account:     account,
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}
func (m *mockGatewayBalancesLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockGatewayBalancesLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockGatewayBalancesLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// setupGatewayBalancesTestServices initializes the Services singleton with a mock for testing
func setupGatewayBalancesTestServices(mock *mockGatewayBalancesLedgerService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// TestGatewayBalancesErrorValidation tests error handling for invalid inputs
// Based on rippled GatewayBalances_test.cpp
func TestGatewayBalancesErrorValidation(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	cleanup := setupGatewayBalancesTestServices(mock)
	defer cleanup()

	method := &handlers.GatewayBalancesMethod{}
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
			name: "Account not found",
			params: map[string]interface{}{
				// Use a valid r-address so it passes ValidateAccount; mock returns "account not found"
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.gatewayBalancesErr = errors.New("account not found")
			},
		},
		{
			name: "Malformed account address",
			params: map[string]interface{}{
				// n-prefix address is not a valid account address -- caught by ValidateAccount
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.gatewayBalancesResult = nil
			mock.gatewayBalancesErr = nil

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

// TestGatewayBalancesInvalidHotwallet tests invalid hotwallet parameter handling
// Based on rippled GatewayBalances_test.cpp testGWBApiVersions
func TestGatewayBalancesInvalidHotwallet(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	cleanup := setupGatewayBalancesTestServices(mock)
	defer cleanup()

	method := &handlers.GatewayBalancesMethod{}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Invalid hotwallet - api version 1 returns invalidHotwallet error", func(t *testing.T) {
		mock.gatewayBalancesErr = errors.New("invalid hotwallet address: asdf")

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := map[string]interface{}{
			"account":   aliceAccount,
			"hotwallet": "asdf",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "Invalid hotwallet")
	})

	t.Run("Invalid hotwallet - api version 2 returns invalidParams error", func(t *testing.T) {
		mock.gatewayBalancesErr = errors.New("invalid hotwallet address: asdf")

		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
		}

		params := map[string]interface{}{
			"account":   aliceAccount,
			"hotwallet": "asdf",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})
}

// TestGatewayBalancesBasic tests basic gateway balance functionality
// Based on rippled GatewayBalances_test.cpp testGWB
func TestGatewayBalancesBasic(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	cleanup := setupGatewayBalancesTestServices(mock)
	defer cleanup()

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		assert.Equal(t, aliceAccount, resp["account"])
		// obligations, balances, assets are always present (may be empty)
		_, hasObligations := resp["obligations"]
		assert.True(t, hasObligations, "obligations should always be present in response")
		obligations := resp["obligations"].(map[string]interface{})
		assert.Empty(t, obligations, "obligations should be empty for gateway with no issued currency")
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		obligations := resp["obligations"].(map[string]interface{})
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

		params := map[string]interface{}{
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
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check balances
		balances := resp["balances"].(map[string]interface{})
		hwBalances := balances[hwAccount].([]interface{})
		assert.Len(t, hwBalances, 2)

		// Check that both USD and JPY are present
		currencies := make(map[string]string)
		for _, b := range hwBalances {
			bal := b.(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		// Check frozen_balances
		frozenBalances := resp["frozen_balances"].(map[string]interface{})
		daveFrozen := frozenBalances[daveAccount].([]interface{})
		assert.Len(t, daveFrozen, 1)
		daveBal := daveFrozen[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		// Check assets
		assets := resp["assets"].(map[string]interface{})
		charleyAssets := assets[charleyAccount].([]interface{})
		assert.Len(t, charleyAssets, 1)
		charleyBal := charleyAssets[0].(map[string]interface{})
		assert.Equal(t, "USD", charleyBal["currency"])
		assert.Equal(t, "10", charleyBal["value"])
	})

	// Test for variable not used warning
	_ = bobAccount
}

// TestGatewayBalancesHotwalletFormats tests different hotwallet parameter formats
func TestGatewayBalancesHotwalletFormats(t *testing.T) {
	mock := newMockGatewayBalancesLedgerService()
	cleanup := setupGatewayBalancesTestServices(mock)
	defer cleanup()

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
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

		params := map[string]interface{}{
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

		params := map[string]interface{}{
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

		params := map[string]interface{}{
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
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	method := &handlers.GatewayBalancesMethod{}
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
	cleanup := setupGatewayBalancesTestServices(mock)
	defer cleanup()

	method := &handlers.GatewayBalancesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.gatewayBalancesResult = &types.GatewayBalancesResult{
		Account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		Obligations: map[string]string{"USD": "100"},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
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

	// Verify all required top-level fields are present
	assert.Contains(t, resp, "account")
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "validated")
}
