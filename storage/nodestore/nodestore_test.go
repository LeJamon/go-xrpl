package nodestore_test

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const (
	minPayloadBytes  = 1
	maxPayloadBytes  = 2000
	numObjectsToTest = 100 // Reduced for faster tests
)

// Test helpers

func createPredictableBatch(t *testing.T, numObjects int, seed int64) []*nodestore.Node {
	t.Helper()

	rng := rand.New(rand.NewSource(seed))
	batch := make([]*nodestore.Node, numObjects)

	for i := 0; i < numObjects; i++ {
		// Generate node type
		nodeType := nodestore.NodeType(rng.Intn(4) + 1)
		if nodeType == 2 { // Skip removed transaction type
			nodeType = nodestore.NodeTransaction
		}

		// Generate random data
		dataSize := rng.Intn(maxPayloadBytes-minPayloadBytes) + minPayloadBytes
		data := make(nodestore.Blob, dataSize)

		for j := 0; j < len(data); j++ {
			data[j] = byte(rng.Intn(256))
		}

		// Create node
		batch[i] = nodestore.NewNode(nodeType, data)
	}

	return batch
}

func areBatchesEqual(t *testing.T, lhs, rhs []*nodestore.Node) bool {
	t.Helper()

	if len(lhs) != len(rhs) {
		return false
	}

	for i := range lhs {
		if !nodesEqual(lhs[i], rhs[i]) {
			return false
		}
	}
	return true
}

func nodesEqual(lhs, rhs *nodestore.Node) bool {
	if lhs.Type != rhs.Type {
		return false
	}
	if lhs.Hash != rhs.Hash {
		return false
	}
	return reflect.DeepEqual(lhs.Data, rhs.Data)
}

func sortBatch(batch []*nodestore.Node) {
	sort.Slice(batch, func(i, j int) bool {
		for k := 0; k < 32; k++ {
			if batch[i].Hash[k] < batch[j].Hash[k] {
				return true
			} else if batch[i].Hash[k] > batch[j].Hash[k] {
				return false
			}
		}
		return false
	})
}

// Basic tests (equivalent to Basics_test.cpp)

func TestNode(t *testing.T) {
	t.Run("Creation", func(t *testing.T) {
		data := nodestore.Blob("test data")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		if !node.IsValid() {
			t.Error("node should be valid")
		}

		if node.Size() != len(data) {
			t.Errorf("expected size %d, got %d", len(data), node.Size())
		}

		expectedHash := nodestore.ComputeHash256(data)
		if node.Hash != expectedHash {
			t.Error("hash mismatch")
		}
	})

	t.Run("InvalidNode", func(t *testing.T) {
		node := &nodestore.Node{
			Type: nodestore.NodeUnknown,
			Data: nil,
		}

		if node.IsValid() {
			t.Error("node should be invalid")
		}
	})

	t.Run("EmptyData", func(t *testing.T) {
		node := &nodestore.Node{
			Type: nodestore.NodeTransaction,
			Data: nodestore.Blob{},
		}

		if node.IsValid() {
			t.Error("node with empty data should be invalid")
		}
	})
}

func TestBatches(t *testing.T) {
	const seedValue = 50

	t.Run("Deterministic", func(t *testing.T) {
		batch1 := createPredictableBatch(t, numObjectsToTest, seedValue)
		batch2 := createPredictableBatch(t, numObjectsToTest, seedValue)

		if !areBatchesEqual(t, batch1, batch2) {
			t.Error("batches with same seed should be equal")
		}
	})

	t.Run("DifferentSeed", func(t *testing.T) {
		batch1 := createPredictableBatch(t, numObjectsToTest, seedValue)
		batch2 := createPredictableBatch(t, numObjectsToTest, seedValue+1)

		if areBatchesEqual(t, batch1, batch2) {
			t.Error("batches with different seeds should not be equal")
		}
	})

	t.Run("Sorting", func(t *testing.T) {
		batch := createPredictableBatch(t, 10, seedValue)
		original := make([]*nodestore.Node, len(batch))
		copy(original, batch)

		sortBatch(batch)

		// Verify it's actually sorted
		for i := 1; i < len(batch); i++ {
			for k := 0; k < 32; k++ {
				if batch[i-1].Hash[k] < batch[i].Hash[k] {
					break
				} else if batch[i-1].Hash[k] > batch[i].Hash[k] {
					t.Error("batch is not properly sorted")
					return
				}
			}
		}
	})
}
