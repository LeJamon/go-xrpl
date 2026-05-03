// Package nodestore provides persistent key-value storage optimized for XRPL ledger objects.
// It offers content-addressable storage using SHA-256 hashes as keys with features like
// caching and asynchronous I/O.
package nodestore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

type Hash256 [32]byte
type Blob []byte

func Hash256FromData(b Blob) (Hash256, error) {
	if len(b) != 32 {
		return Hash256{}, fmt.Errorf("invalid hash length: expected 32 bytes, got %d", len(b))
	}
	var h Hash256
	copy(h[:], b)
	return h, nil
}

func IsZero(h Hash256) bool {
	return h == [32]byte{}
}

// ComputeHash256 computes SHA-256 hash from data
func ComputeHash256(data Blob) Hash256 {
	return Hash256(sha256.Sum256(data))
}

// NodeType represents the type of ledger object stored in the nodestore.
type NodeType uint32

const (
	// NodeUnknown represents an unknown or invalid node type
	NodeUnknown NodeType = 0
	// NodeLedger represents a complete ledger header
	NodeLedger NodeType = 1
	// NodeAccount represents an account state object
	NodeAccount NodeType = 3
	// NodeTransaction represents a transaction object
	NodeTransaction NodeType = 4
	// NodeDummy represents an invalid or missing object (used for negative caching)
	NodeDummy NodeType = 512
)

// String returns the string representation of the NodeType.
func (nt NodeType) String() string {
	switch nt {
	case NodeUnknown:
		return "NodeUnknown"
	case NodeLedger:
		return "NodeLedger"
	case NodeAccount:
		return "NodeAccount"
	case NodeTransaction:
		return "NodeTransaction"
	case NodeDummy:
		return "NodeDummy"
	default:
		return fmt.Sprintf("NodeType(%d)", uint32(nt))
	}
}

// Node represents a stored ledger object with its metadata.
type Node struct {
	Type      NodeType  // Type of the ledger object
	Hash      Hash256   // SHA-256 content hash (serves as the key)
	Data      Blob      // Serialized ledger object data
	LedgerSeq uint32    // Optional ledger sequence number
	CreatedAt time.Time // Timestamp when the node was created
}

// NewNode creates a new Node with the specified type and data.
// The hash is computed automatically from the data.
func NewNode(nodeType NodeType, data Blob) *Node {
	hash := ComputeHash256(data)
	return &Node{
		Type:      nodeType,
		Hash:      hash,
		Data:      append(Blob(nil), data...), // defensive copy
		CreatedAt: time.Now(),
	}
}

// Size returns the size of the node's data in bytes.
func (n *Node) Size() int {
	if n == nil {
		return 0
	}
	return len(n.Data)
}

// IsValid returns true if the node has valid data and hash.
func (n *Node) IsValid() bool {
	if n == nil {
		return false
	}
	if n.Type == NodeUnknown || n.Type == NodeDummy {
		return false
	}
	if len(n.Data) == 0 {
		return false
	}
	// Verify hash matches data
	expectedHash := ComputeHash256(n.Data)
	return n.Hash == expectedHash
}

// Result represents the result of an asynchronous operation.
type Result struct {
	Node *Node // The retrieved node (nil if not found or error occurred)
	Err  error // Error that occurred during the operation (nil if successful)
}

// Database defines the main interface for the NodeStore.
type Database interface {
	// Store persists a node to the store.
	Store(ctx context.Context, node *Node) error

	// Fetch retrieves a node by its hash synchronously.
	Fetch(ctx context.Context, hash Hash256) (*Node, error)

	// FetchBatch retrieves multiple nodes efficiently in a single operation.
	FetchBatch(ctx context.Context, hashes []Hash256) ([]*Node, error)

	// FetchAsync retrieves a node asynchronously, returning a channel for the result.
	FetchAsync(ctx context.Context, hash Hash256) <-chan Result

	// StoreBatch stores multiple nodes efficiently in a single operation.
	StoreBatch(ctx context.Context, nodes []*Node) error

	// Sweep removes expired entries from caches.
	Sweep() error

	// Stats returns performance statistics.
	Stats() Statistics

	// Close gracefully closes the database and releases resources.
	Close() error

	// Sync forces any pending writes to be flushed to disk.
	// The supplied ctx unblocks the caller on cancellation; the
	// underlying backend flush is uninterruptible and continues
	// running so partial fsync state is never observed.
	Sync(ctx context.Context) error
}

// Statistics holds performance metrics for the NodeStore.
type Statistics struct {
	// Read metrics
	Reads        uint64 // Total number of read operations
	CacheHits    uint64 // Number of successful cache hits
	CacheMisses  uint64 // Number of cache misses
	ReadBytes    uint64 // Total bytes read
	ReadDuration uint64 // Total read duration in microseconds

	// Write metrics
	Writes        uint64 // Total number of write operations
	WriteBytes    uint64 // Total bytes written
	WriteDuration uint64 // Total write duration in microseconds

	// Cache metrics
	CacheSize    uint64 // Current number of items in cache
	CacheMaxSize uint64 // Maximum cache size

	// Backend metrics
	BackendName string // Name of the storage backend
	AsyncReads  uint64 // Number of pending async reads
}

// String returns a formatted string representation of the statistics.
func (s Statistics) String() string {
	cacheHitRate := float64(0)
	if s.Reads > 0 {
		cacheHitRate = float64(s.CacheHits) / float64(s.Reads) * 100
	}

	return fmt.Sprintf(`NodeStore Statistics:
  Backend: %s
  Reads: %d (%.2f%% cache hit rate)
  Cache: %d/%d items
  Writes: %d
  Read Bytes: %d
  Write Bytes: %d
  Async Reads: %d`,
		s.BackendName,
		s.Reads, cacheHitRate,
		s.CacheSize, s.CacheMaxSize,
		s.Writes,
		s.ReadBytes,
		s.WriteBytes,
		s.AsyncReads)
}

// Status represents the status of a backend operation.
type Status int

const (
	// OK indicates the operation was successful
	OK Status = iota
	// NotFound indicates the requested object was not found
	NotFound
	// DataCorrupt indicates the stored data is corrupted
	DataCorrupt
	// BackendError indicates an error in the storage backend
	BackendError
	// Unknown indicates an unknown error occurred
	Unknown
)

// String returns the string representation of Status.
func (s Status) String() string {
	switch s {
	case OK:
		return "OK"
	case NotFound:
		return "NotFound"
	case DataCorrupt:
		return "DataCorrupt"
	case BackendError:
		return "BackendError"
	case Unknown:
		return "Unknown"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Backend defines the interface for storage backends.
type Backend interface {
	// Name returns a human-readable name for this backend.
	Name() string

	// Open opens the backend for use.
	Open(createIfMissing bool) error

	// Close closes the backend and releases resources.
	Close() error

	// IsOpen returns true if the backend is currently open.
	IsOpen() bool

	// Fetch retrieves a single object by key.
	Fetch(key Hash256) (*Node, Status)

	// FetchBatch retrieves multiple objects efficiently.
	FetchBatch(keys []Hash256) ([]*Node, Status)

	// Store saves a single object.
	Store(node *Node) Status

	// StoreBatch saves multiple objects efficiently.
	StoreBatch(nodes []*Node) Status

	// Sync forces pending writes to be flushed.
	Sync() Status

	// ForEach iterates over all objects in the backend.
	ForEach(fn func(*Node) error) error

	// GetWriteLoad returns an estimate of pending write operations.
	GetWriteLoad() int

	// SetDeletePath marks the backend for deletion when closed.
	SetDeletePath()

	// FdRequired returns the number of file descriptors needed.
	FdRequired() int
}
