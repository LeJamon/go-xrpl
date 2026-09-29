package applystate

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestThreadItemTypedFieldsPreserveOptionalState(t *testing.T) {
	fields := map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": uint32(0), "RootIndex": "0",
		"Indexes": []string{}, "IndexNext": "0", "PreviousTxnLgrSeq": uint32(7),
	}
	raw, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	hash := [32]byte{3}
	previous, sequence, threaded, changed := threadItem(raw, hash, 8)
	require.True(t, changed)
	require.Equal(t, [32]byte{}, previous)
	require.Equal(t, uint32(7), sequence)
	var decoded entry.DirectoryNode
	require.NoError(t, decoded.Decode(threaded))
	require.True(t, decoded.HasIndexNext())
	gotHash, err := decoded.GetPreviousTxnID()
	require.NoError(t, err)
	require.Equal(t, hash, gotHash)
	require.Equal(t, uint32(8), decoded.PreviousTxnLgrSeq)
	_, _, repeated, changed := threadItem(threaded, hash, 8)
	require.False(t, changed)
	require.True(t, bytes.Equal(threaded, repeated))
}
