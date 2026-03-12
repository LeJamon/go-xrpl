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

// accountOffersMock wraps mockLedgerService and overrides GetAccountOffers
type accountOffersMock struct {
	*mockLedgerService
	getAccountOffersFn func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error)
}

func newAccountOffersMock() *accountOffersMock {
	return &accountOffersMock{
		mockLedgerService: newMockLedgerService(),
	}
}

func (m *accountOffersMock) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	if m.getAccountOffersFn != nil {
		return m.getAccountOffersFn(account, ledgerIndex, limit)
	}
	// Default: return empty offers
	return &types.AccountOffersResult{
		Account:     account,
		Offers:      []types.AccountOffer{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
		Validated:   true,
	}, nil
}

// setupAccountOffersTestServices sets up the test services with the accountOffersMock
func setupAccountOffersTestServices(mock *accountOffersMock) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// TestAccountOffersErrorValidation tests error handling for invalid inputs
// Based on rippled AccountOffers_test.cpp testBadInput()
func TestAccountOffersErrorValidation(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
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
				"account": 1.1,
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
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  19, // actNotFound
			setupMock: func() {
				mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
					return nil, errors.New("account not found")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.getAccountOffersFn = nil

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

// TestAccountOffersNonAdminMinLimit tests that non-admin requests enforce a minimum limit
// Based on rippled AccountOffers_test.cpp testNonAdminMinLimit()
func TestAccountOffersNonAdminMinLimit(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Create 12 offers
	offers := make([]types.AccountOffer, 12)
	for i := 0; i < 12; i++ {
		offers[i] = types.AccountOffer{
			Flags:     0,
			Seq:       uint32(i + 1),
			TakerGets: map[string]interface{}{"currency": "USD", "issuer": "rGWrZyQqhTp9Xu7G5iFQmGEXsoZYhHbSEw", "value": "1"},
			TakerPays: "100000000",
			Quality:   "100000000",
		}
	}

	// Mock returns all 12 offers when no limit is set
	mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
		result := &types.AccountOffersResult{
			Account:     account,
			Offers:      offers,
			LedgerIndex: mock.validatedLedgerIndex,
			LedgerHash:  [32]byte{0x4B, 0xC5},
			Validated:   true,
		}
		return result, nil
	}

	t.Run("No limit returns all offers", func(t *testing.T) {
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

		offersArr := resp["offers"].([]interface{})
		assert.Equal(t, 12, len(offersArr), "Should return all 12 offers when no limit specified")
	})

	t.Run("Low limit passed to service", func(t *testing.T) {
		// Verify that the limit parameter is forwarded to the service
		var capturedLimit uint32
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			capturedLimit = limit
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      offers[:3],
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   1,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		// Non-admin limit=1 is clamped to minimum of 10 by ClampLimit
		assert.Equal(t, uint32(10), capturedLimit, "Limit should be clamped to minimum")
	})
}

// TestAccountOffersSequentialRetrieval tests sequential retrieval of offers
// Based on rippled AccountOffers_test.cpp testSequential()
func TestAccountOffersSequentialRetrieval(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	gwAccount := "rGWrZyQqhTp9Xu7G5iFQmGEXsoZYhHbSEw"

	// Simulate three offers as in rippled testSequential
	allOffers := []types.AccountOffer{
		{
			Flags: 0,
			Seq:   2,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   gwAccount,
				"value":    "2",
			},
			TakerPays: "200000000",
			Quality:   "100000000",
		},
		{
			Flags: 0,
			Seq:   3,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   validAccount,
				"value":    "1",
			},
			TakerPays: "100000000",
			Quality:   "100000000",
		},
		{
			Flags: 0,
			Seq:   4,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   gwAccount,
				"value":    "6",
			},
			TakerPays: "30000000",
			Quality:   "5000000",
		},
	}

	t.Run("All offers returned without limit", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      allOffers,
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
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

		offersArr := resp["offers"].([]interface{})
		assert.Equal(t, 3, len(offersArr), "Should return all 3 offers")

		// Verify first offer fields (quality=100000000, taker_gets=USD/gw 2, taker_pays=200000000 drops)
		offer0 := offersArr[0].(map[string]interface{})
		assert.Equal(t, "100000000", offer0["quality"])
		takerGets0 := offer0["taker_gets"].(map[string]interface{})
		assert.Equal(t, "USD", takerGets0["currency"])
		assert.Equal(t, gwAccount, takerGets0["issuer"])
		assert.Equal(t, "2", takerGets0["value"])
		assert.Equal(t, "200000000", offer0["taker_pays"])

		// Verify second offer (quality=100000000, taker_gets=USD/bob 1, taker_pays=100000000 drops)
		offer1 := offersArr[1].(map[string]interface{})
		assert.Equal(t, "100000000", offer1["quality"])
		takerGets1 := offer1["taker_gets"].(map[string]interface{})
		assert.Equal(t, "USD", takerGets1["currency"])
		assert.Equal(t, validAccount, takerGets1["issuer"])
		assert.Equal(t, "1", takerGets1["value"])
		assert.Equal(t, "100000000", offer1["taker_pays"])

		// Verify third offer (quality=5000000, taker_gets=USD/gw 6, taker_pays=30000000 drops)
		offer2 := offersArr[2].(map[string]interface{})
		assert.Equal(t, "5000000", offer2["quality"])
		takerGets2 := offer2["taker_gets"].(map[string]interface{})
		assert.Equal(t, "USD", takerGets2["currency"])
		assert.Equal(t, gwAccount, takerGets2["issuer"])
		assert.Equal(t, "6", takerGets2["value"])
		assert.Equal(t, "30000000", offer2["taker_pays"])
	})

	t.Run("Offer fields validation", func(t *testing.T) {
		// Test that each offer has the expected fields: flags, seq, taker_gets, taker_pays, quality
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      allOffers[:1],
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
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

		offersArr := resp["offers"].([]interface{})
		require.Equal(t, 1, len(offersArr))

		offer := offersArr[0].(map[string]interface{})
		assert.Contains(t, offer, "flags", "Offer should have flags field")
		assert.Contains(t, offer, "seq", "Offer should have seq field")
		assert.Contains(t, offer, "taker_gets", "Offer should have taker_gets field")
		assert.Contains(t, offer, "taker_pays", "Offer should have taker_pays field")
		assert.Contains(t, offer, "quality", "Offer should have quality field")
	})
}

// TestAccountOffersResponseFields tests that the response contains expected top-level fields
// Based on rippled account_offers response structure
func TestAccountOffersResponseFields(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
		return &types.AccountOffersResult{
			Account: account,
			Offers: []types.AccountOffer{
				{
					Flags:     0,
					Seq:       1,
					TakerGets: "100000000",
					TakerPays: map[string]interface{}{"currency": "USD", "issuer": "rGWrZyQqhTp9Xu7G5iFQmGEXsoZYhHbSEw", "value": "1"},
					Quality:   "100000000",
				},
			},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
			Validated:   true,
		}, nil
	}

	t.Run("Top-level response fields", func(t *testing.T) {
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
		assert.Contains(t, resp, "account", "Response should have account field")
		assert.Contains(t, resp, "offers", "Response should have offers field")
		assert.Contains(t, resp, "ledger_hash", "Response should have ledger_hash field")
		assert.Contains(t, resp, "ledger_index", "Response should have ledger_index field")
		assert.Contains(t, resp, "validated", "Response should have validated field")

		// Verify account matches
		assert.Equal(t, validAccount, resp["account"])

		// Verify validated flag
		assert.Equal(t, true, resp["validated"])

		// Verify ledger_index
		assert.Equal(t, float64(2), resp["ledger_index"])

		// Verify offers is an array
		offersArr, ok := resp["offers"].([]interface{})
		require.True(t, ok, "offers should be an array")
		assert.Equal(t, 1, len(offersArr))
	})

	t.Run("No marker when all offers returned", func(t *testing.T) {
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

		// marker should NOT be present when all results are returned
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "marker should not be present when all results are returned")
	})
}

// TestAccountOffersEmptyOffers tests response for account with no offers
func TestAccountOffersEmptyOffers(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
		return &types.AccountOffersResult{
			Account:     account,
			Offers:      []types.AccountOffer{},
			LedgerIndex: mock.validatedLedgerIndex,
			LedgerHash:  [32]byte{0x4B, 0xC5},
			Validated:   true,
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

	offersArr := resp["offers"].([]interface{})
	assert.Equal(t, 0, len(offersArr), "Should return empty offers array")
	assert.Equal(t, validAccount, resp["account"])

	// No marker for empty results
	_, hasMarker := resp["marker"]
	assert.False(t, hasMarker, "marker should not be present for empty offers")
}

// TestAccountOffersMarkerPagination tests marker-based pagination
// Based on rippled AccountOffers_test.cpp testSequential() with admin limit=1
func TestAccountOffersMarkerPagination(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		IsAdmin:    true,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	gwAccount := "rGWrZyQqhTp9Xu7G5iFQmGEXsoZYhHbSEw"

	allOffers := []types.AccountOffer{
		{
			Flags: 0,
			Seq:   2,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   gwAccount,
				"value":    "2",
			},
			TakerPays: "200000000",
			Quality:   "100000000",
		},
		{
			Flags: 0,
			Seq:   3,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   gwAccount,
				"value":    "1",
			},
			TakerPays: "100000000",
			Quality:   "100000000",
		},
		{
			Flags: 0,
			Seq:   4,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   gwAccount,
				"value":    "6",
			},
			TakerPays: "30000000",
			Quality:   "5000000",
		},
	}

	t.Run("First page with marker", func(t *testing.T) {
		// Simulate paginated response: first page returns 1 offer with marker
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      allOffers[:1],
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
				Marker:      "page1marker",
			}, nil
		}

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

		offersArr := resp["offers"].([]interface{})
		assert.Equal(t, 1, len(offersArr), "First page should have 1 offer")

		// Marker should be present
		marker, hasMarker := resp["marker"]
		assert.True(t, hasMarker, "marker should be present when more results exist")
		assert.NotEmpty(t, marker, "marker should not be empty")
	})

	t.Run("Second page with marker", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      allOffers[1:2],
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
				Marker:      "page2marker",
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   1,
			"marker":  "page1marker",
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

		offersArr := resp["offers"].([]interface{})
		assert.Equal(t, 1, len(offersArr), "Second page should have 1 offer")

		// Marker should still be present (more results)
		marker, hasMarker := resp["marker"]
		assert.True(t, hasMarker, "marker should be present when more results exist")
		assert.NotEmpty(t, marker, "marker should not be empty")
	})

	t.Run("Last page without marker", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account:     account,
				Offers:      allOffers[2:],
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
				// No marker - last page
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   10,
			"marker":  "page2marker",
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

		offersArr := resp["offers"].([]interface{})
		assert.Equal(t, 1, len(offersArr), "Last page should have 1 offer")

		// No marker on last page
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "marker should not be present on last page")
	})
}

// TestAccountOffersOfferFields tests that each offer in the response has expected fields
// Based on rippled AccountOffers_test.cpp response validation
func TestAccountOffersOfferFields(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	gwAccount := "rGWrZyQqhTp9Xu7G5iFQmGEXsoZYhHbSEw"

	t.Run("IOU taker_gets with XRP taker_pays", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account: account,
				Offers: []types.AccountOffer{
					{
						Flags: 0,
						Seq:   5,
						TakerGets: map[string]interface{}{
							"currency": "USD",
							"issuer":   gwAccount,
							"value":    "2",
						},
						TakerPays: "200000000",
						Quality:   "100000000",
					},
				},
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
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

		offersArr := resp["offers"].([]interface{})
		require.Equal(t, 1, len(offersArr))

		offer := offersArr[0].(map[string]interface{})

		// Verify flags
		assert.Equal(t, float64(0), offer["flags"])

		// Verify seq
		assert.Equal(t, float64(5), offer["seq"])

		// Verify quality
		assert.Equal(t, "100000000", offer["quality"])

		// Verify taker_gets is IOU object
		takerGets := offer["taker_gets"].(map[string]interface{})
		assert.Equal(t, "USD", takerGets["currency"])
		assert.Equal(t, gwAccount, takerGets["issuer"])
		assert.Equal(t, "2", takerGets["value"])

		// Verify taker_pays is XRP drops string
		assert.Equal(t, "200000000", offer["taker_pays"])
	})

	t.Run("XRP taker_gets with IOU taker_pays", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account: account,
				Offers: []types.AccountOffer{
					{
						Flags:     131072,
						Seq:       10,
						TakerGets: "500000000",
						TakerPays: map[string]interface{}{
							"currency": "EUR",
							"issuer":   gwAccount,
							"value":    "50",
						},
						Quality: "10000000",
					},
				},
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
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

		offersArr := resp["offers"].([]interface{})
		require.Equal(t, 1, len(offersArr))

		offer := offersArr[0].(map[string]interface{})

		// Verify flags with non-zero value
		assert.Equal(t, float64(131072), offer["flags"])

		// Verify seq
		assert.Equal(t, float64(10), offer["seq"])

		// Verify taker_gets is XRP drops string
		assert.Equal(t, "500000000", offer["taker_gets"])

		// Verify taker_pays is IOU object
		takerPays := offer["taker_pays"].(map[string]interface{})
		assert.Equal(t, "EUR", takerPays["currency"])
		assert.Equal(t, gwAccount, takerPays["issuer"])
		assert.Equal(t, "50", takerPays["value"])
	})

	t.Run("Offer with expiration", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
			return &types.AccountOffersResult{
				Account: account,
				Offers: []types.AccountOffer{
					{
						Flags:      0,
						Seq:        7,
						TakerGets:  "100000000",
						TakerPays:  "200000000",
						Quality:    "2",
						Expiration: 10000000,
					},
				},
				LedgerIndex: mock.validatedLedgerIndex,
				LedgerHash:  [32]byte{0x4B, 0xC5},
				Validated:   true,
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

		offersArr := resp["offers"].([]interface{})
		require.Equal(t, 1, len(offersArr))

		offer := offersArr[0].(map[string]interface{})
		assert.Contains(t, offer, "expiration", "Offer with expiration set should have expiration field")
		assert.Equal(t, float64(10000000), offer["expiration"])
	})
}

// TestAccountOffersServiceUnavailable tests behavior when ledger service is not available
func TestAccountOffersServiceUnavailable(t *testing.T) {
	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Services nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

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
	})

	t.Run("Ledger nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

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
	})
}

// TestAccountOffersMethodMetadata tests the method's metadata functions
func TestAccountOffersMethodMetadata(t *testing.T) {
	method := &handlers.AccountOffersMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_offers should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestAccountOffersLedgerSpecification tests different ledger index specifications
func TestAccountOffersLedgerSpecification(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name        string
		ledgerIndex interface{}
		expectError bool
	}{
		{"string validated", "validated", false},
		{"string current", "current", false},
		{"string closed", "closed", false},
		{"integer 1", 1, false},
		{"integer 2", 2, false},
		{"float 2.0", 2.0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
				return &types.AccountOffersResult{
					Account:     account,
					Offers:      []types.AccountOffer{},
					LedgerIndex: mock.validatedLedgerIndex,
					LedgerHash:  [32]byte{0x4B, 0xC5},
					Validated:   true,
				}, nil
			}

			params := map[string]interface{}{
				"account":      validAccount,
				"ledger_index": tc.ledgerIndex,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected error for ledger_index=%v", tc.ledgerIndex)
			} else {
				require.Nil(t, rpcErr, "Expected no error for ledger_index=%v, got: %v", tc.ledgerIndex, rpcErr)
				require.NotNil(t, result, "Expected result for ledger_index=%v", tc.ledgerIndex)
			}
		})
	}
}

// TestAccountOffersInvalidAccountTypes tests various invalid account parameter types
// Based on rippled AccountOffers_test.cpp testBadInput() - testInvalidAccountParam lambda
func TestAccountOffersInvalidAccountTypes(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

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
			require.NotNil(t, rpcErr, "Expected RPC error for invalid account type: %s", tc.name)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code,
				"Expected invalidParams error code for type: %s", tc.name)
		})
	}
}

// TestAccountOffersMalformedAddresses tests various malformed address formats
// Based on rippled AccountOffers_test.cpp testBadInput() - empty account and bogus account
func TestAccountOffersMalformedAddresses(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Set up mock to return "account not found" for all address lookups
	mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
		return nil, errors.New("account not found")
	}

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
			require.NotNil(t, rpcErr, "Expected RPC error for malformed address: %s", tc.address)
			assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code,
				"Expected actMalformed error for malformed address: %s", tc.address)
		})
	}

	t.Run("Empty string triggers missing parameter", func(t *testing.T) {
		params := map[string]interface{}{
			"account": "",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})
}

// TestAccountOffersServiceError tests behavior when the ledger service returns an error
func TestAccountOffersServiceError(t *testing.T) {
	mock := newAccountOffersMock()
	cleanup := setupAccountOffersTestServices(mock)
	defer cleanup()

	method := &handlers.AccountOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Internal service error", func(t *testing.T) {
		mock.getAccountOffersFn = func(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
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
		assert.Contains(t, rpcErr.Message, "Failed to get account offers")
	})
}
