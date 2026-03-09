package shamap

import (
	"testing"
)

func TestNodeData(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		nd := &NodeData{
			Hash: [32]byte{1, 2, 3, 4, 5, 6, 7, 8},
			Data: []byte{1, 2, 3, 4, 5},
		}

		str := nd.String()
		if str == "" {
			t.Error("String() should return non-empty string")
		}
	})

	t.Run("Clone", func(t *testing.T) {
		original := &NodeData{
			Hash: [32]byte{1, 2, 3, 4, 5, 6, 7, 8},
			Data: []byte{1, 2, 3, 4, 5},
		}

		clone := original.Clone()

		// Check hash matches
		if clone.Hash != original.Hash {
			t.Error("Cloned hash should match")
		}

		// Check data matches
		if len(clone.Data) != len(original.Data) {
			t.Error("Cloned data length should match")
		}

		// Modify clone should not affect original
		clone.Data[0] = 99
		if original.Data[0] == 99 {
			t.Error("Clone should be independent copy")
		}
	})
}

func TestGetNodeFat(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items to create a tree structure
	for i := byte(0); i < 20; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i, i + 1, i + 2}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	t.Run("Depth0", func(t *testing.T) {
		rootHash, err := sMap.Hash()
		if err != nil {
			t.Fatalf("Failed to get hash: %v", err)
		}

		nodes, err := sMap.GetNodeFat(rootHash, 0)
		if err != nil {
			t.Fatalf("GetNodeFat failed: %v", err)
		}

		if len(nodes) != 1 {
			t.Errorf("Depth 0 should return 1 node, got %d", len(nodes))
		}

		if nodes[0].Hash != rootHash {
			t.Error("First node should be the requested node")
		}
	})

	t.Run("Depth1", func(t *testing.T) {
		rootHash, _ := sMap.Hash()

		nodes, err := sMap.GetNodeFat(rootHash, 1)
		if err != nil {
			t.Fatalf("GetNodeFat failed: %v", err)
		}

		// Should have root + children
		if len(nodes) < 2 {
			t.Errorf("Depth 1 should return multiple nodes, got %d", len(nodes))
		}
	})

	t.Run("InvalidDepth", func(t *testing.T) {
		rootHash, _ := sMap.Hash()

		_, err := sMap.GetNodeFat(rootHash, -1)
		if err == nil {
			t.Error("Negative depth should fail")
		}
	})

	t.Run("NonExistentNode", func(t *testing.T) {
		var fakeHash [32]byte
		fakeHash[0] = 0xFF

		_, err := sMap.GetNodeFat(fakeHash, 0)
		if err == nil {
			t.Error("Non-existent node should fail")
		}
	})
}

func TestSerializeRoot(t *testing.T) {
	t.Run("WithContent", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		var key [32]byte
		key[0] = 1
		if err := sMap.Put(key, []byte{1, 2, 3}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}

		data, err := sMap.SerializeRoot()
		if err != nil {
			t.Fatalf("SerializeRoot failed: %v", err)
		}

		if len(data) == 0 {
			t.Error("Serialized data should not be empty")
		}
	})

	t.Run("EmptyMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Empty root should still serialize (though it may fail)
		_, err = sMap.SerializeRoot()
		// This may or may not fail depending on implementation
		// Just verify it doesn't panic
	})
}

func TestGetNodeByHash(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	var key [32]byte
	key[0] = 1
	if err := sMap.Put(key, []byte{1, 2, 3}); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	rootHash, _ := sMap.Hash()

	t.Run("ExistingNode", func(t *testing.T) {
		node, err := sMap.GetNodeByHash(rootHash)
		if err != nil {
			t.Fatalf("GetNodeByHash failed: %v", err)
		}

		if node.Hash() != rootHash {
			t.Error("Hash mismatch")
		}
	})

	t.Run("NonExistentNode", func(t *testing.T) {
		var fakeHash [32]byte
		fakeHash[0] = 0xFF

		_, err := sMap.GetNodeByHash(fakeHash)
		if err == nil {
			t.Error("Should fail for non-existent node")
		}
	})
}

func TestGetSerializedNode(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	var key [32]byte
	key[0] = 1
	if err := sMap.Put(key, []byte{1, 2, 3}); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	rootHash, _ := sMap.Hash()

	data, err := sMap.GetSerializedNode(rootHash)
	if err != nil {
		t.Fatalf("GetSerializedNode failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Serialized data should not be empty")
	}
}

func TestGetChildHashes(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items to create children
	for i := byte(0); i < 10; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	rootHash, _ := sMap.Hash()

	hashes, err := sMap.GetChildHashes(rootHash)
	if err != nil {
		t.Fatalf("GetChildHashes failed: %v", err)
	}

	if len(hashes) == 0 {
		t.Error("Root should have children")
	}
}

func TestBulkGetNodes(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items
	for i := byte(0); i < 5; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	rootHash, _ := sMap.Hash()
	childHashes, _ := sMap.GetChildHashes(rootHash)

	// Request root and some children
	requested := append([][32]byte{rootHash}, childHashes...)

	results, err := sMap.BulkGetNodes(requested)
	if err != nil {
		t.Fatalf("BulkGetNodes failed: %v", err)
	}

	// Should find at least the root
	if _, found := results[rootHash]; !found {
		t.Error("Should find root node")
	}

	// Should find some children
	found := 0
	for _, h := range childHashes {
		if _, ok := results[h]; ok {
			found++
		}
	}

	if found == 0 && len(childHashes) > 0 {
		t.Error("Should find some child nodes")
	}
}

func TestCreateWireMessage(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items
	for i := byte(0); i < 5; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	sMap.SetLedgerSeq(12345)

	t.Run("AllNodes", func(t *testing.T) {
		msg, err := sMap.CreateWireMessage(nil, 0)
		if err != nil {
			t.Fatalf("CreateWireMessage failed: %v", err)
		}

		if msg.MapType != TypeState {
			t.Error("MapType mismatch")
		}

		if msg.Seq != 12345 {
			t.Errorf("Seq mismatch: expected 12345, got %d", msg.Seq)
		}

		if len(msg.Nodes) == 0 {
			t.Error("Should have nodes")
		}
	})

	t.Run("MaxNodes", func(t *testing.T) {
		msg, err := sMap.CreateWireMessage(nil, 2)
		if err != nil {
			t.Fatalf("CreateWireMessage failed: %v", err)
		}

		if len(msg.Nodes) > 2 {
			t.Errorf("Should have at most 2 nodes, got %d", len(msg.Nodes))
		}
	})

	t.Run("SpecificNodes", func(t *testing.T) {
		rootHash, _ := sMap.Hash()

		msg, err := sMap.CreateWireMessage([][32]byte{rootHash}, 0)
		if err != nil {
			t.Fatalf("CreateWireMessage failed: %v", err)
		}

		found := false
		for _, n := range msg.Nodes {
			if n.Hash == rootHash {
				found = true
				break
			}
		}

		if !found {
			t.Error("Should find requested node")
		}
	})
}

func TestFindNodeByHash(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add items to create a deeper tree
	for i := byte(0); i < 20; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i, i + 1}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	// Should be able to find all nodes
	rootHash, _ := sMap.Hash()

	sMap.mu.RLock()
	defer sMap.mu.RUnlock()

	node := sMap.findNodeByHash(rootHash)
	if node == nil {
		t.Error("Should find root node")
	}
}
