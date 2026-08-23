package shamap

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"
)

// Helper function to create a byte slice filled with a repeating byte
func intToBytes(v int) []byte {
	data := make([]byte, 32)
	for i := range 32 {
		data[i] = byte(v)
	}
	return data
}

// Helper function to create a SHAMapItem with the given key and value
func makeItem(key [32]byte, value []byte) *Item {
	return NewItem(key, value)
}

// Parse hex string to actual bytes (like rippled does)
func hexToHash(s string) [32]byte {
	var hash [32]byte
	decoded, err := hex.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("Invalid hex string: %s - %v", s, err))
	}
	if len(decoded) != 32 {
		panic(fmt.Sprintf("Hex string is not 32 bytes: %s (got %d bytes)", s, len(decoded)))
	}
	copy(hash[:], decoded)
	return hash
}

// TestAddAndTraverse tests adding items to a SHAMap and traversing it

func TestBuildAndTear(t *testing.T) {
	// Same keys as rippled C++ test
	keys := [][32]byte{
		hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("b91891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("f22891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
		hexToHash("292891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"),
	}

	// Expected hashes after each addition - same as rippled C++ test
	expectedHashes := [][32]byte{
		hexToHash("B7387CFEA0465759ADC718E8C42B52D2309D179B326E239EB5075C64B6281F7F"),
		hexToHash("FBC195A9592A54AB44010274163CB6BA95F497EC5BA0A8831845467FB2ECE266"),
		hexToHash("4E7D2684B65DFD48937FFB775E20175C43AF0C94066F7D5679F51AE756795B75"),
		hexToHash("7A2F312EB203695FFD164E038E281839EEF06A1B99BFC263F3CECC6C74F93E07"),
		hexToHash("395A6691A372387A703FB0F2C6D2C405DAF307D0817F8F0E207596462B0E3A3E"),
		hexToHash("D044C0A696DE3169CC70AE216A1564D69DE96582865796142CE7D98A84D9DDE4"),
		hexToHash("76DCC77C4027309B5A91AD164083264D70B77B5E43E08AEDA5EBF94361143615"),
		hexToHash("DF4220E93ADC6F5569063A01B4DC79F8DB9553B6A3222ADE23DEA02BBE7230E5"),
	}

	// Create a SHAMap
	sMap := New(TypeTransaction)

	// Verify empty map has zero hash
	emptyHash, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get empty map hash: %v", err)
	}
	zeroHash := [32]byte{} // All zeros
	if emptyHash != zeroHash {
		t.Errorf("Empty map should have zero hash, got %x", emptyHash)
	}

	// Add all keys and verify hash after each addition
	for k, key := range keys {
		if err := sMap.Put(key, intToBytes(k)); err != nil {
			t.Fatalf("Failed to add item %d: %v", k, err)
		}

		// Verify hash matches expected
		actualHash, err := sMap.Hash()
		if err != nil {
			t.Fatalf("Failed to get hash after adding item %d: %v", k, err)
		}

		if actualHash != expectedHashes[k] {
			t.Errorf("Tree dump after adding item %d (hash mismatch):", k)
			dumpTree(sMap.tree.root, "", false)
			t.Errorf("Hash mismatch after adding item %d: expected %x, got %x",
				k, expectedHashes[k], actualHash)
		}
	}

	// Delete all keys in reverse order and verify hashes
	// Delete all keys in reverse order and verify hashes
	for k := len(keys) - 1; k >= 0; k-- {
		// Verify hash BEFORE deletion matches expected
		actualHash, err := sMap.Hash()
		if err != nil {
			t.Fatalf("Failed to get hash before deleting item %d: %v", k, err)
		}
		if actualHash != expectedHashes[k] {
			t.Errorf("Tree dump after adding item %d (hash mismatch):", k)
			dumpTree(sMap.tree.root, "", false)
			t.Errorf("Hash mismatch after adding item %d: expected %x, got %x",
				k, expectedHashes[k], actualHash)
		}

		if err := sMap.Delete(keys[k]); err != nil {
			t.Fatalf("Failed to delete item %d: %v", k, err)
		}

		// Verify item is actually deleted
		_, found, err := sMap.Get(keys[k])
		if err != nil {
			t.Fatalf("Error checking deleted item %d: %v", k, err)
		}
		if found {
			t.Errorf("Item %d should have been deleted", k)
		}

		// Optional: Check invariants if you have that method
		// if err := sMap.invariants(); err != nil {
		//     t.Fatalf("Invariants check failed after deleting item %d: %v", k, err)
		// }
	}

	// Final check - map should be empty (zero hash)
	finalHash, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get final hash: %v", err)
	}
	if finalHash != zeroHash {
		t.Errorf("Final map should have zero hash, got %x", finalHash)
	}
}

func TestSnapshot_StructuralSharing(t *testing.T) {
	src := New(TypeState)

	// 256 keys whose first nibble fans out across all 16 root branches.
	keys := make([][32]byte, 0, 256)
	for i := range 256 {
		var k [32]byte
		k[0] = byte(i)
		k[31] = byte(i)
		keys = append(keys, k)
		if err := src.Put(k, intToBytes(i+1)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}

	srcHashBefore, err := src.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	snap, err := src.SnapshotImmutable()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Mutate exactly one key in the source; the snapshot must not move.
	target := keys[42]
	if err := src.Put(target, intToBytes(0xff)); err != nil {
		t.Fatalf("Put(target): %v", err)
	}

	snapHashAfter, err := snap.Hash()
	if err != nil {
		t.Fatalf("snap.Hash: %v", err)
	}
	if snapHashAfter != srcHashBefore {
		t.Fatalf("snapshot hash drifted after source mutation: got %x want %x", snapHashAfter, srcHashBefore)
	}

	srcHashAfter, err := src.Hash()
	if err != nil {
		t.Fatalf("src.Hash after mutation: %v", err)
	}
	if srcHashAfter == srcHashBefore {
		t.Fatal("source hash unchanged after mutation — mutation did not actually happen")
	}

	// Structural sharing: every root branch that does NOT lead to the
	// mutated key must still point at the exact same child pointer in
	// both maps. The single branch on the mutated path is allowed to
	// diverge (and must, otherwise we mutated through a shared node).
	mutatedBranch := getBranchAtDepth(target, 0)
	shared, diverged := 0, 0
	for i := range BranchFactor {
		srcChild, _, srcSet := src.tree.root.LoadChild(i)
		snapChild, _, snapSet := snap.tree.root.LoadChild(i)
		if srcSet != snapSet {
			t.Fatalf("branch %d set bit diverged: src=%v snap=%v", i, srcSet, snapSet)
		}
		if !srcSet {
			continue
		}
		if i == mutatedBranch {
			if srcChild == snapChild {
				t.Fatalf("branch %d (mutated path) shares pointer: in-place mutation broke snapshot isolation", i)
			}
			diverged++
			continue
		}
		if srcChild != snapChild {
			t.Errorf("branch %d (untouched) does NOT share pointer: src=%p snap=%p — snapshot is deep-cloning instead of path-copying", i, srcChild, snapChild)
		} else {
			shared++
		}
	}
	if shared == 0 {
		t.Fatal("no branches shared between source and snapshot — Snapshot is doing a full deep clone")
	}
	if diverged != 1 {
		t.Fatalf("expected exactly 1 diverged root branch on the mutated path, got %d", diverged)
	}

	// Sanity: snapshot still resolves every original key.
	for i, k := range keys {
		item, found, err := snap.Get(k)
		if err != nil {
			t.Fatalf("snap.Get(%d): %v", i, err)
		}
		if !found {
			t.Fatalf("snap missing key %d after source mutation", i)
		}
		want := intToBytes(i + 1)
		if !bytes.Equal(item.Data(), want) {
			t.Fatalf("snap key %d: data drifted from original value", i)
		}
	}
}

// TestImmutability tests that an immutable map cannot be modified

func TestConcurrency(t *testing.T) {
	sMap := New(TypeState)

	// Add some initial data
	for i := range 10 {
		key := [32]byte{}
		key[0] = byte(i)
		if err := sMap.Put(key, intToBytes(i)); err != nil {
			t.Fatalf("Failed to add initial item %d: %v", i, err)
		}
	}

	// Create immutable snapshot for concurrent reading
	snapshot, err := sMap.SnapshotImmutable()
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}

	// Test concurrent reads on snapshot (should be safe)
	done := make(chan bool, 10)
	for i := range 10 {
		go func(id int) {
			defer func() { done <- true }()

			key := [32]byte{}
			key[0] = byte(id)

			item, found, err := snapshot.Get(key)
			if err != nil {
				t.Errorf("Concurrent read %d failed: %v", id, err)
				return
			}
			if !found {
				t.Errorf("Concurrent read %d: item not found", id)
				return
			}
			if !bytes.Equal(item.Data(), intToBytes(id)) {
				t.Errorf("Concurrent read %d: data mismatch", id)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for range 10 {
		<-done
	}
}

// Benchmarks

func BenchmarkPut(b *testing.B) {
	sMap := New(TypeTransaction)

	keys := make([][32]byte, b.N)
	for i := 0; i < b.N; i++ {
		// Create pseudo-random keys
		copy(keys[i][:], fmt.Sprintf("%032d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sMap.Put(keys[i], intToBytes(i)); err != nil {
			b.Fatalf("Failed to put item %d: %v", i, err)
		}
	}
}

func BenchmarkGet(b *testing.B) {
	sMap := New(TypeTransaction)

	// Pre-populate the map
	keys := make([][32]byte, 1000)
	for i := range 1000 {
		copy(keys[i][:], fmt.Sprintf("%032d", i))
		if err := sMap.Put(keys[i], intToBytes(i)); err != nil {
			b.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%1000]
		_, _, err := sMap.Get(key)
		if err != nil {
			b.Fatalf("Failed to get item: %v", err)
		}
	}
}

func BenchmarkSnapshot(b *testing.B) {
	sMap := New(TypeTransaction)

	// Pre-populate the map
	for i := range 1000 {
		key := [32]byte{}
		copy(key[:], fmt.Sprintf("%032d", i))
		if err := sMap.Put(key, intToBytes(i)); err != nil {
			b.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := sMap.SnapshotImmutable()
		if err != nil {
			b.Fatalf("Failed to create snapshot: %v", err)
		}
	}
}

// Helper function for debugging - simplified tree dump
func dumpTree(node mapNode, prefix string, isTail bool) {
	switch n := node.(type) {
	case *innerNode:
		fmt.Printf("%s%sInnerNode %p, hash: %x\n", prefix, branchSymbol(isTail), n, n.Hash())

		// Get all non-empty children
		var children []struct {
			index int
			child mapNode
		}
		for i := range BranchFactor {
			if !n.IsEmptyBranch(i) {
				if child, err := n.Child(i); err == nil && child != nil {
					children = append(children, struct {
						index int
						child mapNode
					}{index: i, child: child})
				}
			}
		}

		for i, c := range children {
			fmt.Printf("%s%s[Branch %x]\n", prefix, pipeSymbol(isTail), c.index)
			dumpTree(c.child, nextPrefix(prefix, isTail), i == len(children)-1)
		}

	case *leafNode:
		leafName := "?"
		switch n.Type() {
		case NodeTypeAccountState:
			leafName = "Account"
		case NodeTypeTransactionNoMeta:
			leafName = "Tx"
		case NodeTypeTransactionWithMeta:
			leafName = "Tx+Meta"
		}
		fmt.Printf("%s%sLeaf(%s) %p, key: %x\n", prefix, branchSymbol(isTail), leafName, n, n.Item().Key())
	default:
		fmt.Printf("%s%sUnknown node type: %T\n", prefix, branchSymbol(isTail), n)
	}
}

func branchSymbol(isTail bool) string {
	if isTail {
		return "└── "
	}
	return "├── "
}

func pipeSymbol(isTail bool) string {
	if isTail {
		return "    "
	}
	return "│   "
}

func nextPrefix(current string, isTail bool) string {
	if isTail {
		return current + "    "
	}
	return current + "│   "
}

// TestProofPath tests Merkle proof generation and verification
// This test matches the C++ SHAMap_test.cpp proof path test
func TestSHAMapPathProof(t *testing.T) {
	sMap := New(TypeState)

	var key [32]byte
	var rootHash [32]byte
	var goodPath [][]byte

	// Add items 1-99 (matching C++ test exactly)
	for c := byte(1); c < 100; c++ {
		// Create key like C++ uint256(c)
		var k [32]byte
		k[0] = c

		// Create data from key itself
		data := make([]byte, 32)
		copy(data, k[:])

		// Add item to map
		if err := sMap.Put(k, data); err != nil {
			t.Fatalf("Failed to add item %d: %v", c, err)
		}

		// Get current root hash
		root, err := sMap.Hash()
		if err != nil {
			t.Fatalf("Failed to get root hash for item %d: %v", c, err)
		}

		// Get proof path for this key
		proofPath, err := sMap.GetProofPath(k)
		if err != nil {
			t.Fatalf("Failed to get proof path for item %d: %v", c, err)
		}

		// path should not be nil and should be found
		if proofPath == nil {
			t.Fatalf("Got nil proof path for item %d", c)
		}
		if !proofPath.Found {
			t.Fatalf("Proof path not found for item %d", c)
		}

		// Verify the proof path
		if !VerifyProofPath(root, k, proofPath.Path) {
			t.Errorf("Proof verification failed for item %d", c)
		}

		// Special handling for c == 1
		if c == 1 {
			// Test: extra node (insert duplicate at beginning)
			extraNodePath := make([][]byte, len(proofPath.Path)+1)
			extraNodePath[0] = make([]byte, len(proofPath.Path[0]))
			copy(extraNodePath[0], proofPath.Path[0])
			copy(extraNodePath[1:], proofPath.Path)

			if VerifyProofPath(root, k, extraNodePath) {
				t.Error("Proof with extra node should have failed")
			}

			// Test: wrong key (non-existent)
			var wrongKey [32]byte
			wrongKey[0] = c + 100 // Use a key that doesn't exist

			wrongProof, err := sMap.GetProofPath(wrongKey)
			if err != nil {
				t.Errorf("GetProofPath for non-existent key should not error: %v", err)
			}
			if wrongProof != nil && wrongProof.Found {
				t.Error("Should not find proof for non-existent key")
			}
		}

		// Save data for c == 99
		if c == 99 {
			key = k
			rootHash = root
			// Deep copy the proof path
			goodPath = make([][]byte, len(proofPath.Path))
			for i, node := range proofPath.Path {
				goodPath[i] = make([]byte, len(node))
				copy(goodPath[i], node)
			}
		}
	}

	// Test: saved path should still be valid
	if !VerifyProofPath(rootHash, key, goodPath) {
		t.Error("Saved good path should still be valid")
	}

	// Test: empty path should fail
	if VerifyProofPath(rootHash, key, [][]byte{}) {
		t.Error("Empty path should fail verification")
	}

	if len(goodPath) > 0 {
		tooLongPath := make([][]byte, maxDepth+2)
		for i := range tooLongPath {
			tooLongPath[i] = make([]byte, len(goodPath[0]))
			copy(tooLongPath[i], goodPath[0])
		}
		if VerifyProofPath(rootHash, key, tooLongPath) {
			t.Error("Too long path should fail verification")
		}
	}

	// Test: bad node data
	if len(goodPath) > 0 {
		badNodePath := [][]byte{make([]byte, 100)}
		for i := range badNodePath[0] {
			badNodePath[0][i] = 100
		}
		if VerifyProofPath(rootHash, key, badNodePath) {
			t.Error("Bad node data should fail verification")
		}
	}

	// Test: bad wire type
	if len(goodPath) > 0 && len(goodPath[0]) > 0 {
		badTypePath := make([][]byte, len(goodPath))
		for i, node := range goodPath {
			badTypePath[i] = make([]byte, len(node))
			copy(badTypePath[i], node)
		}
		// Change the wire type (last byte) to make it invalid
		badTypePath[0][len(badTypePath[0])-1] = 255
		if VerifyProofPath(rootHash, key, badTypePath) {
			t.Error("Bad wire type should fail verification")
		}
	}

	// Test: path without leaf (remove first node)
	if len(goodPath) > 1 {
		noLeafPath := make([][]byte, len(goodPath)-1)
		copy(noLeafPath, goodPath[1:])
		if VerifyProofPath(rootHash, key, noLeafPath) {
			t.Error("Path without leaf should fail verification")
		}
	}

	// Test: wrong root hash
	var wrongRoot [32]byte
	wrongRoot[0] = 0xFF
	if VerifyProofPath(wrongRoot, key, goodPath) {
		t.Error("Wrong root hash should fail verification")
	}

	// Test: wrong key
	var wrongKey [32]byte
	wrongKey[0] = 0xFF
	if VerifyProofPath(rootHash, wrongKey, goodPath) {
		t.Error("Wrong key should fail verification")
	}
}

// TestUpperBound tests the UpperBound functionality

func TestBoundsMatchingCppTestVectors(t *testing.T) {
	// Helper to create a key from an integer (matching C++ uint256(n))
	makeKey := func(n int) [32]byte {
		var key [32]byte
		// uint256 in rippled stores the value in big-endian at the END of the array
		// For small values, this means key[31] = n for n < 256
		key[31] = byte(n)
		return key
	}

	// Helper to setup a map with given values
	setup := func(values []int) *SHAMap {
		sMap := New(TypeState)
		for _, v := range values {
			key := makeKey(v)
			data := make([]byte, 32)
			data[31] = byte(v)
			if err := sMap.Put(key, data); err != nil {
				t.Fatalf("Failed to put item %d: %v", v, err)
			}
		}
		return sMap
	}

	// Helper to check lower_bound result
	checkLowerBound := func(sMap *SHAMap, searchVal int, expectVal int, expectEnd bool, desc string) {
		searchKey := makeKey(searchVal)
		iter := sMap.LowerBound(searchKey)
		if expectEnd {
			if iter.Valid() {
				t.Errorf("%s: lower_bound(%d) expected end, got key %d", desc, searchVal, iter.Item().Key()[31])
			}
		} else {
			if !iter.Valid() {
				t.Errorf("%s: lower_bound(%d) expected key %d, got end", desc, searchVal, expectVal)
			} else if iter.Item().Key()[31] != byte(expectVal) {
				t.Errorf("%s: lower_bound(%d) expected key %d, got %d", desc, searchVal, expectVal, iter.Item().Key()[31])
			}
		}
	}

	// Helper to check upper_bound result
	checkUpperBound := func(sMap *SHAMap, searchVal int, expectVal int, expectEnd bool, desc string) {
		searchKey := makeKey(searchVal)
		iter := sMap.UpperBound(searchKey)
		if expectEnd {
			if iter.Valid() {
				t.Errorf("%s: upper_bound(%d) expected end, got key %d", desc, searchVal, iter.Item().Key()[31])
			}
		} else {
			if !iter.Valid() {
				t.Errorf("%s: upper_bound(%d) expected key %d, got end", desc, searchVal, expectVal)
			} else if iter.Item().Key()[31] != byte(expectVal) {
				t.Errorf("%s: upper_bound(%d) expected key %d, got %d", desc, searchVal, expectVal, iter.Item().Key()[31])
			}
		}
	}

	// Test case 1: {1, 2, 3} - from C++ View_test.cpp line 423
	t.Run("dataset_{1,2,3}", func(t *testing.T) {
		sMap := setup([]int{1, 2, 3})

		// lower_bound tests (greatest item < key)
		checkLowerBound(sMap, 1, 0, true, "set{1,2,3}")  // no item < 1
		checkLowerBound(sMap, 2, 1, false, "set{1,2,3}") // item 1 < 2
		checkLowerBound(sMap, 3, 2, false, "set{1,2,3}") // item 2 < 3
		checkLowerBound(sMap, 4, 3, false, "set{1,2,3}") // item 3 < 4
		checkLowerBound(sMap, 5, 3, false, "set{1,2,3}") // item 3 < 5

		// upper_bound tests (first item > key)
		checkUpperBound(sMap, 0, 1, false, "set{1,2,3}") // first item > 0 = 1
		checkUpperBound(sMap, 1, 2, false, "set{1,2,3}") // first item > 1 = 2
		checkUpperBound(sMap, 2, 3, false, "set{1,2,3}") // first item > 2 = 3
		checkUpperBound(sMap, 3, 0, true, "set{1,2,3}")  // no item > 3
	})

	// Test case 2: {2, 4, 6} - from C++ View_test.cpp line 444
	t.Run("dataset_{2,4,6}", func(t *testing.T) {
		sMap := setup([]int{2, 4, 6})

		// lower_bound tests
		checkLowerBound(sMap, 1, 0, true, "set{2,4,6}")  // no item < 1
		checkLowerBound(sMap, 2, 0, true, "set{2,4,6}")  // no item < 2
		checkLowerBound(sMap, 3, 2, false, "set{2,4,6}") // item 2 < 3
		checkLowerBound(sMap, 4, 2, false, "set{2,4,6}") // item 2 < 4
		checkLowerBound(sMap, 5, 4, false, "set{2,4,6}") // item 4 < 5
		checkLowerBound(sMap, 6, 4, false, "set{2,4,6}") // item 4 < 6
		checkLowerBound(sMap, 7, 6, false, "set{2,4,6}") // item 6 < 7

		// upper_bound tests
		checkUpperBound(sMap, 1, 2, false, "set{2,4,6}") // first item > 1 = 2
		checkUpperBound(sMap, 2, 4, false, "set{2,4,6}") // first item > 2 = 4
		checkUpperBound(sMap, 3, 4, false, "set{2,4,6}") // first item > 3 = 4
		checkUpperBound(sMap, 4, 6, false, "set{2,4,6}") // first item > 4 = 6
		checkUpperBound(sMap, 5, 6, false, "set{2,4,6}") // first item > 5 = 6
		checkUpperBound(sMap, 6, 0, true, "set{2,4,6}")  // no item > 6
		checkUpperBound(sMap, 7, 0, true, "set{2,4,6}")  // no item > 7
	})

	// Test case 3: {2, 3, 5, 6, 10, 15} - from C++ View_test.cpp line 470
	t.Run("dataset_{2,3,5,6,10,15}", func(t *testing.T) {
		sMap := setup([]int{2, 3, 5, 6, 10, 15})

		// lower_bound tests
		checkLowerBound(sMap, 1, 0, true, "set{2,3,5,6,10,15}")    // no item < 1
		checkLowerBound(sMap, 2, 0, true, "set{2,3,5,6,10,15}")    // no item < 2
		checkLowerBound(sMap, 3, 2, false, "set{2,3,5,6,10,15}")   // item 2 < 3
		checkLowerBound(sMap, 4, 3, false, "set{2,3,5,6,10,15}")   // item 3 < 4
		checkLowerBound(sMap, 5, 3, false, "set{2,3,5,6,10,15}")   // item 3 < 5
		checkLowerBound(sMap, 6, 5, false, "set{2,3,5,6,10,15}")   // item 5 < 6
		checkLowerBound(sMap, 7, 6, false, "set{2,3,5,6,10,15}")   // item 6 < 7
		checkLowerBound(sMap, 10, 6, false, "set{2,3,5,6,10,15}")  // item 6 < 10
		checkLowerBound(sMap, 11, 10, false, "set{2,3,5,6,10,15}") // item 10 < 11
		checkLowerBound(sMap, 15, 10, false, "set{2,3,5,6,10,15}") // item 10 < 15
		checkLowerBound(sMap, 16, 15, false, "set{2,3,5,6,10,15}") // item 15 < 16

		// upper_bound tests
		checkUpperBound(sMap, 0, 2, false, "set{2,3,5,6,10,15}")   // first item > 0 = 2
		checkUpperBound(sMap, 1, 2, false, "set{2,3,5,6,10,15}")   // first item > 1 = 2
		checkUpperBound(sMap, 2, 3, false, "set{2,3,5,6,10,15}")   // first item > 2 = 3
		checkUpperBound(sMap, 3, 5, false, "set{2,3,5,6,10,15}")   // first item > 3 = 5
		checkUpperBound(sMap, 4, 5, false, "set{2,3,5,6,10,15}")   // first item > 4 = 5
		checkUpperBound(sMap, 5, 6, false, "set{2,3,5,6,10,15}")   // first item > 5 = 6
		checkUpperBound(sMap, 6, 10, false, "set{2,3,5,6,10,15}")  // first item > 6 = 10
		checkUpperBound(sMap, 10, 15, false, "set{2,3,5,6,10,15}") // first item > 10 = 15
		checkUpperBound(sMap, 15, 0, true, "set{2,3,5,6,10,15}")   // no item > 15
		checkUpperBound(sMap, 16, 0, true, "set{2,3,5,6,10,15}")   // no item > 16
	})

	// Test case 4: Large dataset - from C++ View_test.cpp line 522
	// {0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,20,25,30,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,66,100}
	t.Run("large_dataset", func(t *testing.T) {
		sMap := setup([]int{
			0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
			20, 25, 30, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48,
			66, 100,
		})

		// lower_bound tests from C++ lines 569-616
		checkLowerBound(sMap, 0, 0, true, "large")     // no item < 0
		checkLowerBound(sMap, 1, 0, false, "large")    // item 0 < 1
		checkLowerBound(sMap, 5, 4, false, "large")    // item 4 < 5
		checkLowerBound(sMap, 15, 14, false, "large")  // item 14 < 15
		checkLowerBound(sMap, 16, 15, false, "large")  // item 15 < 16
		checkLowerBound(sMap, 19, 16, false, "large")  // item 16 < 19
		checkLowerBound(sMap, 20, 16, false, "large")  // item 16 < 20
		checkLowerBound(sMap, 24, 20, false, "large")  // item 20 < 24
		checkLowerBound(sMap, 31, 30, false, "large")  // item 30 < 31
		checkLowerBound(sMap, 32, 30, false, "large")  // item 30 < 32
		checkLowerBound(sMap, 40, 39, false, "large")  // item 39 < 40
		checkLowerBound(sMap, 47, 46, false, "large")  // item 46 < 47
		checkLowerBound(sMap, 48, 47, false, "large")  // item 47 < 48
		checkLowerBound(sMap, 64, 48, false, "large")  // item 48 < 64
		checkLowerBound(sMap, 90, 66, false, "large")  // item 66 < 90
		checkLowerBound(sMap, 96, 66, false, "large")  // item 66 < 96
		checkLowerBound(sMap, 100, 66, false, "large") // item 66 < 100

		// upper_bound tests from C++ lines 618-664
		checkUpperBound(sMap, 0, 1, false, "large")    // first item > 0 = 1
		checkUpperBound(sMap, 5, 6, false, "large")    // first item > 5 = 6
		checkUpperBound(sMap, 15, 16, false, "large")  // first item > 15 = 16
		checkUpperBound(sMap, 16, 20, false, "large")  // first item > 16 = 20
		checkUpperBound(sMap, 18, 20, false, "large")  // first item > 18 = 20
		checkUpperBound(sMap, 20, 25, false, "large")  // first item > 20 = 25
		checkUpperBound(sMap, 31, 32, false, "large")  // first item > 31 = 32
		checkUpperBound(sMap, 32, 33, false, "large")  // first item > 32 = 33
		checkUpperBound(sMap, 47, 48, false, "large")  // first item > 47 = 48
		checkUpperBound(sMap, 48, 66, false, "large")  // first item > 48 = 66
		checkUpperBound(sMap, 53, 66, false, "large")  // first item > 53 = 66
		checkUpperBound(sMap, 66, 100, false, "large") // first item > 66 = 100
		checkUpperBound(sMap, 70, 100, false, "large") // first item > 70 = 100
		checkUpperBound(sMap, 85, 100, false, "large") // first item > 85 = 100
		checkUpperBound(sMap, 98, 100, false, "large") // first item > 98 = 100
		checkUpperBound(sMap, 100, 0, true, "large")   // no item > 100
		checkUpperBound(sMap, 155, 0, true, "large")   // no item > 155
	})
}
