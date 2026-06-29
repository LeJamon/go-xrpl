package types

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

// Single-byte field headers (type nibble | field nibble) used to hand-build
// nested blobs: Memo is an STObject field (type 14, field 10) and Memos is an
// STArray field (type 15, field 9).
const (
	memoFieldHeader  = 0xEA
	memosFieldHeader = 0xF9
)

// nestedObjectBlob returns the canonical bytes for `depth` Memo STObjects nested
// one inside the next, the innermost empty: depth open markers followed by depth
// close markers.
func nestedObjectBlob(depth int) []byte {
	b := make([]byte, 0, depth*2)
	for range depth {
		b = append(b, memoFieldHeader)
	}
	for range depth {
		b = append(b, ObjectEndMarker)
	}
	return b
}

func newParser(b []byte) *serdes.BinaryParser {
	return serdes.NewBinaryParser(b, definitions.Get())
}

// TestSTObject_DecodeNestingDepthLimit pins the recursion bound that rippled
// enforces (STVar.cpp:122, STObject.cpp:89). Decoding must reject an
// over-nested blob with a clean error instead of recursing into a
// stack-overflow fatal error, which recover() cannot catch.
func TestSTObject_DecodeNestingDepthLimit(t *testing.T) {
	t.Run("max depth decodes", func(t *testing.T) {
		st := NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
		_, err := st.ToJSONStrict(newParser(nestedObjectBlob(maxNestingDepth)))
		require.NoError(t, err)
	})

	t.Run("one past max is rejected", func(t *testing.T) {
		st := NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
		_, err := st.ToJSONStrict(newParser(nestedObjectBlob(maxNestingDepth + 1)))
		require.ErrorIs(t, err, errMaxNestingDepth)
	})

	// The DoS payload from the report: ~hundreds of KB of bare Memo headers.
	// With the bound it is rejected after reading a handful of bytes; without
	// it the decoder recurses until the goroutine stack overflows.
	t.Run("deep payload is bounded, not crashing", func(t *testing.T) {
		blob := bytes.Repeat([]byte{memoFieldHeader}, 200_000)
		st := NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
		_, err := st.ToJSONStrict(newParser(blob))
		require.ErrorIs(t, err, errMaxNestingDepth)
	})
}

// TestSTArray_DecodeNestingDepthLimit exercises the matching guard on the array
// path: an STArray's STObject elements sit one level deeper than the array, so a
// nested-too-deep element is rejected (STArray.cpp:95, STVar.cpp:122).
func TestSTArray_DecodeNestingDepthLimit(t *testing.T) {
	// One empty Memo object inside the array, then both end markers.
	elementBlob := func() []byte {
		return []byte{memoFieldHeader, ObjectEndMarker, ArrayEndMarker}
	}

	t.Run("element at max depth decodes", func(t *testing.T) {
		// Array at depth 9 → element object at depth 10 (the limit).
		res, err := (&STArray{}).ToJSON(newParser(elementBlob()), maxNestingDepth-1)
		require.NoError(t, err)
		require.Equal(t, []any{map[string]any{"Memo": map[string]any{}}}, res)
	})

	t.Run("element one past max is rejected", func(t *testing.T) {
		// Array at depth 10 → element object at depth 11 (over the limit).
		_, err := (&STArray{}).ToJSON(newParser(elementBlob()), maxNestingDepth)
		require.ErrorIs(t, err, errMaxNestingDepth)
	})

	// Alternating Memos arrays and Memo objects through the full decode path
	// must also stay bounded.
	t.Run("alternating array/object payload is bounded", func(t *testing.T) {
		blob := make([]byte, 0, 100)
		for range 50 {
			blob = append(blob, memosFieldHeader, memoFieldHeader)
		}
		st := NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
		_, err := st.ToJSONStrict(newParser(blob))
		require.ErrorIs(t, err, errMaxNestingDepth)
	})
}
