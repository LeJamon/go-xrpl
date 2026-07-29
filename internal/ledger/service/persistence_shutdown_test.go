package service

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// gatedStore is an in-memory node store whose StoreBatch blocks until release
// is closed, so a test can pile ledgers onto the persist queue while the worker
// is stuck mid-write and then prove Stop drains every queued job before
// returning.
type gatedStore struct {
	nodestore.Database
	releaseOnce sync.Once
	release     chan struct{}
}

func newGatedStore(t *testing.T) *gatedStore {
	t.Helper()
	return &gatedStore{
		Database: newTestNodeStore(t, 10000),
		release:  make(chan struct{}),
	}
}

func (g *gatedStore) StoreBatch(ctx context.Context, nodes []*nodestore.Node) error {
	<-g.release
	return g.Database.StoreBatch(ctx, nodes)
}

func (g *gatedStore) open() { g.releaseOnce.Do(func() { close(g.release) }) }

// buildLedgerWithState makes a lightweight ledger carrying one state entry (so
// persistToNodeStore issues a gated StoreBatch) with a hash distinct per seq.
func buildLedgerWithState(t *testing.T, seq uint32) *ledger.Ledger {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	key := [32]byte{byte(seq), byte(seq >> 8), 0xAB}
	if err := stateMap.Put(key, []byte("state-payload")); err != nil {
		t.Fatalf("state Put: %v", err)
	}
	hdr := header.LedgerHeader{
		LedgerIndex:         seq,
		Drops:               100_000_000_000_000,
		CloseTime:           time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		ParentCloseTime:     time.Unix(1_699_999_990+int64(seq), 0).UTC(),
		CloseTimeResolution: 10,
		Validated:           true,
		Accepted:            true,
	}
	hdr.Hash = [32]byte{0xED, byte(seq), byte(seq >> 8)}
	return ledger.FromGenesis(hdr, stateMap, shamap.New(shamap.TypeTransaction), drops.Fees{})
}

// TestService_Stop_DrainsQueuedPersists proves the shutdown contract: ledgers
// enqueued before Stop are all durable in the node store by the time Stop
// returns, rather than being abandoned when the process exits. The gated store
// forces the worker to stall so the ledgers genuinely queue behind an in-flight
// write, exercising the drain-then-exit path.
func TestService_Stop_DrainsQueuedPersists(t *testing.T) {
	store := newGatedStore(t)
	svc, err := New(Config{Standalone: false, NodeStore: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const n = 100
	ledgers := make([]*ledger.Ledger, n)
	for i := range ledgers {
		seq := uint32(100 + i)
		ledgers[i] = buildLedgerWithState(t, seq)
	}

	// Enqueue everything while the worker is stalled in the gated StoreBatch,
	// so the jobs sit in the queue rather than being drained inline.
	svc.mu.Lock()
	for _, l := range ledgers {
		svc.enqueuePersist(l)
	}
	svc.mu.Unlock()

	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while a persist was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	store.open()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not drain the persistence queue")
	}

	ctx := context.Background()
	for _, l := range ledgers {
		node, ferr := store.Database.Fetch(ctx, nodestore.Hash256(l.Hash()))
		if ferr != nil {
			t.Fatalf("Fetch(seq=%d): %v", l.Sequence(), ferr)
		}
		if node == nil {
			t.Errorf("ledger seq=%d was not persisted before Stop returned — a queued persist was dropped", l.Sequence())
		}
	}
}

func TestService_PersistLedgerWritesHeader(t *testing.T) {
	store := newGatedStore(t)
	store.open()
	svc, err := New(Config{Standalone: false, NodeStore: store})
	if err != nil {
		t.Fatal(err)
	}
	l := buildLedgerWithState(t, 99)
	if err := svc.persistLedger(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	node, err := store.Fetch(context.Background(), nodestore.Hash256(l.Hash()))
	if err != nil || node == nil {
		t.Fatalf("header fetch = %v, %v", node, err)
	}
}

func TestService_ValidatedTipDoesNotRegress(t *testing.T) {
	store := newGatedStore(t)
	store.open()
	svc, err := New(Config{Standalone: false, NodeStore: store})
	if err != nil {
		t.Fatal(err)
	}
	newer := buildLedgerWithState(t, 101)
	older := buildLedgerWithState(t, 100)
	if err := svc.persistValidatedTip(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistValidatedTip(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Fetch(context.Background(), validatedTipKey)
	if err != nil {
		t.Fatal(err)
	}
	newerHash := newer.Hash()
	if stored == nil || stored.LedgerSeq != newer.Sequence() || !bytes.Equal(stored.Data, newerHash[:]) {
		t.Fatalf("validated tip regressed: %+v", stored)
	}
}

// TestService_Stop_Idempotent verifies Stop is safe to call more than once and
// on a Service that was never started.
func TestService_Stop_Idempotent(t *testing.T) {
	unstarted, err := New(Config{Standalone: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	unstarted.Stop() // never started — must be a no-op, not a panic

	svc, err := New(Config{Standalone: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.Stop()
	svc.Stop() // second call must be a no-op
}

// TestService_EnqueuePersist_AfterStop_Dropped confirms the stopped guard keeps
// a late enqueue from sending on the queue after the worker has been joined.
func TestService_EnqueuePersist_AfterStop_Dropped(t *testing.T) {
	store := newGatedStore(t)
	store.open() // never stall
	svc, err := New(Config{Standalone: false, NodeStore: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.Stop()

	// After Stop, an enqueue must not panic (no send on a live worker) and must
	// be dropped rather than silently sent into a drained queue.
	svc.mu.Lock()
	svc.enqueuePersist(buildLedgerWithState(t, 500))
	svc.mu.Unlock()
}
