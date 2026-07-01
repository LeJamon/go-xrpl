package shamap

import (
	"strings"
	"testing"
)

// buildStoredTree flushes a small multi-node tree into a memory family and
// returns its root hash + serialized root.
func buildStoredTree(t *testing.T, mem *memoryFamily) ([32]byte, []byte) {
	t.Helper()
	src := New(TypeState)
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
		"f22891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca9",
	}
	for i, keyHex := range keys {
		if err := src.Put(hexToHash(keyHex), intToBytes(i+1)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if err := flushToFamily(src, mem); err != nil {
		t.Fatalf("flush: %v", err)
	}
	rootHash, err := src.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := src.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	return rootHash, rootData
}

// The #1161 phantom-missing wedge: a transient family fetch failure during the
// completeness walk must surface as itself — never as "sync incomplete: still
// have N missing nodes", which the acquisition would answer by re-requesting
// over the wire nodes it already has.
func TestFinishSync_TransientFetchError_IsNotPhantomMissing(t *testing.T) {
	mem := newMemoryFamily()
	rootHash, rootData := buildStoredTree(t, mem)

	// Cold map over a family that fails every fetch: the whole tree IS in the
	// store, only the reads blip.
	failing := &failingFamily{inner: mem, failAfter: 0}
	dest, err := NewBacked(TypeState, failing)
	if err != nil {
		t.Fatalf("new backed dest: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	err = dest.FinishSync()
	if err == nil {
		t.Fatal("FinishSync must fail while the store is erroring")
	}
	if strings.Contains(err.Error(), "sync incomplete") {
		t.Fatalf("transient fetch error reported as phantom missing nodes: %v", err)
	}
	if dest.IsComplete() {
		t.Fatal("IsComplete must be conservatively false on a transient store error")
	}

	// Once the store recovers, the same map completes from local data alone.
	failing.failAfter = 1 << 30
	failing.calls.Store(0)
	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync after store recovery: %v", err)
	}
	if !dest.IsComplete() {
		t.Fatal("map must be complete once the store recovers")
	}
}

// A genuine store miss keeps the existing semantics on both paths: the strict
// completeness walk reports missing nodes and the lenient request path
// surfaces them for wire fetch.
func TestFinishSync_TrueMiss_StillReportsMissing(t *testing.T) {
	mem := newMemoryFamily()
	rootHash, rootData := buildStoredTree(t, mem)

	empty := newMemoryFamily() // nothing below the root is in this store
	dest, err := NewBacked(TypeState, empty)
	if err != nil {
		t.Fatalf("new backed dest: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	err = dest.FinishSync()
	if err == nil || !strings.Contains(err.Error(), "sync incomplete") {
		t.Fatalf("true miss must report sync incomplete, got: %v", err)
	}
	if got := dest.GetMissingNodes(0, nil); len(got) == 0 {
		t.Fatal("request path must surface genuinely missing nodes")
	}
}
