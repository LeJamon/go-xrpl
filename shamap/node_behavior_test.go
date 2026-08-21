package shamap

import (
	"errors"
	"strings"
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
	for _, depth := range []uint8{0, 1, 31, 32, 63, MaxDepth} {
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

func TestNid_NewRootNodeID(t *testing.T) {
	root := NewRootNodeID()
	if !root.IsRoot() {
		t.Fatal("NewRootNodeID should return a root node")
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
	// At MaxDepth (64) no masking occurs; all bytes preserved.
	nid, err := createNodeID(MaxDepth, nid_fullKey)
	if err != nil {
		t.Fatal(err)
	}
	if nid.ID() != nid_fullKey {
		t.Fatal("at MaxDepth all bytes should be preserved")
	}
}

func TestNid_CreateNodeID_ExceedsMaxDepth(t *testing.T) {
	_, err := createNodeID(MaxDepth+1, nid_zeroKey)
	if !errors.Is(err, ErrMaxDepthExceeded) {
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
	root := NewRootNodeID()
	for branch := uint8(0); branch <= 15; branch++ {
		child, err := root.ChildNodeID(branch)
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
	root := NewRootNodeID()
	_, err := root.ChildNodeID(16)
	if err == nil {
		t.Fatal("expected error for branch > 15")
	}
}

func TestNid_ChildNodeID_AtMaxDepth(t *testing.T) {
	nid, _ := nodeID(MaxDepth, nid_zeroKey)
	_, err := nid.ChildNodeID(0)
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestNid_ChildNodeID_LowNibble(t *testing.T) {
	// From depth=1, branch should go into the low nibble of byte[0].
	parent, _ := nodeID(1, nid_zeroKey)
	child, err := parent.ChildNodeID(7)
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

func TestNid_ParentNodeID_FromChild(t *testing.T) {
	root := NewRootNodeID()
	child, _ := root.ChildNodeID(5)
	parent, err := child.ParentNodeID()
	if err != nil {
		t.Fatal(err)
	}
	if !parent.Equal(root) {
		t.Fatalf("parent %v != root %v", parent, root)
	}
}

func TestNid_ParentNodeID_RootErrors(t *testing.T) {
	root := NewRootNodeID()
	_, err := root.ParentNodeID()
	if err == nil {
		t.Fatal("expected error when calling ParentNodeID on root")
	}
}

func TestNid_SelectBranch_Root(t *testing.T) {
	root := NewRootNodeID()
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
	nid, _ := nodeID(MaxDepth, nid_zeroKey)
	// should return 0 as the guard
	branch := selectBranch(nid, nid_fullKey)
	if branch != 0 {
		t.Errorf("branch = %d, want 0", branch)
	}
}

func TestNid_MarshalUnmarshalRoundtrip(t *testing.T) {
	original, _ := nodeID(7, nid_gradientKey)
	data, err := original.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != NodeIDSize {
		t.Fatalf("expected %d bytes, got %d", NodeIDSize, len(data))
	}

	decoded, err := ParseNodeID(data)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Equal(original) {
		t.Fatal("decoded NodeID != original")
	}
}

func TestNid_Bytes_UnmarshalBinary_Roundtrip(t *testing.T) {
	nid, _ := nodeID(3, nid_gradientKey)
	b := nid.Bytes()
	decoded, err := ParseNodeID(b)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Equal(nid) {
		t.Fatal("UnmarshalBinary round-trip failed")
	}
}

func TestNid_UnmarshalBinary_WrongLength(t *testing.T) {
	for _, badLen := range []int{0, 1, 32, 34, 100} {
		_, err := ParseNodeID(make([]byte, badLen))
		if !errors.Is(err, ErrInvalidNodeIDLength) {
			t.Errorf("len=%d: expected ErrInvalidNodeIDLength, got %v", badLen, err)
		}
	}
}

func TestNid_UnmarshalBinary_ExceedsMaxDepth(t *testing.T) {
	data := make([]byte, NodeIDSize)
	data[32] = MaxDepth + 1
	_, err := ParseNodeID(data)
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestNid_ParseNodeID_RejectsNonCanonicalPath(t *testing.T) {
	data := make([]byte, NodeIDSize)
	data[0] = 0x1F
	data[32] = 1
	_, err := ParseNodeID(data)
	if !errors.Is(err, ErrNonCanonicalNodeID) {
		t.Fatalf("expected ErrNonCanonicalNodeID, got %v", err)
	}
}

func TestNid_Equal(t *testing.T) {
	a, _ := nodeID(4, nid_gradientKey)
	b, _ := nodeID(4, nid_gradientKey)
	c, _ := nodeID(5, nid_gradientKey)
	var other [32]byte
	other[0] = 1
	d, _ := nodeID(4, other)

	if !a.Equal(b) {
		t.Error("equal NodeIDs should be Equal")
	}
	if a.Equal(c) {
		t.Error("different depth should not be Equal")
	}
	if a.Equal(d) {
		t.Error("different id should not be Equal")
	}
}

func TestNid_Compare(t *testing.T) {
	shallow, _ := nodeID(1, nid_zeroKey)
	deep, _ := nodeID(5, nid_zeroKey)
	same, _ := nodeID(1, nid_zeroKey)

	if shallow.Compare(deep) != -1 {
		t.Error("shallower depth should compare as less")
	}
	if deep.Compare(shallow) != 1 {
		t.Error("deeper depth should compare as greater")
	}
	if shallow.Compare(same) != 0 {
		t.Error("identical NodeIDs should compare as equal")
	}

	var ka, kb [32]byte
	ka[0] = 0x10
	kb[0] = 0x20
	na, _ := nodeID(1, ka)
	nb, _ := nodeID(1, kb)
	if na.Compare(nb) >= 0 {
		t.Error("smaller id should compare as less")
	}
}

func TestNid_String_Root(t *testing.T) {
	s := NewRootNodeID().String()
	if !strings.Contains(s, "root") {
		t.Errorf("root String() should contain 'root', got %q", s)
	}
}

func TestNid_String_NonRoot(t *testing.T) {
	nid, _ := nodeID(3, nid_gradientKey)
	s := nid.String()
	if !strings.Contains(s, "depth=3") {
		t.Errorf("String() should contain depth, got %q", s)
	}
}

func TestNid_Validate_Valid(t *testing.T) {
	nid, _ := createNodeID(3, nid_gradientKey)
	if err := nid.Validate(); err != nil {
		t.Fatalf("valid NodeID failed Validate: %v", err)
	}
}

func TestNid_Validate_ZeroBytes(t *testing.T) {
	if err := NewRootNodeID().Validate(); err != nil {
		t.Fatalf("root NodeID failed Validate: %v", err)
	}
}

func TestNid_Validate_NonZeroTailByte(t *testing.T) {
	var key [32]byte
	key[0] = 0xF0 // valid high nibble for depth=1
	key[1] = 0x01 // dirty byte beyond depth boundary
	nid := NodeID{depth: 1, id: key}
	if err := nid.Validate(); err == nil {
		t.Fatal("expected Validate to fail for dirty tail byte")
	}
}

func TestNid_Validate_DirtyLowNibble(t *testing.T) {
	// depth=2 (even): byte[0] should have zero low nibble.
	var key [32]byte
	key[0] = 0xA3 // low nibble non-zero
	nid := NodeID{depth: 2, id: key}
	if err := nid.Validate(); err == nil {
		t.Fatal("expected Validate to fail for dirty low nibble at even depth")
	}
}

func TestNid_IteratorEmpty(t *testing.T) {
	sm := New(TypeState)
	it := newTestIterator(sm)
	if it.Next() {
		t.Error("empty map: Next() should return false")
	}
	if it.Valid() {
		t.Error("empty map: Valid() should return false")
	}
	if it.Item() != nil {
		t.Error("empty map: Item() should return nil")
	}
	if it.Err() != nil {
		t.Errorf("empty map: Err() should be nil, got %v", it.Err())
	}
}

func TestNid_IteratorFullTraversal(t *testing.T) {
	sm := New(TypeState)

	const n = 20
	for i := 0; i < n; i++ {
		var k [32]byte
		k[0] = byte(i + 1)
		if err := sm.Put(k, intToBytes(i+1)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	it := newTestIterator(sm)
	count := 0
	var prev [32]byte
	for it.Next() {
		item := it.Item()
		if item == nil {
			t.Fatal("Next() returned true but Item() is nil")
		}
		if count > 0 && compareKeys(item.Key(), prev) <= 0 {
			t.Errorf("iteration not in ascending order at step %d", count)
		}
		if !it.Valid() {
			t.Error("Valid() should be true when Next() returned true")
		}
		prev = item.Key()
		count++
	}
	if it.Err() != nil {
		t.Fatalf("iteration ended with error: %v", it.Err())
	}
	if count != n {
		t.Errorf("expected %d items, got %d", n, count)
	}
}

func TestNid_IteratorSingleItem(t *testing.T) {
	sm := New(TypeState)
	var k [32]byte
	k[0] = 0x42
	if err := sm.Put(k, intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	it := newTestIterator(sm)
	if !it.Next() {
		t.Fatal("expected one item")
	}
	if it.Item().Key() != k {
		t.Error("wrong key returned")
	}
	if it.Next() {
		t.Error("expected no second item")
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
	if !errors.Is(err, ErrNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_AccountStateLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0x22)
	item := NewItem(key, []byte("short"))
	_, err := newAccountStateLeafNode(item)
	if !errors.Is(err, ErrItemTooSmall) {
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
	if !errors.Is(err, ErrNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_TransactionLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0xCC)
	item := NewItem(key, []byte("tiny"))
	_, err := newTransactionLeafNode(item)
	if !errors.Is(err, ErrItemTooSmall) {
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
	if !errors.Is(err, ErrNilItem) {
		t.Fatalf("expected ErrNilItem, got %v", err)
	}
}

func TestNid_TransactionWithMetaLeafNode_TooSmall(t *testing.T) {
	key := makeHash(0x22)
	item := NewItem(key, []byte("too short"))
	_, err := newTransactionWithMetaLeafNode(item)
	if !errors.Is(err, ErrItemTooSmall) {
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
	if !errors.Is(err, ErrNilItem) {
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
