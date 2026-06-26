package shamap

import (
	"context"
	"testing"
)

// buildBackedStateMap puts the given keys into a fresh state map, flushes it to
// family, and returns the root hash plus a backed map reconstructed from it.
func buildBackedStateMap(t *testing.T, family *memoryFamily, keys []string) ([32]byte, *SHAMap) {
	t.Helper()
	sMap := New(TypeState)
	for i, keyHex := range keys {
		if err := sMap.Put(hexToHash(keyHex), intToBytes(i+1)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	rootHash, err := sMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}
	return rootHash, backed
}

func TestCheckComplete_BackedMapFullyPresent(t *testing.T) {
	family := newMemoryFamily()
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}
	_, backed := buildBackedStateMap(t, family, keys)

	res, err := backed.CheckComplete(context.Background())
	if err != nil {
		t.Fatalf("CheckComplete: %v", err)
	}
	if !res.Complete() {
		t.Errorf("expected complete tree, got %d missing: %+v", len(res.Missing), res.Missing)
	}
	if res.LeafNodes != len(keys) {
		t.Errorf("expected %d leaves, got %d", len(keys), res.LeafNodes)
	}
	if res.InnerNodes < 1 {
		t.Errorf("expected at least the root inner node, got %d", res.InnerNodes)
	}
}

func TestCheckComplete_DetectsMissingNode(t *testing.T) {
	family := newMemoryFamily()
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}
	rootHash, _ := buildBackedStateMap(t, family, keys)

	// Induce a missing node: delete one stored node that is NOT the root, so
	// the root still deserializes but a referenced child is unresolvable.
	var deleted [32]byte
	family.mu.Lock()
	for h := range family.store {
		if h != rootHash {
			delete(family.store, h)
			deleted = h
			break
		}
	}
	family.mu.Unlock()

	// Reconstruct fresh so no children are cached in memory from a prior walk.
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	res, err := backed.CheckComplete(context.Background())
	if err != nil {
		t.Fatalf("CheckComplete: %v", err)
	}
	if res.Complete() {
		t.Fatal("expected an incomplete tree after deleting a node")
	}
	if len(res.Corrupt) != 0 {
		t.Errorf("a deleted (absent) node must not be reported as corrupt: %+v", res.Corrupt)
	}
	found := false
	for _, m := range res.Missing {
		if m.Hash == deleted {
			found = true
		}
	}
	if !found {
		t.Errorf("missing-node report %+v did not include the deleted node %x", res.Missing, deleted[:8])
	}
}

func TestCheckComplete_DetectsCorruptNode(t *testing.T) {
	family := newMemoryFamily()
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
	}
	rootHash, _ := buildBackedStateMap(t, family, keys)

	// Corrupt one non-root node: keep the key, replace the bytes with garbage
	// that DeserializeFromPrefix rejects.
	var corrupted [32]byte
	family.mu.Lock()
	for h := range family.store {
		if h != rootHash {
			family.store[h] = []byte{0xde, 0xad, 0xbe, 0xef}
			corrupted = h
			break
		}
	}
	family.mu.Unlock()

	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}
	res, err := backed.CheckComplete(context.Background())
	if err != nil {
		t.Fatalf("CheckComplete: %v", err)
	}
	if res.Complete() {
		t.Fatal("expected an incomplete tree after corrupting a node")
	}
	corruptReported := false
	for _, m := range res.Corrupt {
		if m.Hash == corrupted {
			corruptReported = true
		}
	}
	if !corruptReported {
		t.Errorf("expected corrupt node %x flagged, got missing=%+v corrupt=%+v", corrupted[:8], res.Missing, res.Corrupt)
	}
}

func TestCheckComplete_UnbackedMapIsComplete(t *testing.T) {
	sMap := New(TypeState)
	if err := sMap.Put(hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7"), intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	res, err := sMap.CheckComplete(context.Background())
	if err != nil {
		t.Fatalf("CheckComplete: %v", err)
	}
	if !res.Complete() {
		t.Errorf("unbacked in-memory map must always be complete, got %+v", res.Missing)
	}
	if res.LeafNodes != 1 {
		t.Errorf("expected 1 leaf, got %d", res.LeafNodes)
	}
}

func TestCheckComplete_EmptyMap(t *testing.T) {
	sMap := New(TypeState)
	res, err := sMap.CheckComplete(context.Background())
	if err != nil {
		t.Fatalf("CheckComplete: %v", err)
	}
	if !res.Complete() || res.LeafNodes != 0 {
		t.Errorf("empty map should be complete with no leaves, got %+v", res)
	}
}
