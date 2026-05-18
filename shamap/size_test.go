package shamap

import (
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

	// Re-put an existing key: count stays.
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

	// Before SetImmutable the cache stays uncached.
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

	// First post-immutable Size primes the cache.
	if got := sm.Size(); got != 5 {
		t.Errorf("immutable Size = %d, want 5", got)
	}
	if v := sm.cachedSize.Load(); v != 5 {
		t.Errorf("after immutable Size cachedSize = %d, want 5", v)
	}

	// Subsequent calls read the cache (we can't directly observe that the
	// walk is skipped from the test surface, but corrupting the root and
	// confirming Size still reports 5 proves the cache is consulted).
	sm.root = nil
	if got := sm.Size(); got != 5 {
		t.Errorf("cached Size = %d, want 5 (cache not consulted)", got)
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

	// Immutable→immutable snapshot inherits the cached count.
	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if v := snap.cachedSize.Load(); v != 3 {
		t.Errorf("immutable snapshot cachedSize = %d, want 3 (inheritance)", v)
	}

	// Mutable snapshot does NOT inherit the cache (callers may mutate it).
	mut, err := sm.Snapshot(true)
	if err != nil {
		t.Fatalf("snapshot mutable: %v", err)
	}
	if v := mut.cachedSize.Load(); v != -1 {
		t.Errorf("mutable snapshot cachedSize = %d, want -1", v)
	}
}
