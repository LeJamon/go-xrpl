package shamap

import (
	"errors"
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

func TestXtra_InnerNodeSetChildDirectAndForEach(t *testing.T) {
	parent := NewInnerNode()
	leaf := NewItem(makeHash(0x33), []byte("leaf-data-1234"))
	child, err := NewAccountStateLeafNode(leaf)
	if err != nil {
		t.Fatalf("NewAccountStateLeafNode: %v", err)
	}

	// Out-of-range indices are no-ops.
	parent.SetChildDirect(-1, child)
	parent.SetChildDirect(BranchFactor, child)
	parent.SetChildDirect(3, child)

	seen := 0
	parent.ForEachChild(func(index int, c Node) bool {
		if index == 3 && c != nil {
			seen++
		}
		return true
	})
	if seen != 1 {
		t.Fatalf("ForEachChild saw %d children at index 3, want 1", seen)
	}

	child2, err := NewAccountStateLeafNode(NewItem(makeHash(0x44), []byte("xxxxxxxxxxxx")))
	if err != nil {
		t.Fatalf("NewAccountStateLeafNode: %v", err)
	}
	parent.SetChildDirect(7, child2)
	count := 0
	parent.ForEachChild(func(index int, c Node) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("ForEachChild with early stop visited %d, want 1", count)
	}
}

func TestXtra_NodeStringRepresentations(t *testing.T) {
	id := NewRootNodeID()

	inner := NewInnerNode()
	if s := inner.String(id); !strings.Contains(s, "InnerNode ID:") {
		t.Fatalf("InnerNode.String = %q", s)
	}

	// Reach the embedded BaseNode.String directly (shadowed by InnerNode.String).
	if s := inner.BaseNode.String(id); !strings.Contains(s, "NodeID:") {
		t.Fatalf("BaseNode.String = %q", s)
	}
}



func TestXtra_ProofPathError(t *testing.T) {
	wrapped := errors.New("inner cause")
	withErr := &ProofPathError{Position: 2, Depth: 3, Message: "bad branch", Err: wrapped}
	if !strings.Contains(withErr.Error(), "inner cause") {
		t.Fatalf("Error should include wrapped cause: %q", withErr.Error())
	}
	if !errors.Is(withErr, wrapped) {
		t.Fatal("Unwrap should expose the wrapped error")
	}

	noErr := &ProofPathError{Position: 1, Depth: 1, Message: "no wrapped"}
	if strings.Contains(noErr.Error(), ":") == false {
		t.Fatalf("Error without wrapped err = %q", noErr.Error())
	}
	if noErr.Unwrap() != nil {
		t.Fatal("Unwrap with no wrapped error should be nil")
	}
}
