package shamap

import (
	"bytes"
	"testing"
)

func TestNewFromRootHashRejectsMismatchedStoredContent(t *testing.T) {
	t.Parallel()
	source := New(TypeState)
	requirePut := func(key [32]byte, data []byte) {
		t.Helper()
		if err := source.Put(key, data); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	requirePut([32]byte{0x10}, bytes.Repeat([]byte{0xA1}, 20))
	requirePut([32]byte{0x20}, bytes.Repeat([]byte{0xB2}, 20))

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	family := newMemoryFamily()
	if err := flushToFamily(source, family); err != nil {
		t.Fatalf("flushToFamily: %v", err)
	}

	wrongHash := rootHash
	wrongHash[0] ^= 0xff
	family.mu.Lock()
	family.store[wrongHash] = bytes.Clone(family.store[rootHash])
	family.mu.Unlock()

	if _, err := NewFromRootHash(TypeState, wrongHash, family); err == nil {
		t.Fatal("expected mismatched root content hash to be rejected")
	}
}

func TestPutItemsAtomicallyRollsBackAfterFirstStagedItem(t *testing.T) {
	t.Parallel()
	sm := New(TypeState)
	if err := sm.Put([32]byte{1}, bytes.Repeat([]byte{0x11}, 20)); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	before, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash before: %v", err)
	}

	added := [32]byte{2}
	err = sm.PutItemsAtomically(
		NewItem(added, bytes.Repeat([]byte{0x22}, 20)),
		nil,
	)
	if err == nil {
		t.Fatal("expected second staged item to fail")
	}
	after, hashErr := sm.Hash()
	if hashErr != nil {
		t.Fatalf("Hash after: %v", hashErr)
	}
	if after != before {
		t.Fatalf("map root changed after failed atomic update: got %x, want %x", after, before)
	}
	if found, hasErr := sm.Has(added); hasErr != nil || found {
		t.Fatalf("first staged item leaked after failure: found=%v err=%v", found, hasErr)
	}
}

func TestBackedTraversalRejectsSubstitutedDescendant(t *testing.T) {
	t.Parallel()
	source := New(TypeState)
	for _, key := range [][32]byte{{0x10}, {0x20}, {0x30}} {
		if err := source.Put(key, bytes.Repeat([]byte{key[0]}, 20)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	family := newMemoryFamily()
	if err := flushToFamily(source, family); err != nil {
		t.Fatalf("flushToFamily: %v", err)
	}

	node, err := deserializeFromPrefix(family.store[rootHash])
	if err != nil {
		t.Fatalf("deserialize root: %v", err)
	}
	root := node.(*innerNode)
	var children [][32]byte
	for branch := 0; branch < BranchFactor; branch++ {
		childHash, hashErr := root.ChildHash(branch)
		if hashErr != nil {
			t.Fatalf("ChildHash: %v", hashErr)
		}
		if childHash != ([32]byte{}) {
			children = append(children, childHash)
		}
	}
	if len(children) < 2 {
		t.Fatalf("need at least two descendants, got %d", len(children))
	}
	family.mu.Lock()
	family.store[children[0]] = bytes.Clone(family.store[children[1]])
	family.mu.Unlock()

	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	if err := backed.ForEach(func(*Item) bool { return true }); err == nil {
		t.Fatal("expected substituted descendant content hash to be rejected")
	}
}
