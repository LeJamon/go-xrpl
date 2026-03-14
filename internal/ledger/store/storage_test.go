package store

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDB(t *testing.T) (*NodeStore, string) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	log.Printf("Setting up test keyValueDb at: %s", dbPath)

	store, err := NewNodeStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create NodeStore: %v", err)
	}

	return store, tempDir
}

func TestNodeStore(t *testing.T) {
	log.Println("Starting TestNodeStore")
	store, tempDir := setupTestDB(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()

	// Create test node
	testData := []byte("test data")
	testHash := [32]byte{1, 2, 3}
	node := New(TypeLedger, testData, testHash)
	log.Printf("Created test node - Type: %v, Hash: %x", node.Type(), node.Hash())

	// Test Store
	log.Println("Testing Store operation")
	err := store.Store(node)
	if err != nil {
		t.Fatalf("Failed to store node: %v", err)
	}
	log.Println("Successfully stored node")

	// Test Exists
	log.Printf("Testing Exists operation for hash: %x", testHash)
	exists, err := store.Exists(testHash)
	if err != nil {
		t.Fatalf("Failed to check existence: %v", err)
	}
	if !exists {
		t.Error("Node should exist but doesn't")
	}
	log.Printf("Exists check result: %v", exists)

	// Test Fetch
	log.Println("Testing Fetch operation")
	fetchedNode, err := store.Fetch(testHash)
	if err != nil {
		t.Fatalf("Failed to fetch node: %v", err)
	}
	if fetchedNode == nil {
		t.Fatal("Failed to fetch node: returned nil")
	}
	log.Printf("Successfully fetched node - Type: %v, Hash: %x",
		fetchedNode.Type(), fetchedNode.Hash())

	// Compare fetched node with original
	log.Println("Comparing fetched node with original")
	if fetchedNode.Type() != node.Type() {
		t.Errorf("Type mismatch: got %v, want %v", fetchedNode.Type(), node.Type())
	}
	if !bytes.Equal(fetchedNode.Data(), node.Data()) {
		t.Errorf("Data mismatch: got %v, want %v", fetchedNode.Data(), node.Data())
	}
	if fetchedNode.Hash() != node.Hash() {
		t.Errorf("Hash mismatch: got %x, want %x", fetchedNode.Hash(), node.Hash())
	}
	log.Println("Node comparison completed successfully")

	// Test Delete
	log.Printf("Testing Delete operation for hash: %x", testHash)
	err = store.Delete(testHash)
	if err != nil {
		t.Fatalf("Failed to delete node: %v", err)
	}
	log.Println("Successfully deleted node")

	// Verify deletion
	log.Println("Verifying deletion")
	exists, err = store.Exists(testHash)
	if err != nil {
		t.Fatalf("Failed to check existence after deletion: %v", err)
	}
	if exists {
		t.Error("Node should not exist after deletion")
	}
	log.Printf("Deletion verification completed, exists = %v", exists)
}

func TestBatchOperations(t *testing.T) {
	log.Println("Starting TestBatchOperations")
	store, tempDir := setupTestDB(t)
	defer os.RemoveAll(tempDir)
	defer store.Close()

	// Create test nodes
	nodes := make([]*Node, 3)
	for i := range nodes {
		hash := [32]byte{byte(i + 1)}
		nodes[i] = New(TypeLedger, []byte(fmt.Sprintf("data %d", i)), hash)
		log.Printf("Created test node %d - Hash: %x", i, hash)
	}

	// Create and execute batch
	log.Println("Creating new batch")
	batch := store.NewBatch()
	for i, node := range nodes {
		log.Printf("Adding node %d to batch", i)
		err := batch.Store(node)
		if err != nil {
			t.Fatalf("Failed to add node to batch: %v", err)
		}
	}

	log.Println("Executing batch")
	err := batch.Execute()
	if err != nil {
		t.Fatalf("Failed to execute batch: %v", err)
	}
	log.Println("Batch execution completed")

	// Verify all nodes were stored
	log.Println("Verifying stored nodes")
	for i, node := range nodes {
		exists, err := store.Exists(node.Hash())
		if err != nil {
			t.Fatalf("Failed to check existence: %v", err)
		}
		if !exists {
			t.Error("Node should exist but doesn't")
		}
		log.Printf("Verified node %d exists: %v", i, exists)
	}
	log.Println("Batch operations test completed successfully")
}
