package rpc

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This fixture is generated independently from the pinned rippled source
// macros; see testdata/server_definitions_rc1_oracle.py.
//
//go:embed testdata/server_definitions_rc1_hashes.json
var serverDefinitionsRC1HashFixture []byte

type serverDefinitionsSectionFixture struct {
	SHA512Half string `json:"sha512_half"`
	Bytes      int    `json:"bytes"`
	Groups     int    `json:"groups"`
	Entries    int    `json:"entries"`
}

type serverDefinitionsHashFixture struct {
	Oracle struct {
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	} `json:"oracle"`
	FullDocumentSHA512Half string                                     `json:"full_document_sha512_half"`
	FullDocumentBytes      int                                        `json:"full_document_bytes"`
	Sections               map[string]serverDefinitionsSectionFixture `json:"sections"`
}

func loadServerDefinitionsRC1HashFixture(t *testing.T) serverDefinitionsHashFixture {
	t.Helper()
	var fixture serverDefinitionsHashFixture
	require.NoError(t, json.Unmarshal(serverDefinitionsRC1HashFixture, &fixture))
	return fixture
}

func serverDefinitionsSectionStats(value any) (groups, entries int) {
	switch value := value.(type) {
	case []any:
		return 0, len(value)
	case map[string]any:
		for _, child := range value {
			groups++
			switch child := child.(type) {
			case []any:
				entries += len(child)
			case map[string]any:
				entries += len(child)
			default:
				entries++
			}
		}
	}
	return groups, entries
}

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

// TestServerDefinitionsFieldOrder pins the order produced by rippled's
// ServerDefinitions constructor: fixed sentinel rows followed by known fields
// in serialized field-code order.
func TestServerDefinitionsFieldOrder(t *testing.T) {
	method := &handlers.ServerDefinitionsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	resp := result.(map[string]any)
	fields, ok := resp["FIELDS"].([]any)
	require.True(t, ok)

	defsFields := definitions.Get().Fields()
	sentinels := []string{
		"Invalid",
		"ObjectEndMarker",
		"ArrayEndMarker",
		"taker_gets_funded",
		"taker_pays_funded",
	}
	require.Len(t, fields, len(defsFields))
	for i, want := range sentinels {
		pair, ok := fields[i].([]any)
		require.True(t, ok)
		require.Len(t, pair, 2)
		assert.Equal(t, want, pair[0])
	}

	// The field-code order is derived from the same definitions source used by
	// the codec, with Generic occupying the source's code-zero slot.
	orderedNames := make([]string, 0, len(defsFields)-len(sentinels))
	seen := make(map[string]struct{}, len(sentinels))
	for _, name := range sentinels {
		seen[name] = struct{}{}
	}
	for name := range defsFields {
		if _, ok := seen[name]; !ok {
			orderedNames = append(orderedNames, name)
		}
	}
	sort.Slice(orderedNames, func(i, j int) bool {
		left, right := defsFields[orderedNames[i]], defsFields[orderedNames[j]]
		leftCode, rightCode := left.Ordinal, right.Ordinal
		if orderedNames[i] == "Generic" {
			leftCode = 0
		}
		if orderedNames[j] == "Generic" {
			rightCode = 0
		}
		if leftCode != rightCode {
			return leftCode < rightCode
		}
		return orderedNames[i] < orderedNames[j]
	})
	for i, want := range orderedNames {
		pair, ok := fields[len(sentinels)+i].([]any)
		require.True(t, ok)
		require.Len(t, pair, 2)
		assert.Equal(t, want, pair[0])
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
	// Pinned to the v3.4.0-rc1 ServerDefinitions document and its compact
	// Json::FastWriter serialization (oracle commit 2ad4def35fd8580da027462517ba3375cc005c94).
	assert.Equal(t, "1EA05B0FC11101F7C500BD0DAC794A8BC746A7FBA6250B75489603EB820E0FF5", hash)

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

	t.Run("literal zero hash returns full document", func(t *testing.T) {
		params, err := json.Marshal(map[string]any{"hash": "0"})
		require.NoError(t, err)

		full, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		assert.Contains(t, full.(map[string]any), "FIELDS")
	})

	t.Run("invalid hash is rejected", func(t *testing.T) {
		for _, bad := range []any{
			nil,
			"",
			"nothex",
			12345,
			strings.Repeat("a", 63),
			strings.Repeat("g", 64),
			strings.Repeat("a", 65),
		} {
			params, err := json.Marshal(map[string]any{"hash": bad})
			require.NoError(t, err)

			_, rpcErr := method.Handle(ctx, params)
			require.NotNil(t, rpcErr, "invalid hash %v should error", bad)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
		}
	})
}

// TestServerDefinitionsMatchesRC1SourceFixture checks every response section
// against hashes independently derived from rippled's rc1 protocol source.
// The section checks make omissions in formats, flags, or type tables visible
// even when the complete-document hash is accidentally updated.
func TestServerDefinitionsMatchesRC1SourceFixture(t *testing.T) {
	fixture := loadServerDefinitionsRC1HashFixture(t)
	assert.Equal(t, "3.4.0-rc1", fixture.Oracle.Tag)
	assert.Equal(t, "2ad4def35fd8580da027462517ba3375cc005c94", fixture.Oracle.Commit)
	assert.Equal(t,
		"1EA05B0FC11101F7C500BD0DAC794A8BC746A7FBA6250B75489603EB820E0FF5",
		fixture.FullDocumentSHA512Half,
	)

	result, rpcErr := (&handlers.ServerDefinitionsMethod{}).Handle(&types.RpcContext{
		Context: context.Background(), Role: types.RoleGuest, ApiVersion: types.ApiVersion1,
	}, nil)
	require.Nil(t, rpcErr)
	response := result.(map[string]any)
	document := make(map[string]any, len(response)-1)
	for name, value := range response {
		if name != "hash" {
			document[name] = value
		}
	}

	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	sum := sha512half.Sum(encoded)
	assert.Equal(t, fixture.FullDocumentBytes, len(encoded))
	assert.Equal(t, fixture.FullDocumentSHA512Half, strings.ToUpper(hex.EncodeToString(sum[:])))
	assert.Len(t, document, len(fixture.Sections))
	codecResults := definitions.Get().TransactionResults()
	assert.Len(t, codecResults, 197, "codec definitions retain the complete TER enum table")
	assert.Contains(t, codecResults, "tecHOOK_REJECTED")
	assert.Contains(t, codecResults, "tecNO_DELEGATE_PERMISSION")

	for name, expected := range fixture.Sections {
		section, ok := document[name]
		require.True(t, ok, "source fixture section %s is missing", name)
		encoded, err := json.Marshal(section)
		require.NoError(t, err)
		sum := sha512half.Sum(encoded)
		var generic any
		require.NoError(t, json.Unmarshal(encoded, &generic))
		if name == "TRANSACTION_RESULTS" {
			results, ok := generic.(map[string]any)
			require.True(t, ok)
			assert.NotContains(t, results, "tecHOOK_REJECTED")
			assert.NotContains(t, results, "tecNO_DELEGATE_PERMISSION")
		}
		groups, entries := serverDefinitionsSectionStats(generic)
		assert.Equal(t, expected.Bytes, len(encoded), "%s byte length", name)
		assert.Equal(t, expected.Groups, groups, "%s group count", name)
		assert.Equal(t, expected.Entries, entries, "%s entry count", name)
		assert.Equal(t, expected.SHA512Half, strings.ToUpper(hex.EncodeToString(sum[:])), "%s hash", name)
	}
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

func TestServerDefinitions_3_4_0_RC1_Sections(t *testing.T) {
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
					"TxnSignature", "Signers", "NetworkID", "Delegate", "Sponsor",
					"SponsorFlags", "SponsorSignature",
				},
				styles: []int{0, 1, 1, 0, 0, 1, 1, 1, 0, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1},
			},
			{
				name:   "VaultCreate",
				fields: []string{"Asset", "AssetsMaximum", "MPTokenMetadata", "DomainID", "WithdrawalPolicy", "Data", "Scale", "VaultKind", "SubscriptionDate", "RedemptionDate"},
				styles: []int{0, 1, 1, 1, 1, 1, 1, 1, 1, 1},
			},
			{
				name:   "VaultWithdraw",
				fields: []string{"VaultID", "Amount", "Destination", "DestinationTag", "CredentialIDs"},
				styles: []int{0, 0, 1, 1, 1},
			},
			{
				name:   "LoanBrokerCoverWithdraw",
				fields: []string{"LoanBrokerID", "Amount", "Destination", "DestinationTag", "CredentialIDs"},
				styles: []int{0, 0, 1, 1, 1},
			},
			{
				name:   "MPTokenIssuanceCreate",
				fields: []string{"AssetScale", "TransferFee", "MaximumAmount", "MPTokenMetadata", "DomainID", "ImmutableFlags"},
				styles: []int{1, 1, 1, 1, 1, 1},
			},
			{
				name:   "MPTokenIssuanceSet",
				fields: []string{"MPTokenIssuanceID", "Holder", "DomainID", "MPTokenMetadata", "TransferFee", "ImmutableFlags", "IssuerEncryptionKey", "AuditorEncryptionKey"},
				styles: []int{0, 1, 1, 1, 1, 1, 1, 1},
			},
			{
				name:   "ConfidentialMPTConvert",
				fields: []string{"MPTokenIssuanceID", "MPTAmount", "HolderEncryptionKey", "HolderEncryptedAmount", "IssuerEncryptedAmount", "AuditorEncryptedAmount", "BlindingFactor", "ZKProof"},
				styles: []int{0, 0, 1, 0, 0, 1, 0, 1},
			},
			{
				name:   "ConfidentialMPTMergeInbox",
				fields: []string{"MPTokenIssuanceID"},
				styles: []int{0},
			},
			{
				name:   "ConfidentialMPTConvertBack",
				fields: []string{"MPTokenIssuanceID", "MPTAmount", "HolderEncryptedAmount", "IssuerEncryptedAmount", "AuditorEncryptedAmount", "BlindingFactor", "ZKProof", "BalanceCommitment"},
				styles: []int{0, 0, 0, 0, 1, 0, 0, 0},
			},
			{
				name:   "ConfidentialMPTSend",
				fields: []string{"MPTokenIssuanceID", "Destination", "DestinationTag", "SenderEncryptedAmount", "DestinationEncryptedAmount", "IssuerEncryptedAmount", "AuditorEncryptedAmount", "ZKProof", "AmountCommitment", "BalanceCommitment", "CredentialIDs"},
				styles: []int{0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1},
			},
			{
				name:   "ConfidentialMPTClawback",
				fields: []string{"MPTokenIssuanceID", "Holder", "MPTAmount", "ZKProof"},
				styles: []int{0, 0, 0, 0},
			},
			{
				name:   "SponsorshipTransfer",
				fields: []string{"ObjectID", "Sponsee"},
				styles: []int{1, 1},
			},
			{
				name:   "SponsorshipSet",
				fields: []string{"CounterpartySponsor", "Sponsee", "FeeAmountDelta", "MaxFee", "RemainingOwnerCountDelta"},
				styles: []int{1, 1, 1, 1, 1},
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
		for _, test := range []struct {
			name   string
			fields []string
			styles []int
		}{
			{
				name:   "common",
				fields: []string{"LedgerIndex", "LedgerEntryType", "Flags", "Sponsor"},
				styles: []int{1, 0, 0, 1},
			},
			{
				name: "Sponsorship",
				fields: []string{
					"PreviousTxnID", "PreviousTxnLgrSeq", "Owner", "Sponsee",
					"FeeAmount", "MaxFee", "RemainingOwnerCount", "OwnerNode", "SponseeNode",
				},
				styles: []int{0, 0, 0, 0, 1, 1, 2, 0, 0},
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
		assert.EqualValues(t, 0x00080000, num(payment["tfSponsorCreatedAccount"]))
		set, ok := flags["SponsorshipSet"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00100000, num(set["tfDeleteObject"]))
		createMPT, ok := flags["MPTokenIssuanceCreate"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00000080, num(createMPT["tfMPTCanHoldConfidentialBalance"]))
		mptSet, ok := flags["MPTokenIssuanceSet"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00000004, num(mptSet["tfMPTSetCanLock"]))
		assert.EqualValues(t, 0x00000100, num(mptSet["tfMPTSetCanHoldConfidentialBalance"]))
	})

	t.Run("LEDGER_ENTRY_FLAGS", func(t *testing.T) {
		flags, ok := resp["LEDGER_ENTRY_FLAGS"].(map[string]any)
		require.True(t, ok, "LEDGER_ENTRY_FLAGS should be a map")
		ar, ok := flags["AccountRoot"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00040000, num(ar["lsfRequireAuth"]))
		mpt, ok := flags["MPToken"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00000004, num(mpt["lsfMPTAMM"]), "3.3.0 lsfMPTAMM")
		mptIssuance, ok := flags["MPTokenIssuance"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00000080, num(mptIssuance["lsfMPTCanHoldConfidentialBalance"]))
		assert.NotContains(t, flags, "MPTokenIssuanceMutable")
		sponsorship, ok := flags["Sponsorship"].(map[string]any)
		require.True(t, ok)
		assert.EqualValues(t, 0x00020000, num(sponsorship["lsfSponsorshipRequireSignForReserve"]))
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
