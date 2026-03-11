package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Amendment Blocked Conformance Tests
// Reference: rippled/src/test/rpc/AmendmentBlocked_test.cpp
//
// When the server is amendment-blocked, methods with any Condition other than
// NoCondition are blocked with rpcAMENDMENT_BLOCKED (code 40).
// Methods with NoCondition continue to work normally.
// =============================================================================

// blockedMethods are methods that require a condition (NEEDS_CURRENT_LEDGER,
// NEEDS_CLOSED_LEDGER, or NEEDS_NETWORK_CONNECTION) and should be blocked.
// Based on rippled's AmendmentBlocked_test.cpp testBlockedMethods.
var blockedMethods = []string{
	"submit",
	"submit_multisigned",
	"ledger_accept",
	"ledger_current",
	"ledger_closed",
	"fee",
	"owner_info",
	"path_find",
	"deposit_authorized",
	"get_aggregate_price",
	"simulate",
	"tx",
}

// unblockedMethods are methods with NO_CONDITION that should continue working.
// Based on rippled's test which explicitly verifies sign_for works during blocking.
var unblockedMethods = []string{
	"sign_for",
	"sign",
	"server_info",
	"account_info",
	"ping",
	"random",
	"server_definitions",
	"version",
	"feature",
	"book_offers",
	"ledger",
	"ledger_data",
	"ledger_entry",
	"account_lines",
	"account_channels",
	"account_currencies",
	"account_nfts",
	"account_objects",
	"account_offers",
	"account_tx",
	"book_changes",
	"channel_authorize",
	"channel_verify",
	"gateway_balances",
	"noripple_check",
	"nft_buy_offers",
	"nft_sell_offers",
	"transaction_entry",
	"tx_history",
	"wallet_propose",
	"subscribe",
	"unsubscribe",
}

// newTestServer creates a Server with all methods registered for testing
func newTestServer() *Server {
	server := &Server{
		registry: types.NewMethodRegistry(),
	}
	server.registerAllMethods()
	return server
}

// TestAmendmentBlockedMethodsReturnError verifies that methods with conditions
// return rpcAMENDMENT_BLOCKED (code 40) when the server is amendment-blocked.
// Reference: rippled AmendmentBlocked_test.cpp testBlockedMethods - step 3
func TestAmendmentBlockedMethodsReturnError(t *testing.T) {
	mock := newMockLedgerService()
	mock.amendmentBlocked = true
	cleanup := setupTestServices(mock)
	defer cleanup()

	server := newTestServer()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	for _, method := range blockedMethods {
		t.Run(method, func(t *testing.T) {
			// Provide minimal valid params to get past param validation
			params := json.RawMessage(`{}`)
			result, rpcErr := server.executeMethod(method, params, ctx)

			assert.Nil(t, result, "Expected nil result when amendment blocked for %s", method)
			require.NotNil(t, rpcErr, "Expected error when amendment blocked for %s", method)
			assert.Equal(t, types.RpcAMENDMENT_BLOCKED, rpcErr.Code,
				"Expected amendmentBlocked error code (40) for %s, got %d", method, rpcErr.Code)
			assert.Equal(t, "amendmentBlocked", rpcErr.ErrorString,
				"Expected 'amendmentBlocked' error string for %s", method)
		})
	}
}

// TestAmendmentBlockedUnblockedMethodsStillWork verifies that methods with
// NO_CONDITION continue to work when the server is amendment-blocked.
// Reference: rippled AmendmentBlocked_test.cpp testBlockedMethods - sign_for
func TestAmendmentBlockedUnblockedMethodsStillWork(t *testing.T) {
	mock := newMockLedgerService()
	mock.amendmentBlocked = true
	cleanup := setupTestServices(mock)
	defer cleanup()

	server := newTestServer()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	for _, method := range unblockedMethods {
		t.Run(method, func(t *testing.T) {
			params := json.RawMessage(`{}`)
			_, rpcErr := server.executeMethod(method, params, ctx)

			// The method should NOT return amendmentBlocked.
			// It may return other errors (e.g., missing params), but never code 40.
			if rpcErr != nil {
				assert.NotEqual(t, types.RpcAMENDMENT_BLOCKED, rpcErr.Code,
					"Method %s should NOT be amendment-blocked (NoCondition), got error: %s", method, rpcErr.Message)
			}
			// If no error, that's also fine — the method ran successfully
		})
	}
}

// TestAmendmentNotBlockedAllMethodsWork verifies that when the server is NOT
// amendment-blocked, all methods work normally (none return amendmentBlocked).
// Reference: rippled AmendmentBlocked_test.cpp testBlockedMethods - step 1
func TestAmendmentNotBlockedAllMethodsWork(t *testing.T) {
	mock := newMockLedgerService()
	mock.amendmentBlocked = false
	cleanup := setupTestServices(mock)
	defer cleanup()

	server := newTestServer()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	allMethods := append(blockedMethods, unblockedMethods...)
	for _, method := range allMethods {
		t.Run(method, func(t *testing.T) {
			params := json.RawMessage(`{}`)
			_, rpcErr := server.executeMethod(method, params, ctx)

			// No method should return amendmentBlocked when not blocked
			if rpcErr != nil {
				assert.NotEqual(t, types.RpcAMENDMENT_BLOCKED, rpcErr.Code,
					"Method %s should not be amendment-blocked when server is not blocked", method)
			}
		})
	}
}

// TestAmendmentBlockedErrorFormat verifies the exact error response format
// matches rippled's amendmentBlocked error.
func TestAmendmentBlockedErrorFormat(t *testing.T) {
	mock := newMockLedgerService()
	mock.amendmentBlocked = true
	cleanup := setupTestServices(mock)
	defer cleanup()

	server := newTestServer()
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	// Use submit as a representative blocked method
	_, rpcErr := server.executeMethod("submit", json.RawMessage(`{}`), ctx)

	require.NotNil(t, rpcErr)
	assert.Equal(t, 40, rpcErr.Code)
	assert.Equal(t, "amendmentBlocked", rpcErr.ErrorString)
	assert.Equal(t, "amendmentBlocked", rpcErr.Type)
	assert.Equal(t, "Amendment blocked, need upgrade.", rpcErr.Message)
}

// TestAmendmentBlockedConditionClassification verifies the correct count of
// methods per condition, acting as a regression guard.
func TestAmendmentBlockedConditionClassification(t *testing.T) {
	server := newTestServer()
	methods := server.registry.List()

	counts := map[types.Condition]int{
		types.NoCondition:             0,
		types.NeedsNetworkConnection: 0,
		types.NeedsCurrentLedger:     0,
		types.NeedsClosedLedger:      0,
	}

	for _, name := range methods {
		handler, ok := server.registry.Get(name)
		require.True(t, ok, "Method %s should be in registry", name)
		counts[handler.RequiredCondition()]++
	}

	// Verify blocked methods count (NEEDS_CURRENT_LEDGER + NEEDS_CLOSED_LEDGER + NEEDS_NETWORK_CONNECTION)
	totalBlocked := counts[types.NeedsCurrentLedger] + counts[types.NeedsClosedLedger] + counts[types.NeedsNetworkConnection]
	assert.Equal(t, len(blockedMethods), totalBlocked,
		"Blocked method count mismatch: NeedsCurrentLedger=%d, NeedsClosedLedger=%d, NeedsNetworkConnection=%d",
		counts[types.NeedsCurrentLedger], counts[types.NeedsClosedLedger], counts[types.NeedsNetworkConnection])

	// Verify specific condition counts based on rippled
	assert.Equal(t, 10, counts[types.NeedsCurrentLedger], "NeedsCurrentLedger count")
	assert.Equal(t, 1, counts[types.NeedsClosedLedger], "NeedsClosedLedger count")
	assert.Equal(t, 1, counts[types.NeedsNetworkConnection], "NeedsNetworkConnection count")

	t.Logf("Condition distribution: NoCondition=%d, NeedsCurrentLedger=%d, NeedsClosedLedger=%d, NeedsNetworkConnection=%d",
		counts[types.NoCondition], counts[types.NeedsCurrentLedger],
		counts[types.NeedsClosedLedger], counts[types.NeedsNetworkConnection])
}

// TestAllHandlersDeclareCondition verifies every registered handler
// returns a valid Condition value from RequiredCondition().
func TestAllHandlersDeclareCondition(t *testing.T) {
	server := newTestServer()
	methods := server.registry.List()

	validConditions := map[types.Condition]bool{
		types.NoCondition:             true,
		types.NeedsNetworkConnection: true,
		types.NeedsCurrentLedger:     true,
		types.NeedsClosedLedger:      true,
	}

	for _, name := range methods {
		handler, ok := server.registry.Get(name)
		require.True(t, ok)
		cond := handler.RequiredCondition()
		assert.True(t, validConditions[cond],
			"Method %s has invalid condition value: %d", name, cond)
	}
}
