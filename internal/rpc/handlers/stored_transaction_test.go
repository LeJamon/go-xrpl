package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestDecodeTxBlobCanonicalizesJSONStoredObjects(t *testing.T) {
	txJSON := map[string]any{
		"TransactionType": "Payment",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Destination":     "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        1,
		"SigningPubKey":   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"TxnSignature":    "30440220143759437C04F7B61F012563AFE90D8DAFC46E86035E1D965A9CED282C97D4CE02204CFD241E86F17E011298FC1A39B63386C74306A5DE047E213B0F29EFA4571C2C",
	}
	meta := map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  0,
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "AccountRoot",
				"LedgerIndex":     "1111111111111111111111111111111111111111111111111111111111111111",
				"NewFields":       map[string]any{"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			}},
			map[string]any{"ModifiedNode": map[string]any{
				"LedgerEntryType": "AccountRoot",
				"LedgerIndex":     "2222222222222222222222222222222222222222222222222222222222222222",
				"FinalFields":     map[string]any{},
			}},
		},
	}
	data, err := json.Marshal(map[string]any{"tx_json": txJSON, "meta": meta})
	require.NoError(t, err)

	stored, err := decodeTxBlob(data)
	require.NoError(t, err)
	require.IsType(t, uint32(0), stored.TxJSON["Sequence"])
	nodes, ok := stored.Meta["AffectedNodes"].([]any)
	require.True(t, ok)
	require.Len(t, nodes, 2)
	for _, rawNode := range nodes {
		node, ok := rawNode.(map[string]any)
		require.True(t, ok)
		require.Len(t, node, 1)
	}
}

func TestDecodeLedgerTransactionRawCanonicalOpen(t *testing.T) {
	raw, hash := testRawPayment(t)
	hashString := strings.ToUpper(hex.EncodeToString(hash[:]))

	for _, test := range []struct {
		name   string
		binary bool
		api    int
	}{
		{name: "json v1", api: 1},
		{name: "json v2", api: 2},
		{name: "binary v1", binary: true, api: 1},
		{name: "binary v2", binary: true, api: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, err := expandTransaction(raw, hashString, test.binary, test.api, SyntheticMetadataContext{}, false, 0)
			require.NoError(t, err)
			if test.api > 1 || !test.binary {
				require.Equal(t, hashString, entry["hash"])
			} else {
				require.NotContains(t, entry, "hash")
			}
			if test.binary {
				require.Equal(t, strings.ToUpper(hex.EncodeToString(raw)), entry["tx_blob"])
				require.NotContains(t, entry, "meta_blob")
				require.NotContains(t, entry, "meta")
				return
			}
			if test.api == 1 {
				require.Equal(t, "Payment", entry["TransactionType"])
				require.NotContains(t, entry, "metaData")
				return
			}
			txJSON, ok := entry["tx_json"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "Payment", txJSON["TransactionType"])
			require.NotContains(t, entry, "meta")
		})
	}
}

func TestDecodeLedgerTransactionRejectsRawCorruptionAndClosedRawTransaction(t *testing.T) {
	raw, hash := testRawPayment(t)
	hashString := strings.ToUpper(hex.EncodeToString(hash[:]))
	var wrongHash [32]byte
	wrongHash[0] = 0xA5

	_, err := decodeLedgerTransaction(raw, strings.ToUpper(hex.EncodeToString(wrongHash[:])), false)
	require.Error(t, err)

	_, err = decodeLedgerTransaction([]byte{0x01, 0x02}, hashString, false)
	require.Error(t, err)

	txOnly, err := tx.EncodeWithVL(raw)
	require.NoError(t, err)
	stored, err := decodeLedgerTransaction(txOnly, hashString, true)
	require.NoError(t, err)
	require.Nil(t, stored.Meta)
	_, err = decodeLedgerTransaction(raw, hashString, true)
	require.Error(t, err)
}

func TestTransactionOnlyDecodersAllowLegacyOverCapAndKeepMetadataStrict(t *testing.T) {
	const overCap = uint64(binarytypes.MaxDrops) + 1
	txFields := map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":          binarytypes.RawXRPAmount{Drops: overCap},
		"Destination":     "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "EscrowCreate",
	}
	txHex, err := binarycodec.Encode(txFields)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(txHex)
	require.NoError(t, err)
	metaFields := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(4),
		"TransactionResult": "tecNO_LINE",
	}
	metaHex, err := binarycodec.Encode(metaFields)
	require.NoError(t, err)
	metaBlob, err := hex.DecodeString(metaHex)
	require.NoError(t, err)
	txOnly, err := tx.EncodeWithVL(txBlob)
	require.NoError(t, err)
	combined := append(append([]byte(nil), txOnly...), mustEncodeVL(t, metaBlob)...)

	open, err := decodeOpenTxBlob(txOnly)
	require.NoError(t, err)
	assertRawAmount(t, open.TxJSON, overCap)

	closed, err := decodeTxBlobForTx(combined)
	require.NoError(t, err)
	assertRawAmount(t, closed.TxJSON, overCap)

	entry, err := decodeTxBlobForTransactionEntry(combined)
	require.NoError(t, err)
	assertRawAmount(t, entry.TxJSON, overCap)

	badMetaFields := map[string]any{}
	for name, value := range metaFields {
		badMetaFields[name] = value
	}
	badMetaFields["DeliveredAmount"] = binarytypes.RawXRPAmount{Drops: overCap}
	badMetaHex, err := binarycodec.Encode(badMetaFields)
	require.NoError(t, err)
	badMetaBlob, err := hex.DecodeString(badMetaHex)
	require.NoError(t, err)
	badCombined := append(append([]byte(nil), txOnly...), mustEncodeVL(t, badMetaBlob)...)
	_, err = decodeTxBlobForTx(badCombined)
	require.Error(t, err)
	_, err = decodeBinaryObject(badMetaBlob)
	require.Error(t, err)
}

func mustEncodeVL(t *testing.T, data []byte) []byte {
	t.Helper()
	encoded, err := tx.EncodeWithVL(data)
	require.NoError(t, err)
	return encoded
}

func assertRawAmount(t *testing.T, fields map[string]any, want uint64) {
	t.Helper()
	raw, ok := fields["Amount"].(binarytypes.RawXRPAmount)
	require.True(t, ok, "Amount type = %T", fields["Amount"])
	require.Equal(t, want, raw.Drops)
}

func testRawPayment(t *testing.T) ([]byte, [32]byte) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	sender := jtx.MasterAccount()
	txn := payment.Pay(sender, jtx.NewAccount("ledger-raw-rpc"), 1_000_000).Fee(10).Sequence(1).Build()
	env.SignWith(txn, sender)
	txJSON, err := txn.Flatten()
	require.NoError(t, err)
	hexBlob, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	raw, err := hex.DecodeString(hexBlob)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)
	return raw, hash
}
