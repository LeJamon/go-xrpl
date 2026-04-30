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

// ValidatorsMethod tests
// Based on rippled ValidatorRPC_test.cpp

// TestValidatorsResponseStructure tests that the validators method returns
// the expected response structure with all required fields.
// Reference: rippled ValidatorRPC_test.cpp testStaticUNL — checks that
// trusted_validator_keys, publisher_lists, validation_quorum are present.
func TestValidatorsResponseStructure(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ValidatorsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error from validators")
	require.NotNil(t, result, "Expected result from validators")

	// Marshal and unmarshal to get map
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Verify required fields are present per rippled response structure
	assert.Contains(t, resp, "trusted_validator_keys",
		"Response must contain trusted_validator_keys")
	assert.Contains(t, resp, "publisher_lists",
		"Response must contain publisher_lists")
	assert.Contains(t, resp, "validation_quorum",
		"Response must contain validation_quorum")
}

// TestValidatorsEmptyList tests that the stub returns empty validator lists.
// In standalone mode with no configured validators, all lists should be empty.
// Reference: rippled ValidatorRPC_test.cpp — when no validators configured,
// trusted_validator_keys.size() == 0 and publisher_lists.size() == 0.
func TestValidatorsEmptyList(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ValidatorsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Stub should return empty arrays
	trustedKeys := resp["trusted_validator_keys"].([]interface{})
	assert.Empty(t, trustedKeys, "Stub should return empty trusted_validator_keys")

	publisherLists := resp["publisher_lists"].([]interface{})
	assert.Empty(t, publisherLists, "Stub should return empty publisher_lists")

	// Quorum should be 0 for stub
	assert.Equal(t, float64(0), resp["validation_quorum"],
		"Stub should return validation_quorum of 0")
}

// TestValidatorsAdminOnly tests that the validators method requires admin role.
// Reference: rippled ValidatorRPC_test.cpp testPrivileges — non-admin requests
// return HTTP 403 / null result for "validators" and "validator_list_sites".
func TestValidatorsAdminOnly(t *testing.T) {
	method := &handlers.ValidatorsMethod{}

	assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
		"validators should require admin role")
}

// TestValidatorsMethodMetadata tests the method's metadata functions.
func TestValidatorsMethodMetadata(t *testing.T) {
	method := &handlers.ValidatorsMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"validators should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestValidatorsWithParams tests that providing params does not cause errors.
// The validators method accepts no parameters but should not fail if extras are sent.
func TestValidatorsWithParams(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ValidatorsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params, err := json.Marshal(map[string]interface{}{
		"extra": "value",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, params)
	require.Nil(t, rpcErr, "Extra params should not cause an error")
	require.NotNil(t, result, "Should still return a result")
}

// ValidationCreateMethod tests
// Based on rippled ValidatorRPC_test.cpp test_validation_create

// TestValidationCreateReturnsKeyPair tests that validation_create returns
// a response (currently a notImplemented error since it's a stub).
// Reference: rippled ValidatorRPC_test.cpp test_validation_create — expects
// status == "success" and the result to contain validation key fields.
func TestValidationCreateReturnsKeyPair(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ValidationCreateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Call without params (generate random key pair)
	result, rpcErr := method.Handle(ctx, nil)

	// Current stub returns notImplemented
	assert.Nil(t, result, "Stub should return nil result")
	require.NotNil(t, rpcErr, "Stub should return an RPC error")
	assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code,
		"Should return notImplemented error code")
	assert.Equal(t, "notImplemented", rpcErr.ErrorString,
		"Error string should be notImplemented")
}

// TestValidationCreateWithSecret tests validation_create with a secret parameter.
// Reference: rippled ValidatorRPC_test.cpp test_validation_create — calls with
// "BAWL MAN JADE MOON DOVE GEM SON NOW HAD ADEN GLOW TIRE" and expects success.
func TestValidationCreateWithSecret(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ValidationCreateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params, err := json.Marshal(map[string]interface{}{
		"secret": "BAWL MAN JADE MOON DOVE GEM SON NOW HAD ADEN GLOW TIRE",
	})
	require.NoError(t, err)

	// Call with secret param
	result, rpcErr := method.Handle(ctx, params)

	// Current stub returns notImplemented regardless of params
	assert.Nil(t, result, "Stub should return nil result")
	require.NotNil(t, rpcErr, "Stub should return an RPC error")
	assert.Equal(t, types.RpcNOT_IMPL, rpcErr.Code,
		"Should return notImplemented error code")
}

// TestValidationCreateAdminOnly tests that validation_create requires admin role.
// Reference: rippled — validation_create is an admin-only method.
func TestValidationCreateAdminOnly(t *testing.T) {
	method := &handlers.ValidationCreateMethod{}

	assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
		"validation_create should require admin role")
}

// TestValidationCreateMethodMetadata tests the method's metadata functions.
func TestValidationCreateMethodMetadata(t *testing.T) {
	method := &handlers.ValidationCreateMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"validation_create should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// ConsensusInfoMethod tests

// TestConsensusInfoResponseStructure tests that consensus_info returns
// the expected response structure with an "info" field.
// Reference: rippled ConsensusInfo.cpp — returns consensus state info including
// phase, proposing, validating, proposers, etc. In standalone mode, empty info
// is the correct response.
func TestConsensusInfoResponseStructure(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ConsensusInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error from consensus_info")
	require.NotNil(t, result, "Expected result from consensus_info")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Must contain "info" field
	assert.Contains(t, resp, "info", "Response must contain 'info' field")

	// Info should be a map (empty in standalone stub)
	infoMap, ok := resp["info"].(map[string]interface{})
	assert.True(t, ok, "info field should be a map")
	assert.Empty(t, infoMap, "Stub should return empty info map in standalone mode")
}

// TestConsensusInfoAdminOnly tests that consensus_info requires admin role.
func TestConsensusInfoAdminOnly(t *testing.T) {
	method := &handlers.ConsensusInfoMethod{}

	assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
		"consensus_info should require admin role")
}

// TestConsensusInfoMethodMetadata tests the method's metadata functions.
func TestConsensusInfoMethodMetadata(t *testing.T) {
	method := &handlers.ConsensusInfoMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"consensus_info should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestConsensusInfoWithParams tests that providing params does not cause errors.
// The consensus_info method accepts no parameters but should not fail if extras are sent.
func TestConsensusInfoWithParams(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.ConsensusInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params, err := json.Marshal(map[string]interface{}{
		"extra": "value",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, params)
	require.Nil(t, rpcErr, "Extra params should not cause an error")
	require.NotNil(t, result, "Should still return a result")
}

// StopMethod tests

// TestStopReturnsStoppingMessage tests that the stop method returns
// the expected "ripple server stopping" message.
// Reference: rippled Stop.cpp — returns message "ripple server stopping".
func TestStopReturnsStoppingMessage(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	// Set up a shutdown function that records it was called
	shutdownCalled := false
	services.ShutdownFunc = func() {
		shutdownCalled = true
	}

	method := &handlers.StopMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error from stop")
	require.NotNil(t, result, "Expected result from stop")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	assert.Equal(t, "ripple server stopping", resp["message"],
		"Stop should return 'ripple server stopping' message")
	assert.True(t, shutdownCalled,
		"Shutdown function should have been called")
}

// TestStopAdminOnly tests that the stop method requires admin role.
// The stop method is critical and must only be accessible to admins.
func TestStopAdminOnly(t *testing.T) {
	method := &handlers.StopMethod{}

	assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
		"stop should require admin role")
}

// TestStopMethodMetadata tests the method's metadata functions.
func TestStopMethodMetadata(t *testing.T) {
	method := &handlers.StopMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"stop should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestStopServiceUnavailable tests behavior when Services is nil.
// When the service container is not initialized, stop should return an internal error.
func TestStopServiceUnavailable(t *testing.T) {
	method := &handlers.StopMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result, "Expected nil result when service unavailable")
	require.NotNil(t, rpcErr, "Expected RPC error when service unavailable")
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code,
		"Should return internal error code")
	assert.Contains(t, rpcErr.Message, "Shutdown function not available",
		"Error message should indicate shutdown function not available")
}

// TestStopShutdownFuncNil tests behavior when ShutdownFunc is nil.
// When the shutdown function is not set, stop should return an internal error.
func TestStopShutdownFuncNil(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	// ShutdownFunc is nil by default
	services.ShutdownFunc = nil

	method := &handlers.StopMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result, "Expected nil result when shutdown func nil")
	require.NotNil(t, rpcErr, "Expected RPC error when shutdown func nil")
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code,
		"Should return internal error code")
	assert.Contains(t, rpcErr.Message, "Shutdown function not available",
		"Error message should indicate shutdown function not available")
}

// TestStopWithParams tests that providing params does not affect stop behavior.
func TestStopWithParams(t *testing.T) {
	mock := newMockLedgerService()
	services := &types.ServiceContainer{Ledger: mock}

	shutdownCalled := false
	services.ShutdownFunc = func() {
		shutdownCalled = true
	}

	method := &handlers.StopMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params, err := json.Marshal(map[string]interface{}{
		"extra": "value",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, params)

	require.Nil(t, rpcErr, "Extra params should not cause an error")
	require.NotNil(t, result, "Should still return a result")
	assert.True(t, shutdownCalled, "Shutdown should still be triggered")
}
