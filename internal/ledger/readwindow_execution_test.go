package ledger

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

type readWindowBudgetEvent struct {
	op    byte
	bytes uint64
	ok    bool
	used  uint64
}

type tracedReadWindowBudget struct {
	account *membudget.Account

	mu     sync.Mutex
	events []readWindowBudgetEvent
}

func (b *tracedReadWindowBudget) TryReserve(bytes uint64) bool {
	ok := b.account.TryReserve(bytes)
	used := b.account.Snapshot().Used
	b.mu.Lock()
	b.events = append(b.events, readWindowBudgetEvent{op: 'r', bytes: bytes, ok: ok, used: used})
	b.mu.Unlock()
	return ok
}

func (b *tracedReadWindowBudget) Release(bytes uint64) {
	b.account.Release(bytes)
	used := b.account.Snapshot().Used
	b.mu.Lock()
	b.events = append(b.events, readWindowBudgetEvent{op: 'l', bytes: bytes, used: used})
	b.mu.Unlock()
}

func (b *tracedReadWindowBudget) Changed() <-chan struct{} { return b.account.Changed() }

func (b *tracedReadWindowBudget) Limit() uint64 { return b.account.Limit() }

func (b *tracedReadWindowBudget) Events() []readWindowBudgetEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]readWindowBudgetEvent(nil), b.events...)
}

func TestExecutionHashReadWindowEvictsBeforeWorkspacePressure(t *testing.T) {
	pool := membudget.New(2 << 20)
	workspaceAccount, err := pool.NewAccount("workspace", 0, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	workspace := &tracedReadWindowBudget{account: workspaceAccount}
	changes, err := pool.NewAccount("changes", 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	family := &recordingExecutionFamily{Family: backend.NewMemory()}
	store, err := NewExecutionStore(ExecutionConfig{
		Family:    family,
		Workspace: workspace,
		Overlay: overlay.Config{
			MemoryBudget: changes, MaxMemoryBytes: 64 << 10,
			MaxDiskBytes: 16 << 20, SpillDir: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Logf("execution store close: %v", err)
		}
	}()
	base := &executionTree{store: store, kind: shamap.TypeState, sequence: 1}
	tree, err := base.newMutable(1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	for seed := byte(1); seed != 0; seed++ {
		if err := tree.Put(mutAcct(seed).Key, bytes.Repeat(mutData(seed), 32)); err != nil {
			t.Fatalf("Put %d: %v", seed, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if root, err := tree.hash(ctx); err != nil || root == ([32]byte{}) {
		t.Fatalf("hash = %x, %v", root, err)
	}
	snapshot := workspaceAccount.Snapshot()
	if snapshot.Used != 0 {
		t.Fatalf("workspace retained %d bytes after hash", snapshot.Used)
	}
	events := workspace.Events()
	// This fixture's canonical node charges are below 32 KiB, so an exact
	// 32 KiB admission or release identifies a spill read window.
	windowHeld, windowAt, failedAt, releaseAt := 0, -1, -1, -1
	for i, event := range events {
		if event.op == 'r' && event.ok && event.bytes == 32<<10 {
			if windowAt < 0 {
				windowAt = i
			}
			windowHeld++
		}
		if windowHeld > 0 && failedAt < 0 && event.op == 'r' && !event.ok && event.bytes > 0 && event.bytes < 32<<10 && event.used >= 32<<10 {
			failedAt = i
		}
		if event.op == 'l' && event.bytes == 32<<10 {
			windowHeld--
			if failedAt >= 0 && releaseAt < 0 {
				releaseAt = i
			}
		}
	}
	if windowAt < 0 || failedAt < 0 || releaseAt < 0 || !(windowAt < failedAt && failedAt < releaseAt) {
		t.Fatalf("read-window pressure sequence missing: window=%d failed=%d release=%d events=%v", windowAt, failedAt, releaseAt, events)
	}
	if windowHeld != 0 {
		t.Fatalf("read-window pressure left %d exact windows admitted", windowHeld)
	}
	family.mu.Lock()
	batches := append([]int(nil), family.batches...)
	family.mu.Unlock()
	if len(batches) < 2 {
		t.Fatalf("workspace pressure did not flush commitment batches: %v", batches)
	}
}

func TestExecutionCommitmentSinkFlushEvictsEmptyReadWindow(t *testing.T) {
	sink := newExecutionCommitmentSink(context.Background(), backend.NewMemory(), nil)
	evicted := false
	sink.releaseReadBuffers = func() { evicted = true }
	if err := sink.flush(); err != nil {
		t.Fatal(err)
	}
	if !evicted {
		t.Fatal("empty sink flush did not evict optional read buffers")
	}
}
