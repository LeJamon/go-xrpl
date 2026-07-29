package inbound

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeStoredReplayTarget(
	t *testing.T,
	parent *ledger.Ledger,
	blobs [][]byte,
	txIDs [][32]byte,
	mutateHeader func(*header.LedgerHeader),
) *ledger.Ledger {
	t.Helper()
	require.Len(t, txIDs, len(blobs))

	txMap := shamap.New(shamap.TypeTransaction)
	for i, blob := range blobs {
		require.NoError(t, txMap.PutWithNodeType(
			txIDs[i],
			blob,
			shamap.NodeTypeTransactionWithMeta,
		))
	}
	require.NoError(t, txMap.SetImmutable())
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := parent.StateMapSnapshot()
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	hdr := header.LedgerHeader{
		LedgerIndex:         parent.Sequence() + 1,
		ParentHash:          parent.Hash(),
		TxHash:              txRoot,
		AccountHash:         stateRoot,
		ParentCloseTime:     parent.CloseTime(),
		CloseTime:           time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC),
		CloseTimeResolution: uint8(parent.CloseTimeResolution()),
		Drops:               parent.TotalDrops(),
		Accepted:            true,
	}
	if mutateHeader != nil {
		mutateHeader(&hdr)
	}
	hdr.Hash = header.CalculateHash(hdr)

	target, err := ledger.NewFromHeader(hdr, stateMap, txMap, parent.GetFees())
	require.NoError(t, err)
	return target
}

func makeStoredReplayLeafWithoutIndex(t *testing.T, txBytes []byte) ([]byte, [32]byte) {
	t.Helper()
	metaHex, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
	})
	require.NoError(t, err)
	metaBytes, err := hex.DecodeString(metaHex)
	require.NoError(t, err)

	blob := make([]byte, 0, len(txBytes)+len(metaBytes)+4)
	blob = append(blob, encodeVL(len(txBytes))...)
	blob = append(blob, txBytes...)
	blob = append(blob, encodeVL(len(metaBytes))...)
	blob = append(blob, metaBytes...)

	_, txID := makeTxWithMetaBlob(t, txBytes, 0)
	return blob, txID
}

func TestNewStoredLedgerReplayOrdersTransactions(t *testing.T) {
	t.Parallel()
	parent := makeGenesisLedger(t)

	txBytes := [][]byte{
		[]byte("stored-replay-tx-index-2-padding"),
		[]byte("stored-replay-tx-index-0-padding"),
		[]byte("stored-replay-tx-index-1-padding"),
	}
	indices := []uint32{2, 0, 1}
	blobs := make([][]byte, len(txBytes))
	txIDs := make([][32]byte, len(txBytes))
	for i := range txBytes {
		blobs[i], txIDs[i] = makeTxWithMetaBlob(t, txBytes[i], indices[i])
	}
	target := makeStoredReplayTarget(t, parent, blobs, txIDs, nil)

	replay, err := NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	require.True(t, replay.IsComplete())
	assert.Equal(t, target.Hash(), replay.Hash())

	result, err := replay.Result()
	require.NoError(t, err)
	assert.Same(t, target, result)

	ordered := replay.OrderedTxs()
	require.Len(t, ordered, 3)
	assert.Equal(t, []uint32{0, 1, 2}, []uint32{
		ordered[0].Index,
		ordered[1].Index,
		ordered[2].Index,
	})
	assert.Equal(t, txIDs[1], ordered[0].Hash)
	assert.Equal(t, txIDs[2], ordered[1].Hash)
	assert.Equal(t, txIDs[0], ordered[2].Hash)
}

func TestNewStoredLedgerReplayIsReadyToApply(t *testing.T) {
	t.Parallel()
	parent := makeGenesisLedger(t)
	closeTime := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	target, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, target.Close(closeTime, 0))

	replay, err := NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)

	derived, err := replay.Apply(tx.EngineConfig{})
	require.NoError(t, err)
	assert.Equal(t, target.Hash(), derived.Hash())
	assert.Equal(t, target.Header().AccountHash, derived.Header().AccountHash)
	assert.Equal(t, target.Header().TxHash, derived.Header().TxHash)
}

func TestNewStoredLedgerReplayRejectsInvalidLinkage(t *testing.T) {
	t.Parallel()
	parent := makeGenesisLedger(t)

	t.Run("nil parent", func(t *testing.T) {
		_, err := NewStoredLedgerReplay(nil, parent, nil)
		require.ErrorContains(t, err, "parent is nil")
	})
	t.Run("nil target", func(t *testing.T) {
		_, err := NewStoredLedgerReplay(parent, nil, nil)
		require.ErrorContains(t, err, "target is nil")
	})
	t.Run("open parent", func(t *testing.T) {
		open, err := ledger.NewOpen(parent, time.Now())
		require.NoError(t, err)
		target := makeStoredReplayTarget(t, parent, nil, nil, nil)
		_, err = NewStoredLedgerReplay(open, target, nil)
		require.ErrorContains(t, err, "parent is not closed")
	})
	t.Run("open target", func(t *testing.T) {
		open, err := ledger.NewOpen(parent, time.Now())
		require.NoError(t, err)
		_, err = NewStoredLedgerReplay(parent, open, nil)
		require.ErrorContains(t, err, "target is not closed")
	})
	t.Run("parent hash mismatch", func(t *testing.T) {
		target := makeStoredReplayTarget(t, parent, nil, nil, func(h *header.LedgerHeader) {
			h.ParentHash[0] ^= 0xff
		})
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrReplayParentMismatch)
	})
	t.Run("sequence mismatch", func(t *testing.T) {
		target := makeStoredReplayTarget(t, parent, nil, nil, func(h *header.LedgerHeader) {
			h.LedgerIndex++
		})
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrReplaySequenceMismatch)
	})
	t.Run("close time resolution mismatch", func(t *testing.T) {
		target := makeStoredReplayTarget(t, parent, nil, nil, func(h *header.LedgerHeader) {
			h.CloseTimeResolution = 120
		})
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "derived")
	})
}

func TestNewStoredLedgerReplayRejectsInvalidTransactionLeaves(t *testing.T) {
	t.Parallel()
	parent := makeGenesisLedger(t)

	t.Run("malformed leaf", func(t *testing.T) {
		malformed := make([]byte, 12)
		malformed[0] = 0xff
		target := makeStoredReplayTarget(
			t,
			parent,
			[][]byte{malformed},
			[][32]byte{{1}},
			nil,
		)
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "split leaf")
	})
	t.Run("missing index field", func(t *testing.T) {
		blob, txID := makeStoredReplayLeafWithoutIndex(
			t,
			[]byte("stored-replay-missing-index-padding"),
		)
		target := makeStoredReplayTarget(t, parent, [][]byte{blob}, [][32]byte{txID}, nil)
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "missing transaction index")
	})
	t.Run("duplicate index", func(t *testing.T) {
		blobA, txA := makeTxWithMetaBlob(t, []byte("stored-replay-duplicate-a-padding"), 0)
		blobB, txB := makeTxWithMetaBlob(t, []byte("stored-replay-duplicate-b-padding"), 0)
		target := makeStoredReplayTarget(
			t,
			parent,
			[][]byte{blobA, blobB},
			[][32]byte{txA, txB},
			nil,
		)
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "duplicate transaction index 0")
	})
	t.Run("index gap", func(t *testing.T) {
		blobA, txA := makeTxWithMetaBlob(t, []byte("stored-replay-gap-a-padding"), 0)
		blobB, txB := makeTxWithMetaBlob(t, []byte("stored-replay-gap-b-padding"), 2)
		target := makeStoredReplayTarget(
			t,
			parent,
			[][]byte{blobA, blobB},
			[][32]byte{txA, txB},
			nil,
		)
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "missing transaction index 1")
	})
	t.Run("transaction hash mismatch", func(t *testing.T) {
		blob, txID := makeTxWithMetaBlob(t, []byte("stored-replay-hash-mismatch-padding"), 0)
		wrongID := txID
		wrongID[0] ^= 0xff
		target := makeStoredReplayTarget(t, parent, [][]byte{blob}, [][32]byte{wrongID}, nil)
		_, err := NewStoredLedgerReplay(parent, target, nil)
		require.ErrorContains(t, err, "transaction hash mismatch")
	})
}

func TestNewStoredLedgerReplayRejectsInvalidHeaderHash(t *testing.T) {
	t.Parallel()
	parent := makeGenesisLedger(t)
	target := makeStoredReplayTarget(t, parent, nil, nil, nil)
	hdr := target.Header()
	hdr.Hash[0] ^= 0xff

	stateMap, err := target.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := target.TxMapSnapshot()
	require.NoError(t, err)
	target, err = ledger.NewFromHeader(hdr, stateMap, txMap, target.GetFees())
	require.NoError(t, err)

	_, err = NewStoredLedgerReplay(parent, target, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "header hash mismatch")
}
