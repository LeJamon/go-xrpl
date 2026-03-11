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

// TestVersionReturnsVersionInfo tests that the version method returns API version range.
// Based on rippled Version_test.cpp testCorrectVersionNumber() and testVersionRPCV2()
func TestVersionReturnsVersionInfo(t *testing.T) {
	method := &handlers.VersionMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error for version call")
	require.NotNil(t, result, "Expected result")

	// Convert to map
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should have a "version" key
	require.Contains(t, resp, "version", "Response should contain 'version' key")

	version := resp["version"].(map[string]interface{})

	// Verify first, last, good fields exist and have correct values
	assert.Contains(t, version, "first")
	assert.Contains(t, version, "last")
	assert.Contains(t, version, "good")

	assert.Equal(t, float64(types.ApiVersion1), version["first"],
		"first should be ApiVersion1")
	assert.Equal(t, float64(types.ApiVersion3), version["last"],
		"last should be ApiVersion3")
	assert.Equal(t, float64(types.ApiVersion2), version["good"],
		"good should be ApiVersion2")
}

// TestVersionResponseStructure validates the response structure in detail.
// Based on rippled Version_test.cpp - version result should contain "version" object with numeric fields
func TestVersionResponseStructure(t *testing.T) {
	method := &handlers.VersionMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Only "version" key should be present at top level
	assert.Equal(t, 1, len(resp), "Response should have exactly one top-level key")

	version := resp["version"].(map[string]interface{})

	// All version fields should be numeric
	first, ok := version["first"].(float64)
	assert.True(t, ok, "'first' should be a number")
	assert.Greater(t, first, float64(0), "'first' should be positive")

	last, ok := version["last"].(float64)
	assert.True(t, ok, "'last' should be a number")
	assert.GreaterOrEqual(t, last, first, "'last' should be >= 'first'")

	good, ok := version["good"].(float64)
	assert.True(t, ok, "'good' should be a number")
	assert.GreaterOrEqual(t, good, first, "'good' should be >= 'first'")
	assert.LessOrEqual(t, good, last, "'good' should be <= 'last'")
}

// TestVersionNoParamsNeeded tests that the method works without any params.
func TestVersionNoParamsNeeded(t *testing.T) {
	method := &handlers.VersionMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Test with nil params
	result1, rpcErr1 := method.Handle(ctx, nil)
	require.Nil(t, rpcErr1)
	require.NotNil(t, result1)

	// Test with empty params
	paramsJSON, err := json.Marshal(map[string]interface{}{})
	require.NoError(t, err)
	result2, rpcErr2 := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr2)
	require.NotNil(t, result2)

	// Both should return the same result
	json1, err := json.Marshal(result1)
	require.NoError(t, err)
	json2, err := json.Marshal(result2)
	require.NoError(t, err)
	assert.JSONEq(t, string(json1), string(json2),
		"Nil and empty params should produce the same result")
}

// TestVersionMethodMetadata tests the method's metadata functions.
func TestVersionMethodMetadata(t *testing.T) {
	method := &handlers.VersionMethod{}

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"version should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
