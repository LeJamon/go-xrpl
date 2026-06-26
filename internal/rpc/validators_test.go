package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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
	var resp map[string]any
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
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Stub should return empty arrays
	trustedKeys := resp["trusted_validator_keys"].([]any)
	assert.Empty(t, trustedKeys, "Stub should return empty trusted_validator_keys")

	publisherLists := resp["publisher_lists"].([]any)
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

	params, err := json.Marshal(map[string]any{
		"extra": "value",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, params)
	require.Nil(t, rpcErr, "Extra params should not cause an error")
	require.NotNil(t, result, "Should still return a result")
}

// ValidationCreateMethod tests
// Based on rippled ValidatorRPC_test.cpp test_validation_create

// TestValidationCreateReturnsKeyPair tests that validation_create generates a
// fresh validator keypair when called without a secret.
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

	require.Nil(t, rpcErr, "validation_create should succeed")
	require.NotNil(t, result, "validation_create should return a result")

	resp, ok := result.(map[string]any)
	require.True(t, ok, "result should be a map")

	for _, field := range []string{"validation_key", "validation_private_key", "validation_public_key", "validation_seed"} {
		val, ok := resp[field].(string)
		require.True(t, ok, "%s should be a string", field)
		assert.NotEmpty(t, val, "%s should not be empty", field)
	}
	assert.True(t, strings.HasPrefix(resp["validation_public_key"].(string), "n"),
		"validation_public_key should be a node public key (n...)")
	assert.True(t, strings.HasPrefix(resp["validation_seed"].(string), "s"),
		"validation_seed should be a base58 family seed (s...)")

	// Two random invocations must produce distinct keys.
	result2, rpcErr2 := method.Handle(ctx, nil)
	require.Nil(t, rpcErr2)
	resp2 := result2.(map[string]any)
	assert.NotEqual(t, resp["validation_seed"], resp2["validation_seed"],
		"random invocations should produce distinct seeds")
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

	const secret = "BAWL MAN JADE MOON DOVE GEM SON NOW HAD ADEN GLOW TIRE"
	params, err := json.Marshal(map[string]any{
		"secret": secret,
	})
	require.NoError(t, err)

	// Call with secret param
	result, rpcErr := method.Handle(ctx, params)

	require.Nil(t, rpcErr, "validation_create with secret should succeed")
	require.NotNil(t, result, "validation_create should return a result")

	resp, ok := result.(map[string]any)
	require.True(t, ok, "result should be a map")

	// RFC-1751 round-trips: the returned validation_key echoes the secret.
	assert.Equal(t, secret, resp["validation_key"],
		"validation_key should echo the RFC-1751 secret")
	assert.True(t, strings.HasPrefix(resp["validation_public_key"].(string), "n"),
		"validation_public_key should be a node public key (n...)")

	// Derivation is deterministic for a given secret.
	result2, rpcErr2 := method.Handle(ctx, params)
	require.Nil(t, rpcErr2)
	assert.Equal(t, resp, result2.(map[string]any),
		"the same secret should yield identical keys")
}

// callValidationCreate invokes validation_create with the given secret and
// returns the successful result map, failing the test otherwise.
func callValidationCreate(t *testing.T, method *handlers.ValidationCreateMethod, ctx *types.RpcContext, secret string) map[string]any {
	t.Helper()
	params, err := json.Marshal(map[string]any{"secret": secret})
	require.NoError(t, err)
	result, rpcErr := method.Handle(ctx, params)
	require.Nil(t, rpcErr, "validation_create(%q) should succeed", secret)
	resp, ok := result.(map[string]any)
	require.True(t, ok, "result should be a map")
	return resp
}

// TestValidationCreateHexSeed verifies a 32-hex-char secret is parsed as the
// raw 128-bit seed (rippled parseGenericSeed, Seed.cpp:111-116) rather than
// hashed as a passphrase: it must derive the same key as the equivalent base58
// family seed.
func TestValidationCreateHexSeed(t *testing.T) {
	method := &handlers.ValidationCreateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	const hexSeed = "DEDCE9CE67B451D852FD4E846FCDE31C" // 32 hex chars = 16-byte seed
	entropy, err := hex.DecodeString(hexSeed)
	require.NoError(t, err)
	base58Seed, err := addresscodec.EncodeSeed(entropy, secp256k1.SECP256K1())
	require.NoError(t, err)

	fromHex := callValidationCreate(t, method, ctx, hexSeed)
	fromBase58 := callValidationCreate(t, method, ctx, base58Seed)

	assert.Equal(t, base58Seed, fromHex["validation_seed"],
		"a 32-hex secret should resolve to the equivalent base58 family seed")
	assert.Equal(t, fromBase58["validation_public_key"], fromHex["validation_public_key"],
		"hex secret and equivalent base58 seed must derive the same key")
}

// TestValidationCreateRejectsKeyTokens verifies a secret that is itself a
// key/account token is rejected with badSeed (rippled parseGenericSeed,
// Seed.cpp:102-109) instead of being silently hashed as a passphrase.
func TestValidationCreateRejectsKeyTokens(t *testing.T) {
	method := &handlers.ValidationCreateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	// A genuine node public key (n...), produced by the method itself.
	generated, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	nodePublicKey := generated.(map[string]any)["validation_public_key"].(string)

	params, err := json.Marshal(map[string]any{"secret": nodePublicKey})
	require.NoError(t, err)
	_, rpcErr = method.Handle(ctx, params)
	require.NotNil(t, rpcErr, "a node public key must not be accepted as a seed")
	assert.Equal(t, types.RpcBAD_SEED, rpcErr.Code)
}

// TestValidationCreateEmptySecret verifies an explicit empty secret is rejected
// with badSeed: rippled distinguishes an absent secret (random key) from a
// present empty one (Seed.cpp:99-100).
func TestValidationCreateEmptySecret(t *testing.T) {
	method := &handlers.ValidationCreateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	params, err := json.Marshal(map[string]any{"secret": ""})
	require.NoError(t, err)
	_, rpcErr := method.Handle(ctx, params)
	require.NotNil(t, rpcErr, "an explicit empty secret should be rejected")
	assert.Equal(t, types.RpcBAD_SEED, rpcErr.Code)
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
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Must contain "info" field
	assert.Contains(t, resp, "info", "Response must contain 'info' field")

	// Info should be a map (empty in standalone stub)
	infoMap, ok := resp["info"].(map[string]any)
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

	params, err := json.Marshal(map[string]any{
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
	var resp map[string]any
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

	params, err := json.Marshal(map[string]any{
		"extra": "value",
	})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, params)

	require.Nil(t, rpcErr, "Extra params should not cause an error")
	require.NotNil(t, result, "Should still return a result")
	assert.True(t, shutdownCalled, "Shutdown should still be triggered")
}
