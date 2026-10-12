package localtxs_test

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
)

func TestLocalTxsSnapshotStatsCountsOwnedBlobsWithoutMutation(t *testing.T) {
	pool := localtxs.New()
	first := openledger.PendingTx{Hash: [32]byte{1}, Blob: bytes.Repeat([]byte{0x11}, 5)}
	second := openledger.PendingTx{Hash: [32]byte{2}, Blob: bytes.Repeat([]byte{0x22}, 13)}
	pool.PushBack(1, first)
	pool.PushBack(1, second)

	count, blobBytes := pool.SnapshotStats()
	if count != 2 || blobBytes != 18 {
		t.Fatalf("SnapshotStats() = (%d, %d), want (2, 18)", count, blobBytes)
	}

	snapshot := pool.GetTxSet()
	if len(snapshot) != 2 || !bytes.Equal(snapshot[0].Blob, first.Blob) && !bytes.Equal(snapshot[1].Blob, first.Blob) {
		t.Fatal("SnapshotStats changed the held transaction blobs")
	}
	countAfter, bytesAfter := pool.SnapshotStats()
	if countAfter != count || bytesAfter != blobBytes {
		t.Fatalf("SnapshotStats changed pool state: before (%d, %d), after (%d, %d)", count, blobBytes, countAfter, bytesAfter)
	}
}

func TestLocalTxsSnapshotStatsEmpty(t *testing.T) {
	count, blobBytes := localtxs.New().SnapshotStats()
	if count != 0 || blobBytes != 0 {
		t.Fatalf("SnapshotStats() = (%d, %d), want (0, 0)", count, blobBytes)
	}
}
