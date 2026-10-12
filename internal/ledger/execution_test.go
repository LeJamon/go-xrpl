package ledger

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func testExecutionStore(t *testing.T) (*ExecutionStore, *membudget.Account) {
	t.Helper()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("work", 16<<20, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := pool.NewAccount("changes", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: backend.NewMemory(), Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: memory, MaxMemoryBytes: 128 << 10, MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, memory
}

func TestBoundedExecutionLedgerDifferential(t *testing.T) {
	ctx := context.Background()
	store, memory := testExecutionStore(t)
	legacy := newOpenChild(t)
	closeTime := time.Unix(1700000000, 0)
	if err := legacy.Close(closeTime, 0); err != nil {
		t.Fatal(err)
	}
	bounded, err := legacy.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { bounded.Discard() }()
	if err := bounded.EnableBoundedExecution(ctx, store); err != nil {
		t.Fatal(err)
	}
	for round := range 48 {
		legacy, err = NewOpen(legacy, closeTime)
		if err != nil {
			t.Fatal(err)
		}
		bounded, err = NewOpen(bounded, closeTime)
		if err != nil {
			t.Fatal(err)
		}
		for offset := range 64 {
			key := mutAcct(byte(round*13 + offset))
			value := bytes.Repeat(mutData(byte(round+offset)), 128)
			for _, view := range []*Ledger{legacy, bounded} {
				exists, err := view.Exists(key)
				if err != nil {
					t.Fatal(err)
				}
				if exists {
					err = view.Update(key, value)
				} else {
					err = view.Insert(key, value)
				}
				if err != nil {
					t.Fatal(err)
				}
				if offset%7 == 0 {
					if err := view.Erase(key); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		for _, view := range []*Ledger{legacy, bounded} {
			txKey := [32]byte{byte(round + 1)}
			if err := view.AddTransactionWithMeta(txKey, bytes.Repeat([]byte{byte(round)}, 16)); err != nil {
				t.Fatal(err)
			}
			if err := view.Close(closeTime, 0); err != nil {
				t.Fatal(err)
			}
		}
		if bounded.Hash() != legacy.Hash() {
			t.Fatalf("round %d: ledger hash mismatch", round)
		}
		if bounded.TxCount() != legacy.TxCount() {
			t.Fatalf("round %d: transaction count mismatch", round)
		}
		if bounded.stateMap != nil || bounded.txMap != nil {
			t.Fatal("bounded ledger retained legacy trees")
		}
		if got := memory.Snapshot().Used; got != 0 {
			t.Fatalf("round %d: closed ledger retains %d managed bytes", round, got)
		}
		closeTime = closeTime.Add(4 * time.Second)
	}
}

func TestBoundedExecutionRollbackForkAndReaders(t *testing.T) {
	store, memory := testExecutionStore(t)
	parent := newOpenChild(t)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	if err := parent.EnableBoundedExecution(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	l, err := NewOpen(parent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Discard()
	key := mutAcct(55)
	if err := l.Insert(key, mutData(3)); err != nil {
		t.Fatal(err)
	}
	before, err := l.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("rollback")
	err = l.ApplyAtomically(func(writer Writer) error {
		if err := writer.Update(key, mutData(4)); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	fork, err := l.MutableSnapshotUnflushed()
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Discard()
	if err := fork.Update(key, mutData(5)); err != nil {
		t.Fatal(err)
	}
	if err := l.ConsumeState(fork); err != nil {
		t.Fatal(err)
	}
	data, err := before.Read(key)
	if err != nil || !bytes.Equal(data, mutData(3)) {
		t.Fatalf("snapshot changed: %x %v", data, err)
	}
	data, err = l.Read(key)
	if err != nil || !bytes.Equal(data, mutData(5)) {
		t.Fatalf("commit failed: %x %v", data, err)
	}
	if err := l.FreezeExecution(); err != nil {
		t.Fatal(err)
	}
	if got := memory.Snapshot().Used; got != 0 {
		t.Fatalf("frozen view retains %d managed bytes", got)
	}
	seen := false
	if err := l.ForEach(func(k [32]byte, _ []byte) bool {
		_, readErr := l.Read(key)
		if readErr != nil {
			t.Error(readErr)
		}
		seen = seen || k == key.Key
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("iteration omitted entry")
	}
}
