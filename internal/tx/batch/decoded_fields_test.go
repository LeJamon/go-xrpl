package batch

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestParseFromDecodedFieldsOwnsBatchFields(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	inner := func(sequence uint32) map[string]any {
		return map[string]any{
			"TransactionType": "AccountSet", "Account": account,
			"Sequence": sequence, "Fee": "0", "SigningPubKey": "",
			"Flags": tx.TfInnerBatchTxn,
			"Memos": []any{map[string]any{"Memo": map[string]any{"MemoData": "ABCD"}}},
		}
	}
	fields := map[string]any{
		"TransactionType": "Batch", "Account": account,
		"Sequence": uint32(1), "Fee": "40", "SigningPubKey": "",
		"Flags": BatchFlagAllOrNothing,
		"RawTransactions": []any{
			map[string]any{"RawTransaction": inner(2)},
			map[string]any{"CreatedNode": inner(3)},
		},
	}
	wire, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	decoded, err := binarycodec.DecodeBytes(wire)
	require.NoError(t, err)
	before, err := json.Marshal(decoded)
	require.NoError(t, err)
	parsed, err := tx.ParseFromDecodedFields(decoded)
	require.NoError(t, err)
	after, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, wire, parsed.GetRawBytes())
	outer := parsed.(*Batch)
	require.Equal(t, "CreatedNode", outer.RawTransactions[1].Wrapper)
	originalID, err := tx.ComputeTransactionHash(parsed)
	require.NoError(t, err)

	decoded["Sequence"] = uint32(100)
	wrappers := decoded["RawTransactions"].([]any)
	innerFields := wrappers[0].(map[string]any)["RawTransaction"].(map[string]any)
	innerFields["Sequence"] = uint32(101)
	innerFields["Memos"].([]any)[0].(map[string]any)["Memo"].(map[string]any)["MemoData"] = "00"
	wrappers[1] = wrappers[0]
	require.Equal(t, uint32(1), *parsed.GetCommon().Sequence)
	require.Equal(t, uint32(2), *outer.RawTransactions[0].RawTransaction.InnerTx.GetCommon().Sequence)
	matches, err := tx.CurrentFieldsMatchRaw(parsed)
	require.NoError(t, err)
	require.True(t, matches)
	currentID, err := tx.ComputeCurrentTransactionHash(parsed)
	require.NoError(t, err)
	require.Equal(t, originalID, currentID)
	require.Equal(t, wire, parsed.GetRawBytes())
}
