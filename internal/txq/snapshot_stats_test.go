package txq

import (
	"bytes"
	"testing"
)

func TestTxQSnapshotStatsCountsStoredAndFallbackBlobs(t *testing.T) {
	q := mustNew(DefaultConfig())
	storedBlob := bytes.Repeat([]byte{0x42}, 17)
	stored := &candidate{
		TxID:     [32]byte{1},
		FeeLevel: FeeLevel(BaseLevel),
		blob:     storedBlob,
		Txn:      &mockTx{id: 99},
	}
	fallback := &candidate{
		TxID:     [32]byte{2},
		FeeLevel: FeeLevel(BaseLevel),
		Txn:      &mockTx{id: 7},
	}
	q.insertByFee(stored)
	q.insertByFee(fallback)

	count, blobBytes := q.SnapshotStats()
	if count != 2 || blobBytes != uint64(len(storedBlob)+1) {
		t.Fatalf("SnapshotStats() = (%d, %d), want (2, %d)", count, blobBytes, len(storedBlob)+1)
	}
	if !bytes.Equal(stored.blob, storedBlob) {
		t.Fatal("SnapshotStats mutated the stored blob")
	}
}

func TestTxQSnapshotStatsEmpty(t *testing.T) {
	count, blobBytes := mustNew(DefaultConfig()).SnapshotStats()
	if count != 0 || blobBytes != 0 {
		t.Fatalf("SnapshotStats() = (%d, %d), want (0, 0)", count, blobBytes)
	}
}
