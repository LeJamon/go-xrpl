package shamap

import (
	"context"
	"sync"
	"testing"
	"time"
)

type snapshotBlockingFamily struct {
	*memoryFamily
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *snapshotBlockingFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return f.memoryFamily.StoreBatch(ctx, entries)
	}
}

func TestSme_SnapshotOnInvalidReturnsError(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.state = stateInvalid
	sm.tree.mu.Unlock()
	if _, err := sm.SnapshotImmutable(); err == nil {
		t.Error("Snapshot on invalid map should return error")
	}
}

func TestSme_MutableSnapshot(t *testing.T) {
	sm := New(TypeState)
	k1 := sme_keyFromByte(0x10)
	k2 := sme_keyFromByte(0x20)
	if err := sm.Put(k1, sme_data12(1)); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	origHash, _ := sm.Hash()

	snap, err := sm.SnapshotMutable()
	if err != nil {
		t.Fatalf("Snapshot(mutable): %v", err)
	}
	if snap.tree.state != stateModifying {
		t.Errorf("mutable snapshot state = %v, want modifying", snap.tree.state)
	}
	if err := snap.Put(k2, sme_data12(2)); err != nil {
		t.Fatalf("Put on mutable snapshot: %v", err)
	}
	smHash, _ := sm.Hash()
	if smHash != origHash {
		t.Error("original hash changed after mutating mutable snapshot")
	}
	_, ok, err := snap.Get(k2)
	if err != nil || !ok {
		t.Errorf("k2 not in snapshot: ok=%v err=%v", ok, err)
	}
}

func TestSme_ImmutableSnapshotCachesSize(t *testing.T) {
	sm := New(TypeState)
	for i := 0; i < 5; i++ {
		if err := sm.Put(sme_keyFromByte(byte(i+1)), sme_data12(byte(i))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	sz1 := sm.Size()
	if sz1 != 5 {
		t.Errorf("Size = %d, want 5", sz1)
	}
	snap, err := sm.SnapshotImmutable()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Size() != 5 {
		t.Errorf("snapshot.Size() = %d, want 5", snap.Size())
	}
}

func TestSme_BackedSnapshotFlushes(t *testing.T) {
	family := newMemoryFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	k := sme_keyFromByte(0x10)
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	snap, err := sm.SnapshotImmutable()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if family.Len() == 0 {
		t.Error("backed Snapshot should flush dirty nodes to family")
	}
	smHash, _ := sm.Hash()
	snapHash, _ := snap.Hash()
	if smHash != snapHash {
		t.Errorf("snap hash mismatch: sm=%x snap=%x", smHash[:4], snapHash[:4])
	}
}

func TestSnapshotPinsFamilyAcrossFlush(t *testing.T) {
	first := &snapshotBlockingFamily{
		memoryFamily: newMemoryFamily(),
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	second := newMemoryFamily()
	sm, err := NewBacked(TypeState, first)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	key := sme_keyFromByte(0x40)
	if err := sm.Put(key, sme_data12(4)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	type snapshotResult struct {
		snapshot *SHAMap
		err      error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		snapshot, snapshotErr := sm.SnapshotImmutable()
		snapshotDone <- snapshotResult{snapshot: snapshot, err: snapshotErr}
	}()
	<-first.entered

	setFamilyStarted := make(chan struct{})
	setFamilyDone := make(chan struct{})
	go func() {
		close(setFamilyStarted)
		sm.SetFamily(second)
		close(setFamilyDone)
	}()
	<-setFamilyStarted
	select {
	case <-setFamilyDone:
		t.Fatal("SetFamily overtook an in-flight snapshot flush")
	case <-time.After(100 * time.Millisecond):
	}

	close(first.release)
	result := <-snapshotDone
	if result.err != nil {
		t.Fatalf("SnapshotImmutable: %v", result.err)
	}
	<-setFamilyDone

	result.snapshot.backing.mu.RLock()
	snapshotFamily := result.snapshot.backing.access.Family
	result.snapshot.backing.mu.RUnlock()
	if snapshotFamily != first {
		t.Fatalf("snapshot family = %T, want %T", snapshotFamily, first)
	}
	rootHash, err := result.snapshot.Hash()
	if err != nil {
		t.Fatalf("snapshot Hash: %v", err)
	}
	reloaded, err := NewFromRootHash(TypeState, rootHash, first)
	if err != nil {
		t.Fatalf("reload snapshot from flushed family: %v", err)
	}
	if _, found, getErr := reloaded.Get(key); getErr != nil || !found {
		t.Fatalf("reloaded Get: found=%v err=%v", found, getErr)
	}
}
