package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexesField builds a serialized DirectoryNode Indexes field (Vector256,
// type 19 field 1) carrying payloadLen raw bytes behind the XRPL single-byte
// length prefix. payloadLen must be <= 192 so the prefix stays one byte.
func indexesField(payloadLen int) []byte {
	b := []byte{0x01, 0x13, byte(payloadLen)} // field header + 1-byte VL length
	return append(b, make([]byte, payloadLen)...)
}

// TestParseDirectoryNode_RejectsMalformedIndexes pins issue 1078: a DirectoryNode
// whose Indexes Vector256 payload length is not a multiple of 32 must be rejected,
// matching the public binary codec and the streaming ledgerfields decoder, rather
// than silently dropping the trailing bytes and misreading directory membership.
func TestParseDirectoryNode_RejectsMalformedIndexes(t *testing.T) {
	t.Parallel()

	// 33 bytes: one whole hash plus a stray trailing byte.
	_, err := ParseDirectoryNode(indexesField(33))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad serialization for STVector256")

	// 32 bytes: a single well-formed hash still parses cleanly.
	dir, err := ParseDirectoryNode(indexesField(32))
	require.NoError(t, err)
	require.Len(t, dir.Indexes, 1)
}
