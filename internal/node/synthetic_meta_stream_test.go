package node

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDecodeTxWithMetaToJSONInjectsSyntheticFields(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const sequence = uint32(42)
	txJSON := map[string]any{
		"Account":         issuer,
		"Fee":             "10",
		"Sequence":        sequence,
		"SigningPubKey":   "",
		"TransactionType": "MPTokenIssuanceCreate",
	}
	meta := map[string]any{
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "MPTokenIssuance",
				"LedgerIndex":     strings.Repeat("A", 64),
				"NewFields": map[string]any{
					"Issuer":   issuer,
					"Sequence": sequence,
				},
			}},
		},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}

	txHex, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(txHex)
	require.NoError(t, err)
	metaHex, err := binarycodec.Encode(meta)
	require.NoError(t, err)
	metaBlob, err := hex.DecodeString(metaHex)
	require.NoError(t, err)
	require.Less(t, len(txBlob), 193)
	require.Less(t, len(metaBlob), 193)
	data := append([]byte{byte(len(txBlob))}, txBlob...)
	data = append(data, byte(len(metaBlob)))
	data = append(data, metaBlob...)

	_, metaRaw := decodeTxWithMetaToJSON(data)
	var decodedMeta map[string]any
	require.NoError(t, json.Unmarshal(metaRaw, &decodedMeta))

	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], accountBytes)
	mptID := keylet.MakeMPTID(sequence, accountID)
	want := strings.ToUpper(hex.EncodeToString(mptID[:]))
	require.Equal(t, want, decodedMeta["mpt_issuance_id"])
}
