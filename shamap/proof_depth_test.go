package shamap

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

func deepProofKeys() ([32]byte, [32]byte) {
	var first, second [32]byte
	for i := range first {
		first[i] = 0xAB
		second[i] = 0xAB
	}
	first[31] = 0xA1
	second[31] = 0xA2
	return first, second
}

func TestProofPathDeepBoundary(t *testing.T) {
	first, second := deepProofKeys()
	sm := New(TypeState)
	for _, key := range [][32]byte{first, second} {
		if err := sm.Put(key, bytes.Repeat([]byte{1}, 12)); err != nil {
			t.Fatal(err)
		}
	}

	root, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range [][32]byte{first, second} {
		proof, err := sm.GetProofPath(key)
		if err != nil {
			t.Fatal(err)
		}
		if !proof.Found || len(proof.Path) != maxDepth+1 {
			t.Fatalf("deep proof = found %v, path length %d; want found and %d nodes", proof.Found, len(proof.Path), maxDepth+1)
		}
		if !VerifyProofPath(root, key, proof.Path) {
			t.Fatalf("deep proof for %x did not verify", key[:4])
		}
	}
}

func innerProofNode(t *testing.T, branch int, childHash [32]byte) ([]byte, [32]byte) {
	t.Helper()
	data := make([]byte, BranchFactor*32+1)
	copy(data[branch*32:], childHash[:])
	data[len(data)-1] = byte(protocol.WireTypeInner)
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.UpdateHash(); err != nil {
		t.Fatal(err)
	}
	return data, node.Hash()
}

func malformedInnerProof(t *testing.T, key [32]byte) ([32]byte, [][]byte) {
	t.Helper()
	path := make([][]byte, 0, maxDepth+1)
	childHash := makeHash(0xA5)
	for depth := maxDepth; ; depth-- {
		branchDepth := depth
		if branchDepth >= maxDepth {
			branchDepth = maxDepth - 1
		}
		branch := getBranchAtDepth(key, branchDepth)
		blob, hash := innerProofNode(t, branch, childHash)
		path = append(path, blob)
		childHash = hash
		if depth == 0 {
			break
		}
	}
	return childHash, path
}

func TestVerifyProofPathRejectsInnerAtLeafDepth(t *testing.T) {
	key, _ := deepProofKeys()
	root, path := malformedInnerProof(t, key)
	if len(path) != maxDepth+1 {
		t.Fatalf("malformed path length = %d, want %d", len(path), maxDepth+1)
	}
	if VerifyProofPath(root, key, path) {
		t.Fatal("inner node at leaf depth verified")
	}
}

func TestVerifyProofPathRejectsInvalidInnerNode(t *testing.T) {
	data := make([]byte, BranchFactor*32+1)
	data[len(data)-1] = byte(protocol.WireTypeInner)
	if VerifyProofPath([32]byte{}, [32]byte{}, [][]byte{data}) {
		t.Fatal("empty inner node verified")
	}
}

func TestVerifyProofPathRejectsSubstitutedTerminalLeaf(t *testing.T) {
	key := [32]byte{0x10}
	otherKey := [32]byte{0x11}
	leaf, err := newAccountStateLeafNode(NewItem(otherKey, bytes.Repeat([]byte{1}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	root := newInnerNode()
	if err := root.SetChild(int(selectBranch(newRootNodeID(), key)), leaf); err != nil {
		t.Fatal(err)
	}
	rootWire, err := root.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	leafWire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	path := [][]byte{leafWire, rootWire}
	if !VerifyProofPath(root.Hash(), otherKey, path) {
		t.Fatal("proof for the stored leaf did not verify")
	}
	if VerifyProofPath(root.Hash(), key, path) {
		t.Fatal("hash-valid proof verified for the wrong terminal key")
	}
}

func TestProofPathEmptyMap(t *testing.T) {
	sm := New(TypeState)
	key := [32]byte{1}
	root, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := sm.GetProofPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Found || len(proof.Path) != 0 {
		t.Fatalf("empty-map proof = %+v, want absent proof", proof)
	}
	if VerifyProofPath(root, key, proof.Path) {
		t.Fatal("empty-map proof verified")
	}
}
