package store

import (
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
)

// NodeStore handles storage and retrieval of Nodes using LevelDB
type NodeStore struct {
	db *leveldb.DB
}

// NewNodeStore creates a new NodeStore with the given keyValueDb path
func NewNodeStore(dbPath string) (*NodeStore, error) {
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open keyValueDb: %w", err)
	}
	return &NodeStore{db: db}, nil
}

// Close closes the keyValueDb
func (s *NodeStore) Close() error {
	return s.db.Close()
}

// Store saves a Node to the keyValueDb
func (s *NodeStore) Store(node *Node) error {
	if node == nil {
		return fmt.Errorf("cannot store nil node")
	}

	// Create key from node hash
	key := node.Hash()

	// Serialize node data
	// Format: [1 byte type][variable length data]
	dataSize := 1 + len(node.Data())
	serialized := make([]byte, dataSize)

	// Write type
	serialized[0] = byte(node.Type())

	// Write data
	copy(serialized[1:], node.Data())

	// Store in leveldb
	err := s.db.Put(key[:], serialized, nil)
	if err != nil {
		return fmt.Errorf("failed to store node: %w", err)
	}

	return nil
}

// Fetch retrieves a Node from the keyValueDb
func (s *NodeStore) Fetch(hash [32]byte) (*Node, error) {
	data, err := s.db.Get(hash[:], nil)
	if err == leveldb.ErrNotFound {
		return nil, nil // Return nil if not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch node: %w", err)
	}

	if len(data) < 1 {
		return nil, fmt.Errorf("corrupted data: too short")
	}

	// Extract type and data
	nodeType := ObjectType(data[0])
	nodeData := make([]byte, len(data)-1)
	copy(nodeData, data[1:])

	return New(nodeType, nodeData, hash), nil
}

// Delete removes a Node from the keyValueDb
func (s *NodeStore) Delete(hash [32]byte) error {
	err := s.db.Delete(hash[:], nil)
	if err != nil {
		return fmt.Errorf("failed to delete node: %w", err)
	}
	return nil
}

// Exists checks if a Node exists in the keyValueDb
func (s *NodeStore) Exists(hash [32]byte) (bool, error) {
	exists, err := s.db.Has(hash[:], nil)
	if err != nil {
		return false, fmt.Errorf("failed to check node existence: %w", err)
	}
	return exists, nil
}

// Batch represents a batch write operation
type Batch struct {
	batch *leveldb.Batch
	store *NodeStore
}

// NewBatch creates a new batch operation
func (s *NodeStore) NewBatch() *Batch {
	return &Batch{
		batch: new(leveldb.Batch),
		store: s,
	}
}

// Store adds a node to the batch
func (b *Batch) Store(node *Node) error {
	if node == nil {
		return fmt.Errorf("cannot store nil node")
	}

	key := node.Hash()

	dataSize := 1 + len(node.Data())
	serialized := make([]byte, dataSize)
	serialized[0] = byte(node.Type())
	copy(serialized[1:], node.Data())

	b.batch.Put(key[:], serialized)
	return nil
}

// Execute executes the batch operation
func (b *Batch) Execute() error {
	err := b.store.db.Write(b.batch, nil)
	if err != nil {
		return fmt.Errorf("failed to execute batch: %w", err)
	}
	return nil
}
