package shamap

import (
	"errors"
	"testing"
)

func TestLazyFetchRejectsChildHashMismatch(t *testing.T) {
	family := newMemoryFamily()
	source, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	first := [32]byte{0x10}
	second := [32]byte{0x80}
	if err := source.Put(first, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}); err != nil {
		t.Fatal(err)
	}
	if err := source.Put(second, []byte{12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}); err != nil {
		t.Fatal(err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(source, family); err != nil {
		t.Fatal(err)
	}
	firstHash, err := source.tree.root.ChildHash(1)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := source.tree.root.ChildHash(8)
	if err != nil {
		t.Fatal(err)
	}

	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}
	family.mu.Lock()
	family.store[firstHash] = append([]byte(nil), family.store[secondHash]...)
	family.mu.Unlock()

	_, _, err = backed.Get(first)
	if !errors.Is(err, ErrInvalidNodeData) {
		t.Fatalf("Get error = %v, want ErrInvalidNodeData", err)
	}
}
