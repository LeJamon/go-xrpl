package ledger

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

type recordingExecutionFamily struct {
	shamap.Family
	mu      sync.Mutex
	batches []int
	fetches int
	fail    error
}

type droppingExecutionFamily struct {
	shamap.Family
}

func (f *droppingExecutionFamily) StoreBatch(context.Context, []shamap.FlushEntry) error {
	return nil
}

func (f *recordingExecutionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	return f.Family.Fetch(ctx, hash)
}

func (f *recordingExecutionFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	f.batches = append(f.batches, len(entries))
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return fail
	}
	return f.Family.StoreBatch(ctx, entries)
}

func executionTestStore(t *testing.T, family shamap.Family) (*ExecutionStore, *membudget.Account) {
	t.Helper()
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("work", 16<<20, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := pool.NewAccount("changes", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewExecutionStore(ExecutionConfig{Family: family, Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: changes, MaxMemoryBytes: 128 << 10, MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, workspace
}

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

func TestExecutionCommitmentSinkBatchesBorrowedNodes(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, workspace := executionTestStore(t, family)
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	for seed := byte(1); seed != 0; seed++ {
		key := mutAcct(seed).Key
		if err := tree.Put(key, bytes.Repeat(mutData(seed), 4)); err != nil {
			t.Fatal(err)
		}
	}
	if root, err := tree.hash(context.Background()); err != nil || root == ([32]byte{}) {
		t.Fatalf("hash = %x, %v", root, err)
	}
	family.mu.Lock()
	batches := append([]int(nil), family.batches...)
	family.mu.Unlock()
	if len(batches) < 2 {
		t.Fatalf("commitment batches = %v, want bounded multi-batch output", batches)
	}
	for _, entries := range batches {
		if entries == 0 || entries > executionCommitmentBatchEntries {
			t.Fatalf("batch entry count = %d", entries)
		}
	}
	if used := workspace.Snapshot().Used; used != 0 {
		t.Fatalf("workspace retained %d bytes after commitment", used)
	}
	stats := store.Stats()
	if stats.Builds != 1 || stats.BuildErrors != 0 || stats.BuildNodes == 0 || stats.MaxFrontier == 0 || stats.BuildNanos == 0 {
		t.Fatalf("unexpected execution stats: %+v", stats)
	}
}

func TestExecutionCommitmentSinkFailureReleasesWorkspace(t *testing.T) {
	want := errors.New("store failed")
	family := &recordingExecutionFamily{Family: backend.NewMemory(), fail: want}
	store, workspace := executionTestStore(t, family)
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err := tree.Put(mutAcct(1).Key, bytes.Repeat(mutData(1), 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.hash(context.Background()); !errors.Is(err, want) {
		t.Fatalf("hash error = %v, want %v", err, want)
	}
	if used := workspace.Snapshot().Used; used != 0 {
		t.Fatalf("workspace retained %d bytes after failed commitment: %d", used, used)
	}
}

func TestEnableBoundedExecutionRejectsPartialDestination(t *testing.T) {
	parent := newOpenChild(t)
	if err := parent.Insert(mutAcct(0x7a), mutData(0x31)); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	family := &droppingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	if err := parent.EnableBoundedExecution(context.Background(), store); err == nil {
		t.Fatal("EnableBoundedExecution accepted a destination that dropped every node")
	}
	if parent.execution != nil || parent.stateMap == nil || parent.txMap == nil {
		t.Fatal("failed bounded conversion discarded the source maps")
	}
}

func TestBoundedExecutionClosureProofIsOneTimeAndPromotable(t *testing.T) {
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, _ := executionTestStore(t, family)
	parent := newOpenChild(t)
	if err := parent.Close(time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	if err := parent.EnableBoundedExecution(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if !parent.HasVerifiedExecutionClosure() {
		t.Fatal("imported bounded execution has no verified closure")
	}
	otherStore, _ := testExecutionStore(t)
	if err := parent.EnableBoundedExecution(context.Background(), otherStore); err == nil {
		t.Fatal("bounded execution accepted a different execution store")
	}
	child, err := NewOpen(parent, time.Unix(1700000004, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Insert(mutAcct(0xef), mutData(9)); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(time.Unix(1700000004, 0), 0); err != nil {
		t.Fatal(err)
	}
	if child.HasVerifiedExecutionClosure() {
		t.Fatal("newly emitted closure was marked durable before establishment")
	}
	family.mu.Lock()
	family.fetches = 0
	family.mu.Unlock()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := child.EstablishBoundedClosure(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled closure establishment error = %v, want context cancellation", err)
	}
	if child.HasVerifiedExecutionClosure() {
		t.Fatal("canceled closure establishment marked the root verified")
	}
	if err := child.EstablishBoundedClosure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !child.HasVerifiedExecutionClosure() {
		t.Fatal("pending closure was not promoted")
	}
	family.mu.Lock()
	fetches := family.fetches
	family.mu.Unlock()
	if fetches != 0 {
		t.Fatalf("promoting pending closure fetched %d nodes", fetches)
	}
	child.Discard()
}
