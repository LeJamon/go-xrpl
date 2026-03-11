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

// TestServerDefinitionsReturnsTypeDefinitions tests that server_definitions returns
// all required definition categories: TYPES, FIELDS, LEDGER_ENTRY_TYPES,
// TRANSACTION_TYPES, and TRANSACTION_RESULTS.
// Reference: rippled ServerDefinitions.cpp
func TestServerDefinitionsReturnsTypeDefinitions(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)

	require.Nil(t, rpcErr, "Expected no error for server_definitions")
	require.NotNil(t, result, "Expected result")

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Verify all top-level definition categories are present
	requiredKeys := []string{
		"TYPES",
		"FIELDS",
		"LEDGER_ENTRY_TYPES",
		"TRANSACTION_TYPES",
		"TRANSACTION_RESULTS",
	}
	for _, key := range requiredKeys {
		assert.Contains(t, resp, key, "Response should contain '%s'", key)
	}
}

// TestServerDefinitionsFieldsArrayFormat validates that FIELDS is an array of
// [name, {nth, isVLEncoded, isSerialized, isSigningField, type}] pairs.
// Reference: rippled definitions.json format
func TestServerDefinitionsFieldsArrayFormat(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
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

	fieldsRaw, ok := resp["FIELDS"].([]interface{})
	require.True(t, ok, "FIELDS should be an array")
	require.Greater(t, len(fieldsRaw), 0, "FIELDS should not be empty")

	// Validate the format of at least the first few entries
	for i, entry := range fieldsRaw {
		if i >= 5 {
			break // Spot-check first 5
		}
		pair, ok := entry.([]interface{})
		require.True(t, ok, "Each FIELDS entry should be an array")
		require.Equal(t, 2, len(pair), "Each FIELDS entry should have 2 elements [name, info]")

		// First element is the field name (string)
		fieldName, ok := pair[0].(string)
		assert.True(t, ok, "Field name should be a string")
		assert.NotEmpty(t, fieldName, "Field name should not be empty")

		// Second element is the field info (object)
		fieldInfo, ok := pair[1].(map[string]interface{})
		require.True(t, ok, "Field info should be an object")

		// Verify required field info keys
		assert.Contains(t, fieldInfo, "nth", "Field '%s' info should have 'nth'", fieldName)
		assert.Contains(t, fieldInfo, "isVLEncoded", "Field '%s' info should have 'isVLEncoded'", fieldName)
		assert.Contains(t, fieldInfo, "isSerialized", "Field '%s' info should have 'isSerialized'", fieldName)
		assert.Contains(t, fieldInfo, "isSigningField", "Field '%s' info should have 'isSigningField'", fieldName)
		assert.Contains(t, fieldInfo, "type", "Field '%s' info should have 'type'", fieldName)

		// Type should be a non-empty string
		fieldType, ok := fieldInfo["type"].(string)
		assert.True(t, ok, "Field type should be a string")
		assert.NotEmpty(t, fieldType, "Field type should not be empty")
	}
}

// TestServerDefinitionsNonEmptyResults verifies that all definition categories
// contain actual data.
func TestServerDefinitionsNonEmptyResults(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
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

	t.Run("TYPES is non-empty", func(t *testing.T) {
		typesMap, ok := resp["TYPES"].(map[string]interface{})
		require.True(t, ok, "TYPES should be a map")
		assert.Greater(t, len(typesMap), 0, "TYPES should not be empty")
		// Verify some well-known types exist
		assert.Contains(t, typesMap, "Hash256", "TYPES should contain Hash256")
		assert.Contains(t, typesMap, "UInt32", "TYPES should contain UInt32")
		assert.Contains(t, typesMap, "Amount", "TYPES should contain Amount")
	})

	t.Run("LEDGER_ENTRY_TYPES is non-empty", func(t *testing.T) {
		ledgerTypes, ok := resp["LEDGER_ENTRY_TYPES"].(map[string]interface{})
		require.True(t, ok, "LEDGER_ENTRY_TYPES should be a map")
		assert.Greater(t, len(ledgerTypes), 0, "LEDGER_ENTRY_TYPES should not be empty")
		// Verify some well-known ledger entry types
		assert.Contains(t, ledgerTypes, "AccountRoot", "Should contain AccountRoot")
		assert.Contains(t, ledgerTypes, "Offer", "Should contain Offer")
	})

	t.Run("TRANSACTION_TYPES is non-empty", func(t *testing.T) {
		txTypes, ok := resp["TRANSACTION_TYPES"].(map[string]interface{})
		require.True(t, ok, "TRANSACTION_TYPES should be a map")
		assert.Greater(t, len(txTypes), 0, "TRANSACTION_TYPES should not be empty")
		// Verify some well-known transaction types
		assert.Contains(t, txTypes, "Payment", "Should contain Payment")
		assert.Contains(t, txTypes, "OfferCreate", "Should contain OfferCreate")
	})

	t.Run("TRANSACTION_RESULTS is non-empty", func(t *testing.T) {
		txResults, ok := resp["TRANSACTION_RESULTS"].(map[string]interface{})
		require.True(t, ok, "TRANSACTION_RESULTS should be a map")
		assert.Greater(t, len(txResults), 0, "TRANSACTION_RESULTS should not be empty")
		// Verify some well-known result codes
		assert.Contains(t, txResults, "tesSUCCESS", "Should contain tesSUCCESS")
	})

	t.Run("FIELDS is non-empty", func(t *testing.T) {
		fields, ok := resp["FIELDS"].([]interface{})
		require.True(t, ok, "FIELDS should be an array")
		assert.Greater(t, len(fields), 0, "FIELDS should not be empty")
	})
}

// TestServerDefinitionsMethodMetadata tests the method's metadata functions.
func TestServerDefinitionsMethodMetadata(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"server_definitions should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}
