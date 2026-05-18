package shamap

import "context"

// Family provides access to a persistent store for backed SHAMap instances.
// Each SHAMap independently fetches and deserializes nodes from the Family,
// ensuring no shared mutable state between SHAMap instances.
type Family interface {
	// Fetch returns the node's serialized data (prefix format) by SHAMap hash,
	// or (nil, nil) when absent.
	Fetch(ctx context.Context, hash [32]byte) ([]byte, error)

	// StoreBatch persists a batch of serialized nodes.
	StoreBatch(ctx context.Context, entries []FlushEntry) error
}
