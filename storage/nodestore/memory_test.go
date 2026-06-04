package nodestore_test

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func TestMemoryBackend(t *testing.T) {
	t.Run("Creation", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if backend == nil {
			t.Fatal("NewMemoryBackend returned nil")
		}

		if backend.Name() != "memory" {
			t.Errorf("expected name 'memory', got %q", backend.Name())
		}

		if backend.FdRequired() != 0 {
			t.Errorf("expected 0 file descriptors, got %d", backend.FdRequired())
		}
	})

	t.Run("OpenClose", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()

		if backend.IsOpen() {
			t.Error("backend should not be open initially")
		}

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

	t.Run("StoreAndFetch", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Create and store a node
		data := nodestore.Blob("test data for memory backend")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		if status := backend.Store(node); status != nodestore.OK {
			t.Fatalf("failed to store node: %v", status)
		}

		// Fetch the node
		fetched, status := backend.Fetch(node.Hash)
		if status != nodestore.OK {
			t.Fatalf("failed to fetch node: %v", status)
		}

		if fetched == nil {
			t.Fatal("fetched node is nil")
		}

		if fetched.Hash != node.Hash {
			t.Error("fetched hash doesn't match")
		}

		if string(fetched.Data) != string(node.Data) {
			t.Error("fetched data doesn't match")
		}
	})

	t.Run("FetchNotFound", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Try to fetch a non-existent node
		hash := nodestore.ComputeHash256(nodestore.Blob("non-existent"))
		fetched, status := backend.Fetch(hash)

		if status != nodestore.NotFound {
			t.Errorf("expected NotFound, got %v", status)
		}

		if fetched != nil {
			t.Error("expected nil node")
		}
	})

	t.Run("StoreBatch", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Create batch of nodes
		nodes := make([]*nodestore.Node, 10)
		for i := range 10 {
			data := nodestore.Blob("batch data " + string(rune('A'+i)))
			nodes[i] = nodestore.NewNode(nodestore.NodeTransaction, data)
		}

		// Store batch
		if status := backend.StoreBatch(nodes); status != nodestore.OK {
			t.Fatalf("failed to store batch: %v", status)
		}

		// Verify all nodes were stored
		for _, node := range nodes {
			fetched, status := backend.Fetch(node.Hash)
			if status != nodestore.OK {
				t.Errorf("failed to fetch node from batch: %v", status)
			}
			if string(fetched.Data) != string(node.Data) {
				t.Error("fetched data doesn't match")
			}
		}
	})

	t.Run("FetchBatch", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Create and store nodes
		nodes := make([]*nodestore.Node, 5)
		hashes := make([]nodestore.Hash256, 5)
		for i := range 5 {
			data := nodestore.Blob("fetch batch data " + string(rune('A'+i)))
			nodes[i] = nodestore.NewNode(nodestore.NodeTransaction, data)
			hashes[i] = nodes[i].Hash
			backend.Store(nodes[i])
		}

		// Fetch batch
		fetched, status := backend.FetchBatch(hashes)
		if status != nodestore.OK {
			t.Fatalf("failed to fetch batch: %v", status)
		}

		if len(fetched) != len(nodes) {
			t.Errorf("expected %d nodes, got %d", len(nodes), len(fetched))
		}

		for i, node := range fetched {
			if node == nil {
				t.Errorf("node %d is nil", i)
				continue
			}
			if string(node.Data) != string(nodes[i].Data) {
				t.Errorf("node %d data doesn't match", i)
			}
		}
	})

	t.Run("HasNode", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		data := nodestore.Blob("has node test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		// Should not have the node initially
		if backend.HasNode(node.Hash) {
			t.Error("should not have node before storing")
		}

		// Store and check again
		backend.Store(node)
		if !backend.HasNode(node.Hash) {
			t.Error("should have node after storing")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		data := nodestore.Blob("delete test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		// Store the node
		backend.Store(node)
		if !backend.HasNode(node.Hash) {
			t.Fatal("node should exist after storing")
		}

		// Delete the node
		if status := backend.Delete(node.Hash); status != nodestore.OK {
			t.Fatalf("failed to delete node: %v", status)
		}

		// Should not have the node anymore
		if backend.HasNode(node.Hash) {
			t.Error("node should not exist after deletion")
		}
	})

	t.Run("ForEach", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some nodes
		expectedCount := 5
		for i := range expectedCount {
			data := nodestore.Blob("foreach test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		// Count nodes via ForEach
		count := 0
		err := backend.ForEach(func(node *nodestore.Node) error {
			count++
			return nil
		})

		if err != nil {
			t.Errorf("ForEach returned error: %v", err)
		}

		if count != expectedCount {
			t.Errorf("expected %d nodes, counted %d", expectedCount, count)
		}
	})

	t.Run("Clear", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some nodes
		for i := range 5 {
			data := nodestore.Blob("clear test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		if backend.Size() == 0 {
			t.Error("backend should have nodes before clear")
		}

		// Clear
		backend.Clear()

		if backend.Size() != 0 {
			t.Errorf("expected 0 nodes after clear, got %d", backend.Size())
		}
	})

	t.Run("Stats", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Perform some operations
		data := nodestore.Blob("stats test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		backend.Store(node)
		backend.Fetch(node.Hash)

		stats := backend.Stats()

		if stats.Writes < 1 {
			t.Error("expected at least 1 write")
		}

		if stats.Reads < 1 {
			t.Error("expected at least 1 read")
		}

		if stats.BytesWritten < int64(len(data)) {
			t.Error("bytes written should be at least data size")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		const goroutines = 10
		const opsPerGoroutine = 100

		var wg sync.WaitGroup
		wg.Add(goroutines)

		for g := range goroutines {
			go func(id int) {
				defer wg.Done()

				for i := range opsPerGoroutine {
					data := nodestore.Blob("concurrent " + string(rune('A'+id)) + string(rune('0'+i%10)))
					node := nodestore.NewNode(nodestore.NodeTransaction, data)

					// Store
					backend.Store(node)

					// Fetch
					backend.Fetch(node.Hash)

					// Has
					backend.HasNode(node.Hash)
				}
			}(g)
		}

		wg.Wait()

		// Backend should be in a consistent state
		if backend.Size() == 0 {
			t.Error("backend should have some nodes")
		}
	})

	t.Run("Info", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()

		info := backend.Info()

		if info.Name != "memory" {
			t.Errorf("expected name 'memory', got %q", info.Name)
		}

		if info.Persistent {
			t.Error("memory backend should not be persistent")
		}

		if info.FileDescriptors != 0 {
			t.Errorf("expected 0 file descriptors, got %d", info.FileDescriptors)
		}
	})

	t.Run("CloseClearsData", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}

		// Store some data
		for i := range 5 {
			data := nodestore.Blob("close clear test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		if backend.Size() == 0 {
			t.Error("backend should have nodes before close")
		}

		// Close should clear data
		backend.Close()

		// Reopen
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to reopen backend: %v", err)
		}

		// Data should be cleared
		if backend.Size() != 0 {
			t.Errorf("expected 0 nodes after close/reopen, got %d", backend.Size())
		}

		backend.Close()
	})
}

func TestMemoryBackendRegistration(t *testing.T) {
	// Memory backend should be registered
	if !nodestore.IsBackendAvailable("memory") {
		t.Error("memory backend should be registered")
	}

	// Should be able to create via factory
	config := &nodestore.Config{
		Backend: "memory",
	}

	backend, err := nodestore.CreateBackend("memory", config)
	if err != nil {
		t.Fatalf("failed to create memory backend via factory: %v", err)
	}

	if backend.Name() != "memory" {
		t.Errorf("expected name 'memory', got %q", backend.Name())
	}
}
