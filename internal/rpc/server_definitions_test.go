package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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
	var resp map[string]any
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
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	fieldsRaw, ok := resp["FIELDS"].([]any)
	require.True(t, ok, "FIELDS should be an array")
	require.Greater(t, len(fieldsRaw), 0, "FIELDS should not be empty")

	// Validate the format of at least the first few entries
	for i, entry := range fieldsRaw {
		if i >= 5 {
			break // Spot-check first 5
		}
		pair, ok := entry.([]any)
		require.True(t, ok, "Each FIELDS entry should be an array")
		require.Equal(t, 2, len(pair), "Each FIELDS entry should have 2 elements [name, info]")

		// First element is the field name (string)
		fieldName, ok := pair[0].(string)
		assert.True(t, ok, "Field name should be a string")
		assert.NotEmpty(t, fieldName, "Field name should not be empty")

		// Second element is the field info (object)
		fieldInfo, ok := pair[1].(map[string]any)
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
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	t.Run("TYPES is non-empty", func(t *testing.T) {
		typesMap, ok := resp["TYPES"].(map[string]any)
		require.True(t, ok, "TYPES should be a map")
		assert.Greater(t, len(typesMap), 0, "TYPES should not be empty")
		// Verify some well-known types exist
		assert.Contains(t, typesMap, "Hash256", "TYPES should contain Hash256")
		assert.Contains(t, typesMap, "UInt32", "TYPES should contain UInt32")
		assert.Contains(t, typesMap, "Amount", "TYPES should contain Amount")
	})

	t.Run("LEDGER_ENTRY_TYPES is non-empty", func(t *testing.T) {
		ledgerTypes, ok := resp["LEDGER_ENTRY_TYPES"].(map[string]any)
		require.True(t, ok, "LEDGER_ENTRY_TYPES should be a map")
		assert.Greater(t, len(ledgerTypes), 0, "LEDGER_ENTRY_TYPES should not be empty")
		// Verify some well-known ledger entry types
		assert.Contains(t, ledgerTypes, "AccountRoot", "Should contain AccountRoot")
		assert.Contains(t, ledgerTypes, "Offer", "Should contain Offer")
	})

	t.Run("TRANSACTION_TYPES is non-empty", func(t *testing.T) {
		txTypes, ok := resp["TRANSACTION_TYPES"].(map[string]any)
		require.True(t, ok, "TRANSACTION_TYPES should be a map")
		assert.Greater(t, len(txTypes), 0, "TRANSACTION_TYPES should not be empty")
		// Verify some well-known transaction types
		assert.Contains(t, txTypes, "Payment", "Should contain Payment")
		assert.Contains(t, txTypes, "OfferCreate", "Should contain OfferCreate")
	})

	t.Run("TRANSACTION_RESULTS is non-empty", func(t *testing.T) {
		txResults, ok := resp["TRANSACTION_RESULTS"].(map[string]any)
		require.True(t, ok, "TRANSACTION_RESULTS should be a map")
		assert.Greater(t, len(txResults), 0, "TRANSACTION_RESULTS should not be empty")
		// Verify some well-known result codes
		assert.Contains(t, txResults, "tesSUCCESS", "Should contain tesSUCCESS")
	})

	t.Run("FIELDS is non-empty", func(t *testing.T) {
		fields, ok := resp["FIELDS"].([]any)
		require.True(t, ok, "FIELDS should be an array")
		assert.Greater(t, len(fields), 0, "FIELDS should not be empty")
	})
}

// TestServerDefinitionsHash verifies the response carries a deterministic
// 256-bit hash and that echoing it back short-circuits to just the hash.
// Reference: rippled ServerInfo.cpp:288-317.
func TestServerDefinitionsHash(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	resp := result.(map[string]any)

	hash, ok := resp["hash"].(string)
	require.True(t, ok, "response should contain a string hash")
	require.Len(t, hash, 64, "hash should be a 256-bit hex string")

	t.Run("matching hash short-circuits", func(t *testing.T) {
		params, err := json.Marshal(map[string]any{"hash": hash})
		require.NoError(t, err)

		short, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		shortResp := short.(map[string]any)
		assert.Equal(t, hash, shortResp["hash"])
		assert.NotContains(t, shortResp, "FIELDS",
			"matching hash should return only the hash")
	})

	t.Run("lowercase hash also matches", func(t *testing.T) {
		params, err := json.Marshal(map[string]any{"hash": strings.ToLower(hash)})
		require.NoError(t, err)

		short, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		assert.NotContains(t, short.(map[string]any), "FIELDS")
	})

	t.Run("non-matching hash returns full document", func(t *testing.T) {
		other := strings.Repeat("0", 64)
		params, err := json.Marshal(map[string]any{"hash": other})
		require.NoError(t, err)

		full, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		assert.Contains(t, full.(map[string]any), "FIELDS")
	})

	t.Run("invalid hash is rejected", func(t *testing.T) {
		for _, bad := range []any{"nothex", 12345, strings.Repeat("a", 63)} {
			params, err := json.Marshal(map[string]any{"hash": bad})
			require.NoError(t, err)

			_, rpcErr := method.Handle(ctx, params)
			require.NotNil(t, rpcErr, "invalid hash %v should error", bad)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
		}
	})
}

// TestServerDefinitionsInvalidSentinel verifies the Invalid:-1 sentinel is
// present in TRANSACTION_TYPES and LEDGER_ENTRY_TYPES (rippled ServerInfo.cpp:282).
func TestServerDefinitionsInvalidSentinel(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(resultJSON, &resp))

	for _, key := range []string{"TRANSACTION_TYPES", "LEDGER_ENTRY_TYPES"} {
		m, ok := resp[key].(map[string]any)
		require.True(t, ok, "%s should be a map", key)
		val, ok := m["Invalid"]
		require.True(t, ok, "%s should contain Invalid sentinel", key)
		assert.EqualValues(t, -1, val, "%s.Invalid should be -1", key)
	}
}

// TestServerDefinitions_3_2_0_Sections verifies the five sections added in
// rippled 3.2.0: TRANSACTION_FORMATS, LEDGER_ENTRY_FORMATS,
// TRANSACTION_FLAGS, LEDGER_ENTRY_FLAGS and ACCOUNT_SET_FLAGS.
func TestServerDefinitions_3_2_0_Sections(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}
	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(resultJSON, &resp))

	// asJSONNum returns the numeric value of resp[...] regardless of the
	// json.Number/float64 decode.
	num := func(v any) float64 {
		f, ok := v.(float64)
		require.True(t, ok, "expected numeric value, got %T", v)
		return f
	}

	t.Run("TRANSACTION_FORMATS", func(t *testing.T) {
		formats, ok := resp["TRANSACTION_FORMATS"].(map[string]any)
		require.True(t, ok, "TRANSACTION_FORMATS should be a map")

		for _, test := range []struct {
			name   string
			fields []string
			styles []int
		}{
			{
				name: "common",
				fields: []string{
					"TransactionType", "Flags", "SourceTag", "Account", "Sequence",
					"PreviousTxnID", "LastLedgerSequence", "AccountTxnID", "Fee",
					"OperationLimit", "Memos", "SigningPubKey", "TicketSequence",
					"TxnSignature", "Signers", "NetworkID", "Delegate",
				},
				styles: []int{0, 1, 1, 0, 0, 1, 1, 1, 0, 1, 1, 0, 1, 1, 1, 1, 1},
			},
			{
				name:   "VaultCreate",
				fields: []string{"Asset", "AssetsMaximum", "MPTokenMetadata", "DomainID", "WithdrawalPolicy", "Data", "Scale"},
				styles: []int{0, 1, 1, 1, 1, 1, 1},
			},
			{
				name:   "MPTokenIssuanceCreate",
				fields: []string{"AssetScale", "TransferFee", "MaximumAmount", "MPTokenMetadata", "DomainID", "MutableFlags"},
				styles: []int{1, 1, 1, 1, 1, 1},
			},
			{
				name:   "MPTokenIssuanceSet",
				fields: []string{"MPTokenIssuanceID", "Holder", "DomainID", "MPTokenMetadata", "TransferFee", "MutableFlags"},
				styles: []int{0, 1, 1, 1, 1, 1},
			},
		} {
			section, ok := formats[test.name].([]any)
			require.True(t, ok, "should carry a %s format", test.name)
			require.Len(t, section, len(test.fields))
			for i, field := range section {
				entry, ok := field.(map[string]any)
				require.True(t, ok, "%s[%d] should be an object", test.name, i)
				assert.Equal(t, test.fields[i], entry["name"])
				assert.EqualValues(t, test.styles[i], num(entry["optionality"]))
			}
		}

		payment, ok := formats["Payment"].([]any)
		require.True(t, ok, "should carry a Payment format")
		// Payment's Amount is required (optionality 0); a common field like
		// Fee must NOT appear in the per-type list.
		names := map[string]float64{}
		for _, f := range payment {
			m := f.(map[string]any)
			names[m["name"].(string)] = num(m["optionality"])
		}
		require.Contains(t, names, "Amount")
		assert.EqualValues(t, 0, names["Amount"], "Payment.Amount is required")
		assert.NotContains(t, names, "Fee", "common fields excluded from per-type list")
	})

	t.Run("LEDGER_ENTRY_FORMATS", func(t *testing.T) {
		formats, ok := resp["LEDGER_ENTRY_FORMATS"].(map[string]any)
		require.True(t, ok, "LEDGER_ENTRY_FORMATS should be a map")
		_, ok = formats["common"].([]any)
		require.True(t, ok, "should carry a 'common' array")
		ar, ok := formats["AccountRoot"].([]any)
		require.True(t, ok, "should carry an AccountRoot format")
		require.NotEmpty(t, ar)
	})

	t.Run("TRANSACTION_FLAGS", func(t *testing.T) {
		flags, ok := resp["TRANSACTION_FLAGS"].(map[string]any)
		require.True(t, ok, "TRANSACTION_FLAGS should be a map")
		universal, ok := flags["universal"].(map[string]any)
		require.True(t, ok, "should carry a 'universal' group")
		assert.EqualValues(t, 0x80000000, num(universal["tfFullyCanonicalSig"]))
		payment, ok := flags["Payment"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00020000, num(payment["tfPartialPayment"]))
	})

	t.Run("LEDGER_ENTRY_FLAGS", func(t *testing.T) {
		flags, ok := resp["LEDGER_ENTRY_FLAGS"].(map[string]any)
		require.True(t, ok, "LEDGER_ENTRY_FLAGS should be a map")
		ar, ok := flags["AccountRoot"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00040000, num(ar["lsfRequireAuth"]))
		mpt, ok := flags["MPToken"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00000004, num(mpt["lsfMPTAMM"]), "3.2.0 lsfMPTAMM")
	})

	t.Run("ACCOUNT_SET_FLAGS", func(t *testing.T) {
		asf, ok := resp["ACCOUNT_SET_FLAGS"].(map[string]any)
		require.True(t, ok, "ACCOUNT_SET_FLAGS should be a map")
		assert.EqualValues(t, 1, num(asf["asfRequireDest"]))
		assert.EqualValues(t, 17, num(asf["asfAllowTrustLineLocking"]))
		assert.NotContains(t, asf, "asfTshCollect", "asf 11 is intentionally absent")
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
