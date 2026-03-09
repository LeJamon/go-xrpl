package shamap

import (
	"testing"
)

func TestInvariantError(t *testing.T) {
	t.Run("WithError", func(t *testing.T) {
		nodeID := NewRootNodeID()
		inner := ErrInvalidType

		err := &InvariantError{
			NodeID:      nodeID,
			Description: "test error",
			Err:         inner,
		}

		str := err.Error()
		if str == "" {
			t.Error("Error() should return non-empty string")
		}

		if err.Unwrap() != inner {
			t.Error("Unwrap should return inner error")
		}
	})

	t.Run("WithoutError", func(t *testing.T) {
		nodeID := NewRootNodeID()

		err := &InvariantError{
			NodeID:      nodeID,
			Description: "test error",
		}

		str := err.Error()
		if str == "" {
			t.Error("Error() should return non-empty string")
		}

		if err.Unwrap() != nil {
			t.Error("Unwrap should return nil")
		}
	})
}

func TestInvariantCheckResult(t *testing.T) {
	t.Run("NoErrors", func(t *testing.T) {
		result := &InvariantCheckResult{
			Errors:            make([]*InvariantError, 0),
			NodesChecked:      10,
			LeavesChecked:     5,
			InnerNodesChecked: 5,
		}

		if result.HasErrors() {
			t.Error("HasErrors should return false")
		}

		str := result.String()
		if str == "" {
			t.Error("String() should return non-empty string")
		}
	})

	t.Run("WithErrors", func(t *testing.T) {
		result := &InvariantCheckResult{
			Errors: []*InvariantError{
				{Description: "error 1"},
				{Description: "error 2"},
			},
			NodesChecked:      10,
			LeavesChecked:     5,
			InnerNodesChecked: 5,
		}

		if !result.HasErrors() {
			t.Error("HasErrors should return true")
		}

		str := result.String()
		if str == "" {
			t.Error("String() should return non-empty string")
		}
	})
}

func TestInvariants(t *testing.T) {
	t.Run("EmptyMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		if err := sMap.Invariants(); err != nil {
			t.Errorf("Empty map should pass invariants: %v", err)
		}
	})

	t.Run("ValidMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Add some items - use non-zero keys to avoid zero-key validation error
		for i := byte(1); i <= 10; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Put(key, []byte{i, i + 1, i + 2}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		if err := sMap.Invariants(); err != nil {
			t.Errorf("Valid map should pass invariants: %v", err)
		}
	})

	t.Run("LargerMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Add many items - use non-zero keys to avoid zero-key validation error
		for i := byte(1); i <= 100; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		if err := sMap.Invariants(); err != nil {
			t.Errorf("Larger map should pass invariants: %v", err)
		}
	})

	t.Run("AfterDeletions", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Add items - use non-zero keys to avoid zero-key validation error
		for i := byte(1); i <= 20; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		// Delete some
		for i := byte(6); i <= 14; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Delete(key); err != nil {
				t.Fatalf("Failed to delete: %v", err)
			}
		}

		if err := sMap.Invariants(); err != nil {
			t.Errorf("Map after deletions should pass invariants: %v", err)
		}
	})
}

func TestInvariantsDetailed(t *testing.T) {
	t.Run("EmptyMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		result := sMap.InvariantsDetailed()
		if result.HasErrors() {
			t.Errorf("Empty map should have no errors: %+v", result.Errors)
		}
	})

	t.Run("ValidMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Use non-zero keys to avoid zero-key validation error
		for i := byte(1); i <= 10; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		result := sMap.InvariantsDetailed()
		if result.HasErrors() {
			t.Errorf("Valid map should have no errors: %+v", result.Errors)
		}

		if result.NodesChecked == 0 {
			t.Error("Should have checked some nodes")
		}

		if result.LeavesChecked == 0 {
			t.Error("Should have checked some leaves")
		}
	})
}

func TestVerifyHashes(t *testing.T) {
	t.Run("EmptyMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		if err := sMap.VerifyHashes(); err != nil {
			t.Errorf("Empty map should verify: %v", err)
		}
	})

	t.Run("ValidMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		for i := byte(0); i < 20; i++ {
			var key [32]byte
			key[0] = i
			if err := sMap.Put(key, []byte{i, i + 1}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		if err := sMap.VerifyHashes(); err != nil {
			t.Errorf("Valid map hashes should verify: %v", err)
		}
	})
}

func TestInvariantsWithDifferentMapTypes(t *testing.T) {
	mapTypes := []Type{TypeState, TypeTransaction}

	for _, mapType := range mapTypes {
		t.Run(mapType.String(), func(t *testing.T) {
			sMap, err := New(mapType)
			if err != nil {
				t.Fatalf("Failed to create SHAMap: %v", err)
			}

			// Use non-zero keys to avoid zero-key validation error
			for i := byte(1); i <= 10; i++ {
				var key [32]byte
				key[0] = i
				if err := sMap.Put(key, []byte{i, i + 1, i + 2}); err != nil {
					t.Fatalf("Failed to put: %v", err)
				}
			}

			if err := sMap.Invariants(); err != nil {
				t.Errorf("%s map should pass invariants: %v", mapType.String(), err)
			}
		})
	}
}

func TestInvariantsAfterSnapshot(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Use non-zero keys to avoid zero-key validation error
	for i := byte(1); i <= 10; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, []byte{i}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	// Create snapshot
	snapshot, err := sMap.Snapshot(false)
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}

	// Both should pass invariants
	if err := sMap.Invariants(); err != nil {
		t.Errorf("Original map should pass invariants: %v", err)
	}

	if err := snapshot.Invariants(); err != nil {
		t.Errorf("Snapshot should pass invariants: %v", err)
	}

	// Modify original
	var newKey [32]byte
	newKey[0] = 100
	if err := sMap.Put(newKey, []byte{100}); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	// Both should still pass
	if err := sMap.Invariants(); err != nil {
		t.Errorf("Modified map should pass invariants: %v", err)
	}

	if err := snapshot.Invariants(); err != nil {
		t.Errorf("Snapshot should still pass invariants: %v", err)
	}
}
