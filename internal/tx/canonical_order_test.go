package tx

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestParseFromBinaryCanonicalizesFieldOrder(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	fields := map[string]any{
		"TransactionType": "AccountSet",
		"Account":         account,
		"Sequence":        uint32(1),
		"Fee":             "10",
		"SigningPubKey":   "",
	}
	canonical, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(canonical), 8)
	require.Equal(t, byte(0x12), canonical[0])
	require.Equal(t, byte(0x24), canonical[3])

	reordered := make([]byte, 0, len(canonical))
	reordered = append(reordered, canonical[3:8]...)
	reordered = append(reordered, canonical[:3]...)
	reordered = append(reordered, canonical[8:]...)
	require.False(t, bytes.Equal(canonical, reordered))
	_, err = binarycodec.DecodeBytes(reordered)
	require.NoError(t, err)

	canonicalTx, err := ParseFromBinary(canonical)
	require.NoError(t, err)
	reorderedTx, err := ParseFromBinary(reordered)
	require.NoError(t, err)

	canonicalID, err := ComputeTransactionHash(canonicalTx)
	require.NoError(t, err)
	reorderedID, err := ComputeTransactionHash(reorderedTx)
	require.NoError(t, err)
	require.Equal(t, canonicalID, reorderedID)
	require.Equal(t, canonical, reorderedTx.GetRawBytes())

	programmatic := NewBaseTx(TypeAccountSet, account)
	programmatic.Fee = "10"
	programmatic.Sequence = ptrTo(uint32(1))
	require.NoError(t, BindRawBytes(programmatic, reordered))
	require.Equal(t, canonical, programmatic.GetRawBytes())
	boundID, err := ComputeTransactionHash(programmatic)
	require.NoError(t, err)
	require.Equal(t, canonicalID, boundID)
}

func TestParseFromBinaryPreservesExplicitEmptyMemoFields(t *testing.T) {
	fields := map[string]any{
		"TransactionType": "AccountSet",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Sequence":        uint32(1),
		"Fee":             "10",
		"SigningPubKey":   "",
		"Memos": []any{map[string]any{
			"Memo": map[string]any{"MemoData": ""},
		}},
	}
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)

	parsed, err := ParseFromBinary(blob)
	require.NoError(t, err)
	require.Equal(t, blob, parsed.GetRawBytes())
	matches, err := CurrentFieldsMatchRaw(parsed)
	require.NoError(t, err)
	require.True(t, matches)

	flattened, err := parsed.Flatten()
	require.NoError(t, err)
	memos := flattened["Memos"].([]map[string]any)
	require.Contains(t, memos[0]["Memo"], "MemoData")
	require.Empty(t, memos[0]["Memo"].(map[string]any)["MemoData"])

	parsed.GetCommon().Memos[0].Memo.MemoData = "AA"
	matches, err = CurrentFieldsMatchRaw(parsed)
	require.NoError(t, err)
	require.False(t, matches)
}

func ptrTo[T any](value T) *T { return &value }
