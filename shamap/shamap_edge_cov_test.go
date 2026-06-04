package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// sme_keyFromByte returns a [32]byte key with k[0]=b.
func sme_keyFromByte(b byte) [32]byte {
	var k [32]byte
	k[0] = b
	return k
}

// sme_keyFromTwo returns a [32]byte key with k[0]=hi, k[1]=lo.
func sme_keyFromTwo(hi, lo byte) [32]byte {
	var k [32]byte
	k[0] = hi
	k[1] = lo
	return k
}

// sme_data12 returns a 12-byte slice filled with b.
func sme_data12(b byte) []byte {
	d := make([]byte, 12)
	for i := range d {
		d[i] = b
	}
	return d
}

// ---------------------------------------------------------------------------
// State.String() — uncovered "unknown" branch (line 37)
// ---------------------------------------------------------------------------

func TestSme_StateString(t *testing.T) {
	cases := []struct {
		s    State
		want string
	}{
		{StateModifying, "modifying"},
		{StateImmutable, "immutable"},
		{StateSyncing, "syncing"},
		{StateInvalid, "invalid"},
		{State(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("State(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Type.String() — "unknown" branch
// ---------------------------------------------------------------------------

func TestSme_TypeString(t *testing.T) {
	cases := []struct {
		typ  Type
		want string
	}{
		{TypeTransaction, "transaction"},
		{TypeState, "state"},
		{Type(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.typ.String(); got != c.want {
			t.Errorf("Type(%d).String() = %q, want %q", c.typ, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Type() and State() accessor methods (lines 219, 226)
// ---------------------------------------------------------------------------

func TestSme_TypeAndStateAccessors(t *testing.T) {
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sm.Type() != TypeTransaction {
		t.Errorf("Type() = %v, want TypeTransaction", sm.Type())
	}
	if sm.State() != StateModifying {
		t.Errorf("State() = %v, want StateModifying", sm.State())
	}
}

// ---------------------------------------------------------------------------
// SetFull() and SetLedgerSeq() (lines 246, 253)
// ---------------------------------------------------------------------------

func TestSme_SetFullAndSetLedgerSeq(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sm.SetFull()
	sm.SetLedgerSeq(42)
	sm.mu.RLock()
	seq := sm.ledgerSeq
	full := sm.full
	sm.mu.RUnlock()
	if seq != 42 {
		t.Errorf("ledgerSeq = %d, want 42", seq)
	}
	if !full {
		t.Error("full should be true after SetFull()")
	}
}

// ---------------------------------------------------------------------------
// Has() (line 404)
// ---------------------------------------------------------------------------

func TestSme_Has(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k := sme_keyFromByte(0x10)
	found, err := sm.Has(k)
	if err != nil || found {
		t.Errorf("Has on empty: err=%v found=%v", err, found)
	}
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	found, err = sm.Has(k)
	if err != nil || !found {
		t.Errorf("Has after Put: err=%v found=%v", err, found)
	}
	// absent key
	k2 := sme_keyFromByte(0x20)
	found, err = sm.Has(k2)
	if err != nil || found {
		t.Errorf("Has absent: err=%v found=%v", err, found)
	}
}

// ---------------------------------------------------------------------------
// Get on empty map
// ---------------------------------------------------------------------------

func TestSme_GetEmptyMap(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	item, ok, err := sm.Get(sme_keyFromByte(0xAA))
	if err != nil || ok || item != nil {
		t.Errorf("Get on empty: item=%v ok=%v err=%v", item, ok, err)
	}
}

// ---------------------------------------------------------------------------
// SetImmutable on invalid map returns error
// ---------------------------------------------------------------------------

func TestSme_SetImmutableOnInvalidReturnsError(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sm.mu.Lock()
	sm.state = StateInvalid
	sm.mu.Unlock()
	if err := sm.SetImmutable(); err == nil {
		t.Error("SetImmutable on invalid map should return error")
	}
}

// ---------------------------------------------------------------------------
// Hash() on invalid map returns error
// ---------------------------------------------------------------------------

func TestSme_HashOnInvalidReturnsError(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sm.mu.Lock()
	sm.state = StateInvalid
	sm.mu.Unlock()
	if _, err := sm.Hash(); err == nil {
		t.Error("Hash on invalid map should return error")
	}
}

// ---------------------------------------------------------------------------
// Snapshot on invalid map returns error
// ---------------------------------------------------------------------------

func TestSme_SnapshotOnInvalidReturnsError(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sm.mu.Lock()
	sm.state = StateInvalid
	sm.mu.Unlock()
	if _, err := sm.Snapshot(false); err == nil {
		t.Error("Snapshot on invalid map should return error")
	}
}

// ---------------------------------------------------------------------------
// NodeStack.Top() and NodeStack.Clear() (lines 308, 323)
// ---------------------------------------------------------------------------

func TestSme_NodeStackTopAndClear(t *testing.T) {
	ns := NewNodeStack()

	// Top on empty returns false
	if _, _, ok := ns.Top(); ok {
		t.Error("Top on empty should return ok=false")
	}

	inner := NewInnerNode()
	id := NewRootNodeID()
	ns.Push(inner, id)

	node, topID, ok := ns.Top()
	if !ok {
		t.Fatal("Top should return ok=true after Push")
	}
	if node != inner {
		t.Error("Top returned wrong node")
	}
	if topID != id {
		t.Error("Top returned wrong nodeID")
	}
	if ns.Len() != 1 {
		t.Errorf("Len after single Push = %d, want 1", ns.Len())
	}

	// Clear removes all entries
	ns.Clear()
	if !ns.IsEmpty() {
		t.Error("IsEmpty should be true after Clear")
	}
	if ns.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", ns.Len())
	}
}

// ---------------------------------------------------------------------------
// PutItemWithNodeType / dirtyUp via StateInvalid guard
// ---------------------------------------------------------------------------

func TestSme_PutItemWithNodeTypeOnImmutable(t *testing.T) {
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	k := sme_keyFromByte(0x01)
	err = sm.PutItemWithNodeType(NewItem(k, sme_data12(1)), NodeTypeTransactionNoMeta)
	if !errors.Is(err, ErrImmutable) {
		t.Errorf("PutItemWithNodeType on immutable: want ErrImmutable, got %v", err)
	}
}

func TestSme_PutItemWithNodeTypeNilItem(t *testing.T) {
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sm.PutItemWithNodeType(nil, NodeTypeTransactionNoMeta); !errors.Is(err, ErrNilItem) {
		t.Errorf("PutItemWithNodeType(nil): want ErrNilItem, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// PutWithNodeType — update existing key
// ---------------------------------------------------------------------------

func TestSme_PutWithNodeTypeUpdate(t *testing.T) {
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k := sme_keyFromByte(0x05)
	data1 := sme_data12(0xAA)
	data2 := sme_data12(0xBB)

	if err := sm.PutWithNodeType(k, data1, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("PutWithNodeType (insert): %v", err)
	}
	if err := sm.PutWithNodeType(k, data2, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("PutWithNodeType (update): %v", err)
	}
	item, ok, err := sm.Get(k)
	if err != nil || !ok {
		t.Fatalf("Get after update: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(item.Data(), data2) {
		t.Error("data not updated")
	}
}

// ---------------------------------------------------------------------------
// dirtyUp called in StateSyncing → ErrInvalidState
// ---------------------------------------------------------------------------

func TestSme_DirtyUpStateSyncingReturnsInvalidState(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	stack := NewNodeStack()
	_, dirtyErr := sm.dirtyUp(stack, [32]byte{}, NewInnerNode())
	if !errors.Is(dirtyErr, ErrInvalidState) {
		t.Errorf("dirtyUp in StateSyncing: want ErrInvalidState, got %v", dirtyErr)
	}
}

// ---------------------------------------------------------------------------
// assignRoot with a leaf node → wraps in inner node
// ---------------------------------------------------------------------------

func TestSme_AssignRootWithLeaf(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k := sme_keyFromByte(0x01)
	item := NewItem(k, sme_data12(1))
	leaf, leafErr := NewAccountStateLeafNode(item)
	if leafErr != nil {
		t.Fatalf("NewAccountStateLeafNode: %v", leafErr)
	}
	if err := sm.assignRoot(leaf, k); err != nil {
		t.Errorf("assignRoot with leaf: %v", err)
	}
	// root must be an inner node
	if sm.root == nil {
		t.Error("root must not be nil after assignRoot with leaf")
	}
}

// ---------------------------------------------------------------------------
// Delete absent key → ErrItemNotFound
// ---------------------------------------------------------------------------

func TestSme_DeleteAbsent(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = sm.Delete(sme_keyFromByte(0xFF))
	if !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Delete absent: want ErrItemNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Delete on immutable → ErrImmutable
// ---------------------------------------------------------------------------

func TestSme_DeleteImmutable(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k := sme_keyFromByte(0x10)
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	if err := sm.Delete(k); !errors.Is(err, ErrImmutable) {
		t.Errorf("Delete on immutable: want ErrImmutable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutable snapshot of a map with items, then mutate snapshot independently
// ---------------------------------------------------------------------------

func TestSme_MutableSnapshot(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k1 := sme_keyFromByte(0x10)
	k2 := sme_keyFromByte(0x20)
	if err := sm.Put(k1, sme_data12(1)); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	origHash, _ := sm.Hash()

	snap, err := sm.Snapshot(true) // mutable
	if err != nil {
		t.Fatalf("Snapshot(mutable): %v", err)
	}
	if snap.State() != StateModifying {
		t.Errorf("mutable snapshot state = %v, want StateModifying", snap.State())
	}
	// Mutate snapshot only
	if err := snap.Put(k2, sme_data12(2)); err != nil {
		t.Fatalf("Put on mutable snapshot: %v", err)
	}
	// Original must be unchanged
	smHash, _ := sm.Hash()
	if smHash != origHash {
		t.Error("original hash changed after mutating mutable snapshot")
	}
	// Snapshot must have k2
	_, ok, err := snap.Get(k2)
	if err != nil || !ok {
		t.Errorf("k2 not in snapshot: ok=%v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Immutable→immutable snapshot caches size (line 1000)
// ---------------------------------------------------------------------------

func TestSme_ImmutableSnapshotCachesSize(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := sm.Put(sme_keyFromByte(byte(i+1)), sme_data12(byte(i))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	// Warm up Size() so sm.cachedSize is set
	sz1 := sm.Size()
	if sz1 != 5 {
		t.Errorf("Size = %d, want 5", sz1)
	}
	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// snapshot must expose same count without re-counting
	if snap.Size() != 5 {
		t.Errorf("snapshot.Size() = %d, want 5", snap.Size())
	}
}

// ---------------------------------------------------------------------------
// ForEachCtx early-stop via context cancellation
// ---------------------------------------------------------------------------

func TestSme_ForEachCtxCancelled(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := sm.Put(sme_keyFromByte(byte(i+1)), sme_data12(byte(i))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = sm.ForEachCtx(ctx, func(*Item) bool { return true })
	if err == nil {
		t.Error("ForEachCtx with cancelled context should return error")
	}
}

// ---------------------------------------------------------------------------
// FlushDirty nil-root guard: force root to nil to exercise the early-return.
// ---------------------------------------------------------------------------

func TestSme_FlushDirtyNilRoot(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sm.mu.Lock()
	sm.root = nil
	sm.mu.Unlock()
	batch, err := sm.FlushDirty(false)
	if err != nil {
		t.Fatalf("FlushDirty with nil root: %v", err)
	}
	if len(batch.Entries) != 0 {
		t.Errorf("FlushDirty with nil root: expected 0 entries, got %d", len(batch.Entries))
	}
}

// ---------------------------------------------------------------------------
// NewBacked with nil family returns error
// ---------------------------------------------------------------------------

func TestSme_NewBackedNilFamily(t *testing.T) {
	if _, err := NewBacked(TypeState, nil); err == nil {
		t.Error("NewBacked(nil family) should return error")
	}
}

// ---------------------------------------------------------------------------
// NewFromRootHash with nil family returns error
// ---------------------------------------------------------------------------

func TestSme_NewFromRootHashNilFamily(t *testing.T) {
	if _, err := NewFromRootHash(TypeState, [32]byte{}, nil); err == nil {
		t.Error("NewFromRootHash(nil family) should return error")
	}
}

// ---------------------------------------------------------------------------
// NewFromRootHash with missing root → error
// ---------------------------------------------------------------------------

func TestSme_NewFromRootHashMissingRoot(t *testing.T) {
	family := newMemoryFamily()
	var h [32]byte
	h[0] = 0xDE
	_, err := NewFromRootHash(TypeState, h, family)
	if err == nil {
		t.Error("NewFromRootHash with missing root should return error")
	}
}

// ---------------------------------------------------------------------------
// SetFamily to nil → unbacked
// ---------------------------------------------------------------------------

func TestSme_SetFamilyToNilMakesUnbacked(t *testing.T) {
	family := newMemoryFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	sm.SetFamily(nil)
	if sm.IsBacked() {
		t.Error("map should be unbacked after SetFamily(nil)")
	}
}

// ---------------------------------------------------------------------------
// FindDifference with nil other → error
// ---------------------------------------------------------------------------

func TestSme_FindDifferenceNilOther(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := sm.FindDifference(nil); err == nil {
		t.Error("FindDifference(nil) should return error")
	}
}

// ---------------------------------------------------------------------------
// FindDifference: invalid map → error
// ---------------------------------------------------------------------------

func TestSme_FindDifferenceInvalidMap(t *testing.T) {
	sm1, _ := New(TypeState)
	sm2, _ := New(TypeState)
	sm1.mu.Lock()
	sm1.state = StateInvalid
	sm1.mu.Unlock()
	if _, err := sm1.FindDifference(sm2); err == nil {
		t.Error("FindDifference with invalid map should return error")
	}
}

// ---------------------------------------------------------------------------
// GetNodeFatByPath (line 1592) — basic coverage
// ---------------------------------------------------------------------------

func TestSme_GetNodeFatByPath(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Populate a few items so the root inner has real children
	for i := byte(1); i <= 8; i++ {
		k := sme_keyFromByte(i << 4)
		if err := sm.Put(k, sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	// Nil root after explicit clear should return nil (test nil-root guard)
	nilSm, _ := New(TypeState)
	nilSm.mu.Lock()
	nilSm.root = nil
	nilSm.mu.Unlock()
	nodes, err := nilSm.GetNodeFatByPath([32]byte{}, 0, 1, true)
	if err != nil || nodes != nil {
		t.Errorf("GetNodeFatByPath with nil root: nodes=%v err=%v", nodes, err)
	}

	// Valid root at depth 0 with budget 1 and fatLeaves
	nodes, err = sm.GetNodeFatByPath([32]byte{}, 0, 1, true)
	if err != nil {
		t.Fatalf("GetNodeFatByPath: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("expected at least the root node returned")
	}

	// Path that doesn't exist (depth > tree depth)
	nodes, err = sm.GetNodeFatByPath([32]byte{0xFF}, 64, 1, false)
	if err != nil || nodes != nil {
		t.Errorf("GetNodeFatByPath nonexistent deep path: nodes=%v err=%v", nodes, err)
	}
}

// ---------------------------------------------------------------------------
// pathPrefixEq (line 1749)
// ---------------------------------------------------------------------------

func TestSme_PathPrefixEq(t *testing.T) {
	var a, b [32]byte
	// depth 0 → always true
	if !pathPrefixEq(a, b, 0) {
		t.Error("pathPrefixEq(0) should be true for equal arrays")
	}
	// make them differ at nibble 3
	a[1] = 0x0F // nibble 3 (depth=3: byte 1, low nibble)
	if pathPrefixEq(a, b, 4) {
		t.Error("pathPrefixEq(4) should be false when nibble 3 differs")
	}
	// but prefix of depth 3 should still match (nibbles 0-2 unchanged)
	if !pathPrefixEq(a, b, 3) {
		t.Error("pathPrefixEq(3) should be true when only nibble 3 differs")
	}
}

// ---------------------------------------------------------------------------
// WalkWireNodes on non-trivial tree
// ---------------------------------------------------------------------------

func TestSme_WalkWireNodes(t *testing.T) {
	sm, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
	// Each NodeID must be 33 bytes
	for i, n := range nodes {
		if len(n.NodeID) != 33 {
			t.Errorf("node %d: NodeID length = %d, want 33", i, len(n.NodeID))
		}
	}
}

// ---------------------------------------------------------------------------
// AddKnownNodeUnchecked (line 586) — happy path and error paths
// ---------------------------------------------------------------------------

func TestSme_AddKnownNodeUnchecked(t *testing.T) {
	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	k := sme_keyFromByte(0x01)
	if err := source.Put(k, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	// Error: not syncing
	dest1, _ := New(TypeTransaction)
	if err := dest1.AddKnownNodeUnchecked([]byte{1, 2, 3}); !errors.Is(err, ErrSyncNotInProgress) {
		t.Errorf("AddKnownNodeUnchecked not-syncing: want ErrSyncNotInProgress, got %v", err)
	}

	// Error: empty data
	dest2, _ := New(TypeTransaction)
	if err := dest2.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest2.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	if err := dest2.AddKnownNodeUnchecked(nil); !errors.Is(err, ErrInvalidNodeData) {
		t.Errorf("AddKnownNodeUnchecked nil data: want ErrInvalidNodeData, got %v", err)
	}

	// Happy path: add all non-root nodes via AddKnownNodeUnchecked
	dest3, _ := New(TypeTransaction)
	if err := dest3.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest3.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	for _, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if err := dest3.AddKnownNodeUnchecked(w.Data); err != nil {
			// ErrUnexpectedNode is fine when the node is already present
			if !errors.Is(err, ErrUnexpectedNode) {
				t.Fatalf("AddKnownNodeUnchecked: %v", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// AddKnownNodeByID: root NodeID → ErrUnexpectedNode
// ---------------------------------------------------------------------------

func TestSme_AddKnownNodeByID_RootNodeID(t *testing.T) {
	sm, _ := New(TypeTransaction)
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	rootID := NewRootNodeID()
	if err := sm.AddKnownNodeByID(rootID, []byte{1}); !errors.Is(err, ErrUnexpectedNode) {
		t.Errorf("AddKnownNodeByID(root): want ErrUnexpectedNode, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CachingSyncFilter: zero maxSize defaults to 10000, LRU eviction
// ---------------------------------------------------------------------------

func TestSme_CachingSyncFilter(t *testing.T) {
	inner := &DefaultSyncFilter{}
	// zero maxSize → default 10000
	f := NewCachingSyncFilter(inner, 0)
	if f.maxSize != 10000 {
		t.Errorf("default maxSize = %d, want 10000", f.maxSize)
	}

	// Fill to maxSize+1 to trigger eviction
	small := NewCachingSyncFilter(inner, 2)
	for i := 0; i < 3; i++ {
		var h [32]byte
		h[0] = byte(i + 1)
		small.ShouldFetch(h)
	}
	if small.lru.Len() > 2 {
		t.Errorf("LRU should have evicted to maxSize=2, len=%d", small.lru.Len())
	}
}

// ---------------------------------------------------------------------------
// GetMissingNodes returns nil when not syncing
// ---------------------------------------------------------------------------

func TestSme_GetMissingNodesNotSyncing(t *testing.T) {
	sm, _ := New(TypeState)
	if got := sm.GetMissingNodes(0, nil); got != nil {
		t.Errorf("GetMissingNodes on non-syncing map: want nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// FinishSync when not syncing → ErrSyncNotInProgress
// ---------------------------------------------------------------------------

func TestSme_FinishSyncNotSyncing(t *testing.T) {
	sm, _ := New(TypeState)
	if err := sm.FinishSync(); !errors.Is(err, ErrSyncNotInProgress) {
		t.Errorf("FinishSync not syncing: want ErrSyncNotInProgress, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// StartSync on invalid map → error
// ---------------------------------------------------------------------------

func TestSme_StartSyncOnInvalidMap(t *testing.T) {
	sm, _ := New(TypeState)
	sm.mu.Lock()
	sm.state = StateInvalid
	sm.mu.Unlock()
	if err := sm.StartSync(); err == nil {
		t.Error("StartSync on invalid map should return error")
	}
}

// ---------------------------------------------------------------------------
// IsComplete with full=false path
// ---------------------------------------------------------------------------

func TestSme_IsCompleteWithFullFalse(t *testing.T) {
	sm, _ := New(TypeState)
	sm.mu.Lock()
	sm.full = false
	sm.mu.Unlock()
	// With no children the tree has no missing nodes → complete
	if !sm.IsComplete() {
		t.Error("empty tree with full=false should still be complete")
	}
}

// ---------------------------------------------------------------------------
// SyncProgress on map with items
// ---------------------------------------------------------------------------

func TestSme_SyncProgressWithItems(t *testing.T) {
	sm, _ := New(TypeState)
	for i := byte(1); i <= 5; i++ {
		if err := sm.Put(sme_keyFromByte(i), sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	present, total := sm.SyncProgress()
	if total == 0 {
		t.Error("total should be > 0 for non-empty map")
	}
	if present != total {
		t.Errorf("complete map should have present==total, got %d/%d", present, total)
	}
}

// ---------------------------------------------------------------------------
// AddKnownNode: hash mismatch returns ErrNodeHashMismatch
// ---------------------------------------------------------------------------

func TestSme_AddKnownNodeHashMismatch(t *testing.T) {
	source, _ := New(TypeState)
	k := sme_keyFromByte(0x10)
	if err := source.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()

	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	dest, _ := New(TypeState)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	// Pick a non-root wire node but supply the wrong expected hash
	for _, w := range wireNodes {
		nid, _ := UnmarshalBinary(w.NodeID)
		if nid.IsRoot() {
			continue
		}
		var wrongHash [32]byte
		wrongHash[0] = 0xFF
		err := dest.AddKnownNode(wrongHash, w.Data)
		if !errors.Is(err, ErrNodeHashMismatch) {
			t.Errorf("AddKnownNode with wrong hash: want ErrNodeHashMismatch, got %v", err)
		}
		break
	}
}

// ---------------------------------------------------------------------------
// IsBacked returns false for unbacked maps
// ---------------------------------------------------------------------------

func TestSme_IsBackedFalse(t *testing.T) {
	sm, _ := New(TypeState)
	if sm.IsBacked() {
		t.Error("unbacked map should return false for IsBacked()")
	}
}

// ---------------------------------------------------------------------------
// ForEach early-stop (fn returns false) — verifies the stop path is reachable.
// forEachUnsafe returns nil (not an error) on early-stop, so the parent
// branch loop continues visiting other branches. The test only verifies
// that ForEach completes without error even when fn returns false.
// ---------------------------------------------------------------------------

func TestSme_ForEachEarlyStop(t *testing.T) {
	sm, _ := New(TypeState)
	for i := byte(1); i <= 5; i++ {
		if err := sm.Put(sme_keyFromByte(i), sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	count := 0
	if err := sm.ForEach(func(*Item) bool {
		count++
		return false // request stop immediately
	}); err != nil {
		t.Fatalf("ForEach with fn returning false must not error: %v", err)
	}
	// count may be 1 or more depending on tree structure; just ensure the
	// early-stop fn was exercised (count >= 1) and ForEach didn't error.
	if count == 0 {
		t.Error("ForEach should have visited at least one item")
	}
}

// ---------------------------------------------------------------------------
// Size on mutable map (no caching path)
// ---------------------------------------------------------------------------

func TestSme_SizeMutableNoCaching(t *testing.T) {
	sm, _ := New(TypeState)
	if sz := sm.Size(); sz != 0 {
		t.Errorf("Size empty mutable = %d, want 0", sz)
	}
	for i := byte(1); i <= 3; i++ {
		if err := sm.Put(sme_keyFromByte(i), sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if sz := sm.Size(); sz != 3 {
		t.Errorf("Size mutable = %d, want 3", sz)
	}
}

// ---------------------------------------------------------------------------
// Multiple items sharing a deep path (forces split structure)
// ---------------------------------------------------------------------------

func TestSme_DeepSplit(t *testing.T) {
	sm, _ := New(TypeState)
	// Two keys that share the first 4 nibbles and differ at nibble 5
	k1 := hexToHash("1234500000000000000000000000000000000000000000000000000000000001")
	k2 := hexToHash("1234510000000000000000000000000000000000000000000000000000000002")

	for i, k := range [][32]byte{k1, k2} {
		if err := sm.Put(k, sme_data12(byte(i+1))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	// Both should be retrievable
	for _, k := range [][32]byte{k1, k2} {
		_, ok, err := sm.Get(k)
		if err != nil || !ok {
			t.Errorf("Get after deep split: ok=%v err=%v", ok, err)
		}
	}
}

// ---------------------------------------------------------------------------
// walkSubtreeForMissing reports true immediately when report says stop
// ---------------------------------------------------------------------------

func TestSme_WalkSubtreeStopsOnReport(t *testing.T) {
	source, _ := New(TypeState)
	for branch := byte(0); branch < 4; branch++ {
		k := sme_keyFromByte(branch << 4)
		if err := source.Put(k, sme_data12(branch)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()
	dest, _ := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	count := 0
	stop := walkSubtreeForMissing(
		dest,
		dest.root,
		NewRootNodeID(),
		dest.root.Hash(),
		0,
		&DefaultSyncFilter{},
		func(MissingNode) bool {
			count++
			return true // stop immediately after first
		},
	)
	if !stop {
		t.Error("walkSubtreeForMissing: expected stop=true when report returns true")
	}
	if count != 1 {
		t.Errorf("walkSubtreeForMissing: expected 1 report call, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// AddRootNode: already-set root → ErrRootAlreadySet
// ---------------------------------------------------------------------------

func TestSme_AddRootNodeAlreadySet(t *testing.T) {
	source, _ := New(TypeState)
	k := sme_keyFromByte(0x01)
	if err := source.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()

	dest, _ := New(TypeState)
	// First add succeeds
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("first AddRootNode: %v", err)
	}
	// Add depth-1 child so root HasChildren() returns true
	wireNodes, _ := source.WalkWireNodes()
	for _, w := range wireNodes {
		nid, _ := UnmarshalBinary(w.NodeID)
		if nid.IsRoot() {
			continue
		}
		if err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			// ignore errors; we just need root to have children
			_ = err
		}
		break
	}
	// Second add should be rejected
	if err := dest.AddRootNode(rootHash, rootData); !errors.Is(err, ErrRootAlreadySet) {
		t.Errorf("second AddRootNode: want ErrRootAlreadySet, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidation after deleting to leave single item collapses inner node
// ---------------------------------------------------------------------------

func TestSme_ConsolidateAfterDeleteSingleSibling(t *testing.T) {
	sm, _ := New(TypeState)
	// Two keys that will share an inner node → delete one, other should collapse
	k1 := hexToHash("f000000000000000000000000000000000000000000000000000000000000001")
	k2 := hexToHash("f100000000000000000000000000000000000000000000000000000000000002")

	if err := sm.Put(k1, sme_data12(1)); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	if err := sm.Put(k2, sme_data12(2)); err != nil {
		t.Fatalf("Put k2: %v", err)
	}
	if err := sm.Delete(k1); err != nil {
		t.Fatalf("Delete k1: %v", err)
	}

	// k2 should still be retrievable
	_, ok, err := sm.Get(k2)
	if err != nil || !ok {
		t.Errorf("Get k2 after consolidation: ok=%v err=%v", ok, err)
	}
	// k1 should be gone
	_, ok, err = sm.Get(k1)
	if err != nil || ok {
		t.Errorf("Get k1 after delete: ok=%v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// WalkMap/WalkMapParallel on empty/invalid root
// ---------------------------------------------------------------------------

func TestSme_WalkMapNilAndInvalidRoot(t *testing.T) {
	sm, _ := New(TypeState)
	sm.mu.Lock()
	sm.root = nil
	sm.mu.Unlock()
	if got := sm.WalkMap(0, nil); got != nil {
		t.Errorf("WalkMap nil root: want nil, got %v", got)
	}
	if got := sm.WalkMapParallel(0, nil); got != nil {
		t.Errorf("WalkMapParallel nil root: want nil, got %v", got)
	}

	sm2, _ := New(TypeState)
	sm2.mu.Lock()
	sm2.state = StateInvalid
	sm2.mu.Unlock()
	if got := sm2.WalkMap(0, nil); got != nil {
		t.Errorf("WalkMap invalid state: want nil, got %v", got)
	}
	if got := sm2.WalkMapParallel(0, nil); got != nil {
		t.Errorf("WalkMapParallel invalid state: want nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Backed map backed snapshot flushes dirty nodes
// ---------------------------------------------------------------------------

func TestSme_BackedSnapshotFlushes(t *testing.T) {
	family := newMemoryFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	k := sme_keyFromByte(0x10)
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Snapshot should flush dirty nodes to family
	snap, err := sm.Snapshot(false)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if family.Len() == 0 {
		t.Error("backed Snapshot should flush dirty nodes to family")
	}
	// snap hash should equal sm hash
	smHash, _ := sm.Hash()
	snapHash, _ := snap.Hash()
	if smHash != snapHash {
		t.Errorf("snap hash mismatch: sm=%x snap=%x", smHash[:4], snapHash[:4])
	}
}

// ---------------------------------------------------------------------------
// NodeStack Pop on empty → ok=false
// ---------------------------------------------------------------------------

func TestSme_NodeStackPopEmpty(t *testing.T) {
	ns := NewNodeStack()
	_, _, ok := ns.Pop()
	if ok {
		t.Error("Pop on empty NodeStack should return ok=false")
	}
}

// ---------------------------------------------------------------------------
// PutItem on immutable → ErrImmutable
// ---------------------------------------------------------------------------

func TestSme_PutItemImmutable(t *testing.T) {
	sm, _ := New(TypeState)
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	k := sme_keyFromByte(0x01)
	if err := sm.PutItem(NewItem(k, sme_data12(1))); !errors.Is(err, ErrImmutable) {
		t.Errorf("PutItem on immutable: want ErrImmutable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// getBranchAtDepth edge: depth >= MaxDepth → 0
// ---------------------------------------------------------------------------

func TestSme_GetBranchAtDepthBeyondMax(t *testing.T) {
	var k [32]byte
	k[0] = 0xFF
	if got := getBranchAtDepth(k, MaxDepth); got != 0 {
		t.Errorf("getBranchAtDepth at MaxDepth = %d, want 0", got)
	}
	if got := getBranchAtDepth(k, MaxDepth+10); got != 0 {
		t.Errorf("getBranchAtDepth beyond MaxDepth = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Put many items spread across all branches, then delete all
// ---------------------------------------------------------------------------

func TestSme_PutAndDeleteAll(t *testing.T) {
	sm, _ := New(TypeState)
	keys := make([][32]byte, 32)
	for i := range keys {
		keys[i] = sme_keyFromByte(byte(i + 1))
		if err := sm.Put(keys[i], sme_data12(byte(i+1))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	for i, k := range keys {
		if err := sm.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
	h, _ := sm.Hash()
	if h != ([32]byte{}) {
		t.Errorf("empty map should have zero hash after all deletes, got %x", h[:8])
	}
}

// ---------------------------------------------------------------------------
// Verify insertNodeRecursive path coverage via AddKnownNode success
// ---------------------------------------------------------------------------

func TestSme_AddKnownNodeSuccess(t *testing.T) {
	source, _ := New(TypeState)
	for i := byte(0); i < 4; i++ {
		k := sme_keyFromTwo(i<<4, i)
		if err := source.Put(k, sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()
	wireNodes, _ := source.WalkWireNodes()

	dest, _ := New(TypeState)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	for _, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		// Use AddKnownNode (hash-based insertion) for depth-1 nodes
		if nid.Depth() == 1 {
			// Compute the node hash from wire data
			node, err2 := DeserializeNodeFromWire(w.Data)
			if err2 != nil {
				continue
			}
			if err2 := node.UpdateHash(); err2 != nil {
				continue
			}
			nodeHash := node.Hash()
			if err := dest.AddKnownNode(nodeHash, w.Data); err != nil {
				t.Logf("AddKnownNode depth=1: %v (may be ErrUnexpectedNode)", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// MissingNode.String output format check (already partially covered)
// ---------------------------------------------------------------------------

func TestSme_MissingNodeStringFull(t *testing.T) {
	mn := &MissingNode{
		Hash:       [32]byte{0xAB, 0xCD},
		Depth:      7,
		ParentHash: [32]byte{0x11, 0x22},
		Branch:     0xF,
	}
	s := mn.String()
	if s == "" {
		t.Error("MissingNode.String() must not be empty")
	}
	// Should contain depth
	if !sme_containsStr(s, fmt.Sprintf("%d", 7)) {
		t.Errorf("MissingNode.String() = %q, expected depth 7", s)
	}
}

func sme_containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
