package shamap

import (
	"bytes"
	"testing"
)

func TestMutableForkDoesNotFlushBackedMap(t *testing.T) {
	family := newMemoryFamily()
	source, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	key := [32]byte{1}
	original := bytes.Repeat([]byte{1}, 12)
	if err := source.Put(key, original); err != nil {
		t.Fatalf("source Put: %v", err)
	}

	fork, err := source.MutableFork()
	if err != nil {
		t.Fatalf("MutableFork: %v", err)
	}
	if family.Len() != 0 {
		t.Fatalf("MutableFork flushed %d dirty nodes", family.Len())
	}

	updated := bytes.Repeat([]byte{2}, 12)
	if err := fork.Put(key, updated); err != nil {
		t.Fatalf("fork Put: %v", err)
	}
	item, found, err := source.Get(key)
	if err != nil {
		t.Fatalf("source Get: %v", err)
	}
	if !found {
		t.Fatal("source item missing after fork update")
	}
	if !bytes.Equal(item.Data(), original) {
		t.Fatalf("source changed through fork: data=%x", item.Data())
	}
	item, found, err = fork.Get(key)
	if err != nil {
		t.Fatalf("fork Get: %v", err)
	}
	if !found {
		t.Fatal("fork item missing after update")
	}
	if !bytes.Equal(item.Data(), updated) {
		t.Fatalf("fork update missing: data=%x", item.Data())
	}
	if _, err := fork.Snapshot(false); err != nil {
		t.Fatalf("Snapshot fork: %v", err)
	}
	if family.Len() == 0 {
		t.Fatal("snapshot did not persist retained fork")
	}
}
