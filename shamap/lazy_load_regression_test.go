package shamap

import (
	"context"
	"sync"
	"testing"
)

// llr_key returns a non-zero key spread across root branches.
func llr_key(i byte) [32]byte {
	var k [32]byte
	k[0] = i * 17 // vary the high nibble
	k[1] = i
	k[31] = i ^ 0xA5
	return k
}

func llr_val(i byte) []byte {
	return []byte{i, i, i, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

// llr_buildBacked builds a backed map with n items, flushes it with
// releaseChildren=true (leaving the tree hash-only below the root) and
// stores the batch in family.
func llr_buildBacked(t *testing.T, family *memoryFamily, n byte) *SHAMap {
	t.Helper()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	for i := byte(0); i < n; i++ {
		if err := sm.Put(llr_key(i), llr_val(i)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	batch, err := sm.FlushDirty(true)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	return sm
}

// TestFindDifferenceBackedLazyLoad is a regression test for the lazy-load
// divergence where FindDifference descended via raw child pointers: on a
// backed map with released children it misclassified entire subtrees as
// added/removed instead of loading them from the store.
func TestFindDifferenceBackedLazyLoad(t *testing.T) {
	family := newMemoryFamily()
	m1 := llr_buildBacked(t, family, 12)

	rootHash, err := m1.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	m2, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}

	modifiedKey := llr_key(5)
	addedKey := llr_key(200)
	if err := m2.Put(modifiedKey, []byte{9, 9, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("Put modified: %v", err)
	}
	if err := m2.Put(addedKey, llr_val(99)); err != nil {
		t.Fatalf("Put added: %v", err)
	}

	keys, err := m1.FindDifference(m2)
	if err != nil {
		t.Fatalf("FindDifference: %v", err)
	}

	want := map[Key]bool{modifiedKey: true, addedKey: true}
	if len(keys) != len(want) {
		t.Fatalf("FindDifference on backed maps: got %d keys, want %d (%x)", len(keys), len(want), keys)
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected difference key %x", k)
		}
	}
}

// TestIsCompleteBackedLazyLoad is a regression test for the missing-node
// walk divergence: FinishSync/IsComplete used a raw-pointer walk that did
// not lazy-load, so a backed map with released children was reported
// incomplete even though WalkMap (which lazy-loads) said it was complete.
func TestIsCompleteBackedLazyLoad(t *testing.T) {
	family := newMemoryFamily()
	sm := llr_buildBacked(t, family, 12)

	// IsComplete/FinishSync must run before WalkMap: WalkMap's lazy load
	// installs the fetched children back into the tree, which would mask
	// a non-lazy-loading completeness walk.
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if !sm.IsComplete() {
		t.Error("IsComplete must agree with WalkMap on a backed map with released children")
	}
	if err := sm.FinishSync(); err != nil {
		t.Errorf("FinishSync on fully-stored backed map: %v", err)
	}
	if missing := sm.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("WalkMap reports %d missing nodes on a fully-stored map", len(missing))
	}
}

// TestSnapshotSharedSubtreeFlushRace exercises concurrent flushing and
// reading of structurally-shared subtrees between a map and its snapshots.
// Run with -race: the dirty flag is atomic and node hashes are read under
// each node's lock, so sharing dirty nodes across maps must not race.
func TestSnapshotSharedSubtreeFlushRace(t *testing.T) {
	sm := New(TypeState)
	for i := byte(0); i < 24; i++ {
		if err := sm.Put(llr_key(i), llr_val(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	// Unbacked snapshot: shares the still-dirty tree with the source.
	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	mutable, err := sm.Snapshot(true)
	if err != nil {
		t.Fatalf("Snapshot(mutable): %v", err)
	}

	var wg sync.WaitGroup
	flush := func(m *SHAMap) {
		defer wg.Done()
		if _, err := m.FlushDirty(false); err != nil {
			t.Errorf("FlushDirty: %v", err)
		}
	}
	read := func(m *SHAMap) {
		defer wg.Done()
		if _, err := m.Hash(); err != nil {
			t.Errorf("Hash: %v", err)
		}
		_ = m.ForEach(func(*Item) bool { return true })
	}
	mutate := func(m *SHAMap) {
		defer wg.Done()
		for i := byte(100); i < 110; i++ {
			if err := m.Put(llr_key(i), llr_val(i)); err != nil {
				t.Errorf("Put: %v", err)
			}
		}
	}

	wg.Add(6)
	go flush(sm)
	go flush(snap)
	go read(sm)
	go read(snap)
	go mutate(mutable)
	go flush(mutable)
	wg.Wait()

	h1, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	h2, err := snap.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("source and immutable snapshot hashes diverged: %x vs %x", h1[:8], h2[:8])
	}
}
