package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVersionReturnsVersionInfo tests the version method's api_version 1 shape:
// first/good/last are the SemanticVersion strings "1.0.0", mirroring rippled
// setVersion's apiVersionIfUnspecified branch (RPCHelpers.h:219-224 with
// firstVersion/goodVersion/lastVersion = "1.0.0", RPCHelpers.cpp:1001-1003).
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
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should have a "version" key
	require.Contains(t, resp, "version", "Response should contain 'version' key")

	version := resp["version"].(map[string]any)

	// api_version 1: first/good/last are SemanticVersion strings, all "1.0.0".
	assert.Contains(t, version, "first")
	assert.Contains(t, version, "last")
	assert.Contains(t, version, "good")

	assert.Equal(t, "1.0.0", version["first"],
		"v1 first should be the SemanticVersion string 1.0.0")
	assert.Equal(t, "1.0.0", version["good"],
		"v1 good should be the SemanticVersion string 1.0.0")
	assert.Equal(t, "1.0.0", version["last"],
		"v1 last should be the SemanticVersion string 1.0.0")
}

// TestVersionV2ShapeNumericNoGood verifies the api_version >= 2 shape: numeric
// first (= 1) and last (beta-gated), with NO `good` field — mirroring rippled
// setVersion's else branch (RPCHelpers.h:227-229) and Version_test.cpp
// testVersionRPCV2.
func TestVersionV2ShapeNumericNoGood(t *testing.T) {
	method := &handlers.VersionMethod{}

	cases := []struct {
		name     string
		version  int
		beta     bool
		wantLast int
	}{
		{"v2_beta_off", types.ApiVersion2, false, types.MaxSupportedApiVersion},
		{"v2_beta_on", types.ApiVersion2, true, types.BetaApiVersion},
		{"v3_beta_on", types.ApiVersion3, true, types.BetaApiVersion},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: tc.version,
				Services:   &types.ServiceContainer{BetaRPCAPI: tc.beta},
			}

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(resultJSON, &resp))

			version := resp["version"].(map[string]any)
			assert.Equal(t, float64(types.ApiVersion1), version["first"],
				"v2+ first should be numeric apiMinimumSupportedVersion (1)")
			assert.Equal(t, float64(tc.wantLast), version["last"],
				"v2+ last should be numeric, beta-gated")
			assert.NotContains(t, version, "good",
				"v2+ must NOT emit a `good` field")
		})
	}
}

// TestVersionLastTracksBetaFlag verifies that the `version` method reports
// `last:2` when beta_rpc_api is off and `last:3` when it is on, mirroring
// rippled setVersion which caps `last` at apiBetaVersion only with BETA_RPC_API.
// The beta-gated `last` lives in the numeric (api_version >= 2) branch, so the
// request is resolved at v2.
func TestVersionLastTracksBetaFlag(t *testing.T) {
	method := &handlers.VersionMethod{}

	cases := []struct {
		name     string
		beta     bool
		wantLast int
	}{
		{"beta_disabled", false, types.MaxSupportedApiVersion},
		{"beta_enabled", true, types.BetaApiVersion},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   &types.ServiceContainer{BetaRPCAPI: tc.beta},
			}

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, err := json.Marshal(result)
			require.NoError(t, err)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(resultJSON, &resp))

			version := resp["version"].(map[string]any)
			assert.Equal(t, float64(types.ApiVersion1), version["first"])
			assert.Equal(t, float64(tc.wantLast), version["last"])
		})
	}
}

// TestVersionResponseStructure validates the api_version >= 2 response
// structure in detail: a single top-level "version" object with numeric
// first/last and no `good` field (rippled setVersion else branch,
// RPCHelpers.h:227-229).
func TestVersionResponseStructure(t *testing.T) {
	method := &handlers.VersionMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion2,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Only "version" key should be present at top level
	assert.Equal(t, 1, len(resp), "Response should have exactly one top-level key")

	version := resp["version"].(map[string]any)

	// v2+ first/last are numeric.
	first, ok := version["first"].(float64)
	assert.True(t, ok, "'first' should be a number")
	assert.Greater(t, first, float64(0), "'first' should be positive")

	last, ok := version["last"].(float64)
	assert.True(t, ok, "'last' should be a number")
	assert.GreaterOrEqual(t, last, first, "'last' should be >= 'first'")

	// v2+ omits `good` entirely.
	assert.NotContains(t, version, "good", "v2+ must not emit a `good` field")
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
	paramsJSON, err := json.Marshal(map[string]any{})
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
