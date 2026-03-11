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

// TestPeersResponseStructure tests that peers returns the expected response structure.
// Based on rippled Peers_test.cpp testRequest() - basic structure check
func TestPeersResponseStructure(t *testing.T) {
	method := &handlers.PeersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error for peers call")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should contain "peers" key
	assert.Contains(t, resp, "peers", "Response should contain 'peers' key")
}

// TestPeersEmptyList tests that the stub returns an empty peers list.
// Based on rippled Peers_test.cpp testRequest() - empty cluster before any nodes added
func TestPeersEmptyList(t *testing.T) {
	method := &handlers.PeersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
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

	// In standalone mode (stub), peers list should be empty
	peersRaw := resp["peers"]
	peers, ok := peersRaw.([]interface{})
	require.True(t, ok, "peers should be an array")
	assert.Equal(t, 0, len(peers), "Stub should return empty peers list")
}

// TestPeersWithEmptyParams tests that peers works with empty params.
func TestPeersWithEmptyParams(t *testing.T) {
	method := &handlers.PeersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}

	paramsJSON, err := json.Marshal(map[string]interface{}{})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error with empty params")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	assert.Contains(t, resp, "peers")
}

// TestPeersMethodMetadata tests the method's metadata functions.
// Verifies admin-only access requirement.
func TestPeersMethodMetadata(t *testing.T) {
	method := &handlers.PeersMethod{}

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"peers should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
