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
	for i := 0; i < 32; i++ {
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
func TestAddAndTraverse(t *testing.T) {
	// Define test keys - same as rippled C++ test
	h1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	h2 := hexToHash("436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe")
	h3 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	h4 := hexToHash("b92891fe4ef6cee585fdc6fda2e09eb4d386363158ec3321b8123e5a772c6ca8")

	// Create items with values 1-4
	i1 := makeItem(h1, intToBytes(1))
	i2 := makeItem(h2, intToBytes(2))
	i3 := makeItem(h3, intToBytes(3))
	i4 := makeItem(h4, intToBytes(4))

	// Create a SHAMap
	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items to the map - same order as C++ test
	if err := sMap.PutItem(i2); err != nil {
		t.Fatalf("Failed to add item 2: %v", err)
	}

	if err := sMap.PutItem(i1); err != nil {
		t.Fatalf("Failed to add item 1: %v", err)
	}

	// Verify we can retrieve the items
	retrievedI1, found, err := sMap.Get(h1)
	if err != nil {
		t.Fatalf("Error getting item 1: %v", err)
	}
	if !found {
		t.Error("Item 1 not found")
	}
	if !bytes.Equal(retrievedI1.Data(), i1.Data()) {
		t.Error("Item 1 data mismatch")
	}

	retrievedI2, found, err := sMap.Get(h2)
	if err != nil {
		t.Fatalf("Error getting item 2: %v", err)
	}
	if !found {
		t.Error("Item 2 not found")
	}
	if !bytes.Equal(retrievedI2.Data(), i2.Data()) {
		t.Error("Item 2 data mismatch")
	}

	// Traverse and verify order
	var items []*Item
	err = sMap.ForEach(func(item *Item) bool {
		items = append(items, item)
		return true // Continue iteration
	})
	if err != nil {
		t.Fatalf("Error during traversal: %v", err)
	}

	// Should have 2 items
	if len(items) != 2 {
		t.Errorf("Expected 2 items, got %d", len(items))
	}

	// Verify we have i1 and i2
	found1, found2 := false, false
	for _, item := range items {
		itemKey := item.Key()
		if itemKey == h1 {
			found1 = true
		}
		if itemKey == h2 {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Errorf("Failed to find expected items after traverse: found1=%v, found2=%v", found1, found2)
	}

	// Add item 4
	if err := sMap.PutItem(i4); err != nil {
		t.Fatalf("Failed to add item 4: %v", err)
	}

	// Delete item 2
	if err := sMap.Delete(h2); err != nil {
		t.Fatalf("Failed to delete item 2: %v", err)
	}

	// Verify item 2 is gone
	_, found, err = sMap.Get(h2)
	if err != nil {
		t.Fatalf("Error checking for deleted item: %v", err)
	}
	if found {
		t.Error("Item 2 should have been deleted")
	}

	// Add item 3
	if err := sMap.PutItem(i3); err != nil {
		t.Fatalf("Failed to add item 3: %v", err)
	}

	// Final traverse - should have i1, i3, i4
	items = nil
	err = sMap.ForEach(func(item *Item) bool {
		items = append(items, item)
		return true
	})
	if err != nil {
		t.Fatalf("Error during final traversal: %v", err)
	}

	if len(items) != 3 {
		t.Errorf("Expected 3 items, got %d", len(items))
	}

	// Check that we have i1, i3, and i4 but not i2
	found1, found3, found4 := false, false, false
	for _, item := range items {
		itemKey := item.Key()
		switch itemKey {
		case h1:
			found1 = true
		case h2:
			t.Error("Found deleted item 2 in the map")
		case h3:
			found3 = true
		case h4:
			found4 = true
		}
	}
	if !found1 || !found3 || !found4 {
		t.Errorf("Failed to find expected items: found1=%v, found3=%v, found4=%v", found1, found3, found4)
	}
}

// TestBuildAndTear matches the exact C++ "build/tear" test
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
	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

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
			dumpTree(sMap.root, "", false)
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
			dumpTree(sMap.root, "", false)
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
		// if err := sMap.Invariants(); err != nil {
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

func TestIteration(t *testing.T) {
	keys := [][32]byte{
		hexToHash("f22891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[0]
		hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[1]
		hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[2]
		hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[3]
		hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[4]
		hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[5]
		hexToHash("b91891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[6]
		hexToHash("292891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8"), // keys[7]
	}

	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add all keys in order (keys[0] through keys[7])
	for i, key := range keys {
		if err := sMap.Put(key, intToBytes(0)); err != nil {
			t.Fatalf("Failed to add item %d: %v", i, err)
		}
	}

	// Collect iteration order
	var visitedKeys [][32]byte
	err = sMap.ForEach(func(item *Item) bool {
		visitedKeys = append(visitedKeys, item.Key())
		return true
	})
	if err != nil {
		t.Fatalf("Error during iteration: %v", err)
	}

	// Verify we got all keys
	if len(visitedKeys) != len(keys) {
		t.Errorf("Expected exactly %d keys, got %d", len(keys), len(visitedKeys))
		return
	}

	// Check each position matches the expected reverse order
	// C++ test expects: keys[7], keys[6], keys[5], keys[4], keys[3], keys[2], keys[1], keys[0]
	for pos := 0; pos < len(keys); pos++ {
		expectedIndex := len(keys) - 1 - pos // 7, 6, 5, 4, 3, 2, 1, 0
		expectedKey := keys[expectedIndex]

		if visitedKeys[pos] != expectedKey {
			t.Errorf("Iteration position %d: expected keys[%d] (%x), got (%x)",
				pos, expectedIndex, expectedKey[:4], visitedKeys[pos][:4])
		}
	}
}

// TestSnapshot tests creating a snapshot of a SHAMap
func TestSnapshot(t *testing.T) {
	h1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")

	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	if err := sMap.Put(h1, intToBytes(1)); err != nil {
		t.Fatalf("Failed to add item: %v", err)
	}

	mapHash, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get map hash: %v", err)
	}

	snapShotMap, err := sMap.Snapshot(false) // immutable snapshot
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}

	// Hashes should match
	smHash, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get original map hash: %v", err)
	}
	if smHash != mapHash {
		t.Error("Original map hash changed after snapshot")
	}

	snapShotHash, err := snapShotMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get snapshot hash: %v", err)
	}
	if snapShotHash != mapHash {
		t.Error("Snapshot hash doesn't match original")
	}

	// Verify both maps have the same item
	originalItem, found, err := sMap.Get(h1)
	if err != nil {
		t.Fatalf("Error getting item from original: %v", err)
	}
	if !found {
		t.Error("Item not found in original map")
	}

	snapshotItem, found, err := snapShotMap.Get(h1)
	if err != nil {
		t.Fatalf("Error getting item from snapshot: %v", err)
	}
	if !found {
		t.Error("Item not found in snapshot map")
	}

	if !originalItem.Equal(snapshotItem) {
		t.Error("Items should be equal between original and snapshot")
	}

	// Modify original map
	if err := sMap.Delete(h1); err != nil {
		t.Fatalf("Failed to delete item: %v", err)
	}

	// Hashes should now be different
	smHash, err = sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get modified map hash: %v", err)
	}
	if smHash == mapHash {
		t.Error("Original map hash unchanged after modification")
	}

	// Snapshot hash should remain the same
	snapShotHash, err = snapShotMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get snapshot hash after modification: %v", err)
	}
	if snapShotHash != mapHash {
		t.Error("Snapshot hash changed")
	}

	// Verify snapshot still has the item
	_, found, err = snapShotMap.Get(h1)
	if err != nil {
		t.Fatalf("Error getting item from snapshot after original deletion: %v", err)
	}
	if !found {
		t.Error("Item should still exist in snapshot after deletion from original")
	}
}

// TestImmutability tests that an immutable map cannot be modified
func TestImmutability(t *testing.T) {
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")

	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	if err := sMap.Put(key, intToBytes(1)); err != nil {
		t.Fatalf("Failed to add item: %v", err)
	}

	if err := sMap.SetImmutable(); err != nil {
		t.Fatalf("Failed to set immutable: %v", err)
	}

	// Try to add an item - should fail
	err = sMap.Put(key, intToBytes(2))
	if err != ErrImmutable {
		t.Errorf("Adding to immutable map should fail with ErrImmutable, got: %v", err)
	}

	// Try to delete an item - should fail
	err = sMap.Delete(key)
	if err != ErrImmutable {
		t.Errorf("Deleting from immutable map should fail with ErrImmutable, got: %v", err)
	}
}

// TestErrorHandling tests various error conditions
func TestErrorHandling(t *testing.T) {
	sMap, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Test adding nil item
	err = sMap.PutItem(nil)
	if err != ErrNilItem {
		t.Errorf("Expected ErrNilItem, got: %v", err)
	}

	// Test getting non-existent item
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	_, found, err := sMap.Get(key)
	if err != nil {
		t.Errorf("Getting non-existent item should not error: %v", err)
	}
	if found {
		t.Error("Should not find non-existent item")
	}

	// Test deleting non-existent item
	err = sMap.Delete(key)
	if err != ErrItemNotFound {
		t.Errorf("Expected ErrItemNotFound, got: %v", err)
	}
}

// TestConcurrency tests concurrent access to the SHAMap
func TestConcurrency(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add some initial data
	for i := 0; i < 10; i++ {
		key := [32]byte{}
		key[0] = byte(i)
		if err := sMap.Put(key, intToBytes(i)); err != nil {
			t.Fatalf("Failed to add initial item %d: %v", i, err)
		}
	}

	// Create immutable snapshot for concurrent reading
	snapshot, err := sMap.Snapshot(false)
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}

	// Test concurrent reads on snapshot (should be safe)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
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
	for i := 0; i < 10; i++ {
		<-done
	}
}

// Benchmarks

func BenchmarkPut(b *testing.B) {
	sMap, err := New(TypeTransaction)
	if err != nil {
		b.Fatalf("Failed to create SHAMap: %v", err)
	}

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
	sMap, err := New(TypeTransaction)
	if err != nil {
		b.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Pre-populate the map
	keys := make([][32]byte, 1000)
	for i := 0; i < 1000; i++ {
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
	sMap, err := New(TypeTransaction)
	if err != nil {
		b.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Pre-populate the map
	for i := 0; i < 1000; i++ {
		key := [32]byte{}
		copy(key[:], fmt.Sprintf("%032d", i))
		if err := sMap.Put(key, intToBytes(i)); err != nil {
			b.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := sMap.Snapshot(false)
		if err != nil {
			b.Fatalf("Failed to create snapshot: %v", err)
		}
	}
}

// Helper function for debugging - simplified tree dump
func dumpTree(node Node, prefix string, isTail bool) {
	switch n := node.(type) {
	case *InnerNode:
		fmt.Printf("%s%sInnerNode %p, hash: %x\n", prefix, branchSymbol(isTail), n, n.Hash())

		// Get all non-empty children
		var children []struct {
			index int
			child Node
		}
		for i := 0; i < BranchFactor; i++ {
			if !n.IsEmptyBranch(i) {
				if child, err := n.Child(i); err == nil && child != nil {
					children = append(children, struct {
						index int
						child Node
					}{index: i, child: child})
				}
			}
		}

		for i, c := range children {
			fmt.Printf("%s%s[Branch %x]\n", prefix, pipeSymbol(isTail), c.index)
			dumpTree(c.child, nextPrefix(prefix, isTail), i == len(children)-1)
		}

	case *AccountStateLeafNode:
		fmt.Printf("%s%sLeaf(Account) %p, key: %x\n", prefix, branchSymbol(isTail), n, n.Item().Key())
	case *TransactionLeafNode:
		fmt.Printf("%s%sLeaf(Tx) %p, key: %x\n", prefix, branchSymbol(isTail), n, n.Item().Key())
	case *TransactionWithMetaLeafNode:
		fmt.Printf("%s%sLeaf(Tx+Meta) %p, key: %x\n", prefix, branchSymbol(isTail), n, n.Item().Key())
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
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

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

	// Test: too long path (longer than MaxDepth+1)
	if len(goodPath) > 0 {
		tooLongPath := make([][]byte, MaxDepth+2)
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

// TestVerifyProofPathDetailed tests the detailed verification function
func TestVerifyProofPathDetailed(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add a single item
	var key [32]byte
	key[0] = 1
	data := make([]byte, 32)
	copy(data, key[:])

	if err := sMap.Put(key, data); err != nil {
		t.Fatalf("Failed to add item: %v", err)
	}

	root, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get root hash: %v", err)
	}

	proofPath, err := sMap.GetProofPath(key)
	if err != nil {
		t.Fatalf("Failed to get proof path: %v", err)
	}

	// Valid path should return nil error
	if err := VerifyProofPathDetailed(root, key, proofPath.Path); err != nil {
		t.Errorf("Valid proof should not return error: %v", err)
	}

	// Empty path should return ProofPathError
	err = VerifyProofPathDetailed(root, key, [][]byte{})
	if err == nil {
		t.Error("Empty path should return error")
	}
	if _, ok := err.(*ProofPathError); !ok {
		t.Errorf("Expected ProofPathError, got %T", err)
	}

	// Wrong root should return ProofPathError with hash mismatch
	var wrongRoot [32]byte
	wrongRoot[0] = 0xFF
	err = VerifyProofPathDetailed(wrongRoot, key, proofPath.Path)
	if err == nil {
		t.Error("Wrong root should return error")
	}
	if pathErr, ok := err.(*ProofPathError); ok {
		if pathErr.Message != "hash mismatch" {
			t.Errorf("Expected 'hash mismatch', got '%s'", pathErr.Message)
		}
	}
}

// TestVerifyProofPathWithValue tests proof verification with value extraction
func TestVerifyProofPathWithValue(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add a single item
	var key [32]byte
	key[0] = 42
	data := []byte("test data for proof verification")

	if err := sMap.Put(key, data); err != nil {
		t.Fatalf("Failed to add item: %v", err)
	}

	root, err := sMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get root hash: %v", err)
	}

	proofPath, err := sMap.GetProofPath(key)
	if err != nil {
		t.Fatalf("Failed to get proof path: %v", err)
	}

	// Valid proof should return the data
	result := VerifyProofPathWithValue(root, key, proofPath.Path)
	if result == nil {
		t.Fatal("Valid proof should return data")
	}
	if string(result) != string(data) {
		t.Errorf("Expected data '%s', got '%s'", string(data), string(result))
	}

	// Invalid proof should return nil
	var wrongRoot [32]byte
	wrongRoot[0] = 0xFF
	result = VerifyProofPathWithValue(wrongRoot, key, proofPath.Path)
	if result != nil {
		t.Error("Invalid proof should return nil")
	}
}

// TestIterator tests the basic iterator functionality
func TestIterator(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items with keys that will be in known order
	keys := make([][32]byte, 10)
	for i := 0; i < 10; i++ {
		keys[i][0] = byte(i * 10) // 0, 10, 20, ..., 90
		data := make([]byte, 32)
		data[0] = byte(i)
		if err := sMap.Put(keys[i], data); err != nil {
			t.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	// Test Begin() iterator - should visit all items in key order
	iter := sMap.Begin()
	count := 0
	var lastKey [32]byte
	for iter.Next() {
		item := iter.Item()
		if item == nil {
			t.Fatal("Iterator returned nil item")
		}
		if count > 0 && compareKeys(item.Key(), lastKey) <= 0 {
			t.Errorf("Items not in ascending order: %x <= %x", item.Key(), lastKey)
		}
		lastKey = item.Key()
		count++
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if count != 10 {
		t.Errorf("Expected 10 items, got %d", count)
	}

	// Test empty map
	emptyMap, _ := New(TypeState)
	iter = emptyMap.Begin()
	if iter.Next() {
		t.Error("Empty map iterator should return false on Next()")
	}
	if iter.Valid() {
		t.Error("Empty map iterator should not be valid")
	}
}

// TestUpperBound tests the UpperBound functionality
func TestUpperBound(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items with keys 10, 20, 30, 40, 50
	keys := make([][32]byte, 5)
	for i := 0; i < 5; i++ {
		keys[i][0] = byte((i + 1) * 10) // 10, 20, 30, 40, 50
		data := make([]byte, 32)
		data[0] = byte(i + 1)
		if err := sMap.Put(keys[i], data); err != nil {
			t.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	tests := []struct {
		name       string
		searchKey  byte
		expectKey  byte
		expectNone bool
	}{
		{"before all", 5, 10, false},      // key=5, expect first item > 5 = 10
		{"exact match", 20, 30, false},    // key=20, expect first item > 20 = 30
		{"between items", 25, 30, false},  // key=25, expect first item > 25 = 30
		{"at last", 50, 0, true},          // key=50, no item > 50
		{"after all", 60, 0, true},        // key=60, no item > 60
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var searchKey [32]byte
			searchKey[0] = tt.searchKey

			iter := sMap.UpperBound(searchKey)
			if tt.expectNone {
				if iter.Valid() {
					t.Errorf("Expected no result, got key[0]=%d", iter.Item().Key()[0])
				}
			} else {
				if !iter.Valid() {
					t.Errorf("Expected key[0]=%d, got no result", tt.expectKey)
				} else if iter.Item().Key()[0] != tt.expectKey {
					t.Errorf("Expected key[0]=%d, got %d", tt.expectKey, iter.Item().Key()[0])
				}
			}
		})
	}
}

// TestLowerBound tests the LowerBound functionality
func TestLowerBound(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items with keys 10, 20, 30, 40, 50
	keys := make([][32]byte, 5)
	for i := 0; i < 5; i++ {
		keys[i][0] = byte((i + 1) * 10) // 10, 20, 30, 40, 50
		data := make([]byte, 32)
		data[0] = byte(i + 1)
		if err := sMap.Put(keys[i], data); err != nil {
			t.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	// LowerBound returns greatest item < key (rippled semantics)
	tests := []struct {
		name       string
		searchKey  byte
		expectKey  byte
		expectNone bool
	}{
		{"before all", 5, 0, true},        // key=5, no item < 5
		{"at first", 10, 0, true},         // key=10, no item < 10
		{"after first", 15, 10, false},    // key=15, greatest item < 15 = 10
		{"exact match", 30, 20, false},    // key=30, greatest item < 30 = 20
		{"between items", 35, 30, false},  // key=35, greatest item < 35 = 30
		{"after all", 60, 50, false},      // key=60, greatest item < 60 = 50
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var searchKey [32]byte
			searchKey[0] = tt.searchKey

			iter := sMap.LowerBound(searchKey)
			if tt.expectNone {
				if iter.Valid() {
					t.Errorf("Expected no result, got key[0]=%d", iter.Item().Key()[0])
				}
			} else {
				if !iter.Valid() {
					t.Errorf("Expected key[0]=%d, got no result", tt.expectKey)
				} else if iter.Item().Key()[0] != tt.expectKey {
					t.Errorf("Expected key[0]=%d, got %d", tt.expectKey, iter.Item().Key()[0])
				}
			}
		})
	}
}

// TestIteratorWithManyItems tests iterator with a larger dataset
func TestIteratorWithManyItems(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add 100 items
	for i := 0; i < 100; i++ {
		var key [32]byte
		key[0] = byte(i)
		data := make([]byte, 32)
		copy(data, key[:])
		if err := sMap.Put(key, data); err != nil {
			t.Fatalf("Failed to put item %d: %v", i, err)
		}
	}

	// Count items via iterator
	iter := sMap.Begin()
	count := 0
	for iter.Next() {
		count++
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if count != 100 {
		t.Errorf("Expected 100 items, got %d", count)
	}

	// Test UpperBound in the middle
	var midKey [32]byte
	midKey[0] = 50
	iter = sMap.UpperBound(midKey)
	if !iter.Valid() {
		t.Fatal("UpperBound(50) should return valid iterator")
	}
	if iter.Item().Key()[0] != 51 {
		t.Errorf("UpperBound(50) should return key 51, got %d", iter.Item().Key()[0])
	}

	// Test LowerBound in the middle
	iter = sMap.LowerBound(midKey)
	if !iter.Valid() {
		t.Fatal("LowerBound(50) should return valid iterator")
	}
	if iter.Item().Key()[0] != 49 {
		t.Errorf("LowerBound(50) should return key 49, got %d", iter.Item().Key()[0])
	}
}

// TestUpperBoundLowerBoundEdgeCases tests edge cases for bounds
func TestUpperBoundLowerBoundEdgeCases(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add a single item
	var singleKey [32]byte
	singleKey[0] = 50
	if err := sMap.Put(singleKey, singleKey[:]); err != nil {
		t.Fatalf("Failed to put item: %v", err)
	}

	// UpperBound for key < single item
	var beforeKey [32]byte
	beforeKey[0] = 40
	iter := sMap.UpperBound(beforeKey)
	if !iter.Valid() || iter.Item().Key()[0] != 50 {
		t.Error("UpperBound(40) should return key 50")
	}

	// UpperBound for the exact single item
	iter = sMap.UpperBound(singleKey)
	if iter.Valid() {
		t.Error("UpperBound(50) should return invalid (no item > 50)")
	}

	// LowerBound for the exact single item
	iter = sMap.LowerBound(singleKey)
	if iter.Valid() {
		t.Error("LowerBound(50) should return invalid (no item < 50)")
	}

	// LowerBound for key > single item
	var afterKey [32]byte
	afterKey[0] = 60
	iter = sMap.LowerBound(afterKey)
	if !iter.Valid() || iter.Item().Key()[0] != 50 {
		t.Error("LowerBound(60) should return key 50")
	}

	// Test on empty map
	emptyMap, _ := New(TypeState)
	iter = emptyMap.UpperBound(singleKey)
	if iter.Valid() {
		t.Error("UpperBound on empty map should return invalid")
	}
	iter = emptyMap.LowerBound(singleKey)
	if iter.Valid() {
		t.Error("LowerBound on empty map should return invalid")
	}
}

// TestBoundsMatchingCppTestVectors tests upper_bound and lower_bound using
// the same test vectors as rippled's View_test.cpp to ensure identical behavior.
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
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}
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
		checkLowerBound(sMap, 1, 0, true, "set{1,2,3}")   // no item < 1
		checkLowerBound(sMap, 2, 1, false, "set{1,2,3}")  // item 1 < 2
		checkLowerBound(sMap, 3, 2, false, "set{1,2,3}")  // item 2 < 3
		checkLowerBound(sMap, 4, 3, false, "set{1,2,3}")  // item 3 < 4
		checkLowerBound(sMap, 5, 3, false, "set{1,2,3}")  // item 3 < 5

		// upper_bound tests (first item > key)
		checkUpperBound(sMap, 0, 1, false, "set{1,2,3}")  // first item > 0 = 1
		checkUpperBound(sMap, 1, 2, false, "set{1,2,3}")  // first item > 1 = 2
		checkUpperBound(sMap, 2, 3, false, "set{1,2,3}")  // first item > 2 = 3
		checkUpperBound(sMap, 3, 0, true, "set{1,2,3}")   // no item > 3
	})

	// Test case 2: {2, 4, 6} - from C++ View_test.cpp line 444
	t.Run("dataset_{2,4,6}", func(t *testing.T) {
		sMap := setup([]int{2, 4, 6})

		// lower_bound tests
		checkLowerBound(sMap, 1, 0, true, "set{2,4,6}")   // no item < 1
		checkLowerBound(sMap, 2, 0, true, "set{2,4,6}")   // no item < 2
		checkLowerBound(sMap, 3, 2, false, "set{2,4,6}")  // item 2 < 3
		checkLowerBound(sMap, 4, 2, false, "set{2,4,6}")  // item 2 < 4
		checkLowerBound(sMap, 5, 4, false, "set{2,4,6}")  // item 4 < 5
		checkLowerBound(sMap, 6, 4, false, "set{2,4,6}")  // item 4 < 6
		checkLowerBound(sMap, 7, 6, false, "set{2,4,6}")  // item 6 < 7

		// upper_bound tests
		checkUpperBound(sMap, 1, 2, false, "set{2,4,6}")  // first item > 1 = 2
		checkUpperBound(sMap, 2, 4, false, "set{2,4,6}")  // first item > 2 = 4
		checkUpperBound(sMap, 3, 4, false, "set{2,4,6}")  // first item > 3 = 4
		checkUpperBound(sMap, 4, 6, false, "set{2,4,6}")  // first item > 4 = 6
		checkUpperBound(sMap, 5, 6, false, "set{2,4,6}")  // first item > 5 = 6
		checkUpperBound(sMap, 6, 0, true, "set{2,4,6}")   // no item > 6
		checkUpperBound(sMap, 7, 0, true, "set{2,4,6}")   // no item > 7
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
