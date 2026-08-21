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
	batch, err := collectDirtyAndReleaseForTest(sm)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	return sm
}

// TestCompareBackedLazyLoad is a regression test for the lazy-load
// divergence where comparison descended via raw child pointers: on a
// backed map with released children it misclassified entire subtrees as
// added/removed instead of loading them from the store.
func TestCompareBackedLazyLoad(t *testing.T) {
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

	differences, err := m1.CompareContext(context.Background(), m2, 0)
	if err != nil {
		t.Fatalf("CompareContext: %v", err)
	}

	want := map[[32]byte]bool{modifiedKey: true, addedKey: true}
	if differences.Len() != len(want) {
		t.Fatalf("CompareContext on backed maps: got %d differences, want %d", differences.Len(), len(want))
	}
	for _, difference := range differences.Differences {
		if !want[difference.Key] {
			t.Errorf("unexpected difference key %x", difference.Key)
		}
	}
}

// TestFinishSyncBackedLazyLoad ensures the completeness walk lazy-loads a
// fully stored backed map whose children have been released.
func TestFinishSyncBackedLazyLoad(t *testing.T) {
	family := newMemoryFamily()
	sm := llr_buildBacked(t, family, 12)

	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := sm.FinishSync(); err != nil {
		t.Errorf("FinishSync on fully-stored backed map: %v", err)
	}
	if missing := sm.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("walkMap reports %d missing nodes on a fully-stored map", len(missing))
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
	snap, err := sm.SnapshotImmutable()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	mutable, err := sm.SnapshotMutable()
	if err != nil {
		t.Fatalf("Snapshot(mutable): %v", err)
	}

	var wg sync.WaitGroup
	flush := func(m *SHAMap) {
		defer wg.Done()
		if err := m.StoreDirty(func([]FlushEntry) error { return nil }); err != nil {
			t.Errorf("StoreDirty: %v", err)
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
