package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccountNFTsLedgerService implements LedgerService for account_nfts testing
type mockAccountNFTsLedgerService struct {
	*mockLedgerService
	accountNFTsResult *types.AccountNFTsResult
	accountNFTsErr    error
	accountNFTsMarker string
}

func newMockAccountNFTsLedgerService() *mockAccountNFTsLedgerService {
	return &mockAccountNFTsLedgerService{mockLedgerService: newMockLedgerService()}
}

func (m *mockAccountNFTsLedgerService) GetAccountNFTs(_ context.Context, account string, ledgerIndex string, limit uint32, marker string) (*types.AccountNFTsResult, error) {
	m.accountNFTsMarker = marker
	if m.accountNFTsErr != nil {
		return nil, m.accountNFTsErr
	}
	if m.accountNFTsResult != nil {
		return m.accountNFTsResult, nil
	}
	ledgerSeq := m.currentLedgerIndex
	validated := false
	if ledgerIndex != "current" {
		ledgerSeq = m.validatedLedgerIndex
		validated = true
	}
	return &types.AccountNFTsResult{
		Account:     account,
		AccountNFTs: []types.NFTInfo{},
		LedgerIndex: ledgerSeq,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   validated,
	}, nil
}

// newAccountNFTsTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountNFTsTestServices(mock *mockAccountNFTsLedgerService) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock})
}

// TestAccountNFTsErrorValidation tests error handling for invalid inputs
// Based on rippled AccountObjects_test.cpp testAccountNFTs()
func TestAccountNFTsErrorValidation(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
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
			name: "Invalid account type - integer",
			params: map[string]any{
				"account": 12345,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - boolean",
			params: map[string]any{
				"account": true,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// Test case from rippled: malformed account using node public key format
			// rippled returns rpcACT_MALFORMED
			name: "Malformed account address - node public key format (actMalformed)",
			params: map[string]any{
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
		},
		{
			// Test case from rippled: account not found (unfunded account)
			name: "Account not found - valid format but not in ledger (actNotFound)",
			params: map[string]any{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  rpcerrors.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountNFTsErr = svcerr.ErrAccountNotFound
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountNFTsResult = nil
			mock.accountNFTsErr = nil

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

// TestAccountNFTsInvalidAccountTypes tests various invalid account parameter types
// Based on rippled AccountObjects_test.cpp testAccountNFTs() - testInvalidAccountParam
func TestAccountNFTsInvalidAccountTypes(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// These test cases mirror rippled's testInvalidAccountParam lambda
	invalidParams := []struct {
		name  string
		value any
	}{
		{"integer", 1},
		{"float", 1.1},
		{"boolean true", true},
		{"boolean false", false},
		{"null", nil},
		{"empty object", map[string]any{}},
		{"non-empty object", map[string]any{"key": "value"}},
		{"empty array", []any{}},
		{"non-empty array", []any{"value1", "value2"}},
	}

	for _, tc := range invalidParams {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"account": tc.value,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for invalid account type")
			require.NotNil(t, rpcErr, "Expected RPC error for invalid account type")
			// Should return invalid params error
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code,
				"Expected invalidParams error code for type: %s", tc.name)
		})
	}
}

// TestAccountNFTsBasic tests basic NFT retrieval functionality
// Based on rippled NFToken_test.cpp
func TestAccountNFTsBasic(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

		params := map[string]any{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]any)
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

		params := map[string]any{
			"account": bobAccount,
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

		// Check top-level fields
		assert.Equal(t, bobAccount, resp["account"])
		assert.Contains(t, resp, "ledger_current_index")
		assert.NotContains(t, resp, "ledger_hash")
		assert.NotContains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check account_nfts array
		nfts := resp["account_nfts"].([]any)
		require.Len(t, nfts, 1)

		nft := nfts[0].(map[string]any)
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

		params := map[string]any{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]any)
		assert.Len(t, nfts, 3, "Should have 3 NFTs")
	})
}

// TestAccountNFTsOptionalFields tests that optional fields are properly included/excluded
// Based on rippled NFToken_test.cpp
func TestAccountNFTsOptionalFields(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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

		params := map[string]any{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]any)
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]any)
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

		params := map[string]any{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]any)
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]any)
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

		params := map[string]any{
			"account": bobAccount,
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

		nfts := resp["account_nfts"].([]any)
		require.Len(t, nfts, 1)
		nft := nfts[0].(map[string]any)

		// These optional fields should not be present when zero/empty
		assert.NotContains(t, nft, "URI")
		assert.NotContains(t, nft, "TransferFee")
	})
}

// TestAccountNFTsLedgerSpecification tests different ledger index specifications
func TestAccountNFTsLedgerSpecification(t *testing.T) {
	mock := newMockAccountNFTsLedgerService()
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name         string
		params       map[string]any
		setupMock    func()
		expectError  bool
		expectedCode int
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name: "ledger_index: validated",
			params: map[string]any{
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
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "ledger_index: current",
			params: map[string]any{
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
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, validAccount, resp["account"])
				assert.EqualValues(t, 3, resp["ledger_current_index"])
				assert.NotContains(t, resp, "ledger_hash")
				assert.NotContains(t, resp, "ledger_index")
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]any{
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
			validateResp: func(t *testing.T, resp map[string]any) {
				ledgerIndex := resp["ledger_index"]
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				}
			},
		},
		{
			name: "ledger_index: current sequence projects the actual open ledger",
			params: map[string]any{
				"account":      validAccount,
				"ledger_index": 3,
			},
			setupMock: func() {
				mock.accountNFTsResult = &types.AccountNFTsResult{
					Account:     validAccount,
					AccountNFTs: []types.NFTInfo{},
					LedgerIndex: 3,
					Validated:   false,
				}
				mock.accountNFTsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.EqualValues(t, 3, resp["ledger_current_index"])
				assert.NotContains(t, resp, "ledger_hash")
				assert.NotContains(t, resp, "ledger_index")
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
				var respMap map[string]any
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
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Limit parameter restricts result count", func(t *testing.T) {
		// Guest requests use rippled's minimum account_nfts page size of 20.
		nfts := make([]types.NFTInfo, 21)
		for i := range nfts {
			nfts[i] = types.NFTInfo{
				Flags:        0,
				Issuer:       bobAccount,
				NFTokenID:    fmt.Sprintf("%064X", i),
				NFTokenTaxon: 0,
				NFTSerial:    uint32(i),
			}
		}

		mock.accountNFTsResult = &types.AccountNFTsResult{
			Account:     bobAccount,
			AccountNFTs: nfts[:20],
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
			Marker:      nfts[19].NFTokenID,
		}
		mock.accountNFTsErr = nil

		params := map[string]any{
			"account": bobAccount,
			"limit":   20,
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

		nftsResp := resp["account_nfts"].([]any)
		assert.Len(t, nftsResp, 20, "Should have only 20 NFTs with limit=20")
		assert.Contains(t, resp, "marker", "Should have marker for pagination")
		assert.EqualValues(t, 20, resp["limit"])
		assert.NotContains(t, resp, "account")
		assert.Contains(t, resp, "ledger_current_index")
		assert.NotContains(t, resp, "ledger_hash")
		assert.NotContains(t, resp, "ledger_index")
		assert.Empty(t, mock.accountNFTsMarker)
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

		params := map[string]any{
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
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		nftsResp := resp["account_nfts"].([]any)
		assert.Len(t, nftsResp, 2, "Should have 2 NFTs from marker")
		assert.Equal(t, params["marker"], mock.accountNFTsMarker)
		assert.NotContains(t, resp, "marker")
		assert.NotContains(t, resp, "limit")
		assert.Equal(t, bobAccount, resp["account"])
	})

	t.Run("Malformed and null markers are rejected", func(t *testing.T) {
		for _, marker := range []any{"ABCD", strings.Repeat("G", 64), nil} {
			paramsJSON, err := json.Marshal(map[string]any{"account": bobAccount, "marker": marker})
			require.NoError(t, err)
			result, rpcErr := method.Handle(ctx, paramsJSON)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
		}
	})

	t.Run("Stale marker maps to invalid field", func(t *testing.T) {
		mock.accountNFTsResult = nil
		mock.accountNFTsErr = svcerr.ErrInvalidMarker
		marker := strings.Repeat("A", 64)
		paramsJSON, err := json.Marshal(map[string]any{"account": bobAccount, "marker": marker})
		require.NoError(t, err)
		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "Invalid field 'marker'.", rpcErr.Message)
	})
}

func TestAccountNFTsMarkerValidation(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	method := &handlers.AccountNftsMethod{}
	mock := newMockAccountNFTsLedgerService()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
		Services:   newAccountNFTsTestServices(mock),
	}

	tests := []struct {
		name    string
		params  map[string]any
		service error
		message string
	}{
		{
			name:    "marker must be a string",
			params:  map[string]any{"account": account, "marker": true},
			message: "Invalid field 'marker', not string.",
		},
		{
			name:    "marker must contain a uint256",
			params:  map[string]any{"account": account, "marker": "DEADBEEF"},
			message: "Invalid field 'marker'.",
		},
		{
			name:    "marker must exist in the account NFT sequence",
			params:  map[string]any{"account": account, "marker": strings.Repeat("A", 64)},
			service: svcerr.ErrInvalidMarker,
			message: "Invalid field 'marker'.",
		},
		{
			name:    "zero marker parses but is not a sequence member",
			params:  map[string]any{"account": account, "marker": "0"},
			service: svcerr.ErrInvalidMarker,
			message: "Invalid field 'marker'.",
		},
		{
			name:    "limit validation precedes marker validation",
			params:  map[string]any{"account": account, "limit": "bad", "marker": true},
			message: "Invalid field 'limit', not unsigned integer.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.accountNFTsErr = tc.service
			params, err := json.Marshal(tc.params)
			require.NoError(t, err)
			result, rpcErr := method.Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

// TestAccountNFTsServiceUnavailable tests behavior when ledger service is not available
func TestAccountNFTsServiceUnavailable(t *testing.T) {
	method := &handlers.AccountNftsMethod{}
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
	services := newAccountNFTsTestServices(mock)

	method := &handlers.AccountNftsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
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
		LedgerIndex: 3,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   false,
	}

	params := map[string]any{
		"account": bobAccount,
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

	// Verify all required top-level fields are present
	assert.Contains(t, resp, "account")
	assert.Contains(t, resp, "account_nfts")
	assert.Contains(t, resp, "ledger_current_index")
	assert.NotContains(t, resp, "ledger_hash")
	assert.NotContains(t, resp, "ledger_index")
	assert.Contains(t, resp, "validated")
	assert.NotContains(t, resp, "limit")
	assert.NotContains(t, resp, "marker")

	// Verify NFT object fields
	nfts := resp["account_nfts"].([]any)
	require.Len(t, nfts, 1)
	nft := nfts[0].(map[string]any)

	assert.Contains(t, nft, "Flags")
	assert.Contains(t, nft, "Issuer")
	assert.Contains(t, nft, "NFTokenID")
	assert.Contains(t, nft, "NFTokenTaxon")
	assert.Contains(t, nft, "nft_serial")
}
