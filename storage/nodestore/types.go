package nodestore

import (
	"context"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// Hash256 is a 32-byte content key — an XRPL SHA-512Half digest.
type Hash256 [32]byte

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
)

// Node represents a stored ledger object with its metadata.
//
// Nodes are immutable once stored, cached, or returned from
// Database.Fetch — mutating Data after that point corrupts every other
// holder of the same pointer. The node cache deep-copies on insert so a
// caller may construct, Store, and continue using its local pointer;
// downstream readers see an isolated copy.
type Node struct {
	Type      NodeType // Type of the ledger object
	Hash      Hash256  // Content key (caller-supplied SHA-512Half for production nodes); stored verbatim
	Data      []byte   // Serialized ledger object data
	LedgerSeq uint32   // Optional ledger sequence number
}

// Clone returns a deep copy of the node with its own Data buffer,
// enforcing the immutability contract at cache and Fetch boundaries.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	data := make([]byte, len(n.Data))
	copy(data, n.Data)
	return &Node{
		Type:      n.Type,
		Hash:      n.Hash,
		Data:      data,
		LedgerSeq: n.LedgerSeq,
	}
}

// Database defines the main interface for the NodeStore.
type Database interface {
	// Store persists a node to the store.
	Store(ctx context.Context, node *Node) error

	// Fetch retrieves a node by its hash synchronously and may be called
	// concurrently. A node that is not present is reported in-band as (nil, nil):
	// a nil node with a nil error. A non-nil error signals an actual I/O or decode
	// failure, not absence.
	Fetch(ctx context.Context, hash Hash256) (*Node, error)

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
	//
	// Sync calls are serialized. Cancellation unblocks the caller without
	// allowing a later Sync or Close to overlap the backend flush.
	Sync(ctx context.Context) error
}

// GenerationDatabase is a Database backed by two rotating generations.
// FetchForPromotion bypasses the decoded-node cache so an archive hit is
// durably copied into the writable generation without populating that cache.
type GenerationDatabase interface {
	Database
	CanRotateWithoutRefresh(ctx context.Context) (bool, error)
	FetchForPromotion(ctx context.Context, hash Hash256) (*Node, error)
	RotateGeneration(ctx context.Context, lastRotated, minimumOnline uint32) (committed bool, err error)
	GenerationState() (lastRotated, minimumOnline uint32)
}

// BatchGenerationDatabase extends a generation database with bounded batch promotion.
type BatchGenerationDatabase interface {
	GenerationDatabase
	FetchBatchForPromotion(ctx context.Context, hashes []Hash256, maxBytes int) ([]*Node, kvstore.PromotionStats, error)
}

// Statistics holds performance metrics for the NodeStore.
type Statistics struct {
	Reads       uint64
	FetchHits   uint64
	CacheHits   uint64
	CacheMisses uint64
	CacheSize   uint64
	ReadBytes   uint64
	Writes      uint64
	WriteBytes  uint64
}
