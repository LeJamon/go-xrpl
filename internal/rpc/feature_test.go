package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFeatureNoParams tests that calling feature with no params returns all features.
// Based on rippled Feature_test.cpp testNoParams()
func TestFeatureNoParams(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Call with nil params (no parameters)
	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error for feature with no params")
	require.NotNil(t, result, "Expected result")

	// Convert to map
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should have a "features" key
	require.Contains(t, resp, "features", "Response should contain 'features' key")

	features := resp["features"].(map[string]interface{})

	// There should be at least some features registered
	allFeatures := amendment.AllFeatures()
	require.Greater(t, len(allFeatures), 0, "There should be at least some features")
	assert.Equal(t, len(allFeatures), len(features),
		"All registered features should be returned")

	// Verify each feature has the expected structure
	for hexID, featureData := range features {
		feature := featureData.(map[string]interface{})
		assert.Contains(t, feature, "name", "Feature %s should have 'name'", hexID)
		assert.Contains(t, feature, "enabled", "Feature %s should have 'enabled'", hexID)
		assert.Contains(t, feature, "supported", "Feature %s should have 'supported'", hexID)
		assert.Contains(t, feature, "vetoed", "Feature %s should have 'vetoed'", hexID)

		// Name should be a non-empty string
		name, ok := feature["name"].(string)
		assert.True(t, ok, "Feature name should be a string")
		assert.NotEmpty(t, name, "Feature name should not be empty")

		// enabled and supported should be booleans
		_, ok = feature["enabled"].(bool)
		assert.True(t, ok, "Feature enabled should be a boolean")
		_, ok = feature["supported"].(bool)
		assert.True(t, ok, "Feature supported should be a boolean")
	}
}

// TestFeatureNoParamsEmptyObject tests that calling feature with empty params returns all features.
func TestFeatureNoParamsEmptyObject(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	paramsJSON, err := json.Marshal(map[string]interface{}{})
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error for feature with empty params")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	require.Contains(t, resp, "features")
	features := resp["features"].(map[string]interface{})
	assert.Greater(t, len(features), 0, "Should return all features")
}

// TestFeatureSingleLookupByName tests looking up a single feature by name.
// Based on rippled Feature_test.cpp testSingleFeature()
func TestFeatureSingleLookupByName(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Pick the first feature from the registry to test with
	allFeatures := amendment.AllFeatures()
	require.Greater(t, len(allFeatures), 0, "Need at least one feature for test")
	testFeature := allFeatures[0]

	params := map[string]interface{}{
		"feature": testFeature.Name,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error for valid feature name lookup")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should contain exactly one entry keyed by hex ID
	assert.Equal(t, 1, len(resp), "Single feature lookup should return one entry")

	expectedHexID := strings.ToUpper(hex.EncodeToString(testFeature.ID[:]))
	require.Contains(t, resp, expectedHexID, "Response should contain feature hex ID")

	feature := resp[expectedHexID].(map[string]interface{})
	assert.Equal(t, testFeature.Name, feature["name"], "Feature name should match")
	assert.Contains(t, feature, "enabled")
	assert.Contains(t, feature, "supported")
	assert.Contains(t, feature, "vetoed")
}

// TestFeatureSingleLookupByHexID tests looking up a single feature by hex ID.
// Based on rippled Feature_test.cpp testNonAdmin - single feature by hex
func TestFeatureSingleLookupByHexID(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Pick a feature and use its hex ID
	allFeatures := amendment.AllFeatures()
	require.Greater(t, len(allFeatures), 0, "Need at least one feature for test")
	testFeature := allFeatures[0]

	hexID := strings.ToUpper(hex.EncodeToString(testFeature.ID[:]))

	params := map[string]interface{}{
		"feature": hexID,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	require.Nil(t, rpcErr, "Expected no error for valid feature hex ID lookup")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Response should contain exactly one entry
	assert.Equal(t, 1, len(resp), "Single feature lookup should return one entry")
	require.Contains(t, resp, hexID)

	feature := resp[hexID].(map[string]interface{})
	assert.Equal(t, testFeature.Name, feature["name"])
	assert.Contains(t, feature, "enabled")
	assert.Contains(t, feature, "supported")
	assert.Contains(t, feature, "vetoed")
}

// TestFeatureInvalidName tests that looking up a non-existent feature name returns error.
// Based on rippled Feature_test.cpp testInvalidFeature()
func TestFeatureInvalidName(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name    string
		feature string
	}{
		{"unknown feature name", "AllTheThings"},
		{"case sensitive mismatch", "multisignreserve"}, // wrong case
		{"empty-ish string", "x"},
		{"random string", "notAFeature"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{
				"feature": tc.feature,
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for invalid feature name")
			require.NotNil(t, rpcErr, "Expected error for invalid feature name")
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Contains(t, rpcErr.Message, "Feature not found")
		})
	}
}

// TestFeatureResponseStructure validates the structure of each feature entry.
// Based on rippled Feature_test.cpp testNoParams() - validates enabled/supported/vetoed fields
func TestFeatureResponseStructure(t *testing.T) {
	mock := newMockLedgerService()
	services := newTestServices(mock)

	method := &handlers.FeatureMethod{}
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

	features := resp["features"].(map[string]interface{})

	for hexID, featureData := range features {
		feature := featureData.(map[string]interface{})

		t.Run("feature_"+hexID[:8], func(t *testing.T) {
			// Verify hex ID is valid 64-char hex string
			assert.Equal(t, 64, len(hexID), "Feature hex ID should be 64 characters")
			_, err := hex.DecodeString(hexID)
			assert.NoError(t, err, "Feature hex ID should be valid hex")

			// Verify structure: {name, supported, enabled, vetoed}
			name := feature["name"].(string)
			assert.NotEmpty(t, name)

			supported := feature["supported"].(bool)
			enabled := feature["enabled"].(bool)

			// If enabled, it must be supported
			if enabled {
				assert.True(t, supported, "Enabled feature %s must be supported", name)
			}

			// vetoed can be bool or string "Obsolete"
			vetoed := feature["vetoed"]
			switch v := vetoed.(type) {
			case bool:
				// Valid
				_ = v
			case string:
				assert.Equal(t, "Obsolete", v, "String vetoed value should be 'Obsolete'")
			default:
				t.Errorf("vetoed for feature %s has unexpected type %T", name, vetoed)
			}
		})
	}
}

// TestFeatureMethodMetadata tests the method's metadata functions.
// Verifies admin-only access requirement.
func TestFeatureMethodMetadata(t *testing.T) {
	method := &handlers.FeatureMethod{}

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole(),
			"feature should require admin role")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
