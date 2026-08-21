package shamap

import (
	"errors"
	"testing"
)

var nid_zeroKey [32]byte

var nid_fullKey = makeHash(0xFF)

var nid_gradientKey = func() [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	return k
}()

// nodeID is a test-only replacement for the removed NewNodeID.
func nodeID(depth uint8, id [32]byte) (NodeID, error) {
	return createNodeID(depth, id)
}

func TestNid_NewNodeID_ValidDepths(t *testing.T) {
	for _, depth := range []uint8{0, 1, 31, 32, 63, maxDepth} {
		nid, err := nodeID(depth, nid_zeroKey)
		if err != nil {
			t.Errorf("depth %d: unexpected error: %v", depth, err)
			continue
		}
		if nid.Depth() != depth {
			t.Errorf("depth %d: got Depth() = %d", depth, nid.Depth())
		}
	}
}

func TestNid_RootNodeID(t *testing.T) {
	root := newRootNodeID()
	if !root.IsRoot() {
		t.Fatal("root NodeID should report itself as root")
	}
	if root.Depth() != 0 {
		t.Fatalf("root depth should be 0, got %d", root.Depth())
	}
	if root.ID() != nid_zeroKey {
		t.Fatal("root ID should be all zeros")
	}
}

func TestNid_CreateNodeID_MasksIrrelevantBits(t *testing.T) {
	// depth=1: only high nibble of byte[0] is relevant; everything else zeroed.
	key := makeHash(0xFF)
	nid, err := createNodeID(1, key)
	if err != nil {
		t.Fatal(err)
	}
	id := nid.ID()
	// byte[0] should have low nibble cleared → 0xF0
	if id[0] != 0xF0 {
		t.Errorf("byte[0] = %02X, want 0xF0", id[0])
	}
	for i := 1; i < 32; i++ {
		if id[i] != 0 {
			t.Errorf("byte[%d] = %02X, want 0x00", i, id[i])
		}
	}
}

func TestNid_CreateNodeID_MaxDepth(t *testing.T) {
	// At the maximum depth no masking occurs; all bytes are preserved.
	nid, err := createNodeID(maxDepth, nid_fullKey)
	if err != nil {
		t.Fatal(err)
	}
	if nid.ID() != nid_fullKey {
		t.Fatal("at maximum depth all bytes should be preserved")
	}
}

func TestNid_CreateNodeID_ExceedsMaxDepth(t *testing.T) {
	_, err := createNodeID(maxDepth+1, nid_zeroKey)
	if !errors.Is(err, errMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestNid_CreateNodeID_EvenDepth(t *testing.T) {
	// depth=2: bytes beyond byte[0] (index ≥1) should be zeroed, byte[0] fully preserved.
	key := makeHash(0xAB)
	nid, err := createNodeID(2, key)
	if err != nil {
		t.Fatal(err)
	}
	id := nid.ID()
	if id[0] != 0xAB {
		t.Errorf("byte[0] = %02X, want 0xAB", id[0])
	}
	for i := 1; i < 32; i++ {
		if id[i] != 0 {
			t.Errorf("byte[%d] = %02X, want 0x00", i, id[i])
		}
	}
}

func TestNid_ChildNodeID_ValidBranches(t *testing.T) {
	root := newRootNodeID()
	for branch := uint8(0); branch <= 15; branch++ {
		child, err := root.childNodeID(branch)
		if err != nil {
			t.Errorf("branch %d: unexpected error: %v", branch, err)
			continue
		}
		if child.Depth() != 1 {
			t.Errorf("branch %d: child depth = %d, want 1", branch, child.Depth())
		}
		// High nibble of byte[0] should equal branch.
		if child.ID()[0]>>4 != branch {
			t.Errorf("branch %d: id[0]>>4 = %d", branch, child.ID()[0]>>4)
		}
	}
}

func TestNid_ChildNodeID_InvalidBranch(t *testing.T) {
	root := newRootNodeID()
	_, err := root.childNodeID(16)
	if err == nil {
		t.Fatal("expected error for branch > 15")
	}
}

func TestNid_ChildNodeID_AtMaxDepth(t *testing.T) {
	nid, _ := nodeID(maxDepth, nid_zeroKey)
	_, err := nid.childNodeID(0)
	if !errors.Is(err, errMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestNid_ChildNodeID_LowNibble(t *testing.T) {
	// From depth=1, branch should go into the low nibble of byte[0].
	parent, _ := nodeID(1, nid_zeroKey)
	child, err := parent.childNodeID(7)
	if err != nil {
		t.Fatal(err)
	}
	if child.ID()[0]&0x0F != 7 {
		t.Errorf("low nibble = %d, want 7", child.ID()[0]&0x0F)
	}
	if child.Depth() != 2 {
		t.Errorf("depth = %d, want 2", child.Depth())
	}
}

func TestNid_SelectBranch_Root(t *testing.T) {
	root := newRootNodeID()
	var key [32]byte
	key[0] = 0xB0
	branch := selectBranch(root, key)
	if branch != 0x0B {
		t.Errorf("branch = %d, want 11", branch)
	}
}

func TestNid_SelectBranch_OddDepth(t *testing.T) {
	nid, _ := nodeID(1, nid_zeroKey)
	var key [32]byte
	key[0] = 0x0C
	branch := selectBranch(nid, key)
	if branch != 0x0C {
		t.Errorf("branch = %d, want 12", branch)
	}
}

func TestNid_SelectBranch_AtMaxDepth(t *testing.T) {
	nid, _ := nodeID(maxDepth, nid_zeroKey)
	// should return 0 as the guard
	branch := selectBranch(nid, nid_fullKey)
	if branch != 0 {
		t.Errorf("branch = %d, want 0", branch)
	}
}

func TestNid_BytesParseRoundtrip(t *testing.T) {
	nid, _ := nodeID(3, nid_gradientKey)
	b := nid.Bytes()
	decoded, err := ParseNodeID(b)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != nid {
		t.Fatal("NodeID binary round-trip failed")
	}
}

func TestNid_UnmarshalBinary_WrongLength(t *testing.T) {
	for _, badLen := range []int{0, 1, 32, 34, 100} {
		_, err := ParseNodeID(make([]byte, badLen))
		if !errors.Is(err, errInvalidNodeIDLength) {
			t.Errorf("len=%d: expected ErrInvalidNodeIDLength, got %v", badLen, err)
		}
	}
}

func TestNid_UnmarshalBinary_ExceedsMaxDepth(t *testing.T) {
	data := make([]byte, NodeIDSize)
	data[32] = maxDepth + 1
	_, err := ParseNodeID(data)
	if !errors.Is(err, errMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestNid_ParseNodeID_RejectsNonCanonicalPath(t *testing.T) {
	data := make([]byte, NodeIDSize)
	data[0] = 0x1F
	data[32] = 1
	_, err := ParseNodeID(data)
	if !errors.Is(err, errNonCanonicalNodeID) {
		t.Fatalf("expected ErrNonCanonicalNodeID, got %v", err)
	}
}

func nid_makeData(n int) []byte {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i)
	}
	return d
}

func TestNid_AccountStateLeafNode_Basic(t *testing.T) {
	key := makeHash(0x11)
	item := NewItem(key, nid_makeData(32))
	leaf, err := newAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mapNode(leaf).(mapLeaf); !ok {
		t.Error("leaf should implement mapLeaf")
	}
	if leaf.Item() == nil {
		t.Error("Item() should not be nil")
	}
	if leaf.Type() != NodeTypeAccountState {
		t.Errorf("Type() = %v, want NodeTypeAccountState", leaf.Type())
	}
}

func TestNid_AccountStateLeafNode_NilItem(t *testing.T) {
	_, err := newAccountStateLeafNode(nil)
	if !errors.Is(err, errNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_AccountStateLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0x22)
	item := NewItem(key, []byte("short"))
	_, err := newAccountStateLeafNode(item)
	if !errors.Is(err, errItemTooSmall) {
		t.Fatalf("expected ErrItemTooSmall, got %v", err)
	}
}

func TestNid_AccountStateLeafNode_SerializeForWire(t *testing.T) {
	key := makeHash(0x77)
	data := nid_makeData(32)
	item := NewItem(key, data)
	leaf, _ := newAccountStateLeafNode(item)
	wire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) == 0 {
		t.Error("SerializeForWire should return non-empty bytes")
	}
}

func TestNid_AccountStateLeafNode_SerializeWithPrefix(t *testing.T) {
	key := makeHash(0x88)
	data := nid_makeData(32)
	item := NewItem(key, data)
	leaf, _ := newAccountStateLeafNode(item)
	prefixed, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixed) == 0 {
		t.Error("SerializeWithPrefix should return non-empty bytes")
	}
}

func TestNid_TransactionLeafNode_Basic(t *testing.T) {
	key := makeHash(0xBB)
	item := NewItem(key, nid_makeData(32))
	leaf, err := newTransactionLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Type() != NodeTypeTransactionNoMeta {
		t.Errorf("Type() = %v, want NodeTypeTransactionNoMeta", leaf.Type())
	}
	if leaf.Item() == nil {
		t.Error("Item() should not be nil")
	}
}

func TestNid_TransactionLeafNode_NilItem(t *testing.T) {
	_, err := newTransactionLeafNode(nil)
	if !errors.Is(err, errNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_TransactionLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0xCC)
	item := NewItem(key, []byte("tiny"))
	_, err := newTransactionLeafNode(item)
	if !errors.Is(err, errItemTooSmall) {
		t.Fatalf("expected ErrItemTooSmall, got %v", err)
	}
}

func TestNid_TransactionLeafNode_SerializeForWire(t *testing.T) {
	key := makeHash(0x13)
	item := NewItem(key, nid_makeData(32))
	leaf, _ := newTransactionLeafNode(item)
	wire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) == 0 {
		t.Error("SerializeForWire should return non-empty bytes")
	}
}

func TestNid_TransactionLeafNode_SerializeWithPrefix(t *testing.T) {
	key := makeHash(0x14)
	item := NewItem(key, nid_makeData(32))
	leaf, _ := newTransactionLeafNode(item)
	p, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) == 0 {
		t.Error("SerializeWithPrefix should return non-empty bytes")
	}
}

func TestNid_TransactionWithMetaLeafNode_Basic(t *testing.T) {
	key := makeHash(0x21)
	item := NewItem(key, nid_makeData(32))
	leaf, err := newTransactionWithMetaLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Type() != NodeTypeTransactionWithMeta {
		t.Errorf("Type() = %v, want NodeTypeTransactionWithMeta", leaf.Type())
	}
	if leaf.Item() == nil {
		t.Error("Item() should not be nil")
	}
}

func TestNid_TransactionWithMetaLeafNode_NilItem(t *testing.T) {
	_, err := newTransactionWithMetaLeafNode(nil)
	if !errors.Is(err, errNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_TransactionWithMetaLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0x22)
	item := NewItem(key, []byte("too short"))
	_, err := newTransactionWithMetaLeafNode(item)
	if !errors.Is(err, errItemTooSmall) {
		t.Fatalf("expected ErrItemTooSmall, got %v", err)
	}
}

func TestNid_TransactionWithMetaLeafNode_SerializeForWire(t *testing.T) {
	key := makeHash(0x27)
	item := NewItem(key, nid_makeData(32))
	leaf, _ := newTransactionWithMetaLeafNode(item)
	wire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) == 0 {
		t.Error("SerializeForWire should return non-empty bytes")
	}
}

func TestNid_TransactionWithMetaLeafNode_SerializeWithPrefix(t *testing.T) {
	key := makeHash(0x28)
	item := NewItem(key, nid_makeData(32))
	leaf, _ := newTransactionWithMetaLeafNode(item)
	p, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) == 0 {
		t.Error("SerializeWithPrefix should return non-empty bytes")
	}
}

func TestNid_CreateLeafNode_AllTypes(t *testing.T) {
	key := makeHash(0x31)
	data := nid_makeData(32)
	item := NewItem(key, data)

	for _, nodeType := range []NodeType{NodeTypeAccountState, NodeTypeTransactionNoMeta, NodeTypeTransactionWithMeta} {
		leaf, err := createLeafNode(nodeType, item)
		if err != nil {
			t.Errorf("createLeafNode(%v): %v", nodeType, err)
			continue
		}
		if leaf == nil {
			t.Errorf("createLeafNode(%v) returned nil", nodeType)
		}
	}
}

func TestNid_CreateLeafNode_InvalidType(t *testing.T) {
	key := makeHash(0x32)
	item := NewItem(key, nid_makeData(32))
	_, err := createLeafNode(NodeTypeInner, item)
	if err == nil {
		t.Fatal("expected error for invalid node type")
	}
}

func TestNid_CreateLeafNode_NilItem(t *testing.T) {
	_, err := createLeafNode(NodeTypeAccountState, nil)
	if !errors.Is(err, errNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_LeafNodeItem(t *testing.T) {
	key := makeHash(0x33)
	item := NewItem(key, nid_makeData(32))
	leaf, _ := newAccountStateLeafNode(item)

	result := leaf.Item()
	if result == nil {
		t.Fatal("leaf.Item() should not return nil for valid leaf")
	}

	inner := newInnerNode()
	if _, ok := mapNode(inner).(mapLeaf); ok {
		t.Error("innerNode must not implement mapLeaf")
	}
}

func TestNid_NewAccountStateLeafFromWire_Valid(t *testing.T) {
	key := makeHash(0x44)
	data := nid_makeData(32)
	item := NewItem(key, data)
	leaf, _ := newAccountStateLeafNode(item)
	wire, _ := leaf.SerializeForWire()

	recovered, err := newAccountStateLeafFromWire(wire)
	if err != nil {
		t.Fatalf("newAccountStateLeafFromWire: %v", err)
	}
	if recovered.Item().Key() != key {
		t.Error("recovered leaf has wrong key")
	}
}

func TestNid_NewAccountStateLeafFromWire_Empty(t *testing.T) {
	_, err := newAccountStateLeafFromWire([]byte{})
	if err == nil {
		t.Fatal("expected error for empty wire data")
	}
}

func TestNid_NewTransactionLeafFromWire_Valid(t *testing.T) {
	key := makeHash(0x55)
	data := nid_makeData(32)
	item := NewItem(key, data)
	leaf, _ := newTransactionLeafNode(item)
	wire, _ := leaf.SerializeForWire()

	recovered, err := newTransactionLeafFromWire(wire)
	if err != nil {
		t.Fatalf("NewTransactionLeafFromWire: %v", err)
	}
	if recovered.Item() == nil {
		t.Error("recovered leaf item should not be nil")
	}
}

func TestNid_NewTransactionLeafFromWire_Empty(t *testing.T) {
	_, err := newTransactionLeafFromWire([]byte{})
	if err == nil {
		t.Fatal("expected error for empty wire data")
	}
}

func TestNid_NewTransactionWithMetaLeafFromWire_Valid(t *testing.T) {
	key := makeHash(0x66)
	data := nid_makeData(32)
	item := NewItem(key, data)
	leaf, _ := newTransactionWithMetaLeafNode(item)
	wire, _ := leaf.SerializeForWire()

	recovered, err := newTransactionWithMetaLeafFromWire(wire)
	if err != nil {
		t.Fatalf("newTransactionWithMetaLeafFromWire: %v", err)
	}
	if recovered.Item() == nil {
		t.Error("recovered leaf item should not be nil")
	}
}

func TestNid_NewTransactionWithMetaLeafFromWire_Empty(t *testing.T) {
	_, err := newTransactionWithMetaLeafFromWire([]byte{})
	if err == nil {
		t.Fatal("expected error for empty wire data")
	}
}
