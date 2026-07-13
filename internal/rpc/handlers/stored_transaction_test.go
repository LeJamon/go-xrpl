package handlers

import (
	"encoding/json"
	"testing"

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
		"AffectedNodes": []any{map[string]any{
			"CreatedNode": map[string]any{
				"LedgerEntryType": "AccountRoot",
				"LedgerIndex":     "1111111111111111111111111111111111111111111111111111111111111111",
				"NewFields":       map[string]any{"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			},
			"ModifiedNode": map[string]any{
				"LedgerEntryType": "AccountRoot",
				"LedgerIndex":     "2222222222222222222222222222222222222222222222222222222222222222",
				"FinalFields":     map[string]any{},
			},
		}},
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
