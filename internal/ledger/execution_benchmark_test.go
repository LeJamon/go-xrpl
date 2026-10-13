package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func BenchmarkExecutionDisjointPathsRootFallback(b *testing.B) {
	family := backend.NewMemory()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("workspace", 16<<20, 16<<20)
	if err != nil {
		b.Fatal(err)
	}
	memory, err := pool.NewAccount("overlay", 1<<20, 8<<20)
	if err != nil {
		b.Fatal(err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: family, Workspace: workspace, Overlay: overlay.Config{MemoryBudget: memory, MaxMemoryBytes: 1 << 20, MaxDiskBytes: 16 << 20, SpillDir: b.TempDir()}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Error(err)
		}
	})
	stateMap, err := shamap.NewBacked(shamap.TypeState, family)
	if err != nil {
		b.Fatal(err)
	}
	txMap, err := shamap.NewBacked(shamap.TypeTransaction, family)
	if err != nil {
		b.Fatal(err)
	}
	keys := make([]keylet.Keylet, 4096)
	for i := range keys {
		var input [8]byte
		binary.BigEndian.PutUint64(input[:], uint64(i))
		keys[i] = keylet.Keylet{Key: sha256.Sum256(input[:])}
		if err := stateMap.Put(keys[i].Key, make([]byte, 128)); err != nil {
			b.Fatal(err)
		}
	}
	closeTime := time.Unix(1700000000, 0)
	current, err := NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 2, Drops: 1000000000, CloseTimeResolution: 30}, stateMap, txMap, drops.Fees{}, store)
	if err != nil {
		b.Fatal(err)
	}
	if err := current.Close(closeTime, 0); err != nil {
		b.Fatal(err)
	}
	history := make([]*Ledger, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		current, err = NewOpen(current, closeTime)
		if err != nil {
			b.Fatal(err)
		}
		for offset := range 128 {
			key := keys[(n*131+offset)%len(keys)]
			if _, err := current.Read(key); err != nil {
				b.Fatal(err)
			}
			if offset < 16 {
				value := make([]byte, 128)
				binary.BigEndian.PutUint64(value, uint64(n+1))
				if err := current.Update(key, value); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := current.Close(closeTime, 0); err != nil {
			b.Fatal(err)
		}
		history[n%len(history)] = current
		closeTime = closeTime.Add(4 * time.Second)
	}
	b.StopTimer()
	b.ReportMetric(float64(memory.Snapshot().Peak), "overlay-peak-bytes")
}
