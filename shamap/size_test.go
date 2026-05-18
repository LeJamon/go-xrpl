package shamap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestSize_EmptyMap(t *testing.T) {
	t.Parallel()
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := sm.Size(); got != 0 {
		t.Errorf("Size on empty map = %d, want 0", got)
	}
}

func TestSize_AfterPutAndDelete(t *testing.T) {
	t.Parallel()
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
		"b92891fe4ef6cee585fdc6fda2e09eb4d386363158ec3321b8123e5a772c6ca8",
	}
	hashes := make([][32]byte, len(keys))
	for i, k := range keys {
		hashes[i] = hexToHash(k)
		if err := sm.PutItem(makeItem(hashes[i], intToBytes(i+1))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	if got := sm.Size(); got != len(keys) {
		t.Errorf("Size after %d puts = %d, want %d", len(keys), got, len(keys))
	}

	if err := sm.Delete(hashes[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := sm.Size(); got != len(keys)-1 {
		t.Errorf("Size after delete = %d, want %d", got, len(keys)-1)
	}
}

func TestSize_CachedWhenImmutable(t *testing.T) {
	t.Parallel()
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for i := 0; i < 5; i++ {
		k := [32]byte{byte(i)}
		if err := sm.PutItem(makeItem(k, intToBytes(i))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	if v := sm.cachedSize.Load(); v != -1 {
		t.Fatalf("mutable map cachedSize = %d, want -1", v)
	}
	if got := sm.Size(); got != 5 {
		t.Errorf("Size = %d, want 5", got)
	}
	if v := sm.cachedSize.Load(); v != -1 {
		t.Errorf("after mutable Size cachedSize = %d, want -1 (no caching for mutable maps)", v)
	}

	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}

	if got := sm.Size(); got != 5 {
		t.Errorf("immutable Size = %d, want 5", got)
	}
	if v := sm.cachedSize.Load(); v != 5 {
		t.Errorf("after immutable Size cachedSize = %d, want 5", v)
	}

	// Overwrite the cache with a sentinel: Size returning it proves the
	// walk is skipped on subsequent calls.
	sm.cachedSize.Store(999)
	if got := sm.Size(); got != 999 {
		t.Errorf("cached Size = %d, want 999 (cache not consulted)", got)
	}
}

func TestSize_SnapshotInheritsImmutableCache(t *testing.T) {
	t.Parallel()
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for i := 0; i < 3; i++ {
		k := [32]byte{byte(i)}
		if err := sm.PutItem(makeItem(k, intToBytes(i))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	_ = sm.Size() // prime parent cache
	if v := sm.cachedSize.Load(); v != 3 {
		t.Fatalf("parent cachedSize = %d, want 3", v)
	}

	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if v := snap.cachedSize.Load(); v != 3 {
		t.Errorf("immutable snapshot cachedSize = %d, want 3 (inheritance)", v)
	}

	// Mutable snapshot must not inherit: callers may mutate it.
	mut, err := sm.Snapshot(true)
	if err != nil {
		t.Fatalf("snapshot mutable: %v", err)
	}
	if v := mut.cachedSize.Load(); v != -1 {
		t.Errorf("mutable snapshot cachedSize = %d, want -1", v)
	}
}

// failingFamily forces Fetch to fail after the first N successful calls,
// simulating a NodeStore blip mid-traversal.
type failingFamily struct {
	inner     Family
	failAfter int32
	calls     atomic.Int32
}

func (f *failingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.calls.Add(1) > f.failAfter {
		return nil, errors.New("simulated fetch failure")
	}
	return f.inner.Fetch(ctx, hash)
}

func (f *failingFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.inner.StoreBatch(ctx, entries)
}

func TestSize_DoesNotCacheOnWalkError(t *testing.T) {
	t.Parallel()
	mem := NewMemoryFamily()
	sm, err := NewBacked(TypeState, mem)
	if err != nil {
		t.Fatalf("new backed: %v", err)
	}
	// Diverse keys → internal branching, so Size's walk descends through
	// multiple inner nodes (each a potential fetch).
	keys := [][32]byte{
		{0x00, 0x11}, {0x10, 0x22}, {0x20, 0x33}, {0x30, 0x44},
		{0x40, 0x55}, {0x50, 0x66}, {0x60, 0x77}, {0x70, 0x88},
	}
	for i, k := range keys {
		if err := sm.PutItem(makeItem(k, intToBytes(i+1))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	// FlushDirty(true) drops in-memory children, so any subsequent
	// descend() goes through Family.Fetch.
	batch, err := sm.FlushDirty(true)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := mem.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("storebatch: %v", err)
	}

	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}

	sm.SetFamily(&failingFamily{inner: mem, failAfter: 1})

	_ = sm.Size()
	if v := sm.cachedSize.Load(); v != -1 {
		t.Errorf("after errored walk cachedSize = %d, want -1 (partial counts must not be cached)", v)
	}

	// With a working family restored, the next Size() must re-walk and
	// cache the true count — proving the prior error didn't poison the map.
	sm.SetFamily(mem)
	if got := sm.Size(); got != len(keys) {
		t.Errorf("post-recovery Size = %d, want %d", got, len(keys))
	}
	if v := sm.cachedSize.Load(); v != int64(len(keys)) {
		t.Errorf("post-recovery cachedSize = %d, want %d", v, len(keys))
	}
}

func TestSize_BackedSnapshotInheritsImmutableCache(t *testing.T) {
	t.Parallel()
	mem := NewMemoryFamily()
	sm, err := NewBacked(TypeState, mem)
	if err != nil {
		t.Fatalf("new backed: %v", err)
	}
	for i := 0; i < 4; i++ {
		k := [32]byte{byte(i * 0x11)}
		if err := sm.PutItem(makeItem(k, intToBytes(i+1))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	if got := sm.Size(); got != 4 {
		t.Fatalf("source Size = %d, want 4", got)
	}
	if v := sm.cachedSize.Load(); v != 4 {
		t.Fatalf("source cachedSize = %d, want 4", v)
	}

	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if v := snap.cachedSize.Load(); v != 4 {
		t.Errorf("backed immutable snapshot cachedSize = %d, want 4 (inheritance)", v)
	}

	// Mutable snapshot must not inherit: callers may mutate it.
	mut, err := sm.Snapshot(true)
	if err != nil {
		t.Fatalf("snapshot mutable: %v", err)
	}
	if v := mut.cachedSize.Load(); v != -1 {
		t.Errorf("backed mutable snapshot cachedSize = %d, want -1", v)
	}
}
