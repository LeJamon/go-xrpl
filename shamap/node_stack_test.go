package shamap

import "testing"

func TestSme_NodeStackClear(t *testing.T) {
	ns := newNodeStack()

	inner := newInnerNode()
	id := newRootNodeID()
	ns.Push(inner, id)

	if ns.Len() != 1 {
		t.Errorf("Len after single Push = %d, want 1", ns.Len())
	}

	ns.Clear()
	if !ns.IsEmpty() {
		t.Error("IsEmpty should be true after Clear")
	}
	if ns.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", ns.Len())
	}
}

func TestSme_NodeStackPopEmpty(t *testing.T) {
	ns := newNodeStack()
	_, _, ok := ns.Pop()
	if ok {
		t.Error("Pop on empty nodeStack should return ok=false")
	}
}
