package nodestore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

// resurrectOnIterate wraps a store and fires a one-shot callback immediately
// after the prune iterator has snapshotted the keyspace, before the flush runs.
// It reproduces the online-delete race: a key present below the boundary in the
// frozen snapshot is re-created live (at seq >= boundary) during the scan window.
type resurrectOnIterate struct {
	*memorydb.MemDatabase
	once sync.Once
	fn   func()
}

func (r *resurrectOnIterate) NewIterator(prefix, start []byte) kvstore.Iterator {
	it := r.MemDatabase.NewIterator(prefix, start)
	if r.fn != nil {
		r.once.Do(r.fn)
	}
	return it
}

// storeNodeAt persists a node carrying an explicit ledger sequence so the
// prune scanner can classify it. The key is derived from the data plus seq so
// nodes at different sequences never collide.
func storeNodeAt(t *testing.T, db *KVDatabaseImpl, seq uint32, tag byte) Hash256 {
	t.Helper()
	data := Blob{tag, byte(seq), byte(seq >> 8), byte(seq >> 16), byte(seq >> 24)}
	h := ComputeHash256(data)
	node := &Node{Type: NodeAccount, Hash: h, Data: data, LedgerSeq: seq}
	if err := db.Store(context.Background(), node); err != nil {
		t.Fatalf("Store(seq=%d): %v", seq, err)
	}
	return h
}

func present(t *testing.T, db *KVDatabaseImpl, h Hash256) bool {
	t.Helper()
	n, err := db.Fetch(context.Background(), h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return n != nil
}

func TestDeleteBefore_RemovesBelowBoundaryKeepsAtOrAbove(t *testing.T) {
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	keys := make(map[uint32]Hash256)
	for seq := uint32(1); seq <= 10; seq++ {
		keys[seq] = storeNodeAt(t, db, seq, 0xAA)
	}

	const boundary = 6
	deleted, err := db.DeleteBefore(context.Background(), boundary, 0)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	// Sequences 1..5 are below the boundary → 5 nodes deleted.
	if deleted != 5 {
		t.Fatalf("deleted = %d, want 5", deleted)
	}

	for seq := uint32(1); seq < boundary; seq++ {
		if present(t, db, keys[seq]) {
			t.Errorf("seq %d should have been deleted", seq)
		}
	}
	for seq := uint32(boundary); seq <= 10; seq++ {
		if !present(t, db, keys[seq]) {
			t.Errorf("seq %d should have been retained", seq)
		}
	}
}

func TestDeleteBefore_LiveNodeReWrittenAtLatestSeqSurvives(t *testing.T) {
	// Models the persistence contract: a state node still live in recent
	// ledgers is re-persisted every ledger at the current sequence, so its
	// stored LedgerSeq is the latest, not the one it first appeared at.
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	data := Blob("live-account-state")
	h := ComputeHash256(data)
	// First written at seq 3, then re-written (same key) at seq 50.
	for _, seq := range []uint32{3, 50} {
		node := &Node{Type: NodeAccount, Hash: h, Data: data, LedgerSeq: seq}
		if err := db.Store(context.Background(), node); err != nil {
			t.Fatalf("Store(seq=%d): %v", seq, err)
		}
	}

	if _, err := db.DeleteBefore(context.Background(), 40, 0); err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if !present(t, db, h) {
		t.Fatal("live node re-written at seq 50 must survive a prune below 40")
	}
}

func TestDeleteBefore_Batched(t *testing.T) {
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	for seq := uint32(1); seq <= 200; seq++ {
		storeNodeAt(t, db, seq, 0xBB)
	}
	// Tiny batch forces multiple flush cycles.
	deleted, err := db.DeleteBefore(context.Background(), 150, 7)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 149 {
		t.Fatalf("deleted = %d, want 149", deleted)
	}
}

func TestDeleteBefore_ZeroBoundaryNoOp(t *testing.T) {
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	k := storeNodeAt(t, db, 5, 0xCC)
	deleted, err := db.DeleteBefore(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if !present(t, db, k) {
		t.Fatal("zero-boundary prune must delete nothing")
	}
}

func TestDeleteBefore_EvictsPositiveCache(t *testing.T) {
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	h := storeNodeAt(t, db, 3, 0xDD)
	// Warm the positive cache.
	if _, err := db.Fetch(context.Background(), h); err != nil {
		t.Fatalf("warm Fetch: %v", err)
	}
	if _, ok := db.cache.Get(h); !ok {
		t.Fatal("expected node cached before prune")
	}

	if _, err := db.DeleteBefore(context.Background(), 10, 0); err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if _, ok := db.cache.Get(h); ok {
		t.Fatal("pruned node must be evicted from the positive cache")
	}
	if present(t, db, h) {
		t.Fatal("pruned node must not be fetchable after cache eviction")
	}
}

func TestDeleteBefore_ResurrectedDuringScanSurvives(t *testing.T) {
	// A key whose snapshot copy is below the boundary is re-created live at
	// seq >= boundary after the prune iterator snapshots but before the flush
	// (e.g. AccountDelete then re-funding the same deterministic keylet). The
	// flush must observe the live value and leave the resurrected node alone.
	store := &resurrectOnIterate{MemDatabase: memorydb.New()}
	db := NewKVDatabase(store, "mem", 1000, time.Hour)
	defer db.Close()

	data := Blob("resurrected-account-state")
	h := ComputeHash256(data)
	writeAt := func(seq uint32) {
		node := &Node{Type: NodeAccount, Hash: h, Data: data, LedgerSeq: seq}
		if err := db.Store(context.Background(), node); err != nil {
			t.Fatalf("Store(seq=%d): %v", seq, err)
		}
	}

	// Snapshot copy: a superseded (below-boundary) sequence.
	writeAt(3)
	// Live resurrection at a retained sequence, injected after the snapshot.
	store.fn = func() { writeAt(50) }

	if _, err := db.DeleteBefore(context.Background(), 40, 0); err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if !present(t, db, h) {
		t.Fatal("node re-created live during the scan window must not be pruned")
	}
	// The live value must also be intact, not a stale below-boundary copy.
	n, err := db.Fetch(context.Background(), h)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n == nil || n.LedgerSeq != 50 {
		t.Fatalf("resurrected node LedgerSeq = %v, want 50", n)
	}
}

func TestDeleteBefore_ContextCancelled(t *testing.T) {
	db := NewKVDatabase(memorydb.New(), "mem", 1000, time.Hour)
	defer db.Close()

	storeNodeAt(t, db, 1, 0xEE)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.DeleteBefore(ctx, 10, 0); err != context.Canceled {
		t.Fatalf("DeleteBefore on cancelled ctx = %v, want context.Canceled", err)
	}
}
