package shamap

import (
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"
)

// staleHash is an obviously-wrong preimage used to simulate an inner node whose
// cached hashes[i] drifted out of sync with its live child.
var staleHash = [32]byte{0xAB, 0xAB, 0xAB, 0xAB}

func newInnerWithLeaf(t *testing.T) (*InnerNode, *AccountStateLeafNode) {
	t.Helper()
	inner := NewInnerNode()
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	leaf, err := NewAccountStateLeafNode(NewItem(key, intToBytes(1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.SetChild(0, leaf); err != nil {
		t.Fatal(err)
	}
	return inner, leaf
}

// TestUpdateHashDeep_ResyncsStalePreimage verifies updateHashDeep pulls every
// loaded child's hash back into the cache and that the resulting serialized
// preimage hashes to the node's own hash.
func TestUpdateHashDeep_ResyncsStalePreimage(t *testing.T) {
	inner, leaf := newInnerWithLeaf(t)
	good := inner.Hash()

	// Corrupting the cache must not touch the node hash (it derives from the
	// live child), which is exactly what makes a stale preimage invisible.
	inner.hashes[0] = staleHash
	if inner.Hash() != good {
		t.Fatal("corrupting the cached preimage must not change the node hash")
	}

	if err := inner.updateHashDeep(); err != nil {
		t.Fatalf("updateHashDeep: %v", err)
	}
	if inner.hashes[0] != leaf.Hash() {
		t.Errorf("hashes[0] not resynced: got %x want %x", inner.hashes[0][:8], leaf.Hash())
	}
	if inner.Hash() != good {
		t.Errorf("node hash changed after resync: got %x want %x", inner.Hash(), good)
	}

	data, err := inner.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}
	if got := common.Sha512Half(data); got != good {
		t.Errorf("serialized preimage hashes to %x, want %x", got[:8], good[:8])
	}
}

// TestFlushNode_GuardsStalePreimage verifies the flush-time updateHashDeep guard
// rewrites a stale cached branch hash so the flushed bytes hash to the node's
// hash, mirroring rippled's walkSubTree (SHAMap.cpp:1139).
func TestFlushNode_GuardsStalePreimage(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}
	for i, keyHex := range keys {
		if err := sm.Put(hexToHash(keyHex), intToBytes(i+1)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	root := sm.root
	if root == nil {
		t.Fatal("root is nil after Put")
	}
	branch := -1
	for i := range BranchFactor {
		if root.children[i] != nil {
			branch = i
			break
		}
	}
	if branch < 0 {
		t.Fatal("root has no loaded child")
	}

	rootHash := root.Hash()
	root.hashes[branch] = staleHash
	root.dirty = true

	batch, err := sm.FlushDirty(false)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}

	var rootData []byte
	for _, e := range batch.Entries {
		if e.Hash == rootHash {
			rootData = e.Data
			break
		}
	}
	if rootData == nil {
		t.Fatal("root node not present in flush batch")
	}
	if got := common.Sha512Half(rootData); got != rootHash {
		t.Errorf("flushed root preimage hashes to %x, want %x", got[:8], rootHash[:8])
	}
}

// TestVerifyNodeHash_DetectsStalePreimage verifies the strengthened invariant
// catches a stale loaded-child preimage that the clone+recompute check, which
// derives from live children, cannot see.
func TestVerifyNodeHash_DetectsStalePreimage(t *testing.T) {
	sm, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := newInnerWithLeaf(t)

	if err := sm.verifyNodeHash(inner, NewRootNodeID()); err != nil {
		t.Fatalf("healthy inner node should pass: %v", err)
	}

	inner.hashes[0] = staleHash
	if err := sm.verifyNodeHash(inner, NewRootNodeID()); err == nil {
		t.Fatal("verifyNodeHash must detect a stale loaded-child preimage")
	}
}
