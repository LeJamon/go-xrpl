package adaptor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
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
// corresponds to Txs[i] — the consensus dispute builder zips
// the two slices.
func TestTxSet_TxsTxIDsZipped(t *testing.T) {
	blobs := make([][]byte, 32)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}
	ts, err := newTxSet(blobs)
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
	ts, err := newTxSet(blobs)
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

	forward, err := newTxSet(blobs)
	require.NoError(t, err)
	reversed := make([][]byte, len(blobs))
	for i, b := range blobs {
		reversed[len(blobs)-1-i] = b
	}
	backward, err := newTxSet(reversed)
	require.NoError(t, err)

	assert.Equal(t, forward.ID(), backward.ID(),
		"tx-set ID must be insertion-order independent")
}

func TestTxSet_DuplicateInputIsDeduplicated(t *testing.T) {
	blob := makeBlob(1)
	single, err := newTxSet([][]byte{blob})
	require.NoError(t, err)
	duplicates, err := newTxSet([][]byte{blob, blob, blob})
	require.NoError(t, err)

	assert.Equal(t, single.ID(), duplicates.ID())
	assert.Equal(t, 1, duplicates.Size())
	assert.Equal(t, single.Txs(), duplicates.Txs())
}

// TestTxSet_RejectsInvalidBlobs pins that newTxSet surfaces SHAMap
// rejection (e.g. <12-byte transaction leaves) instead of silently
// shrinking the set. Rippled never silently truncates a tx-set during
// construction (RCLCxTx.h:87-91); a truncated set would compute the
// wrong root hash and break consensus.
func TestTxSet_RejectsInvalidBlobs(t *testing.T) {
	good := makeBlob(1)
	tooShort := []byte{0x01, 0x02, 0x03} // <12 bytes — rejected by NewTransactionLeafNode

	ts, err := newTxSet([][]byte{good, tooShort})
	require.Error(t, err, "short blob must surface as a construction error")
	assert.Nil(t, ts, "failed construction must not return a partial tx-set")
}

func TestTxSet_ConcurrentReaders(t *testing.T) {
	const count = 200
	blobs := make([][]byte, count)
	for i := range blobs {
		blobs[i] = makeBlob(uint32(i + 1))
	}

	ts, err := newTxSet(blobs)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				assert.Equal(t, count, ts.Size())
				assert.NotZero(t, ts.ID())
				assert.True(t, ts.Contains(computeTxID(blobs[0])))
				assert.Len(t, ts.Txs(), count)
				assert.Len(t, ts.TxIDs(), count)
			}
		}()
	}
	wg.Wait()
}

func TestTxSet_BackingMapIsImmutable(t *testing.T) {
	blob := makeBlob(1)
	ts, err := newTxSet([][]byte{blob})
	require.NoError(t, err)
	originalID := ts.ID()

	newBlob := makeBlob(2)
	err = ts.shamap().PutWithNodeType(
		[32]byte(computeTxID(newBlob)),
		newBlob,
		shamap.NodeTypeTransactionNoMeta,
	)
	require.True(t, errors.Is(err, shamap.ErrImmutable))
	require.True(t, errors.Is(ts.shamap().Delete([32]byte(computeTxID(blob))), shamap.ErrImmutable))
	assert.Equal(t, originalID, ts.ID())
	assert.Equal(t, 1, ts.Size())
	assert.Equal(t, [][]byte{blob}, ts.Txs())
}
