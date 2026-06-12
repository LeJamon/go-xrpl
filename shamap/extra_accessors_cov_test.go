package shamap

import (
	"strings"
	"testing"
)

func TestXtra_ItemSizeStringIsEmpty(t *testing.T) {
	it := NewItem(makeHash(0x11), []byte("abcd"))
	if it.Size() != 4 {
		t.Fatalf("Size = %d, want 4", it.Size())
	}
	if s := it.String(); !strings.HasPrefix(s, "Item(key=") {
		t.Fatalf("unexpected String: %q", s)
	}
	if it.IsEmpty() {
		t.Fatal("item with data should not be empty")
	}

	empty := NewItem(makeHash(0x22), nil)
	if empty.Size() != 0 || !empty.IsEmpty() {
		t.Fatal("item with no data should be empty with size 0")
	}

	var nilItem *Item
	if !nilItem.IsEmpty() {
		t.Fatal("nil item should report empty")
	}
	if s := nilItem.String(); s != "Item(nil)" {
		t.Fatalf("nil item String = %q, want Item(nil)", s)
	}
}

func TestXtra_NodeStringRepresentations(t *testing.T) {
	id := NewRootNodeID()

	inner := newInnerNode()
	if s := inner.String(id); !strings.Contains(s, "innerNode ID:") {
		t.Fatalf("innerNode.String = %q", s)
	}

	// Reach the embedded baseNode.String directly (shadowed by innerNode.String).
	if s := inner.baseNode.String(id); !strings.Contains(s, "NodeID:") {
		t.Fatalf("baseNode.String = %q", s)
	}
}
