package shamap

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// memoryFamily is a test implementation of Family using an in-memory map.
type memoryFamily struct {
	mu    sync.RWMutex
	store map[[32]byte][]byte
}

func newMemoryFamily() *memoryFamily {
	return &memoryFamily{
		store: make(map[[32]byte][]byte),
	}
}

func (f *memoryFamily) Fetch(hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, ok := f.store[hash]
	if !ok {
		return nil, nil
	}
	// Return a copy to avoid shared mutable state
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (f *memoryFamily) StoreBatch(entries []FlushEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range entries {
		cp := make([]byte, len(e.Data))
		copy(cp, e.Data)
		f.store[e.Hash] = cp
	}
	return nil
}

func (f *memoryFamily) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.store)
}

// flushToFamily is a helper that flushes a SHAMap and stores entries in a Family.
func flushToFamily(sm *SHAMap, family *memoryFamily) error {
	batch, err := sm.FlushDirty(false)
	if err != nil {
		return fmt.Errorf("FlushDirty: %w", err)
	}
	if len(batch.Entries) > 0 {
		return family.StoreBatch(batch.Entries)
	}
	return nil
}

// TestDirtyFlag_NewNodes verifies that newly created nodes are marked dirty.
func TestDirtyFlag_NewNodes(t *testing.T) {
	// Inner node
	inner := NewInnerNode()
	if !inner.IsDirty() {
		t.Error("NewInnerNode should be dirty")
	}

	// Account state leaf
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item := NewItem(key, intToBytes(1))
	leaf, err := NewAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.IsDirty() {
		t.Error("NewAccountStateLeafNode should be dirty")
	}

	// Transaction leaf
	txLeaf, err := NewTransactionLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if !txLeaf.IsDirty() {
		t.Error("NewTransactionLeafNode should be dirty")
	}

	// Transaction+meta leaf
	txMetaLeaf, err := NewTransactionWithMetaLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if !txMetaLeaf.IsDirty() {
		t.Error("NewTransactionWithMetaLeafNode should be dirty")
	}
}

// TestDirtyFlag_SetChild verifies that SetChild marks the inner node dirty.
func TestDirtyFlag_SetChild(t *testing.T) {
	inner := NewInnerNode()
	inner.SetDirty(false) // manually clear

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item := NewItem(key, intToBytes(1))
	leaf, err := NewAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}

	if err := inner.SetChild(0, leaf); err != nil {
		t.Fatal(err)
	}

	if !inner.IsDirty() {
		t.Error("SetChild should mark inner node dirty")
	}
}

// TestDirtyFlag_SetItem verifies that SetItem marks leaf nodes dirty.
func TestDirtyFlag_SetItem(t *testing.T) {
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item1 := NewItem(key, intToBytes(1))
	item2 := NewItem(key, intToBytes(2))

	// AccountStateLeafNode
	leaf, err := NewAccountStateLeafNode(item1)
	if err != nil {
		t.Fatal(err)
	}
	leaf.SetDirty(false)

	if _, err := leaf.SetItem(item2); err != nil {
		t.Fatal(err)
	}
	if !leaf.IsDirty() {
		t.Error("SetItem should mark AccountStateLeafNode dirty")
	}

	// TransactionLeafNode
	txLeaf, err := NewTransactionLeafNode(item1)
	if err != nil {
		t.Fatal(err)
	}
	txLeaf.SetDirty(false)

	if _, err := txLeaf.SetItem(item2); err != nil {
		t.Fatal(err)
	}
	if !txLeaf.IsDirty() {
		t.Error("SetItem should mark TransactionLeafNode dirty")
	}

	// TransactionWithMetaLeafNode
	txMetaLeaf, err := NewTransactionWithMetaLeafNode(item1)
	if err != nil {
		t.Fatal(err)
	}
	txMetaLeaf.SetDirty(false)

	if _, err := txMetaLeaf.SetItem(item2); err != nil {
		t.Fatal(err)
	}
	if !txMetaLeaf.IsDirty() {
		t.Error("SetItem should mark TransactionWithMetaLeafNode dirty")
	}
}

// TestDirtyFlag_WireDeserialize verifies that wire-deserialized nodes are NOT dirty.
func TestDirtyFlag_WireDeserialize(t *testing.T) {
	// Create an inner node and serialize it
	inner := NewInnerNode()
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item := NewItem(key, intToBytes(1))
	leaf, err := NewAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.SetChild(0, leaf); err != nil {
		t.Fatal(err)
	}

	// Serialize and deserialize inner node
	wireData, err := inner.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	deserialized, err := NewInnerNodeFromWire(wireData)
	if err != nil {
		t.Fatal(err)
	}
	if deserialized.IsDirty() {
		t.Error("wire-deserialized inner node should NOT be dirty")
	}

	// Serialize and deserialize account state leaf
	leafWire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	deserializedLeaf, err := NewAccountStateLeafFromWire(leafWire)
	if err != nil {
		t.Fatal(err)
	}
	if deserializedLeaf.IsDirty() {
		t.Error("wire-deserialized account state leaf should NOT be dirty")
	}
}

// TestFlushDirty_BasicRoundTrip verifies FlushDirty collects all dirty nodes
// and they can be deserialized back.
func TestFlushDirty_BasicRoundTrip(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	// Add several items
	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		if err := sMap.Put(key, intToBytes(i+1)); err != nil {
			t.Fatalf("Failed to add item %d: %v", i, err)
		}
	}

	// Get root hash before flush
	hashBefore, err := sMap.Hash()
	if err != nil {
		t.Fatal(err)
	}

	// Flush dirty nodes
	batch, err := sMap.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}

	// Should have flushed: root + inner nodes + 3 leaves
	if len(batch.Entries) == 0 {
		t.Fatal("FlushDirty returned empty batch")
	}
	t.Logf("Flushed %d nodes", len(batch.Entries))

	// Hash should not change after flush
	hashAfter, err := sMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if hashBefore != hashAfter {
		t.Error("Hash should not change after FlushDirty")
	}

	// Root should no longer be dirty
	if sMap.root.IsDirty() {
		t.Error("Root should be clean after FlushDirty")
	}

	// Verify all entries can be deserialized
	for i, entry := range batch.Entries {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Errorf("Failed to deserialize entry %d: %v", i, err)
			continue
		}
		if node.IsDirty() {
			t.Errorf("Deserialized node %d should not be dirty", i)
		}

		// Verify hash matches
		deserializedHash := node.Hash()
		if deserializedHash != entry.Hash {
			t.Errorf("Entry %d hash mismatch: entry=%x deserialized=%x",
				i, entry.Hash[:8], deserializedHash[:8])
		}
	}
}

// TestFlushDirty_Idempotent verifies that flushing twice produces empty batch on second call.
func TestFlushDirty_Idempotent(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(1)); err != nil {
		t.Fatal(err)
	}

	// First flush
	batch1, err := sMap.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch1.Entries) == 0 {
		t.Fatal("First flush should return entries")
	}

	// Second flush — nothing dirty
	batch2, err := sMap.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch2.Entries) != 0 {
		t.Errorf("Second flush should return 0 entries, got %d", len(batch2.Entries))
	}
}

// TestFlushDirty_AfterModification verifies only modified nodes are re-flushed.
func TestFlushDirty_AfterModification(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	key2 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	if err := sMap.Put(key1, intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	if err := sMap.Put(key2, intToBytes(2)); err != nil {
		t.Fatal(err)
	}

	// First flush — all nodes
	batch1, err := sMap.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}
	count1 := len(batch1.Entries)
	t.Logf("First flush: %d nodes", count1)

	// Modify only key1
	if err := sMap.Put(key1, intToBytes(99)); err != nil {
		t.Fatal(err)
	}

	// Second flush — only modified path
	batch2, err := sMap.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}
	count2 := len(batch2.Entries)
	t.Logf("Second flush: %d nodes", count2)

	if count2 >= count1 {
		t.Errorf("Second flush should have fewer entries (%d) than first (%d)", count2, count1)
	}
	if count2 == 0 {
		t.Error("Second flush should have at least the modified leaf + path")
	}
}

// TestFlushDirty_ReleaseChildren verifies child pointers are released when requested.
func TestFlushDirty_ReleaseChildren(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(1)); err != nil {
		t.Fatal(err)
	}

	// Flush with releaseChildren=true
	_, err = sMap.FlushDirty(true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify root's children are nil (released)
	for i := 0; i < BranchFactor; i++ {
		child := sMap.root.ChildUnsafe(i)
		if child != nil {
			t.Errorf("Branch %d child should be nil after release", i)
		}
	}

	// But hashes should still be set for non-empty branches
	hasNonEmpty := false
	for i := 0; i < BranchFactor; i++ {
		if !sMap.root.IsEmptyBranch(i) {
			hash := sMap.root.ChildHashUnsafe(i)
			if isZeroHash(hash) {
				t.Errorf("Branch %d has bit set but zero hash after release", i)
			}
			hasNonEmpty = true
		}
	}
	if !hasNonEmpty {
		t.Error("Root should have at least one non-empty branch")
	}
}

// TestDeserializeFromPrefix_InnerNode tests inner node round-trip via prefix format.
func TestDeserializeFromPrefix_InnerNode(t *testing.T) {
	// Create inner node with a child
	inner := NewInnerNode()
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item := NewItem(key, intToBytes(42))
	leaf, err := NewAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.SetChild(0, leaf); err != nil {
		t.Fatal(err)
	}

	originalHash := inner.Hash()

	// Serialize
	data, err := inner.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}

	// Deserialize
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		t.Fatal(err)
	}

	deserializedInner, ok := node.(*InnerNode)
	if !ok {
		t.Fatal("Expected InnerNode")
	}

	// Hash should match
	if deserializedInner.Hash() != originalHash {
		t.Errorf("Hash mismatch: original=%x deserialized=%x",
			originalHash[:8], deserializedInner.Hash())
	}

	// Children should be nil (hash-only)
	child := deserializedInner.ChildUnsafe(0)
	if child != nil {
		t.Error("Deserialized inner node should have nil children (hash-only)")
	}

	// But hash should be set
	childHash := deserializedInner.ChildHashUnsafe(0)
	if isZeroHash(childHash) {
		t.Error("Deserialized inner node should have child hash set")
	}

	// Should not be dirty
	if deserializedInner.IsDirty() {
		t.Error("Deserialized inner node should not be dirty")
	}
}

// TestDeserializeFromPrefix_AccountStateLeaf tests leaf round-trip.
func TestDeserializeFromPrefix_AccountStateLeaf(t *testing.T) {
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	item := NewItem(key, intToBytes(42))

	leaf, err := NewAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := leaf.Hash()

	// Serialize
	data, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}

	// Deserialize
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		t.Fatal(err)
	}

	deserializedLeaf, ok := node.(*AccountStateLeafNode)
	if !ok {
		t.Fatal("Expected AccountStateLeafNode")
	}

	if deserializedLeaf.Hash() != originalHash {
		t.Errorf("Hash mismatch")
	}

	if deserializedLeaf.item.Key() != key {
		t.Error("Key mismatch")
	}

	if !bytes.Equal(deserializedLeaf.item.Data(), intToBytes(42)) {
		t.Error("Data mismatch")
	}

	if deserializedLeaf.IsDirty() {
		t.Error("Should not be dirty")
	}
}

// ===== Phase 2: Backed SHAMap + Lazy Loading Tests =====

// TestBacked_NewFromRootHash creates a map, flushes it, then recreates from root hash
// and verifies all data is accessible via lazy loading.
func TestBacked_NewFromRootHash(t *testing.T) {
	family := newMemoryFamily()

	// Create unbacked map and populate
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		if err := sMap.Put(key, intToBytes(i+1)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	rootHash, err := sMap.Hash()
	if err != nil {
		t.Fatal(err)
	}

	// Flush to family
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}
	t.Logf("Stored %d nodes in family", family.Len())

	// Recreate from root hash
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	if !backed.IsBacked() {
		t.Error("backed map should report IsBacked()=true")
	}

	// Verify root hash matches
	backedHash, err := backed.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if backedHash != rootHash {
		t.Errorf("Root hash mismatch: original=%x backed=%x", rootHash[:8], backedHash[:8])
	}

	// Verify all items are accessible via lazy loading
	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		item, found, err := backed.Get(key)
		if err != nil {
			t.Errorf("Get key %d: %v", i, err)
			continue
		}
		if !found || item == nil {
			t.Errorf("Key %d not found in backed map", i)
			continue
		}
		if !bytes.Equal(item.Data(), intToBytes(i+1)) {
			t.Errorf("Key %d data mismatch", i)
		}
	}
}

// TestBacked_LazyLoading verifies that children are nil until accessed.
func TestBacked_LazyLoading(t *testing.T) {
	family := newMemoryFamily()

	// Create and populate
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	key2 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	if err := sMap.Put(key1, intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	if err := sMap.Put(key2, intToBytes(2)); err != nil {
		t.Fatal(err)
	}

	rootHash, err := sMap.Hash()
	if err != nil {
		t.Fatal(err)
	}

	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Recreate from root hash
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	// Root should have branches with hashes set but children nil (not yet loaded)
	childrenLoaded := 0
	for i := 0; i < BranchFactor; i++ {
		if !backed.root.IsEmptyBranch(i) {
			child := backed.root.ChildUnsafe(i)
			if child != nil {
				childrenLoaded++
			}
			// Hash should be set
			hash := backed.root.ChildHashUnsafe(i)
			if isZeroHash(hash) {
				t.Errorf("Branch %d has bit set but zero hash", i)
			}
		}
	}
	if childrenLoaded != 0 {
		t.Errorf("Expected 0 children loaded initially, got %d", childrenLoaded)
	}

	// Now access a key — this should trigger lazy loading
	item, found, err := backed.Get(key1)
	if err != nil {
		t.Fatal(err)
	}
	if !found || item == nil {
		t.Fatal("Key1 not found")
	}
	if !bytes.Equal(item.Data(), intToBytes(1)) {
		t.Error("Key1 data mismatch after lazy load")
	}

	// Now some children should be loaded (the path to key1)
	childrenLoadedAfter := 0
	for i := 0; i < BranchFactor; i++ {
		if backed.root.ChildUnsafe(i) != nil {
			childrenLoadedAfter++
		}
	}
	if childrenLoadedAfter == 0 {
		t.Error("At least one child should be loaded after Get")
	}
}

// TestBacked_DescendUnbacked verifies unbacked maps work identically to before.
func TestBacked_DescendUnbacked(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(42)); err != nil {
		t.Fatal(err)
	}

	if sMap.IsBacked() {
		t.Error("Unbacked map should report IsBacked()=false")
	}

	// Get should work normally
	item, found, err := sMap.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found || item == nil {
		t.Fatal("Key not found")
	}
	if !bytes.Equal(item.Data(), intToBytes(42)) {
		t.Error("Data mismatch")
	}

	// ForEach should work
	count := 0
	if err := sMap.ForEach(func(item *Item) bool {
		count++
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("Expected 1 item, got %d", count)
	}
}

// TestBacked_FullRoundTrip tests: put → flush → recreate → verify all items → modify → flush → recreate
func TestBacked_FullRoundTrip(t *testing.T) {
	family := newMemoryFamily()

	// Phase 1: Create and populate
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"1a2b3c4d5e6f708192a3b4c5d6e7f80192a3b4c5d6e7f80192a3b4c5d6e7f801",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
		"dead0000000000000000000000000000000000000000000000000000beefcafe",
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		if err := sMap.Put(key, intToBytes(i+10)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	rootHash1, _ := sMap.Hash()
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Phase 2: Recreate and verify all items
	backed1, err := NewFromRootHash(TypeState, rootHash1, family)
	if err != nil {
		t.Fatal(err)
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		item, found, err := backed1.Get(key)
		if err != nil {
			t.Errorf("Round 1 Get %d: %v", i, err)
			continue
		}
		if !found || item == nil {
			t.Errorf("Round 1 Key %d not found", i)
			continue
		}
		if !bytes.Equal(item.Data(), intToBytes(i+10)) {
			t.Errorf("Round 1 Key %d data mismatch", i)
		}
	}

	// Phase 3: Modify backed map
	modKey := hexToHash(keys[0])
	if err := backed1.Put(modKey, intToBytes(99)); err != nil {
		t.Fatal(err)
	}

	rootHash2, _ := backed1.Hash()
	if rootHash2 == rootHash1 {
		t.Error("Root hash should change after modification")
	}

	// Flush modifications
	if err := flushToFamily(backed1, family); err != nil {
		t.Fatal(err)
	}

	// Phase 4: Recreate from new root hash and verify
	backed2, err := NewFromRootHash(TypeState, rootHash2, family)
	if err != nil {
		t.Fatal(err)
	}

	// Modified key should have new value
	modItem, modFound, err := backed2.Get(modKey)
	if err != nil {
		t.Fatal(err)
	}
	if !modFound || modItem == nil {
		t.Fatal("Modified key not found")
	}
	if !bytes.Equal(modItem.Data(), intToBytes(99)) {
		t.Error("Modified key should have new value")
	}

	// Other keys should still be accessible
	for i := 1; i < len(keys); i++ {
		key := hexToHash(keys[i])
		item, found, err := backed2.Get(key)
		if err != nil {
			t.Errorf("Round 2 Get %d: %v", i, err)
			continue
		}
		if !found || item == nil {
			t.Errorf("Round 2 Key %d not found", i)
			continue
		}
		if !bytes.Equal(item.Data(), intToBytes(i+10)) {
			t.Errorf("Round 2 Key %d data mismatch", i)
		}
	}
}

// TestBacked_ForEach verifies iteration works over lazily-loaded nodes.
func TestBacked_ForEach(t *testing.T) {
	family := newMemoryFamily()

	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		if err := sMap.Put(key, intToBytes(i+1)); err != nil {
			t.Fatal(err)
		}
	}

	rootHash, _ := sMap.Hash()
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Create backed map and iterate
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[[32]byte]bool)
	if err := backed.ForEach(func(item *Item) bool {
		found[item.Key()] = true
		return true
	}); err != nil {
		t.Fatal(err)
	}

	if len(found) != len(keys) {
		t.Errorf("ForEach found %d items, expected %d", len(found), len(keys))
	}

	for _, keyHex := range keys {
		key := hexToHash(keyHex)
		if !found[key] {
			t.Errorf("Key %s not found via ForEach", keyHex[:8])
		}
	}
}

// TestBacked_DeleteItem verifies deletion works on a backed map.
func TestBacked_DeleteItem(t *testing.T) {
	family := newMemoryFamily()

	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	key2 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	if err := sMap.Put(key1, intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	if err := sMap.Put(key2, intToBytes(2)); err != nil {
		t.Fatal(err)
	}

	rootHash, _ := sMap.Hash()
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Create backed map and delete key1
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	if err := backed.Delete(key1); err != nil {
		t.Fatal(err)
	}

	// key1 should be gone
	_, found, err := backed.Get(key1)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("Deleted key should not be found")
	}

	// key2 should still exist
	item, found2, err := backed.Get(key2)
	if err != nil {
		t.Fatal(err)
	}
	if !found2 || item == nil {
		t.Fatal("Non-deleted key should still exist")
	}
	if !bytes.Equal(item.Data(), intToBytes(2)) {
		t.Error("Non-deleted key should still be accessible")
	}
}

// TestBacked_IndependentInstances verifies two backed maps from the same root
// are fully independent (no shared mutable state).
func TestBacked_IndependentInstances(t *testing.T) {
	family := newMemoryFamily()

	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(1)); err != nil {
		t.Fatal(err)
	}

	rootHash, _ := sMap.Hash()
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Create two independent backed maps from same root
	map1, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}
	map2, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	// Modify map1
	if err := map1.Put(key, intToBytes(99)); err != nil {
		t.Fatal(err)
	}

	// map2 should still have original value
	item2, found2, err := map2.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found2 || item2 == nil {
		t.Fatal("Key not found in map2")
	}
	if !bytes.Equal(item2.Data(), intToBytes(1)) {
		t.Error("map2 should not be affected by map1's modification")
	}

	// map1 should have modified value
	item1, found1, err := map1.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found1 || item1 == nil {
		t.Fatal("Key not found in map1")
	}
	if !bytes.Equal(item1.Data(), intToBytes(99)) {
		t.Error("map1 should have modified value")
	}
}

// TestBacked_Snapshot verifies Snapshot creates an independent backed copy.
func TestBacked_Snapshot(t *testing.T) {
	family := newMemoryFamily()

	// Create a backed map
	sMap, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}

	key1 := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	key2 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	if err := sMap.Put(key1, intToBytes(1)); err != nil {
		t.Fatal(err)
	}
	if err := sMap.Put(key2, intToBytes(2)); err != nil {
		t.Fatal(err)
	}

	// Create a mutable snapshot (this flushes + creates from root hash)
	snap, err := sMap.Snapshot(true)
	if err != nil {
		t.Fatal(err)
	}

	if !snap.IsBacked() {
		t.Error("Snapshot should be backed")
	}

	// Verify snapshot has same data
	item1, found1, err := snap.Get(key1)
	if err != nil {
		t.Fatal(err)
	}
	if !found1 || !bytes.Equal(item1.Data(), intToBytes(1)) {
		t.Error("Snapshot should have key1")
	}

	item2, found2, err := snap.Get(key2)
	if err != nil {
		t.Fatal(err)
	}
	if !found2 || !bytes.Equal(item2.Data(), intToBytes(2)) {
		t.Error("Snapshot should have key2")
	}

	// Modify snapshot — should not affect original
	if err := snap.Put(key1, intToBytes(99)); err != nil {
		t.Fatal(err)
	}

	origItem, origFound, err := sMap.Get(key1)
	if err != nil {
		t.Fatal(err)
	}
	if !origFound || !bytes.Equal(origItem.Data(), intToBytes(1)) {
		t.Error("Original should be unaffected by snapshot modification")
	}

	// Hashes should differ now
	origHash, _ := sMap.Hash()
	snapHash, _ := snap.Hash()
	if origHash == snapHash {
		t.Error("Hashes should differ after snapshot modification")
	}
}

// TestBacked_NewBacked verifies NewBacked creates a working backed map.
func TestBacked_NewBacked(t *testing.T) {
	family := newMemoryFamily()

	sMap, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}

	if !sMap.IsBacked() {
		t.Error("NewBacked should create a backed map")
	}

	// Put and get
	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(42)); err != nil {
		t.Fatal(err)
	}

	item, found, err := sMap.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !bytes.Equal(item.Data(), intToBytes(42)) {
		t.Error("Get should return put data")
	}

	// Flush
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}
	if family.Len() == 0 {
		t.Error("Family should have entries after flush")
	}
}

// TestBacked_SetFamily verifies converting unbacked to backed.
func TestBacked_SetFamily(t *testing.T) {
	family := newMemoryFamily()

	// Start unbacked
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	if sMap.IsBacked() {
		t.Error("New() should create unbacked map")
	}

	key := hexToHash("092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7")
	if err := sMap.Put(key, intToBytes(1)); err != nil {
		t.Fatal(err)
	}

	// Convert to backed
	sMap.SetFamily(family)
	if !sMap.IsBacked() {
		t.Error("SetFamily should make map backed")
	}

	// Flush should work now
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}
	if family.Len() == 0 {
		t.Error("Family should have entries")
	}

	// Snapshot should use backed path
	rootHash, _ := sMap.Hash()
	snap, err := sMap.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	snapHash, _ := snap.Hash()
	if snapHash != rootHash {
		t.Error("Snapshot hash should match")
	}
}

// TestBacked_Iterator verifies iterator works with lazy loading.
func TestBacked_Iterator(t *testing.T) {
	family := newMemoryFamily()

	sMap, err := New(TypeState)
	if err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
		"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
		"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
	}

	for i, keyHex := range keys {
		key := hexToHash(keyHex)
		if err := sMap.Put(key, intToBytes(i+1)); err != nil {
			t.Fatal(err)
		}
	}

	rootHash, _ := sMap.Hash()
	if err := flushToFamily(sMap, family); err != nil {
		t.Fatal(err)
	}

	// Create backed map and use iterator
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	iter := backed.Begin()
	for iter.Next() {
		item := iter.Item()
		if item == nil {
			t.Error("Iterator item should not be nil")
		}
		count++
	}
	if err := iter.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(keys) {
		t.Errorf("Iterator found %d items, expected %d", count, len(keys))
	}
}
