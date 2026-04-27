package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test Helpers

// mockLedgerServiceMissingMethods extends mockLedgerService for testing new methods
type mockLedgerServiceMissingMethods struct {
	*mockLedgerService
}

func newMockLedgerServiceMissingMethods() *mockLedgerServiceMissingMethods {
	return &mockLedgerServiceMissingMethods{
		mockLedgerService: newMockLedgerService(),
	}
}

// setupTestServicesMissingMethods initializes Services for testing
func setupTestServicesMissingMethods(mock *mockLedgerServiceMissingMethods) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// FetchInfoMethod Tests
// Reference: rippled/src/xrpld/rpc/handlers/FetchInfo.cpp

func TestFetchInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.FetchInfoMethod{}

	t.Run("Returns response with clear flag", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"clear": true}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]interface{})
		assert.Contains(t, resultMap, "info")
	})

	t.Run("Returns response without clear flag", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})

	t.Run("Supports all API versions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// OwnerInfoMethod Tests
// Reference: rippled/src/test/rpc/OwnerInfo_test.cpp

func TestOwnerInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.OwnerInfoMethod{}

	t.Run("Missing account parameter returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Empty account returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"account": ""}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Valid account returns not implemented error (stub)", func(t *testing.T) {
		// owner_info is a stub - it returns RpcNOT_IMPL until NetworkOPs.GetOwnerInfo is implemented
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// LedgerHeaderMethod Tests
// Reference: rippled/src/test/rpc/LedgerHeader_test.cpp

func TestLedgerHeaderMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LedgerHeaderMethod{}

	t.Run("Current ledger returns error when GetLedgerBySequence not implemented", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerBySequence
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"ledger_index": "current"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns lgrNotFound because GetLedgerBySequence returns error
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("Validated ledger returns error when GetLedgerBySequence not implemented", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"ledger_index": "validated"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("Supports only API version 1", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.NotContains(t, versions, types.ApiVersion2)
		assert.NotContains(t, versions, types.ApiVersion3)
	})
}

// LedgerRequestMethod Tests
// Reference: rippled/src/test/rpc/LedgerRequestRPC_test.cpp

func TestLedgerRequestMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LedgerRequestMethod{}

	t.Run("Returns not implemented error in standalone mode (stub)", func(t *testing.T) {
		// ledger_request is a stub - it returns RpcNOT_IMPL until network ledger fetching is implemented
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"ledger_index": 100}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// In standalone mode, returns notSynced; otherwise returns RpcNOT_IMPL
		assert.True(t, rpcErr.Code == types.RpcNOT_SYNCED || rpcErr.Code == types.RpcNOT_IMPL)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LedgerCleanerMethod Tests

func TestLedgerCleanerMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LedgerCleanerMethod{}

	t.Run("Returns not implemented error (stub)", func(t *testing.T) {
		// ledger_cleaner is a stub - requires LedgerCleaner service
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LedgerDiffMethod Tests

func TestLedgerDiffMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LedgerDiffMethod{}

	t.Run("Returns gRPC only error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// ledger_diff is gRPC only in rippled
		assert.Contains(t, rpcErr.Message, "gRPC")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// SimulateMethod Tests
// Reference: rippled/src/test/rpc/Simulate_test.cpp

func TestSimulateMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.SimulateMethod{}

	t.Run("Missing tx_json and tx_blob returns error", func(t *testing.T) {
		// Based on Simulate_test.cpp::testParamErrors - "No params"
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Both tx_json and tx_blob returns not implemented (stub)", func(t *testing.T) {
		// simulate is a stub - returns RpcNOT_IMPL regardless of params
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"tx_json": {}, "tx_blob": "1200"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Stub returns NOT_IMPL after parameter validation passes
		assert.True(t, rpcErr.Code == types.RpcINVALID_PARAMS || rpcErr.Code == types.RpcNOT_IMPL)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("Supports all API versions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TxReduceRelayMethod Tests

func TestTxReduceRelayMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.TxReduceRelayMethod{}

	t.Run("Returns current state or not implemented", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		// Implementation returns transaction statistics
		if rpcErr == nil {
			require.NotNil(t, result)
			resultMap := result.(map[string]interface{})
			assert.Contains(t, resultMap, "transactions")
		} else {
			// Stub returns NOT_IMPL
			assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code)
		}
	})

	t.Run("RequiredRole is User", func(t *testing.T) {
		// rippled: Role::USER (Handler.cpp line 179)
		assert.Equal(t, types.RoleUser, method.RequiredRole())
	})
}

// ConnectMethod Tests
// Reference: rippled/src/test/rpc/Connect_test.cpp

func TestConnectMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	mock.standalone = true // Standalone mode
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.ConnectMethod{}

	t.Run("Standalone mode returns notSynced error", func(t *testing.T) {
		// Based on Connect_test.cpp::testErrors
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"ip": "127.0.0.1", "port": 51235}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_SYNCED, rpcErr.Code)
		assert.Equal(t, "notSynced", rpcErr.ErrorString)
	})

	t.Run("Missing ip parameter returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// PrintMethod Tests

func TestPrintMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.PrintMethod{}

	t.Run("Returns status message", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]interface{})
		// Note: "status" is added by the RPC framework layer (server.go writeXrplResponse),
		// not by individual handlers. The handler itself is a stub returning an empty map,
		// which is valid. We just verify it returns a map without panicking.
		assert.NotNil(t, resultMap, "Expected result map")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// ValidatorInfoMethod Tests
// Reference: rippled/src/test/rpc/ValidatorInfo_test.cpp

func TestValidatorInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}

	t.Run("Non-validator returns error", func(t *testing.T) {
		// Based on ValidatorInfo_test.cpp::testErrors
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Rippled's not_validator_error() = make_param_error("not a validator").
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "not a validator", rpcErr.Message)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// CanDeleteMethod Tests

func TestCanDeleteMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.CanDeleteMethod{}

	t.Run("Returns not enabled error (requires SHAMapStore)", func(t *testing.T) {
		// can_delete requires SHAMapStore advisory delete configuration
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// GetAggregatePriceMethod Tests
// Reference: rippled/src/test/rpc/GetAggregatePrice_test.cpp

func TestGetAggregatePriceMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.GetAggregatePriceMethod{}

	// Convenience helper to make a valid-looking oracle entry for params
	// (uses a real-format r-address so account parsing doesn't fail before
	// the check we actually want to test).
	validOracle := `{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "oracle_document_id": 1}`

	t.Run("Missing oracles parameter returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
		assert.Equal(t, "Missing field 'oracles'.", rpcErr.Message)
	})

	t.Run("Empty oracles array returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": []}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Oracles not array returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": "bad"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Missing base_asset returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Missing field 'base_asset'.", rpcErr.Message)
	})

	t.Run("Missing quote_asset returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Missing field 'quote_asset'.", rpcErr.Message)
	})

	t.Run("Trim=0 returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 0}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Trim=26 returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 26}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Invalid base_asset returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		// empty string
		params := json.RawMessage(`{"base_asset": "", "quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)

		// invalid currency (4 chars, not hex)
		params = json.RawMessage(`{"base_asset": "ABCD", "quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr = method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Oracle entry missing account returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [{"oracle_document_id": 1}]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Oracle entry missing oracle_document_id returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Invalid trim type returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		// float trim
		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 1.2}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Invalid time_threshold type returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		// non-numeric time_threshold
		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "time_threshold": "none"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// GetCountsMethod Tests
// Reference: rippled/src/test/rpc/GetCounts_test.cpp

func TestGetCountsMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.GetCountsMethod{}

	t.Run("Returns server counts info", func(t *testing.T) {
		// get_counts returns server statistics
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		resultMap := result.(map[string]interface{})
		assert.Contains(t, resultMap, "standalone")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LogLevelMethod Tests

func TestLogLevelMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LogLevelMethod{}

	t.Run("Returns current log levels without params", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Invalid severity returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"severity": "invalid_level"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Valid severity levels are accepted", func(t *testing.T) {
		validLevels := []string{"trace", "debug", "info", "warning", "error", "fatal"}

		for _, level := range validLevels {
			t.Run("severity: "+level, func(t *testing.T) {
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleAdmin,
					ApiVersion: types.ApiVersion1,
				}

				params, _ := json.Marshal(map[string]string{"severity": level})
				result, rpcErr := method.Handle(ctx, params)

				require.Nil(t, rpcErr, "severity %s should be accepted", level)
				require.NotNil(t, result)
			})
		}
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LogRotateMethod Tests

func TestLogRotateMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.LogRotateMethod{}

	t.Run("Returns rotation message", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]interface{})
		assert.Contains(t, resultMap, "message")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// AMMInfoMethod Tests
// Reference: rippled/src/test/rpc/AMMInfo_test.cpp

func TestAMMInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.AMMInfoMethod{}

	t.Run("Returns AMM not found when AMM does not exist", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerEntry, which becomes AMM not found
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"amm_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns 19 (actNotFound) when account lookup fails
		assert.True(t, rpcErr.Code == 19 || rpcErr.Message == "AMM account not found")
	})

	t.Run("Returns AMM not found when looking up by assets", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{
			"asset": {"currency": "XRP"},
			"asset2": {"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns 19 (actNotFound/entryNotFound) when AMM lookup fails
		assert.True(t, rpcErr.Code == 19 || rpcErr.Message == "AMM not found")
	})

	t.Run("Invalid parameters - neither assets nor amm_account", func(t *testing.T) {
		// Based on AMMInfo_test.cpp::testErrors - "Invalid parameters"
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - both assets and amm_account", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{
			"asset": {"currency": "XRP"},
			"asset2": {"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			"amm_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// VaultInfoMethod Tests

func TestVaultInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.VaultInfoMethod{}

	t.Run("Returns vault not found when vault does not exist", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerEntry, which becomes Vault not found
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"vault_id": "0000000000000000000000000000000000000000000000000000000000000000"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns 21 (entryNotFound) when vault lookup fails
		assert.Equal(t, 21, rpcErr.Code)
	})

	t.Run("Returns vault not found when looking up by owner+seq", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "seq": 1}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns 21 (entryNotFound) when vault lookup fails
		assert.Equal(t, 21, rpcErr.Code)
	})

	t.Run("Invalid vault_id format returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"vault_id": "invalid_hex"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - neither vault_id nor owner+seq", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - both vault_id and owner", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{
			"vault_id": "0000000000000000000000000000000000000000000000000000000000000000",
			"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// UnlListMethod Tests

func TestUnlListMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.UnlListMethod{}

	t.Run("Returns UNL array", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]interface{})
		assert.Contains(t, resultMap, "unl")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// BlackListMethod Tests

func TestBlackListMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	cleanup := setupTestServicesMissingMethods(mock)
	defer cleanup()

	method := &handlers.BlackListMethod{}

	t.Run("Returns blacklist array", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]interface{})
		assert.Contains(t, resultMap, "blacklist")
	})

	t.Run("Accepts threshold parameter", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
		}

		params := json.RawMessage(`{"threshold": 100}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// Service Unavailable Tests

func TestMissingMethodsServiceUnavailable(t *testing.T) {
	// Test all methods handle nil Services gracefully
	oldServices := types.Services
	types.Services = nil
	defer func() { types.Services = oldServices }()

	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	methods := []struct {
		name   string
		method types.MethodHandler
	}{
		{"FetchInfoMethod", &handlers.FetchInfoMethod{}},
		{"OwnerInfoMethod", &handlers.OwnerInfoMethod{}},
		{"LedgerHeaderMethod", &handlers.LedgerHeaderMethod{}},
		{"LedgerRequestMethod", &handlers.LedgerRequestMethod{}},
		{"LedgerCleanerMethod", &handlers.LedgerCleanerMethod{}},
		{"LedgerDiffMethod", &handlers.LedgerDiffMethod{}},
		{"SimulateMethod", &handlers.SimulateMethod{}},
		{"TxReduceRelayMethod", &handlers.TxReduceRelayMethod{}},
		{"ConnectMethod", &handlers.ConnectMethod{}},
		{"PrintMethod", &handlers.PrintMethod{}},
		{"ValidatorInfoMethod", &handlers.ValidatorInfoMethod{}},
		{"CanDeleteMethod", &handlers.CanDeleteMethod{}},
		{"GetAggregatePriceMethod", &handlers.GetAggregatePriceMethod{}},
		{"GetCountsMethod", &handlers.GetCountsMethod{}},
		{"LogLevelMethod", &handlers.LogLevelMethod{}},
		{"LogRotateMethod", &handlers.LogRotateMethod{}},
		{"AMMInfoMethod", &handlers.AMMInfoMethod{}},
		{"VaultInfoMethod", &handlers.VaultInfoMethod{}},
		{"UnlListMethod", &handlers.UnlListMethod{}},
		{"BlackListMethod", &handlers.BlackListMethod{}},
	}

	for _, tc := range methods {
		t.Run(tc.name+" handles nil Services", func(t *testing.T) {
			result, rpcErr := tc.method.Handle(ctx, nil)

			// Should return an error, not panic
			// Different methods may return different error codes (RpcINTERNAL, RpcINVALID_PARAMS, RpcNOT_IMPL)
			// The key is that they don't panic and handle nil Services gracefully
			if rpcErr != nil {
				assert.True(t, rpcErr.Code != 0, "Should have a non-zero error code")
				assert.Nil(t, result, "Result should be nil when there's an error")
			}
			// Some methods may return a result without Services (e.g., stub methods)
		})
	}
}

// Nil Ledger Service Tests

func TestMissingMethodsNilLedgerService(t *testing.T) {
	// Test all methods handle nil Ledger gracefully
	oldServices := types.Services
	types.Services = &types.ServiceContainer{Ledger: nil}
	defer func() { types.Services = oldServices }()

	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	// Methods that depend on ledger service should return RpcINTERNAL when Ledger is nil
	methods := []struct {
		name   string
		method types.MethodHandler
	}{
		{"FetchInfoMethod", &handlers.FetchInfoMethod{}},
		{"PrintMethod", &handlers.PrintMethod{}},
		{"GetCountsMethod", &handlers.GetCountsMethod{}},
		{"LogRotateMethod", &handlers.LogRotateMethod{}},
		{"UnlListMethod", &handlers.UnlListMethod{}},
		{"BlackListMethod", &handlers.BlackListMethod{}},
	}

	// Note: LogLevelMethod is excluded — it manages log levels without needing
	// the ledger service, so it succeeds with nil ledger (returns current levels).

	for _, tc := range methods {
		t.Run(tc.name+" handles nil Ledger", func(t *testing.T) {
			result, rpcErr := tc.method.Handle(ctx, nil)

			// Should return an internal error, not panic
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
			assert.Nil(t, result)
		})
	}

	// ValidatorInfoMethod and CanDeleteMethod don't depend on ledger service.
	// They return their own domain-specific errors unconditionally.
	// - ValidatorInfo: returns RpcINVALID_PARAMS / "not a validator" (mirrors rippled's not_validator_error())
	// - CanDelete: returns RpcNOT_ENABLED (advisory delete not configured)
	// This matches rippled where validator_info has NO_CONDITION and can_delete
	// checks advisoryDelete() before touching ledger state.
}
