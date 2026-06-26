package shamap

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Used to trigger the descend() error path in invariant checking.
type inv_failingFamily struct{}

func (f *inv_failingFamily) Fetch(_ context.Context, _ [32]byte) ([]byte, error) {
	return nil, fmt.Errorf("fetch always fails")
}
func (f *inv_failingFamily) StoreBatch(_ context.Context, _ []FlushEntry) error { return nil }

// inv_fakeInnerNode implements Node but is neither *innerNode nor a
// LeafNode. Used to cover the type-assertion failure paths.
type inv_fakeInnerNode struct {
	baseNode
}

func (n *inv_fakeInnerNode) Type() NodeType                       { return NodeTypeInner }
func (n *inv_fakeInnerNode) UpdateHash() error                    { return nil }
func (n *inv_fakeInnerNode) SerializeForWire() ([]byte, error)    { return nil, fmt.Errorf("fake") }
func (n *inv_fakeInnerNode) SerializeWithPrefix() ([]byte, error) { return nil, fmt.Errorf("fake") }
func (n *inv_fakeInnerNode) String(nodeID NodeID) string          { return "fakeInner" }
func (n *inv_fakeInnerNode) Invariants(isRoot bool) error         { return nil }
func (n *inv_fakeInnerNode) Clone() (Node, error) {
	c := &inv_fakeInnerNode{}
	c.hash = n.hash
	return c, nil
}

// inv_fakeLeafNode implements Node but does NOT implement LeafNode (no
// Item() / SetItem() methods). Used to cover the leaf type-assertion
// failure paths.
type inv_fakeLeafNode struct {
	baseNode
}

func (n *inv_fakeLeafNode) Type() NodeType                       { return NodeTypeAccountState }
func (n *inv_fakeLeafNode) UpdateHash() error                    { return nil }
func (n *inv_fakeLeafNode) SerializeForWire() ([]byte, error)    { return nil, fmt.Errorf("fake") }
func (n *inv_fakeLeafNode) SerializeWithPrefix() ([]byte, error) { return nil, fmt.Errorf("fake") }
func (n *inv_fakeLeafNode) String(nodeID NodeID) string          { return "fakeLeaf" }
func (n *inv_fakeLeafNode) Invariants(isRoot bool) error         { return nil }
func (n *inv_fakeLeafNode) Clone() (Node, error) {
	c := &inv_fakeLeafNode{}
	c.hash = n.hash
	return c, nil
}

// Used to cover the Clone() error path in verifyNodeHash.
type inv_cloneErrorNode struct {
	baseNode
}

func (n *inv_cloneErrorNode) Type() NodeType                       { return NodeTypeInner }
func (n *inv_cloneErrorNode) UpdateHash() error                    { return nil }
func (n *inv_cloneErrorNode) SerializeForWire() ([]byte, error)    { return nil, fmt.Errorf("fake") }
func (n *inv_cloneErrorNode) SerializeWithPrefix() ([]byte, error) { return nil, fmt.Errorf("fake") }
func (n *inv_cloneErrorNode) String(nodeID NodeID) string          { return "cloneError" }
func (n *inv_cloneErrorNode) Invariants(isRoot bool) error         { return nil }
func (n *inv_cloneErrorNode) Clone() (Node, error)                 { return nil, fmt.Errorf("clone always fails") }

// Used to cover the UpdateHash() error path in verifyNodeHash.
type inv_updateHashErrorNode struct {
	baseNode
}

func (n *inv_updateHashErrorNode) Type() NodeType                    { return NodeTypeInner }
func (n *inv_updateHashErrorNode) UpdateHash() error                 { return fmt.Errorf("update hash always fails") }
func (n *inv_updateHashErrorNode) SerializeForWire() ([]byte, error) { return nil, fmt.Errorf("fake") }
func (n *inv_updateHashErrorNode) SerializeWithPrefix() ([]byte, error) {
	return nil, fmt.Errorf("fake")
}
func (n *inv_updateHashErrorNode) String(nodeID NodeID) string  { return "updateHashError" }
func (n *inv_updateHashErrorNode) Invariants(isRoot bool) error { return nil }
func (n *inv_updateHashErrorNode) Clone() (Node, error) {
	c := &inv_updateHashErrorNode{}
	c.hash = n.hash
	return c, nil
}

func inv_makeValidMap(t *testing.T, n int) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for i := 1; i <= n; i++ {
		var k [32]byte
		k[0] = byte(i)
		if err := sm.Put(k, make([]byte, 12)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	return sm
}

func inv_makeKey(v byte) [32]byte {
	var k [32]byte
	k[0] = v
	return k
}

func TestInv_InvariantsUnsafe_InvalidStateNilRoot(t *testing.T) {
	sm := New(TypeState)
	sm.root = nil
	sm.state = StateInvalid

	if err := sm.invariants(); err == nil {
		t.Fatal("expected error for StateInvalid with nil root, got nil")
	}
}

func TestInv_InvariantsUnsafe_NilRootValidState(t *testing.T) {
	sm := New(TypeState)
	sm.root = nil
	sm.state = StateModifying

	if err := sm.invariants(); err != nil {
		t.Fatalf("expected nil for nil root + StateModifying, got: %v", err)
	}
}

// TestInv_NodeInvariantsHashMismatch covers the stale-preimage path inside
// innerNode.invariants() by corrupting hashes[] while keeping a live child.
// The error path ("node invariants check failed")
// fires before verifyNodeHash is reached.
func TestInv_NodeInvariantsHashMismatch(t *testing.T) {
	sm := New(TypeState)
	// Two keys sharing the same first nibble → root has an inner child at branch 0.
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	root.mu.RLock()
	childNode := root.children[0]
	root.mu.RUnlock()

	inner, ok := childNode.(*innerNode)
	if !ok {
		t.Skip("expected inner child at branch 0 for keys 0x01/0x02")
	}

	// Corrupt hashes[i] for inner's first non-empty branch so that
	// innerNode.invariants() → firstStalePreimage() fires.
	inner.mu.Lock()
	for i := 0; i < BranchFactor; i++ {
		if inner.isBranch&(1<<i) != 0 && inner.children[i] != nil {
			var bad [32]byte
			bad[0] = 0xFF
			inner.hashes[i] = bad
			break
		}
	}
	inner.mu.Unlock()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected invariant error for hash mismatch in inner node, got nil")
	}
	// Error comes from node.invariants() → "node invariants check failed" wrapping
	// "branch N hash mismatch".
	if !strings.Contains(err.Error(), "hash mismatch") && !strings.Contains(err.Error(), "node invariants check failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestInv_VerifyNodeHash_StalePreimage_Detailed covers the stale-preimage path
// inside verifyNodeHash via InvariantsDetailed, which
// continues checking after node.invariants() errors.
func TestInv_VerifyNodeHash_StalePreimage_Detailed(t *testing.T) {
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	root.mu.RLock()
	childNode := root.children[0]
	root.mu.RUnlock()

	inner, ok := childNode.(*innerNode)
	if !ok {
		t.Skip("expected inner child at branch 0 for keys 0x01/0x02")
	}

	inner.mu.Lock()
	for i := 0; i < BranchFactor; i++ {
		if inner.isBranch&(1<<i) != 0 && inner.children[i] != nil {
			var bad [32]byte
			bad[0] = 0xFF
			inner.hashes[i] = bad
			break
		}
	}
	inner.mu.Unlock()

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors in InvariantsDetailed for stale preimage, got none")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Description, "stale preimage") {
			found = true
			break
		}
	}
	if !found {
		// The stale preimage can also surface as "node invariants check failed"
		// wrapping "branch N hash mismatch" from innerNode.invariants().
		// Check that at least one error mentions hash mismatch.
		for _, e := range result.Errors {
			if strings.Contains(e.Description, "node invariants check failed") ||
				strings.Contains(e.Description, "hash mismatch") {
				found = true
				break
			}
			if e.Err != nil && strings.Contains(e.Err.Error(), "hash mismatch") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected stale preimage or hash mismatch error, got: %v", result.Errors)
	}
}

func TestInv_CheckInnerNode_ChildHashMismatch(t *testing.T) {
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	root.mu.RLock()
	childNode := root.children[0]
	root.mu.RUnlock()

	if _, ok := childNode.(*innerNode); !ok {
		t.Skip("expected inner child at branch 0")
	}

	// Corrupt root's stored hashes[0] after ensuring root's own hash is
	// up-to-date (so root passes verifyNodeHash via live-child path).
	// After the corruption, firstStalePreimage fires on root → invariant error.
	var bad [32]byte
	bad[0] = 0xDE
	bad[1] = 0xAD
	root.mu.Lock()
	root.hashes[0] = bad
	root.mu.Unlock()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected invariant error for child hash mismatch, got nil")
	}
}

// TestInv_CheckInnerNode_HasChildNilUnbacked covers the "branch N marked as
// non-empty but child is nil" path in checkInnerNodeInvariants. We flush a
// two-level map with releaseChildren=true so the inner child at depth 1 ends
// up with hash-only branches (nil children), then set sm.backed=false so the
// check fires.
func TestInv_CheckInnerNode_HasChildNilUnbacked(t *testing.T) {
	sm := New(TypeState)
	// Two keys sharing first nibble → root gets an inner child at branch 0.
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	// Flush with releaseChildren=true so inner children get their children
	// released (hash-only branches). Root's direct children are also released,
	// but root.isBranch bits and root.hashes[] stay set.
	if _, err := sm.FlushDirty(true); err != nil {
		t.Fatal(err)
	}

	// Now root has bit 0 set, hashes[0] non-zero, but children[0] == nil.
	// sm.backed is false; flip to backed=false explicitly (it already is) and
	// ensure sm.state != StateSyncing.
	sm.backed = false
	sm.state = StateModifying

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for non-empty branch with nil child in unbacked map, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty but child is nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInv_CheckLeafNode_NilItem(t *testing.T) {
	sm := New(TypeState)
	k := inv_makeKey(0x05)
	if err := sm.Put(k, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	var leaf *leafNode
	for i := 0; i < BranchFactor; i++ {
		root.mu.RLock()
		c := root.children[i]
		root.mu.RUnlock()
		if c == nil {
			continue
		}
		if l, ok := c.(*leafNode); ok {
			leaf = l
			break
		}
	}
	if leaf == nil {
		t.Skip("could not find AccountStateLeafNode directly under root")
	}

	leaf.mu.Lock()
	leaf.item = nil
	leaf.mu.Unlock()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for nil item in leaf, got nil")
	}
	if !strings.Contains(err.Error(), "nil item") {
		t.Fatalf("expected 'nil item' in error, got: %v", err)
	}
}

func TestInv_CheckLeafNode_InvalidItem(t *testing.T) {
	sm := New(TypeState)
	k := inv_makeKey(0x07)
	if err := sm.Put(k, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	var leaf *leafNode
	for i := 0; i < BranchFactor; i++ {
		root.mu.RLock()
		c := root.children[i]
		root.mu.RUnlock()
		if c == nil {
			continue
		}
		if l, ok := c.(*leafNode); ok {
			leaf = l
			break
		}
	}
	if leaf == nil {
		t.Skip("could not find leafNode directly under root")
	}

	// Replace item with one that has a zero key (Validate() returns error).
	badItem := NewItem([32]byte{}, make([]byte, 12))
	leaf.mu.Lock()
	leaf.item = badItem
	leaf.mu.Unlock()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for invalid item, got nil")
	}
	if !strings.Contains(err.Error(), "validation") && !strings.Contains(err.Error(), "zero key") && !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestInv_InvariantsDetailed_InvalidStateNilRoot(t *testing.T) {
	sm := New(TypeState)
	sm.root = nil

	result := sm.invariantsDetailed()
	if result.HasErrors() {
		t.Fatalf("expected no errors for nil root in InvariantsDetailed, got: %v", result.Errors)
	}
	if result.NodesChecked != 0 {
		t.Fatalf("expected 0 nodes checked, got %d", result.NodesChecked)
	}
}

func TestInv_InvariantsDetailed_NilItemLeaf(t *testing.T) {
	sm := New(TypeState)
	k := inv_makeKey(0x03)
	if err := sm.Put(k, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	var leaf *leafNode
	for i := 0; i < BranchFactor; i++ {
		root.mu.RLock()
		c := root.children[i]
		root.mu.RUnlock()
		if c == nil {
			continue
		}
		if l, ok := c.(*leafNode); ok {
			leaf = l
			break
		}
	}
	if leaf == nil {
		t.Skip("could not find leaf directly under root")
	}

	leaf.mu.Lock()
	leaf.item = nil
	leaf.mu.Unlock()

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for nil-item leaf in InvariantsDetailed")
	}
}

func TestInv_InvariantsDetailed_HasChildNilUnbacked(t *testing.T) {
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	if _, err := sm.FlushDirty(true); err != nil {
		t.Fatal(err)
	}
	sm.backed = false
	sm.state = StateModifying

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for hash-only branches in unbacked map")
	}
}

func TestInv_InvariantsDetailed_ChildHashMismatch(t *testing.T) {
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	root.mu.RLock()
	childNode := root.children[0]
	root.mu.RUnlock()

	if _, ok := childNode.(*innerNode); !ok {
		t.Skip("expected inner child at branch 0")
	}

	// Corrupt root's stored hash for branch 0, then re-sync root's own hash
	// using UpdateHash (which reads live children, not hashes[]).
	root.mu.Lock()
	var bad [32]byte
	bad[0] = 0xBA
	bad[1] = 0xD0
	root.hashes[0] = bad
	root.mu.Unlock()
	// Root's own hash update: UpdateHash acquires the lock internally.
	if err := root.UpdateHash(); err != nil {
		t.Fatal(err)
	}
	// Re-corrupt hashes[0] after the update so checkInnerNodeInvariants fires.
	root.mu.Lock()
	root.hashes[0] = bad
	root.mu.Unlock()

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for child hash mismatch in InvariantsDetailed")
	}
}

func TestInv_VerifyHashes_NilRoot(t *testing.T) {
	sm := New(TypeState)
	sm.root = nil
	if err := sm.verifyHashes(); err != nil {
		t.Fatalf("VerifyHashes with nil root should return nil, got: %v", err)
	}
}

func TestInv_VerifyHashes_ValidMap(t *testing.T) {
	sm := inv_makeValidMap(t, 20)
	if err := sm.verifyHashes(); err != nil {
		t.Fatalf("VerifyHashes should pass on valid map: %v", err)
	}
}

func TestInv_VerifyHashes_StalePreimage(t *testing.T) {
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	root.mu.RLock()
	childNode := root.children[0]
	root.mu.RUnlock()

	inner, ok := childNode.(*innerNode)
	if !ok {
		t.Skip("expected inner child at branch 0")
	}

	// Corrupt inner's hashes[] for one branch while keeping the live child.
	inner.mu.Lock()
	for i := 0; i < BranchFactor; i++ {
		if inner.isBranch&(1<<i) != 0 && inner.children[i] != nil {
			var bad [32]byte
			bad[0] = 0xFF
			inner.hashes[i] = bad
			break
		}
	}
	inner.mu.Unlock()

	if err := sm.verifyHashes(); err == nil {
		t.Fatal("expected VerifyHashes to detect stale preimage, got nil")
	}
}

func TestInv_InvariantsDetailed_ValidLargeMap(t *testing.T) {
	sm := inv_makeValidMap(t, 50)
	result := sm.invariantsDetailed()
	if result.HasErrors() {
		t.Fatalf("InvariantsDetailed should pass on valid map: %v", result.Errors)
	}
	if result.NodesChecked == 0 {
		t.Fatal("expected nodes to be checked")
	}
	if result.LeavesChecked == 0 {
		t.Fatal("expected leaves to be checked")
	}
	if result.InnerNodesChecked == 0 {
		t.Fatal("expected inner nodes to be checked")
	}
}

func TestInv_InvariantsDetailed_InvalidItem(t *testing.T) {
	sm := New(TypeState)
	k := inv_makeKey(0x09)
	if err := sm.Put(k, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	var leaf *leafNode
	for i := 0; i < BranchFactor; i++ {
		root.mu.RLock()
		c := root.children[i]
		root.mu.RUnlock()
		if c == nil {
			continue
		}
		if l, ok := c.(*leafNode); ok {
			leaf = l
			break
		}
	}
	if leaf == nil {
		t.Skip("could not find leaf directly under root")
	}

	// Swap to a zero-key item to trigger Validate() failure.
	leaf.mu.Lock()
	leaf.item = NewItem([32]byte{}, make([]byte, 12))
	leaf.mu.Unlock()

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for zero-key item in InvariantsDetailed")
	}
}

// The caller is responsible for ensuring the node has a non-zero hash before
// calling this function (so root.invariants() "bit set but no hash" doesn't fire).
func inv_injectNodeIntoRoot(root *innerNode, branch int, child Node) {
	root.mu.Lock()
	root.children[branch] = child
	root.hashes[branch] = child.Hash()
	root.isBranch |= 1 << branch
	root.mu.Unlock()
	_ = root.UpdateHash()
}

func TestInv_CheckNodeInvariants_NotInnerNode(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_fakeInnerNode{}
	fake.hash[0] = 0xAB // non-zero so root.invariants() "bit set but no hash" doesn't fire
	inv_injectNodeIntoRoot(sm.root, 3, fake)

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for foreign non-inner node, got nil")
	}
	if !strings.Contains(err.Error(), "LeafNode") {
		t.Fatalf("expected LeafNode-implementation error, got: %v", err)
	}
}

func TestInv_CheckLeafNode_NotLeafNode(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_fakeLeafNode{}
	fake.hash[0] = 0xAB
	inv_injectNodeIntoRoot(sm.root, 4, fake)

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for foreign non-LeafNode, got nil")
	}
	if !strings.Contains(err.Error(), "LeafNode") {
		t.Fatalf("expected 'LeafNode' in error, got: %v", err)
	}
}

func TestInv_CheckNodeDetailed_NotInnerNode(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_fakeInnerNode{}
	fake.hash[0] = 0xCD
	inv_injectNodeIntoRoot(sm.root, 5, fake)

	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for foreign non-inner node in invariantsDetailed")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Description, "LeafNode") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected LeafNode-implementation error in detailed result, got: %v", result.Errors)
	}
}

func TestInv_VerifyHashes_NotInnerNode(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_fakeInnerNode{}
	fake.hash[0] = 0xEF
	inv_injectNodeIntoRoot(sm.root, 6, fake)

	// verifyHashes only checks hash integrity; a hash-consistent foreign
	// node has no children to recurse into and passes. Structural checks
	// are invariants()' job.
	if err := sm.verifyHashes(); err != nil {
		t.Fatalf("verifyHashes should ignore structure for foreign node, got: %v", err)
	}
}

func TestInv_VerifyNodeHash_CloneError(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_cloneErrorNode{}
	fake.hash[0] = 0x42 // non-zero so root.invariants() passes
	root := sm.root
	root.mu.Lock()
	root.children[7] = fake
	root.hashes[7] = fake.Hash()
	root.isBranch |= 1 << 7
	root.mu.Unlock()
	_ = root.UpdateHash()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for Clone() failure in verifyNodeHash, got nil")
	}
	if !strings.Contains(err.Error(), "clone node") && !strings.Contains(err.Error(), "failed to clone") {
		t.Fatalf("expected clone error, got: %v", err)
	}
}

func TestInv_VerifyNodeHash_UpdateHashError(t *testing.T) {
	sm := New(TypeState)

	fake := &inv_updateHashErrorNode{}
	fake.hash[0] = 0x55
	root := sm.root
	root.mu.Lock()
	root.children[8] = fake
	root.hashes[8] = fake.Hash()
	root.isBranch |= 1 << 8
	root.mu.Unlock()
	_ = root.UpdateHash()

	err := sm.invariants()
	if err == nil {
		t.Fatal("expected error for UpdateHash() failure in verifyNodeHash, got nil")
	}
	if !strings.Contains(err.Error(), "recompute hash") && !strings.Contains(err.Error(), "failed to recompute") {
		t.Fatalf("expected recompute hash error, got: %v", err)
	}
}

func TestInv_DescendError_Invariants(t *testing.T) {
	// Create a map, populate it with a key that shares the first nibble
	// with another key so we get a non-trivial tree, flush to a good family,
	// then switch to a failing family so that lazy loading triggers an error.
	goodFamily := newMemoryFamily()
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(sm, goodFamily); err != nil {
		t.Fatal(err)
	}

	// Recreate from root hash — this gives us a backed map with hash-only
	// branches (nil children). Switch its family to a failing one so the
	// next descend call returns an error.
	backed, err := NewFromRootHash(TypeState, rootHash, goodFamily)
	if err != nil {
		t.Fatal(err)
	}
	backed.family = &inv_failingFamily{}

	err = backed.invariants()
	if err == nil {
		t.Fatal("expected error from descend() failure in Invariants, got nil")
	}
	if !strings.Contains(err.Error(), "failed to get child") && !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("expected fetch error, got: %v", err)
	}
}

func TestInv_DescendError_InvariantsDetailed(t *testing.T) {
	goodFamily := newMemoryFamily()
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(sm, goodFamily); err != nil {
		t.Fatal(err)
	}

	backed, err := NewFromRootHash(TypeState, rootHash, goodFamily)
	if err != nil {
		t.Fatal(err)
	}
	backed.family = &inv_failingFamily{}

	result := backed.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors from descend() failure in InvariantsDetailed, got none")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Description, "failed to get child") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'failed to get child' error in detailed result, got: %v", result.Errors)
	}
}

func TestInv_DescendError_VerifyHashes(t *testing.T) {
	goodFamily := newMemoryFamily()
	sm := New(TypeState)
	k1 := inv_makeKey(0x01)
	k2 := inv_makeKey(0x02)
	if err := sm.Put(k1, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(k2, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(sm, goodFamily); err != nil {
		t.Fatal(err)
	}

	backed, err := NewFromRootHash(TypeState, rootHash, goodFamily)
	if err != nil {
		t.Fatal(err)
	}
	backed.family = &inv_failingFamily{}

	if err := backed.verifyHashes(); err == nil {
		t.Fatal("expected error from descend() failure in VerifyHashes, got nil")
	}
}

func TestInv_CheckInnerNode_EmptyBranchChildExists(t *testing.T) {
	sm := New(TypeState)
	k := inv_makeKey(0x05)
	if err := sm.Put(k, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	root := sm.root
	var leaf Node
	root.mu.RLock()
	for i := 0; i < BranchFactor; i++ {
		if root.children[i] != nil {
			leaf = root.children[i]
			break
		}
	}
	root.mu.RUnlock()
	if leaf == nil {
		t.Skip("could not find any child under root")
	}

	emptyBranch := -1
	root.mu.RLock()
	for i := 0; i < BranchFactor; i++ {
		if root.isBranch&(1<<i) == 0 {
			emptyBranch = i
			break
		}
	}
	root.mu.RUnlock()
	if emptyBranch < 0 {
		t.Skip("no empty branch available on root")
	}

	root.mu.Lock()
	root.children[emptyBranch] = leaf
	// Deliberately do NOT set isBranch bit or hashes[].
	root.mu.Unlock()

	// Root's Invariants() will now catch "child present but bit not set" before
	// checkInnerNodeInvariants is reached. But checkInnerNodeInvariants also
	// catches "!hasChild && child != nil" independently. Since node.invariants()
	// fires first, the InvariantsDetailed path can reach both.
	result := sm.invariantsDetailed()
	if !result.HasErrors() {
		t.Fatal("expected errors for child in empty branch, got none")
	}
}
