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

// accountObjectsMock wraps mockLedgerService and allows per-test overrides of
// GetAccountObjects while keeping every other LedgerService method from the
// base mock.
type accountObjectsMock struct {
	*mockLedgerService
	getAccountObjectsFn func(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error)
}

func (m *accountObjectsMock) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	if m.getAccountObjectsFn != nil {
		return m.getAccountObjectsFn(account, ledgerIndex, objType, limit)
	}
	return m.mockLedgerService.GetAccountObjects(account, ledgerIndex, objType, limit)
}

// newAccountObjectsMock creates a ready-to-use accountObjectsMock with sensible
// defaults for the base mockLedgerService.
func newAccountObjectsMock() *accountObjectsMock {
	return &accountObjectsMock{
		mockLedgerService: newMockLedgerService(),
	}
}

// setupAccountObjectsTestServices wires the accountObjectsMock into the global
// types.Services singleton and returns a cleanup function.
func setupAccountObjectsTestServices(mock *accountObjectsMock) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// ---------------------------------------------------------------------------
// Test: Error cases – missing / invalid / malformed account
// Based on rippled AccountObjects_test.cpp testErrors()
// ---------------------------------------------------------------------------

func TestAccountObjectsErrorValidation(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
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
				"account": []string{"val1", "val2"},
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// rippled: rpcACT_MALFORMED for node-public-key format
			name: "Malformed account address - node public key format",
			params: map[string]interface{}{
				"account": "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			// rippled: rpcACT_NOT_FOUND for valid-format but non-existing account
			name: "Account not found - valid format but not in ledger",
			params: map[string]interface{}{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.getAccountObjectsFn = func(string, string, string, uint32) (*types.AccountObjectsResult, error) {
					return nil, errors.New("account not found")
				}
			},
		},
		{
			// rippled: rpcACT_MALFORMED for seed string
			name: "Malformed account address - seed string",
			params: map[string]interface{}{
				"account": "foo",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset per-test state
			mock.getAccountObjectsFn = nil

			if tc.setupMock != nil {
				tc.setupMock()
			}

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

// ---------------------------------------------------------------------------
// Test: Response structure validation
// Based on rippled AccountObjects_test.cpp – verify the response shape
// ---------------------------------------------------------------------------

func TestAccountObjectsResponseStructure(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Response contains expected top-level fields", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				LedgerHash:     [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no RPC error")
		require.NotNil(t, result, "Expected non-nil result")

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Verify all required top-level fields per rippled spec
		assert.Contains(t, resp, "account")
		assert.Contains(t, resp, "account_objects")
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		assert.Equal(t, validAccount, resp["account"])
		assert.Equal(t, true, resp["validated"])

		// account_objects should be an array
		objs, ok := resp["account_objects"].([]interface{})
		require.True(t, ok, "account_objects should be an array")
		assert.Empty(t, objs, "account_objects should be empty for empty result")
	})

	t.Run("Marker absent when no more pages", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
				Marker:         "", // no marker
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

		// Marker should not be present when there are no more pages
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "marker should be absent when no more pages")
	})

	t.Run("Marker present when more pages exist", func(t *testing.T) {
		expectedMarker := "ABCD1234,0"
		mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
				Marker:         expectedMarker,
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

		assert.Equal(t, expectedMarker, resp["marker"])
	})
}

// ---------------------------------------------------------------------------
// Test: Empty objects for funded account with no owned objects
// Based on rippled AccountObjects_test.cpp – empty account checks
// ---------------------------------------------------------------------------

func TestAccountObjectsEmptyAccount(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// For each valid type filter, the result should be empty.
	objectTypes := []string{
		"offer", "state", "ticket", "check", "escrow",
		"payment_channel", "nft_page", "signer_list",
		"deposit_preauth", "did", "amm",
	}

	for _, objType := range objectTypes {
		t.Run("empty_objects_type_"+objType, func(t *testing.T) {
			mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
				return &types.AccountObjectsResult{
					Account:        account,
					AccountObjects: []types.AccountObjectItem{},
					LedgerIndex:    2,
					Validated:      true,
				}, nil
			}

			params := map[string]interface{}{
				"account": validAccount,
				"type":    objType,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error for type=%s", objType)
			require.NotNil(t, result)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]interface{}
			err = json.Unmarshal(resultJSON, &resp)
			require.NoError(t, err)

			objs, ok := resp["account_objects"].([]interface{})
			require.True(t, ok)
			assert.Empty(t, objs, "Expected empty account_objects for type=%s", objType)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Type filtering passes through to service layer
// Based on rippled AccountObjects_test.cpp testObjectTypes()
// ---------------------------------------------------------------------------

func TestAccountObjectsTypeFiltering(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Verify the type parameter is correctly forwarded to the service layer.
	validTypes := []string{
		"offer", "state", "ticket", "check", "escrow",
		"payment_channel", "nft_page", "signer_list",
		"deposit_preauth", "did", "amm",
	}

	for _, objType := range validTypes {
		t.Run("type_passthrough_"+objType, func(t *testing.T) {
			var capturedType string
			mock.getAccountObjectsFn = func(account string, _ string, ot string, _ uint32) (*types.AccountObjectsResult, error) {
				capturedType = ot
				return &types.AccountObjectsResult{
					Account:        account,
					AccountObjects: []types.AccountObjectItem{},
					LedgerIndex:    2,
					Validated:      true,
				}, nil
			}

			params := map[string]interface{}{
				"account": validAccount,
				"type":    objType,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			_, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr)
			assert.Equal(t, objType, capturedType,
				"Type parameter should be forwarded to service layer")
		})
	}

	t.Run("no type filter passes empty string", func(t *testing.T) {
		var capturedType string
		mock.getAccountObjectsFn = func(account string, _ string, ot string, _ uint32) (*types.AccountObjectsResult, error) {
			capturedType = ot
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		assert.Equal(t, "", capturedType,
			"Absent type filter should forward empty string")
	})
}

// ---------------------------------------------------------------------------
// Test: deletion_blockers_only flag
// Based on rippled AccountObjects_test.cpp testObjectTypes() – deletion_blockers_only
// ---------------------------------------------------------------------------

func TestAccountObjectsDeletionBlockersOnly(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("deletion_blockers_only passes through and returns result", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account":                validAccount,
			"deletion_blockers_only": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no RPC error")
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		assert.Contains(t, resp, "account_objects")
	})

	t.Run("deletion_blockers_only=false returns result", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account":                validAccount,
			"deletion_blockers_only": false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no RPC error")
		require.NotNil(t, result)
	})
}

// ---------------------------------------------------------------------------
// Test: Pagination with limit and marker
// Based on rippled AccountObjects_test.cpp testUnsteppedThenStepped()
// ---------------------------------------------------------------------------

func TestAccountObjectsPagination(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("limit parameter is forwarded to service layer", func(t *testing.T) {
		var capturedLimit uint32
		mock.getAccountObjectsFn = func(account string, _ string, _ string, limit uint32) (*types.AccountObjectsResult, error) {
			capturedLimit = limit
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   10,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		assert.Equal(t, uint32(10), capturedLimit)
	})

	t.Run("limit=1 returns single object with marker for more", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, _ string, _ string, limit uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account: account,
				AccountObjects: []types.AccountObjectItem{
					{
						Index:           "ABC123",
						LedgerEntryType: "Offer",
						Data:            []byte{},
					},
				},
				LedgerIndex: 2,
				Validated:   true,
				Marker:      "DEF456,1",
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

		objs := resp["account_objects"].([]interface{})
		assert.Len(t, objs, 1, "Should return exactly 1 object with limit=1")
		assert.Equal(t, "DEF456,1", resp["marker"], "Marker should be present when more pages exist")
	})

	t.Run("last page has no marker", func(t *testing.T) {
		mock.getAccountObjectsFn = func(account string, _ string, _ string, limit uint32) (*types.AccountObjectsResult, error) {
			return &types.AccountObjectsResult{
				Account: account,
				AccountObjects: []types.AccountObjectItem{
					{
						Index:           "LAST_OBJ",
						LedgerEntryType: "Offer",
						Data:            []byte{},
					},
				},
				LedgerIndex: 2,
				Validated:   true,
				Marker:      "", // no more pages
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
			"limit":   1,
			"marker":  "DEF456,1",
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

		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "Last page should not have a marker")
	})

	t.Run("default limit when none specified", func(t *testing.T) {
		var capturedLimit uint32
		mock.getAccountObjectsFn = func(account string, _ string, _ string, limit uint32) (*types.AccountObjectsResult, error) {
			capturedLimit = limit
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    2,
				Validated:      true,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		// When no limit is specified, ClampLimit returns the default (200)
		assert.Equal(t, uint32(200), capturedLimit)
	})
}

// ---------------------------------------------------------------------------
// Test: Ledger specification
// Based on rippled's ledger specifier behavior
// ---------------------------------------------------------------------------

func TestAccountObjectsLedgerSpecification(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name              string
		ledgerIndex       interface{}
		expectLedgerIndex string
	}{
		{"validated", "validated", "validated"},
		{"current", "current", "current"},
		{"closed", "closed", "closed"},
		{"integer", 2, "2"},
	}

	for _, tc := range tests {
		t.Run("ledger_index_"+tc.name, func(t *testing.T) {
			var capturedLedgerIndex string
			mock.getAccountObjectsFn = func(account string, li string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
				capturedLedgerIndex = li
				return &types.AccountObjectsResult{
					Account:        account,
					AccountObjects: []types.AccountObjectItem{},
					LedgerIndex:    2,
					Validated:      true,
				}, nil
			}

			params := map[string]interface{}{
				"account":      validAccount,
				"ledger_index": tc.ledgerIndex,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			_, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error for ledger_index=%v", tc.ledgerIndex)
			assert.Equal(t, tc.expectLedgerIndex, capturedLedgerIndex,
				"Ledger index should be forwarded correctly")
		})
	}

	t.Run("default ledger_index is current when not specified", func(t *testing.T) {
		var capturedLedgerIndex string
		mock.getAccountObjectsFn = func(account string, li string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
			capturedLedgerIndex = li
			return &types.AccountObjectsResult{
				Account:        account,
				AccountObjects: []types.AccountObjectItem{},
				LedgerIndex:    3,
				Validated:      false,
			}, nil
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		assert.Equal(t, "current", capturedLedgerIndex,
			"Default ledger_index should be 'current'")
	})

	t.Run("ledger not found returns internal error", func(t *testing.T) {
		mock.getAccountObjectsFn = func(string, string, string, uint32) (*types.AccountObjectsResult, error) {
			return nil, errors.New("ledger not found")
		}

		params := map[string]interface{}{
			"account":      validAccount,
			"ledger_index": 999999,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	})
}

// ---------------------------------------------------------------------------
// Test: Service unavailable / nil ledger
// ---------------------------------------------------------------------------

func TestAccountObjectsServiceUnavailable(t *testing.T) {
	method := &handlers.AccountObjectsMethod{}
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

	t.Run("Services is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Ledger is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// ---------------------------------------------------------------------------
// Test: Method metadata
// ---------------------------------------------------------------------------

func TestAccountObjectsMethodMetadata(t *testing.T) {
	method := &handlers.AccountObjectsMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_objects should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// ---------------------------------------------------------------------------
// Test: Invalid account types (expanded)
// Based on rippled AccountObjects_test.cpp testInvalidAccountParam lambda
// ---------------------------------------------------------------------------

func TestAccountObjectsInvalidAccountTypes(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
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
			require.NotNil(t, rpcErr, "Expected RPC error for invalid account type")
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code,
				"Expected invalidParams error code for type: %s", tc.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Malformed address formats
// Based on rippled AccountObjects_test.cpp malformed account tests
// ---------------------------------------------------------------------------

func TestAccountObjectsMalformedAddresses(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
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
			// rippled returns rpcACT_MALFORMED (code 35) for malformed addresses
			assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code,
				"Expected actMalformed error for malformed address: %s", tc.address)
		})
	}

	t.Run("empty string triggers missing parameter error", func(t *testing.T) {
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

// ---------------------------------------------------------------------------
// Test: Service error propagation
// Based on rippled error semantics for account_objects
// ---------------------------------------------------------------------------

func TestAccountObjectsServiceErrors(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("account not found error", func(t *testing.T) {
		mock.getAccountObjectsFn = func(string, string, string, uint32) (*types.AccountObjectsResult, error) {
			return nil, errors.New("account not found")
		}

		params := map[string]interface{}{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_NOT_FOUND, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Account not found.")
	})

	t.Run("generic service error returns internal error", func(t *testing.T) {
		mock.getAccountObjectsFn = func(string, string, string, uint32) (*types.AccountObjectsResult, error) {
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
		assert.Contains(t, rpcErr.Message, "Failed to get account objects")
	})
}

// ---------------------------------------------------------------------------
// Test: Account field returned matches request account
// Based on rippled – response account should echo the request account
// ---------------------------------------------------------------------------

func TestAccountObjectsAccountEcho(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
		return &types.AccountObjectsResult{
			Account:        account,
			AccountObjects: []types.AccountObjectItem{},
			LedgerIndex:    2,
			Validated:      true,
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

	assert.Equal(t, validAccount, resp["account"],
		"Response account should echo the request account")
}

// ---------------------------------------------------------------------------
// Test: Multiple API versions
// ---------------------------------------------------------------------------

func TestAccountObjectsApiVersions(t *testing.T) {
	mock := newAccountObjectsMock()
	cleanup := setupAccountObjectsTestServices(mock)
	defer cleanup()

	method := &handlers.AccountObjectsMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	mock.getAccountObjectsFn = func(account string, _ string, _ string, _ uint32) (*types.AccountObjectsResult, error) {
		return &types.AccountObjectsResult{
			Account:        account,
			AccountObjects: []types.AccountObjectItem{},
			LedgerIndex:    2,
			Validated:      true,
		}, nil
	}

	versions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for _, version := range versions {
		t.Run("api_version_"+string(rune('0'+version)), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: version,
			}

			params := map[string]interface{}{
				"account": validAccount,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Expected no error for API version %d", version)
			require.NotNil(t, result, "Expected result for API version %d", version)
		})
	}
}
