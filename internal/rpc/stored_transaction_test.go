package rpc

import (
	"encoding/json"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txpkg "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type storedTransactionDataCase struct {
	name string
	data []byte
}

func validStoredPaymentTransaction() map[string]any {
	return map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
}

func validStoredMetadata() map[string]any {
	return map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  0,
		"AffectedNodes":     []any{},
	}
}

func validStoredMetadataWithAffectedNode() map[string]any {
	meta := validStoredMetadata()
	meta["AffectedNodes"] = []any{map[string]any{
		"CreatedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     "1111111111111111111111111111111111111111111111111111111111111111",
			"NewFields": map[string]any{
				"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
		},
	}}
	return meta
}

func marshalStoredTransaction(t *testing.T, txJSON map[string]any, meta any, includeMeta bool) []byte {
	t.Helper()
	stored := map[string]any{"tx_json": txJSON}
	if includeMeta {
		stored["meta"] = meta
	}
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	return data
}

func corruptedStoredTransactionData(t *testing.T) []storedTransactionDataCase {
	t.Helper()

	validTx := validStoredPaymentTransaction()
	txBytes, err := binarycodec.EncodeBytes(validTx)
	require.NoError(t, err)
	vlTx, err := txpkg.EncodeWithVL(txBytes)
	require.NoError(t, err)

	incompleteTxBytes, err := binarycodec.EncodeBytes(map[string]any{"TransactionType": "Payment"})
	require.NoError(t, err)
	vlIncompleteTx, err := txpkg.EncodeWithVL(incompleteTxBytes)
	require.NoError(t, err)
	vlEmptyMeta, err := txpkg.EncodeWithVL(nil)
	require.NoError(t, err)
	incompleteVLTransaction := append(append([]byte(nil), vlIncompleteTx...), vlEmptyMeta...)

	vlMeta, err := txpkg.EncodeWithVL([]byte{0xff})
	require.NoError(t, err)
	malformedMetadata := append(append([]byte(nil), vlTx...), vlMeta...)
	missingDestination := validStoredPaymentTransaction()
	delete(missingDestination, "Destination")
	wrongSequenceType := validStoredPaymentTransaction()
	wrongSequenceType["Sequence"] = "one"
	fractionalSequence := validStoredPaymentTransaction()
	fractionalSequence["Sequence"] = 1.5
	fractionalMetaIndex := validStoredMetadata()
	fractionalMetaIndex["TransactionIndex"] = 1.5
	badAffectedNodesType := validStoredMetadata()
	badAffectedNodesType["AffectedNodes"] = "not an array"
	return []storedTransactionDataCase{
		{name: "invalid JSON", data: []byte("not valid json")},
		{name: "JSON null", data: []byte(`null`)},
		{name: "empty JSON object", data: []byte(`{}`)},
		{name: "missing tx_json", data: []byte(`{"meta":{}}`)},
		{name: "null tx_json", data: []byte(`{"tx_json":null,"meta":{}}`)},
		{name: "empty tx_json", data: []byte(`{"tx_json":{},"meta":{}}`)},
		{name: "non-object tx_json", data: []byte(`{"tx_json":"corrupt transaction"}`)},
		{name: "non-string transaction type", data: []byte(`{"tx_json":{"TransactionType":7}}`)},
		{name: "unknown transaction type", data: []byte(`{"tx_json":{"TransactionType":"Unknown"}}`)},
		{name: "missing required transaction field", data: marshalStoredTransaction(t, missingDestination, nil, false)},
		{name: "wrong transaction field type", data: marshalStoredTransaction(t, wrongSequenceType, nil, false)},
		{name: "fractional integer transaction field", data: marshalStoredTransaction(t, fractionalSequence, nil, false)},
		{name: "scalar metadata", data: marshalStoredTransaction(t, validTx, "corrupt metadata", true)},
		{name: "fractional metadata index", data: marshalStoredTransaction(t, validTx, fractionalMetaIndex, true)},
		{name: "wrong AffectedNodes type", data: marshalStoredTransaction(t, validTx, badAffectedNodesType, true)},
		{name: "incomplete VL transaction", data: incompleteVLTransaction},
		{name: "malformed non-empty VL metadata", data: malformedMetadata},
	}
}

func txMetadataCorruptionData(t *testing.T) []storedTransactionDataCase {
	t.Helper()
	validTx := validStoredPaymentTransaction()
	missingResult := validStoredMetadata()
	delete(missingResult, "TransactionResult")
	missingAffectedNodes := map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  0,
	}
	txBytes, err := binarycodec.EncodeBytes(validTx)
	require.NoError(t, err)
	metaBytes, err := binarycodec.EncodeBytes(missingAffectedNodes)
	require.NoError(t, err)
	vlTx, err := txpkg.EncodeWithVL(txBytes)
	require.NoError(t, err)
	vlMeta, err := txpkg.EncodeWithVL(metaBytes)
	require.NoError(t, err)
	return []storedTransactionDataCase{
		{name: "missing required JSON metadata field", data: marshalStoredTransaction(t, validTx, missingResult, true)},
		{name: "VL metadata missing AffectedNodes", data: append(append([]byte(nil), vlTx...), vlMeta...)},
	}
}

func storedTransactionDataWithoutMetadata(t *testing.T) []storedTransactionDataCase {
	t.Helper()
	validTx := validStoredPaymentTransaction()
	txBytes, err := binarycodec.EncodeBytes(validTx)
	require.NoError(t, err)
	vlTx, err := txpkg.EncodeWithVL(txBytes)
	require.NoError(t, err)
	vlEmptyMeta, err := txpkg.EncodeWithVL(nil)
	require.NoError(t, err)
	vlTxWithEmptyMeta := append(append([]byte(nil), vlTx...), vlEmptyMeta...)
	return []storedTransactionDataCase{
		{name: "metadata absent", data: marshalStoredTransaction(t, validTx, nil, false)},
		{name: "metadata null", data: marshalStoredTransaction(t, validTx, nil, true)},
		{name: "VL metadata absent", data: vlTx},
		{name: "VL metadata empty", data: vlTxWithEmptyMeta},
	}
}

func storedTransactionDataWithMetadata(t *testing.T) []storedTransactionDataCase {
	t.Helper()
	validTx := validStoredPaymentTransaction()
	validMeta := validStoredMetadataWithAffectedNode()
	txBytes, err := binarycodec.EncodeBytes(validTx)
	require.NoError(t, err)
	metaBytes, err := binarycodec.EncodeBytes(validMeta)
	require.NoError(t, err)
	vlTx, err := txpkg.EncodeWithVL(txBytes)
	require.NoError(t, err)
	vlMeta, err := txpkg.EncodeWithVL(metaBytes)
	require.NoError(t, err)
	vlData := append(append([]byte(nil), vlTx...), vlMeta...)
	return []storedTransactionDataCase{
		{name: "JSON", data: marshalStoredTransaction(t, validTx, validMeta, true)},
		{name: "VL", data: vlData},
	}
}

func requireDBDeserializationError(t *testing.T, result any, rpcErr *types.RpcError) {
	t.Helper()

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcDB_DESERIALIZATION, rpcErr.Code)
	assert.Equal(t, "dbDeserialization", rpcErr.ErrorString)
	assert.Equal(t, "dbDeserialization", rpcErr.Type)
	assert.Equal(t, "Database deserialization error.", rpcErr.Message)
}

func requireCanonicalInternalError(t *testing.T, result any, rpcErr *types.RpcError) {
	t.Helper()

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "internal", rpcErr.ErrorString)
	assert.Equal(t, "internal", rpcErr.Type)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}
