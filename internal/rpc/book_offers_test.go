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

// bookOffersMock wraps mockLedgerService to provide custom GetBookOffers behavior
type bookOffersMock struct {
	*mockLedgerService
	getBookOffersFn func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error)
}

func (m *bookOffersMock) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	if m.getBookOffersFn != nil {
		return m.getBookOffersFn(takerGets, takerPays, ledgerIndex, limit)
	}
	return nil, errors.New("not implemented")
}

func newBookOffersMock() *bookOffersMock {
	return &bookOffersMock{
		mockLedgerService: newMockLedgerService(),
	}
}

// newBookOffersTestServices builds a *types.ServiceContainer wrapping the mock.
func newBookOffersTestServices(mock *bookOffersMock) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
}

// TestBookOffersErrorValidation tests error handling for invalid inputs
// Based on rippled Book_test.cpp testBookOfferErrors()
func TestBookOffersErrorValidation(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
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
	}{
		{
			name:          "Missing both taker_gets and taker_pays - empty params",
			params:        map[string]interface{}{},
			expectedError: "taker_gets and taker_pays are required",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing both taker_gets and taker_pays - nil params",
			params:        nil,
			expectedError: "taker_gets and taker_pays are required",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Missing taker_gets - only taker_pays provided",
			params: map[string]interface{}{
				"taker_pays": map[string]interface{}{
					"currency": "XRP",
				},
			},
			expectedError: "taker_gets and taker_pays are required",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Missing taker_pays - only taker_gets provided",
			params: map[string]interface{}{
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			},
			expectedError: "taker_gets and taker_pays are required",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid taker_pays - not a valid amount (integer)",
			params: map[string]interface{}{
				"taker_pays": 12345,
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			},
			expectedError: "Invalid taker_pays",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid taker_gets - not a valid amount (integer)",
			params: map[string]interface{}{
				"taker_pays": map[string]interface{}{
					"currency": "XRP",
				},
				"taker_gets": 12345,
			},
			expectedError: "Invalid taker_gets",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid taker_pays - boolean",
			params: map[string]interface{}{
				"taker_pays": true,
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			},
			expectedError: "Invalid taker_pays",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid taker_gets - boolean",
			params: map[string]interface{}{
				"taker_pays": map[string]interface{}{
					"currency": "XRP",
				},
				"taker_gets": true,
			},
			expectedError: "Invalid taker_gets",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid taker_pays - array",
			params: map[string]interface{}{
				"taker_pays": []string{"XRP"},
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			},
			expectedError: "Invalid taker_pays",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError,
				"Error message should contain expected text")
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

// TestBookOffersXRPAmountHandling tests that XRP amounts are correctly parsed
// In rippled, XRP amounts in book_offers use {currency: "XRP"} object format
func TestBookOffersXRPAmountHandling(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Track what arguments are passed to GetBookOffers
	var capturedGets, capturedPays types.Amount
	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		capturedGets = takerGets
		capturedPays = takerPays
		return &types.BookOffersResult{
			LedgerIndex: 2,
			Offers:      []types.BookOffer{},
			Validated:   true,
		}, nil
	}

	tests := []struct {
		name         string
		takerGets    interface{}
		takerPays    interface{}
		expectedGets types.Amount
		expectedPays types.Amount
	}{
		{
			name: "XRP taker_pays object, IOU taker_gets object",
			takerPays: map[string]interface{}{
				"currency": "XRP",
			},
			takerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectedPays: types.Amount{Currency: "XRP"},
			expectedGets: types.Amount{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		},
		{
			name: "IOU taker_pays, XRP taker_gets",
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			expectedPays: types.Amount{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			expectedGets: types.Amount{Currency: "XRP"},
		},
		{
			name: "Both IOU amounts",
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerGets: map[string]interface{}{
				"currency": "EUR",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			expectedPays: types.Amount{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			expectedGets: types.Amount{Currency: "EUR", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"taker_gets": tc.takerGets,
				"taker_pays": tc.takerPays,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
			require.NotNil(t, result, "Expected result")

			assert.Equal(t, tc.expectedGets.Currency, capturedGets.Currency, "taker_gets currency mismatch")
			assert.Equal(t, tc.expectedGets.Issuer, capturedGets.Issuer, "taker_gets issuer mismatch")
			assert.Equal(t, tc.expectedPays.Currency, capturedPays.Currency, "taker_pays currency mismatch")
			assert.Equal(t, tc.expectedPays.Issuer, capturedPays.Issuer, "taker_pays issuer mismatch")
		})
	}
}

// TestBookOffersValidRequestWithOffers tests a valid request with offers returned
// Based on rippled Book_test.cpp testTrackOffers() book_offers call
func TestBookOffersValidRequestWithOffers(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	expectedOffers := []types.BookOffer{
		{
			Account:         "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			BookDirectory:   "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01",
			BookNode:        "0",
			Flags:           0,
			LedgerEntryType: "Offer",
			OwnerNode:       "0",
			Sequence:        5,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"value":    "10",
			},
			TakerPays:  "4000000000", // 4000 XRP in drops
			Index:      "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
			Quality:    "400000000",
			OwnerFunds: "100",
		},
		{
			Account:         "rPMh7Pi9ct699iZUTWzJCN8JKRWoGSMPqa",
			BookDirectory:   "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01",
			BookNode:        "0",
			Flags:           0,
			LedgerEntryType: "Offer",
			OwnerNode:       "0",
			Sequence:        5,
			TakerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"value":    "5",
			},
			TakerPays:  "2000000000", // 2000 XRP in drops
			Index:      "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
			Quality:    "400000000",
			OwnerFunds: "50",
		},
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return &types.BookOffersResult{
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
			Offers:      expectedOffers,
			Validated:   true,
		}, nil
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
		"ledger_index": "validated",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
	require.NotNil(t, result, "Expected result")

	// Convert result to map for validation
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Check offers array
	offers, ok := resp["offers"].([]interface{})
	require.True(t, ok, "offers should be an array")
	assert.Equal(t, 2, len(offers), "Expected 2 offers")

	// Validate first offer fields (matching rippled testTrackOffers assertions)
	firstOffer := offers[0].(map[string]interface{})
	assert.Equal(t, "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9", firstOffer["Account"])
	assert.Equal(t, "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01", firstOffer["BookDirectory"])
	assert.Equal(t, "0", firstOffer["BookNode"])
	assert.Equal(t, float64(0), firstOffer["Flags"])
	assert.Equal(t, "Offer", firstOffer["LedgerEntryType"])
	assert.Equal(t, "0", firstOffer["OwnerNode"])
	assert.Equal(t, float64(5), firstOffer["Sequence"])
	assert.Equal(t, "400000000", firstOffer["quality"])
	assert.Equal(t, "100", firstOffer["owner_funds"])

	// Check TakerGets is IOU object
	takerGets := firstOffer["TakerGets"].(map[string]interface{})
	assert.Equal(t, "USD", takerGets["currency"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", takerGets["issuer"])
	assert.Equal(t, "10", takerGets["value"])

	// Check TakerPays is XRP drops string
	assert.Equal(t, "4000000000", firstOffer["TakerPays"])

	// Validate second offer
	secondOffer := offers[1].(map[string]interface{})
	assert.Equal(t, "rPMh7Pi9ct699iZUTWzJCN8JKRWoGSMPqa", secondOffer["Account"])
	assert.Equal(t, "50", secondOffer["owner_funds"])
}

// TestBookOffersEmptyOrderBook tests behavior when no offers exist in the order book
// Based on rippled Book_test.cpp testOneSideEmptyBook() - empty offers array
func TestBookOffersEmptyOrderBook(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return &types.BookOffersResult{
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Offers:      []types.BookOffer{},
			Validated:   true,
		}, nil
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Expected no RPC error")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// offers should be present and empty
	offers, ok := resp["offers"].([]interface{})
	require.True(t, ok, "offers should be an array")
	assert.Equal(t, 0, len(offers), "Expected empty offers array")
	assert.Contains(t, resp, "validated")
	assert.Contains(t, resp, "ledger_index")
}

// TestBookOffersLimitParameter tests the limit parameter handling
// Based on rippled Book_test.cpp testBookOfferLimits()
func TestBookOffersLimitParameter(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Track the limit passed to GetBookOffers
	var capturedLimit uint32
	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		capturedLimit = limit
		// Return as many offers as requested (up to our mock max)
		offers := []types.BookOffer{}
		numOffers := int(limit)
		if numOffers == 0 || numOffers > 5 {
			numOffers = 5
		}
		for i := 0; i < numOffers; i++ {
			offers = append(offers, types.BookOffer{
				Account:         "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
				Flags:           0,
				LedgerEntryType: "Offer",
				Sequence:        uint32(i + 1),
				TakerGets:       "1000000",
				TakerPays:       "1000000",
				Quality:         "1",
			})
		}
		return &types.BookOffersResult{
			LedgerIndex: 2,
			Offers:      offers,
			Validated:   true,
		}, nil
	}

	tests := []struct {
		name           string
		limit          interface{}
		expectedLimit  uint32
		expectLimitKey bool
	}{
		{
			name:           "Limit of 1",
			limit:          1,
			expectedLimit:  1,
			expectLimitKey: true,
		},
		{
			name:           "Limit of 10",
			limit:          10,
			expectedLimit:  10,
			expectLimitKey: true,
		},
		{
			name:           "No limit specified",
			limit:          nil,
			expectedLimit:  60, // ClampLimit returns default (60) when user omits limit
			expectLimitKey: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturedLimit = 0
			params := map[string]interface{}{
				"taker_pays": map[string]interface{}{
					"currency": "XRP",
				},
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			}
			if tc.limit != nil {
				params["limit"] = tc.limit
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
			require.NotNil(t, result, "Expected result")

			assert.Equal(t, tc.expectedLimit, capturedLimit, "Limit passed to service should match")

			// Check if limit key is present in response
			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]interface{}
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			if tc.expectLimitKey {
				assert.Contains(t, resp, "limit", "limit should be present in response when specified")
			} else {
				assert.NotContains(t, resp, "limit", "limit should not be present in response when not specified")
			}
		})
	}
}

// TestBookOffersResponseStructure tests the response structure
// Based on rippled book_offers response format (offers array, ledger_index, validated)
func TestBookOffersResponseStructure(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return &types.BookOffersResult{
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
			Offers: []types.BookOffer{
				{
					Account:         "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
					BookDirectory:   "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01",
					BookNode:        "0",
					Flags:           0,
					LedgerEntryType: "Offer",
					OwnerNode:       "0",
					Sequence:        5,
					TakerGets:       "200000000", // 200 XRP in drops
					TakerPays: map[string]interface{}{
						"currency": "USD",
						"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
						"value":    "100",
					},
					Index:      "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
					Quality:    "500000",
					OwnerFunds: "1000",
				},
			},
			Validated: true,
		}, nil
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
		"ledger_index": "validated",
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

	// Required top-level fields
	assert.Contains(t, resp, "offers", "Response must contain offers array")
	assert.Contains(t, resp, "ledger_index", "Response must contain ledger_index")
	assert.Contains(t, resp, "ledger_hash", "Response must contain ledger_hash")
	assert.Contains(t, resp, "validated", "Response must contain validated flag")

	// Validate types
	offers, ok := resp["offers"].([]interface{})
	require.True(t, ok, "offers must be an array")
	assert.Equal(t, 1, len(offers))

	ledgerIndex, ok := resp["ledger_index"].(float64)
	require.True(t, ok, "ledger_index must be a number")
	assert.Equal(t, float64(2), ledgerIndex)

	validated, ok := resp["validated"].(bool)
	require.True(t, ok, "validated must be a boolean")
	assert.True(t, validated)

	ledgerHash, ok := resp["ledger_hash"].(string)
	require.True(t, ok, "ledger_hash must be a string")
	assert.NotEmpty(t, ledgerHash)
}

// TestBookOffersOfferFields tests individual offer field structure
// Based on rippled Book_test.cpp testTrackOffers() field assertions
func TestBookOffersOfferFields(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return &types.BookOffersResult{
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x01, 0x02},
			Offers: []types.BookOffer{
				{
					Account:         "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
					BookDirectory:   "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01",
					BookNode:        "0",
					Flags:           131072, // lsfPassive
					LedgerEntryType: "Offer",
					OwnerNode:       "0",
					Sequence:        42,
					TakerGets: map[string]interface{}{
						"currency": "USD",
						"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
						"value":    "10",
					},
					TakerPays:  "4000000000",
					Index:      "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
					Quality:    "400000000",
					OwnerFunds: "100",
				},
			},
			Validated: true,
		}, nil
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
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

	offers := resp["offers"].([]interface{})
	require.Equal(t, 1, len(offers))

	offer := offers[0].(map[string]interface{})

	// Validate all expected fields from rippled response
	t.Run("Account field", func(t *testing.T) {
		assert.Equal(t, "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9", offer["Account"])
	})

	t.Run("BookDirectory field", func(t *testing.T) {
		assert.Equal(t, "7E5F614417C2D0A7CEFEB73C4AA773ED24566DC3C5A3A0C7D4B3A4DEADBEEF01", offer["BookDirectory"])
	})

	t.Run("BookNode field", func(t *testing.T) {
		assert.Equal(t, "0", offer["BookNode"])
	})

	t.Run("Flags field", func(t *testing.T) {
		assert.Equal(t, float64(131072), offer["Flags"])
	})

	t.Run("LedgerEntryType field", func(t *testing.T) {
		assert.Equal(t, "Offer", offer["LedgerEntryType"])
	})

	t.Run("OwnerNode field", func(t *testing.T) {
		assert.Equal(t, "0", offer["OwnerNode"])
	})

	t.Run("Sequence field", func(t *testing.T) {
		assert.Equal(t, float64(42), offer["Sequence"])
	})

	t.Run("TakerGets IOU field", func(t *testing.T) {
		takerGets := offer["TakerGets"].(map[string]interface{})
		assert.Equal(t, "USD", takerGets["currency"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", takerGets["issuer"])
		assert.Equal(t, "10", takerGets["value"])
	})

	t.Run("TakerPays XRP drops field", func(t *testing.T) {
		assert.Equal(t, "4000000000", offer["TakerPays"])
	})

	t.Run("quality field", func(t *testing.T) {
		assert.Equal(t, "400000000", offer["quality"])
	})

	t.Run("owner_funds field", func(t *testing.T) {
		assert.Equal(t, "100", offer["owner_funds"])
	})

	t.Run("index field", func(t *testing.T) {
		assert.Equal(t, "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789", offer["index"])
	})
}

// TestBookOffersServiceUnavailable tests behavior when ledger service is not available
func TestBookOffersServiceUnavailable(t *testing.T) {
	method := &handlers.BookOffersMethod{}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	t.Run("Services is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Ledger is nil", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// TestBookOffersServiceError tests behavior when GetBookOffers returns an error
func TestBookOffersServiceError(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return nil, errors.New("ledger not found")
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Failed to get book offers")
}

// TestBookOffersMethodMetadata tests the method's metadata functions
func TestBookOffersMethodMetadata(t *testing.T) {
	method := &handlers.BookOffersMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"book_offers should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestBookOffersLedgerIndexPassthrough tests that the ledger_index is forwarded to the service
func TestBookOffersLedgerIndexPassthrough(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	var capturedLedgerIndex string
	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		capturedLedgerIndex = ledgerIndex
		return &types.BookOffersResult{
			LedgerIndex: 2,
			Offers:      []types.BookOffer{},
			Validated:   true,
		}, nil
	}

	tests := []struct {
		name          string
		ledgerIndex   interface{}
		expectedIndex string
	}{
		{
			name:          "validated",
			ledgerIndex:   "validated",
			expectedIndex: "validated",
		},
		{
			name:          "current (default)",
			ledgerIndex:   nil,
			expectedIndex: "current",
		},
		{
			name:          "closed",
			ledgerIndex:   "closed",
			expectedIndex: "closed",
		},
		{
			name:          "numeric sequence",
			ledgerIndex:   2,
			expectedIndex: "2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturedLedgerIndex = ""
			params := map[string]interface{}{
				"taker_pays": map[string]interface{}{
					"currency": "XRP",
				},
				"taker_gets": map[string]interface{}{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				},
			}
			if tc.ledgerIndex != nil {
				params["ledger_index"] = tc.ledgerIndex
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
			require.NotNil(t, result)

			assert.Equal(t, tc.expectedIndex, capturedLedgerIndex,
				"Ledger index passed to service should match")
		})
	}
}

// TestBookOffersNilOffersArray tests that nil offers are serialized as an empty array
func TestBookOffersNilOffersArray(t *testing.T) {
	mock := newBookOffersMock()
	services := newBookOffersTestServices(mock)

	method := &handlers.BookOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getBookOffersFn = func(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
		return &types.BookOffersResult{
			LedgerIndex: 2,
			Offers:      nil, // nil slice
			Validated:   true,
		}, nil
	}

	params := map[string]interface{}{
		"taker_pays": map[string]interface{}{
			"currency": "XRP",
		},
		"taker_gets": map[string]interface{}{
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		},
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	// The response should still contain offers key (even if null or empty array)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	assert.Contains(t, resp, "offers", "Response must contain offers key even when nil")
}
