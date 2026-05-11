package adaptor

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeBlob builds a deterministic >=12-byte tx blob from a seed —
// SHAMap rejects shorter transaction leaves.
func makeBlob(seed uint32) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b, seed)
	binary.BigEndian.PutUint32(b[4:], ^seed)
	binary.BigEndian.PutUint32(b[8:], seed*2654435761) // Knuth mult — spreads txIDs across SHAMap branches
	binary.BigEndian.PutUint32(b[12:], seed+0xA5A5A5A5)
	return b
}

// TestTxSet_TxsTxIDsZipped pins the contract that TxIDs[i]
// corresponds to Txs[i] — the consensus dispute builder
// (rcl/engine.go:813) zips the two slices.
func TestTxSet_TxsTxIDsZipped(t *testing.T) {
	blobs := make([][]byte, 32)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}
	ts, err := NewTxSet(blobs)
	require.NoError(t, err)

	txs := ts.Txs()
	ids := ts.TxIDs()
	require.Equal(t, len(blobs), len(txs))
	require.Equal(t, len(txs), len(ids))

	for i, blob := range txs {
		assert.Equal(t, computeTxID(blob), ids[i],
			"TxIDs[%d] must be the hash of Txs[%d]", i, i)
	}
}

// TestTxSet_TxsCanonicalOrder pins that the SHAMap-backed
// storage walks leaves in canonical key order — Txs() return
// order is the same across calls and is sorted by txID.
func TestTxSet_TxsCanonicalOrder(t *testing.T) {
	blobs := make([][]byte, 16)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}
	ts, err := NewTxSet(blobs)
	require.NoError(t, err)

	ids := ts.TxIDs()
	for i := 1; i < len(ids); i++ {
		prev, cur := ids[i-1], ids[i]
		assert.Negative(t, bytes.Compare(prev[:], cur[:]),
			"TxIDs must be in ascending key order; %x not < %x", prev, cur)
	}

	// Repeat the walk — order must be stable.
	ids2 := ts.TxIDs()
	assert.Equal(t, ids, ids2)
}

// TestTxSet_IDStableAcrossInsertionOrder pins that the tx-set
// ID is a function of the set of blobs, not the insertion order.
// This is the property rippled relies on for cross-validator
// agreement on the proposed tx-set hash.
func TestTxSet_IDStableAcrossInsertionOrder(t *testing.T) {
	blobs := make([][]byte, 8)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}

	forward, err := NewTxSet(blobs)
	require.NoError(t, err)
	reversed := make([][]byte, len(blobs))
	for i, b := range blobs {
		reversed[len(blobs)-1-i] = b
	}
	backward, err := NewTxSet(reversed)
	require.NoError(t, err)

	assert.Equal(t, forward.ID(), backward.ID(),
		"tx-set ID must be insertion-order independent")
}

// TestTxSet_IDChangesOnMutation pins that ID() refreshes after
// Add/Remove — the SHAMap's incremental dirty propagation must
// have updated the root hash by the time ID() returns.
func TestTxSet_IDChangesOnMutation(t *testing.T) {
	blob1 := makeBlob(1)
	blob2 := makeBlob(2)

	ts, err := NewTxSet([][]byte{blob1})
	require.NoError(t, err)
	id0 := ts.ID()
	require.NotEqual(t, consensus.TxSetID{}, id0)

	require.NoError(t, ts.Add(blob2))
	id1 := ts.ID()
	assert.NotEqual(t, id0, id1, "Add must change the tx-set ID")

	// Adding the same blob a second time is a no-op.
	require.NoError(t, ts.Add(blob2))
	assert.Equal(t, id1, ts.ID(), "duplicate Add must not change ID")

	require.NoError(t, ts.Remove(computeTxID(blob2)))
	assert.Equal(t, id0, ts.ID(),
		"Remove of a just-added tx must restore the prior ID")
}

// TestTxSet_RejectsInvalidBlobs pins that NewTxSet surfaces SHAMap
// rejection (e.g. <12-byte transaction leaves) instead of silently
// shrinking the set. Rippled never silently truncates a tx-set during
// construction (RCLCxTx.h:87-91); a truncated set would compute the
// wrong root hash and break consensus.
func TestTxSet_RejectsInvalidBlobs(t *testing.T) {
	good := makeBlob(1)
	tooShort := []byte{0x01, 0x02, 0x03} // <12 bytes — rejected by NewTransactionLeafNode

	ts, err := NewTxSet([][]byte{good, tooShort})
	require.Error(t, err, "short blob must surface as a construction error")
	assert.Nil(t, ts, "failed construction must not return a partial tx-set")
}

// TestTxSet_BytesIsCanonical pins that the Bytes() framing walks
// leaves in canonical order so the serialized form is independent
// of insertion order.
func TestTxSet_BytesIsCanonical(t *testing.T) {
	blobs := make([][]byte, 6)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}

	forwardTs, err := NewTxSet(blobs)
	require.NoError(t, err)
	forward := forwardTs.Bytes()
	reversed := make([][]byte, len(blobs))
	for i, b := range blobs {
		reversed[len(blobs)-1-i] = b
	}
	backwardTs, err := NewTxSet(reversed)
	require.NoError(t, err)
	backward := backwardTs.Bytes()
	assert.Equal(t, forward, backward,
		"Bytes() output must be insertion-order independent")
}

// BenchmarkTxSetAdd_Incremental measures the per-Add cost on
// successively larger sets. With the SHAMap-canonical storage,
// each Add does O(log N) work; the prior blob-slice
// implementation rebuilt the SHAMap on every Add, giving O(N).
//
// Compare ns/op across the sub-benchmarks: with O(log N) Add the
// growth from N=128 → N=8192 should be ~6x (log2 of 64), not 64x.
func BenchmarkTxSetAdd_Incremental(b *testing.B) {
	for _, n := range []int{128, 512, 2048, 8192} {
		b.Run(sizeLabel(n), func(b *testing.B) {
			// Pre-seed the set so each timed Add lands at depth ~log2(n).
			seed := make([][]byte, n)
			for i := range seed {
				seed[i] = makeBlob(uint32(i + 1))
			}

			// Pre-build the candidate blobs the timed loop will Add.
			adds := make([][]byte, b.N)
			for i := 0; i < b.N; i++ {
				adds[i] = makeBlob(uint32(n + i + 1))
			}

			b.ReportAllocs()
			b.ResetTimer()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				// Re-seed a fresh tx-set every iteration so the timed
				// Add always lands on a set of size ~n. Otherwise the
				// loop just grows N unbounded and the per-Add cost
				// shifts as N doubles.
				ts, err := NewTxSet(seed)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				_ = ts.Add(adds[i])
				b.StopTimer()
			}
		})
	}
}

func sizeLabel(n int) string {
	switch n {
	case 128:
		return "N=128"
	case 512:
		return "N=512"
	case 2048:
		return "N=2048"
	case 8192:
		return "N=8192"
	}
	return "N=?"
}
