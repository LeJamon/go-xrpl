package shamap

import "testing"

func TestSme_GetNodeFatByPath(t *testing.T) {
	sm := New(TypeState)
	for i := byte(1); i <= 8; i++ {
		k := sme_keyFromByte(i << 4)
		if err := sm.Put(k, sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	nilSm := New(TypeState)
	nilSm.tree.mu.Lock()
	nilSm.tree.root = nil
	nilSm.tree.mu.Unlock()
	nodes, err := nilSm.GetNodeFatByPath([32]byte{}, 0, 1, true)
	if err != nil || nodes != nil {
		t.Errorf("GetNodeFatByPath with nil root: nodes=%v err=%v", nodes, err)
	}

	nodes, err = sm.GetNodeFatByPath([32]byte{}, 0, 1, true)
	if err != nil {
		t.Fatalf("GetNodeFatByPath: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("expected at least the root node returned")
	}

	nodes, err = sm.GetNodeFatByPath([32]byte{0xFF}, 64, 1, false)
	if err != nil || nodes != nil {
		t.Errorf("GetNodeFatByPath nonexistent deep path: nodes=%v err=%v", nodes, err)
	}
}

func TestSme_PathPrefixEq(t *testing.T) {
	var a, b [32]byte
	if !pathPrefixEq(a, b, 0) {
		t.Error("pathPrefixEq(0) should be true for equal arrays")
	}
	a[1] = 0x0F
	if pathPrefixEq(a, b, 4) {
		t.Error("pathPrefixEq(4) should be false when nibble 3 differs")
	}
	if !pathPrefixEq(a, b, 3) {
		t.Error("pathPrefixEq(3) should be true when only nibble 3 differs")
	}
}

func TestSme_WalkWireNodes(t *testing.T) {
	sm := New(TypeTransaction)
	for i := byte(0); i < 4; i++ {
		k := sme_keyFromTwo(i<<4, 0x00)
		if err := sm.Put(k, append(sme_data12(i), make([]byte, 2)...)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	nodes, err := sm.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("WalkWireNodes should return at least one node")
	}
	for i, n := range nodes {
		if len(n.NodeID) != 33 {
			t.Errorf("node %d: NodeID length = %d, want 33", i, len(n.NodeID))
		}
	}
}

func TestSme_walkMapNilAndInvalidRoot(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.root = nil
	sm.tree.mu.Unlock()
	if got := sm.walkMap(0, nil); got != nil {
		t.Errorf("walkMap nil root: want nil, got %v", got)
	}
	if got := sm.walkMapParallel(0, nil); got != nil {
		t.Errorf("walkMapParallel nil root: want nil, got %v", got)
	}

	sm2 := New(TypeState)
	sm2.tree.mu.Lock()
	sm2.tree.state = stateInvalid
	sm2.tree.mu.Unlock()
	if got := sm2.walkMap(0, nil); got != nil {
		t.Errorf("walkMap invalid state: want nil, got %v", got)
	}
	if got := sm2.walkMapParallel(0, nil); got != nil {
		t.Errorf("walkMapParallel invalid state: want nil, got %v", got)
	}
}
