package shamap

import (
	"bytes"
	"context"
	"testing"
)

func TestRootReaderRetainedItemsStayOwnedAcrossReads(t *testing.T) {
	family := newMemoryFamily()
	sm := New(TypeState)
	keys := [][32]byte{commitmentTestKey(1), commitmentTestKey(2), commitmentTestKey(3)}
	want := make(map[[32]byte][]byte, len(keys))
	for i, key := range keys {
		data := commitmentTestData(byte(i + 1))
		want[key] = bytes.Clone(data)
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("Put(%x): %v", key[:1], err)
		}
	}
	if err := flushToFamily(sm, family); err != nil {
		t.Fatalf("flush: %v", err)
	}
	root, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	reader := NewRootReader(TypeState, root, family)

	first, _, found, err := reader.GetLeaf(context.Background(), keys[0])
	if err != nil || !found {
		t.Fatalf("GetLeaf: found=%v err=%v", found, err)
	}
	mutated := first.Data()
	mutated[0] ^= 0xff
	if got := first.Data(); !bytes.Equal(got, want[keys[0]]) {
		t.Fatalf("GetLeaf item changed through Data result: got %x want %x", got, want[keys[0]])
	}

	retained := []*Item{first}
	if err := reader.IterateLeaves(context.Background(), nil, func(item *Item, _ NodeType) bool {
		retained = append(retained, item)
		return true
	}); err != nil {
		t.Fatalf("IterateLeaves: %v", err)
	}
	for _, key := range keys {
		if _, _, found, err := reader.GetLeaf(context.Background(), key); err != nil || !found {
			t.Fatalf("second GetLeaf(%x): found=%v err=%v", key[:1], found, err)
		}
	}
	for _, item := range retained {
		key := item.Key()
		got, ok := want[key]
		if !ok || !bytes.Equal(item.Data(), got) {
			t.Fatalf("retained item %x changed: got %x want %x", key[:1], item.Data(), got)
		}
	}
}
