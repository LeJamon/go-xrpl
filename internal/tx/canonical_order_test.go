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

func ptrTo[T any](value T) *T { return &value }
