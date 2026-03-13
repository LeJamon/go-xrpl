package nodestore_test

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"

	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/nodestore"
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

func storeBatch(t *testing.T, backend nodestore.Backend, batch []*nodestore.Node) {
	t.Helper()

	for i, node := range batch {
		if status := backend.Store(node); status != nodestore.OK {
			t.Fatalf("failed to store node %d: %v", i, status)
		}
	}
}

func fetchBatch(t *testing.T, backend nodestore.Backend, batch []*nodestore.Node) []*nodestore.Node {
	t.Helper()

	result := make([]*nodestore.Node, 0, len(batch))

	for i, original := range batch {
		node, status := backend.Fetch(original.Hash)
		if status != nodestore.OK {
			t.Fatalf("failed to fetch node %d: %v", i, status)
		}
		if node != nil {
			result = append(result, node)
		}
	}

	return result
}

func setupTempBackend(t *testing.T) (nodestore.Backend, func()) {
	t.Helper()

	tempDir := t.TempDir()
	config := &nodestore.Config{
		Path:       filepath.Join(tempDir, "test.db"),
		Compressor: "none",
	}

	backend, err := nodestore.NewPebbleBackend(config)
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	cleanup := func() {
		backend.Close()
	}

	return backend, cleanup
}

func setupBenchBackend(b *testing.B) (nodestore.Backend, func()) {
	b.Helper()

	tempDir := b.TempDir()
	config := &nodestore.Config{
		Path:       filepath.Join(tempDir, "bench.db"),
		Compressor: "none",
	}

	backend, err := nodestore.NewPebbleBackend(config)
	if err != nil {
		b.Fatalf("failed to create backend: %v", err)
	}

	if err := backend.Open(true); err != nil {
		backend.Close()
		b.Fatalf("failed to open backend: %v", err)
	}

	cleanup := func() {
		backend.Close()
	}

	return backend, cleanup
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

// Backend tests (equivalent to Backend_test.cpp)

func TestBackend(t *testing.T) {
	backend, cleanup := setupTempBackend(t)
	defer cleanup()

	if err := backend.Open(true); err != nil {
		t.Fatalf("failed to open backend: %v", err)
	}
	defer backend.Close()

	t.Run("StoreAndFetch", func(t *testing.T) {
		batch := createPredictableBatch(t, 50, 123)

		// Store
		storeBatch(t, backend, batch)

		// Fetch and compare
		fetched := fetchBatch(t, backend, batch)

		if !areBatchesEqual(t, batch, fetched) {
			t.Error("fetched batch doesn't match stored batch")
		}
	})

	t.Run("ReorderedFetch", func(t *testing.T) {
		batch := createPredictableBatch(t, 20, 456)
		storeBatch(t, backend, batch)

		// Shuffle the batch
		rand.Shuffle(len(batch), func(i, j int) {
			batch[i], batch[j] = batch[j], batch[i]
		})

		// Fetch in new order
		fetched := fetchBatch(t, backend, batch)

		if !areBatchesEqual(t, batch, fetched) {
			t.Error("reordered fetch failed")
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		// Create a node but don't store it
		data := nodestore.Blob("missing data")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		fetchedNode, status := backend.Fetch(node.Hash)
		if status != nodestore.NotFound {
			t.Errorf("expected NotFound, got %v", status)
		}
		if fetchedNode != nil {
			t.Error("expected nil node for not found")
		}
	})

	t.Run("BatchOperations", func(t *testing.T) {
		batch := createPredictableBatch(t, 10, 999)

		// Store batch
		if status := backend.StoreBatch(batch); status != nodestore.OK {
			t.Errorf("batch store failed: %v", status)
		}

		// Fetch batch
		hashes := make([]nodestore.Hash256, len(batch))
		for i, node := range batch {
			hashes[i] = node.Hash
		}

		fetched, status := backend.FetchBatch(hashes)
		if status != nodestore.OK {
			t.Errorf("batch fetch failed: %v", status)
		}

		if len(fetched) != len(batch) {
			t.Errorf("expected %d nodes, got %d", len(batch), len(fetched))
		}
	})
}

func TestBackendPersistence(t *testing.T) {
	tempDir := t.TempDir()

	config := &nodestore.Config{
		Path:       filepath.Join(tempDir, "test.db"),
		Compressor: "none",
	}

	batch := createPredictableBatch(t, 100, 456)

	// Store data
	func() {
		backend, err := nodestore.NewPebbleBackend(config)
		if err != nil {
			t.Fatalf("failed to create backend: %v", err)
		}
		defer backend.Close()

		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}

		storeBatch(t, backend, batch)
	}()

	// Verify persistence
	func() {
		backend, err := nodestore.NewPebbleBackend(config)
		if err != nil {
			t.Fatalf("failed to create backend: %v", err)
		}
		defer backend.Close()

		if err := backend.Open(false); err != nil {
			t.Fatalf("failed to reopen backend: %v", err)
		}

		fetched := fetchBatch(t, backend, batch)

		// Sort both for comparison
		sortBatch(batch)
		sortBatch(fetched)

		if !areBatchesEqual(t, batch, fetched) {
			t.Error("persisted data doesn't match original")
		}
	}()
}

func TestBackendProperties(t *testing.T) {
	backend, cleanup := setupTempBackend(t)
	defer cleanup()

	t.Run("InitialState", func(t *testing.T) {
		if backend.IsOpen() {
			t.Error("backend should not be open initially")
		}

		name := backend.Name()
		if name == "" {
			t.Error("backend name should not be empty")
		}

		fdRequired := backend.FdRequired()
		if fdRequired <= 0 {
			t.Error("backend should require some file descriptors")
		}
	})

	t.Run("OpenClose", func(t *testing.T) {
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}

		if !backend.IsOpen() {
			t.Error("backend should be open after Open()")
		}

		if err := backend.Close(); err != nil {
			t.Errorf("failed to close backend: %v", err)
		}

		if backend.IsOpen() {
			t.Error("backend should not be open after Close()")
		}
	})

	t.Run("WriteLoad", func(t *testing.T) {
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		writeLoad := backend.GetWriteLoad()
		// Write load can be 0 or positive
		if writeLoad < 0 {
			t.Error("write load should not be negative")
		}
	})
}

// Timing/Performance tests (equivalent to Timing_test.cpp)

func TestWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping workload test in short mode")
	}

	backend, cleanup := setupTempBackend(t)
	defer cleanup()

	if err := backend.Open(true); err != nil {
		t.Fatalf("failed to open backend: %v", err)
	}
	defer backend.Close()

	t.Run("MixedWorkload", func(t *testing.T) {
		// Store initial data
		initialBatch := createPredictableBatch(t, 100, 111)
		storeBatch(t, backend, initialBatch)

		// Simulate mixed workload
		rng := rand.New(rand.NewSource(222))

		for i := 0; i < 200; i++ {
			switch rng.Intn(3) {
			case 0: // Insert new
				data := make(nodestore.Blob, rng.Intn(1000)+100)
				rand.Read(data)
				node := nodestore.NewNode(nodestore.NodeTransaction, data)
				backend.Store(node)

			case 1: // Fetch existing
				idx := rng.Intn(len(initialBatch))
				node, status := backend.Fetch(initialBatch[idx].Hash)
				if status != nodestore.OK || node == nil {
					t.Errorf("failed to fetch existing node: %v", status)
				}

			case 2: // Try to fetch missing
				data := make(nodestore.Blob, rng.Intn(100)+50)
				rand.Read(data)
				missingHash := nodestore.ComputeHash256(data)
				_, status := backend.Fetch(missingHash)
				// Should be NotFound (or OK if we happen to hit an existing hash)
				if status != nodestore.NotFound && status != nodestore.OK {
					t.Errorf("unexpected status for missing node: %v", status)
				}
			}
		}
	})
}

// Benchmarks

func BenchmarkBackend(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	backend, cleanup := setupBenchBackend(b)
	defer cleanup()

	b.Run("Store", func(b *testing.B) {
		batch := createPredictableBatch(&testing.T{}, b.N, time.Now().UnixNano())

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			backend.Store(batch[i])
		}
	})

	b.Run("Fetch", func(b *testing.B) {
		// Setup: store some data first
		const setupSize = 1000
		batch := createPredictableBatch(&testing.T{}, setupSize, 789)
		for _, node := range batch {
			backend.Store(node)
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			idx := i % len(batch)
			backend.Fetch(batch[idx].Hash)
		}
	})

	b.Run("Mixed", func(b *testing.B) {
		// Setup initial data
		const setupSize = 500
		batch := createPredictableBatch(&testing.T{}, setupSize, 456)
		for _, node := range batch {
			backend.Store(node)
		}

		extraBatch := createPredictableBatch(&testing.T{}, b.N, time.Now().UnixNano())
		var counter int64

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			if i%5 == 0 { // 20% stores, 80% fetches
				idx := atomic.AddInt64(&counter, 1) - 1
				if int(idx) < len(extraBatch) {
					backend.Store(extraBatch[idx])
				}
			} else {
				idx := i % len(batch)
				backend.Fetch(batch[idx].Hash)
			}
		}
	})
}

func BenchmarkParallelStore(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	backend, cleanup := setupBenchBackend(b)
	defer cleanup()

	batch := createPredictableBatch(&testing.T{}, b.N, time.Now().UnixNano())
	var counter int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			idx := atomic.AddInt64(&counter, 1) - 1
			if int(idx) >= len(batch) {
				return
			}
			backend.Store(batch[idx])
		}
	})
}

// Error handling tests

func TestBackendErrors(t *testing.T) {
	backend, cleanup := setupTempBackend(t)
	defer cleanup()

	t.Run("OperationsOnClosedBackend", func(t *testing.T) {
		// Try operations on unopened backend
		node := createPredictableBatch(t, 1, 999)[0]

		if status := backend.Store(node); status == nodestore.OK {
			t.Error("store should fail on closed backend")
		}

		if _, status := backend.Fetch(node.Hash); status == nodestore.OK {
			t.Error("fetch should fail on closed backend")
		}
	})

	t.Run("InvalidOperations", func(t *testing.T) {
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store nil node
		if status := backend.Store(nil); status == nodestore.OK {
			t.Error("storing nil node should fail")
		}
	})
}

// Cleanup test
func TestCleanup(t *testing.T) {
	tempDir := t.TempDir()

	dbPath := filepath.Join(tempDir, "test.db")
	config := &nodestore.Config{
		Path:       dbPath, // This is what gets deleted
		Compressor: "none",
	}

	backend, err := nodestore.NewPebbleBackend(config)
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	// Test SetDeletePath
	backend.SetDeletePath()

	if err := backend.Open(true); err != nil {
		t.Fatalf("failed to open backend: %v", err)
	}

	// Store some data
	batch := createPredictableBatch(t, 10, 777)
	storeBatch(t, backend, batch)

	if err := backend.Close(); err != nil {
		t.Errorf("failed to close backend: %v", err)
	}

	// Check that the DATABASE PATH was deleted, not the temp dir
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("database directory should have been deleted")
	}

	// The parent tempDir should still exist
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		t.Error("parent temp directory should still exist")
	}
}
