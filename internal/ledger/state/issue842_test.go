package state

import (
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSerializeLedgerOffer_HybridAdditionalBooks pins that a hybrid offer's
// AdditionalBooks STArray is written to the SLE blob and survives a parse
// round-trip, matching rippled's CreateOffer::applyHybrid.
func TestSerializeLedgerOffer_HybridAdditionalBooks(t *testing.T) {
	t.Parallel()

	var bookDir, addlBookDir [32]byte
	for i := range bookDir {
		bookDir[i] = 0x11
	}
	for i := range addlBookDir {
		addlBookDir[i] = 0x22
	}

	offer := &LedgerOffer{
		Account:                 "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		Sequence:                7,
		TakerPays:               NewXRPAmountFromInt(10_000_000),
		TakerGets:               NewXRPAmountFromInt(20_000_000),
		BookDirectory:           bookDir,
		BookNode:                0,
		OwnerNode:               0,
		Flags:                   0x00040000, // lsfHybrid
		AdditionalBookDirectory: addlBookDir,
		AdditionalBookNode:      3,
	}

	data, err := SerializeLedgerOffer(offer)
	require.NoError(t, err)

	// The blob must round-trip through the binary codec with the
	// AdditionalBooks STArray present and well-formed.
	decoded, err := binarycodec.Decode(strings.ToUpper(toHex(data)))
	require.NoError(t, err)
	books, ok := decoded["AdditionalBooks"].([]any)
	require.True(t, ok, "AdditionalBooks must serialize as an STArray")
	require.Len(t, books, 1)
	book := books[0].(map[string]any)["Book"].(map[string]any)
	assert.Equal(t, strings.Repeat("22", 32), strings.ToLower(book["BookDirectory"].(string)))
	assert.Equal(t, "3", book["BookNode"])

	// The hand-rolled parser must recover the linkage so it survives reload.
	parsed, err := ParseLedgerOffer(data)
	require.NoError(t, err)
	assert.Equal(t, addlBookDir, parsed.AdditionalBookDirectory)
	assert.Equal(t, uint64(3), parsed.AdditionalBookNode)
	assert.Equal(t, uint32(7), parsed.Sequence)
	assert.Equal(t, uint32(0x00040000), parsed.Flags)
	assert.Equal(t, bookDir, parsed.BookDirectory)
}

// TestSerializeLedgerOffer_NoAdditionalBooks pins that non-hybrid offers do not
// emit AdditionalBooks.
func TestSerializeLedgerOffer_NoAdditionalBooks(t *testing.T) {
	t.Parallel()

	offer := &LedgerOffer{
		Account:   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		Sequence:  1,
		TakerPays: NewXRPAmountFromInt(1_000_000),
		TakerGets: NewXRPAmountFromInt(2_000_000),
	}

	data, err := SerializeLedgerOffer(offer)
	require.NoError(t, err)

	decoded, err := binarycodec.Decode(strings.ToUpper(toHex(data)))
	require.NoError(t, err)
	_, present := decoded["AdditionalBooks"]
	assert.False(t, present, "non-hybrid offers must not carry AdditionalBooks")

	parsed, err := ParseLedgerOffer(data)
	require.NoError(t, err)
	assert.Equal(t, [32]byte{}, parsed.AdditionalBookDirectory)
}

// TestSerializeDirectoryNode_EmptyIndexesPresent pins that a kept-empty
// directory page serializes sfIndexes even when empty (field-ID 0113 + VL 00),
// matching rippled's dirRemove keepRoot path.
func TestSerializeDirectoryNode_EmptyIndexesPresent(t *testing.T) {
	t.Parallel()

	var root [32]byte
	for i := range root {
		root[i] = 0xAB
	}
	node := &DirectoryNode{RootIndex: root, IndexNext: 1, IndexPrevious: 7}

	data, err := SerializeDirectoryNode(node, false)
	require.NoError(t, err)

	// The empty Vector256 must be on the wire as 0113 + 00.
	hexUpper := strings.ToUpper(toHex(data))
	assert.Contains(t, hexUpper, "011300", "empty sfIndexes must serialize as 0113 + VL 00")

	parsed, err := ParseDirectoryNode(data)
	require.NoError(t, err)
	assert.Empty(t, parsed.Indexes)
	assert.Equal(t, uint64(1), parsed.IndexNext)
	assert.Equal(t, uint64(7), parsed.IndexPrevious)
}

// TestParseLedgerOffer_SkipArrayStructural pins that an unrecognized STArray is
// skipped structurally — by walking its inner objects field-by-field — rather
// than by scanning for the array end byte. A Hash256 payload that contains the
// array/object end markers (0xF1/0xE1) must not terminate the skip early; a
// field positioned after the array must still be parsed.
func TestParseLedgerOffer_SkipArrayStructural(t *testing.T) {
	t.Parallel()

	// Hand-built offer blob. Field order is irrelevant to the parser; what
	// matters is that the trailing Expiration is reached only if skipArray
	// consumes the whole array despite the booby-trapped payload.
	var blob []byte

	// Unrecognized STArray (type 15, nth=20 — not AdditionalBooks/13).
	blob = append(blob, 0xF0, 0x14)
	//   Inner Book object (type 14, nth=36).
	blob = append(blob, 0xE0, 0x24)
	//     BookDirectory Hash256 (type 5, nth=16) whose 32 payload bytes are
	//     all array/object end markers — the trap for a byte-scan skipper.
	blob = append(blob, 0x50, 0x10)
	for range 16 {
		blob = append(blob, arrayEndMarker, objectEndMarker) // 0xF1 0xE1 ×16
	}
	blob = append(blob, objectEndMarker) // end of inner object
	blob = append(blob, arrayEndMarker)  // end of array

	// Trailing Expiration (type 2 UInt32, nth=10) = 0xDEADBEEF.
	blob = append(blob, 0x2A, 0xDE, 0xAD, 0xBE, 0xEF)

	parsed, err := ParseLedgerOffer(blob)
	require.NoError(t, err)
	assert.Equal(t, uint32(0xDEADBEEF), parsed.Expiration,
		"field after the trapped array must be parsed; skipArray must skip structurally")
}

func toHex(b []byte) string {
	const hexits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexits[v>>4]
		out[i*2+1] = hexits[v&0x0f]
	}
	return string(out)
}
