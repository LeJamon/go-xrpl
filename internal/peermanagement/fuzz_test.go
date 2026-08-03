package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/pierrec/lz4"
)

// FuzzDecompressLZ4 feeds arbitrary compressed data and claimed sizes to DecompressLZ4.
// It must never panic. On success it verifies the output length matches the claimed size.
func FuzzDecompressLZ4(f *testing.F) {
	// Seed: empty data + size=0
	f.Add([]byte{}, uint16(0))

	// Seed: empty data + size=100 (mismatch — should error)
	f.Add([]byte{}, uint16(100))

	// Seed: random bytes + size=10
	f.Add([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE}, uint16(10))

	// Seed: valid LZ4 compressed block + correct size.
	// Compress a known payload to produce a valid seed.
	{
		original := []byte("The quick brown fox jumps over the lazy dog. XRPL peer protocol test payload for fuzz testing.")
		maxDst := lz4.CompressBlockBound(len(original))
		compressed := make([]byte, maxDst)
		n, err := lz4.CompressBlock(original, compressed, nil)
		if err == nil && n > 0 {
			f.Add(compressed[:n], uint16(len(original)))
		}
	}

	// Seed: valid LZ4 block but wrong claimed size.
	{
		original := []byte("Another test payload for LZ4 decompression fuzzing with intentionally wrong size claim.")
		maxDst := lz4.CompressBlockBound(len(original))
		compressed := make([]byte, maxDst)
		n, err := lz4.CompressBlock(original, compressed, nil)
		if err == nil && n > 0 {
			// Claim a size that is too small — should fail.
			f.Add(compressed[:n], uint16(10))
		}
	}

	f.Fuzz(func(t *testing.T, data []byte, claimedSize uint16) {
		// Use uint16 directly — max value 65535 bytes, safe from OOM.
		size := int(claimedSize)

		result, err := message.DecompressLZ4(data, size)
		if err != nil {
			return
		}

		// Invariant: successful decompression must produce exactly the claimed size.
		if len(result) != size {
			t.Fatalf("DecompressLZ4 returned %d bytes, expected %d", len(result), size)
		}
	})
}
