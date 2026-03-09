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

// mockAccountNFTsLedgerService implements LedgerService for account_nfts testing
type mockAccountNFTsLedgerService struct {
	accountNFTsResult    *types.AccountNFTsResult
	accountNFTsErr       error
	accountInfo          *types.AccountInfo
	accountInfoErr       error
	currentLedgerIndex   uint32
	closedLedgerIndex    uint32
	validatedLedgerIndex uint32
	standalone           bool
	serverInfo           types.LedgerServerInfo
}

func newMockAccountNFTsLedgerService() *mockAccountNFTsLedgerService {
	return &mockAccountNFTsLedgerService{
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

func (m *mockAccountNFTsLedgerService) GetCurrentLedgerIndex() uint32   { return m.currentLedgerIndex }
func (m *mockAccountNFTsLedgerService) GetClosedLedgerIndex() uint32    { return m.closedLedgerIndex }
func (m *mockAccountNFTsLedgerService) GetValidatedLedgerIndex() uint32 { return m.validatedLedgerIndex }
func (m *mockAccountNFTsLedgerService) AcceptLedger() (uint32, error)   { return m.closedLedgerIndex + 1, nil }
func (m *mockAccountNFTsLedgerService) IsStandalone() bool              { return m.standalone }
func (m *mockAccountNFTsLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockAccountNFTsLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockAccountNFTsLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockAccountNFTsLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
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
func (m *mockAccountNFTsLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	if m.accountNFTsErr != nil {
		return nil, m.accountNFTsErr
	}
	if m.accountNFTsResult != nil {
		return m.accountNFTsResult, nil
	}
	// Return empty NFTs by default
	return &types.AccountNFTsResult{
		Account:     account,
		AccountNFTs: []types.NFTInfo{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}
func (m *mockAccountNFTsLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountNFTsLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}

// setupAccountNFTsTestServices initializes the Services singleton with a mock for testing
func setupAccountNFTsTestServices(mock *mockAccountNFTsLedgerService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// TestAccountNFTsErrorValidation tests error handling for invalid inputs
// Based on rippled AccountObjects_test.cpp testAccountNFTs()
func TestAccountNFTsErrorValidation(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
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
			name: "Invalid account type - boolean",
			params: map[string]interface{}{
				"account": true,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// Test case from rippled: malformed account using node public key format
			name: "Malformed account address - node public key format (actMalformed)",
			params: map[string]interface{}{
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountNFTsErr = errors.New("invalid account address: bad address")
			},
		},
		{
			// Test case from rippled: account not found (unfunded account)
			name: "Account not found - valid format but not in ledger (actNotFound)",
			params: map[string]interface{}{
				"account": "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountNFTsErr = errors.New("account not found")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountNFTsResult = nil
			mock.accountNFTsErr = nil

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

// TestAccountNFTsInvalidAccountTypes tests various invalid account parameter types
// Based on rippled AccountObjects_test.cpp testAccountNFTs() - testInvalidAccountParam
func TestAccountNFTsInvalidAccountTypes(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
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

// TestAccountNFTsBasic tests basic NFT retrieval functionality
// Based on rippled NFToken_test.cpp
func TestAccountNFTsBasic(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Account with no NFTs returns empty array", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account:     bobAccount,
			AccountNFTs: []types.NFTInfo{},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]interface{})
		assert.Len(t, nfts, 0, "Should have no NFTs")
	})

	t.Run("Account with one NFT returns NFT details", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
					NFTokenTaxon: 0,
					NFTSerial:    0,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		// Check top-level fields
		assert.Equal(t, bobAccount, resp["account"])
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check account_nfts array
		nfts := resp["account_nfts"].([]interface{})
		require.Len(t, nfts, 1)

		nft := nfts[0].(map[string]interface{})
		assert.Equal(t, float64(0), nft["Flags"])
		assert.Equal(t, bobAccount, nft["Issuer"])
		assert.Equal(t, "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000", nft["NFTokenID"])
		assert.Equal(t, float64(0), nft["NFTokenTaxon"])
		assert.Equal(t, float64(0), nft["nft_serial"])
	})

	t.Run("Account with multiple NFTs returns all", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
					NFTokenTaxon: 0,
					NFTSerial:    0,
				},
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000001",
					NFTokenTaxon: 0,
					NFTSerial:    1,
				},
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000002",
					NFTokenTaxon: 0,
					NFTSerial:    2,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]interface{})
		assert.Len(t, nfts, 3, "Should have 3 NFTs")
	})
}

// TestAccountNFTsOptionalFields tests that optional fields are properly included/excluded
// Based on rippled NFToken_test.cpp
func TestAccountNFTsOptionalFields(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("NFT with URI shows URI field", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
					NFTokenTaxon: 0,
					NFTSerial:    0,
					URI:          "68747470733A2F2F6578616D706C652E636F6D2F6E66742F31", // https://example.com/nft/1 in hex
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]interface{})
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]interface{})
		assert.Contains(t, nft, "URI")
		assert.Equal(t, "68747470733A2F2F6578616D706C652E636F6D2F6E66742F31", nft["URI"])
	})

	t.Run("NFT with TransferFee shows TransferFee field", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        8, // tfTransferable
					Issuer:       bobAccount,
					NFTokenID:    "00080000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
					NFTokenTaxon: 0,
					NFTSerial:    0,
					TransferFee:  500, // 0.5% = 500 basis points
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]interface{})
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]interface{})
		assert.Contains(t, nft, "TransferFee")
		assert.Equal(t, float64(500), nft["TransferFee"])
	})

	t.Run("NFT without optional fields excludes them from response", func(t *testing.T) {
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
					NFTokenTaxon: 0,
					NFTSerial:    0,
					// No URI or TransferFee set
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]interface{})
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]interface{})

		// These optional fields should not be present when zero/empty
		assert.NotContains(t, nft, "URI")
		assert.NotContains(t, nft, "TransferFee")
	})
}

// TestAccountNFTsLedgerSpecification tests different ledger index specifications
func TestAccountNFTsLedgerSpecification(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
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
				mock.accountNFTsResult = &types.AccountNFTsResult{
					Account:     validAccount,
					AccountNFTs: []types.NFTInfo{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountNFTsErr = nil
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
				mock.accountNFTsResult = &types.AccountNFTsResult{
					Account:     validAccount,
					AccountNFTs: []types.NFTInfo{},
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
					Validated:   false,
				}
				mock.accountNFTsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, validAccount, resp["account"])
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]interface{}{
				"account":      validAccount,
				"ledger_index": 2,
			},
			setupMock: func() {
				mock.accountNFTsResult = &types.AccountNFTsResult{
					Account:     validAccount,
					AccountNFTs: []types.NFTInfo{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountNFTsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				ledgerIndex := resp["ledger_index"]
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.accountNFTsResult = nil
			mock.accountNFTsErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr)
				if tc.expectedCode != 0 {
					assert.Equal(t, tc.expectedCode, rpcErr.Code)
				}
			} else {
				require.Nil(t, rpcErr)
				require.NotNil(t, result)

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

// TestAccountNFTsPagination tests the limit and marker parameters
// Based on rippled AccountObjects_test.cpp testNFTsMarker()
func TestAccountNFTsPagination(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Limit parameter restricts result count", func(t *testing.T) {
		// Create 10 NFTs, but limit to 4
		nfts := make([]types.NFTInfo, 10)
		for i := 0; i < 10; i++ {
			nfts[i] = types.NFTInfo{
				Flags:        0,
				Issuer:       bobAccount,
				NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B90000000000000000" + string(rune('0'+i)),
				NFTokenTaxon: 0,
				NFTSerial:    uint32(i),
			}
		}

		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account:     bobAccount,
			AccountNFTs: nfts[:4], // Only return first 4
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
			Marker:      "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000003",
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
			"limit":   4,
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

		nftsResp := resp["account_nfts"].([]interface{})
		assert.Len(t, nftsResp, 4, "Should have only 4 NFTs with limit=4")
		assert.Contains(t, resp, "marker", "Should have marker for pagination")
	})

	t.Run("Marker continues pagination", func(t *testing.T) {
		// Starting from marker, return next batch
		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account: bobAccount,
			AccountNFTs: []types.NFTInfo{
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000004",
					NFTokenTaxon: 0,
					NFTSerial:    4,
				},
				{
					Flags:        0,
					Issuer:       bobAccount,
					NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000005",
					NFTokenTaxon: 0,
					NFTSerial:    5,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountNFTsErr = nil

		params := map[string]interface{}{
			"account": bobAccount,
			"limit":   4,
			"marker":  "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000003",
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

		nftsResp := resp["account_nfts"].([]interface{})
		assert.Len(t, nftsResp, 2, "Should have 2 NFTs from marker")
	})
}

// TestAccountNFTsServiceUnavailable tests behavior when ledger service is not available
func TestAccountNFTsServiceUnavailable(t *testing.T) {
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	method := &handlers.AccountNftsMethod{}
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

// TestAccountNFTsMethodMetadata tests the method's metadata functions
func TestAccountNFTsMethodMetadata(t *testing.T) {
	method := &handlers.AccountNftsMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_nfts should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestAccountNFTsResponseFields tests that all required fields are present
func TestAccountNFTsResponseFields(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	cleanup := setupAccountNFTsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	mock.accountNFTsResult = &types.AccountNFTsResult{
		Account: bobAccount,
		AccountNFTs: []types.NFTInfo{
			{
				Flags:        0,
				Issuer:       bobAccount,
				NFTokenID:    "00000000F51DFC2A09D62CBBA1DFBDD4691DAC96AD98B9000000000000000000",
				NFTokenTaxon: 12345,
				NFTSerial:    0,
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"account": bobAccount,
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
	assert.Contains(t, resp, "account_nfts")
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "validated")

	// Verify NFT object fields
	nfts := resp["account_nfts"].([]interface{})
	require.Len(t, nfts, 1)
	nft := nfts[0].(map[string]interface{})

	assert.Contains(t, nft, "Flags")
	assert.Contains(t, nft, "Issuer")
	assert.Contains(t, nft, "NFTokenID")
	assert.Contains(t, nft, "NFTokenTaxon")
	assert.Contains(t, nft, "nft_serial")
}
