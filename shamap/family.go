package shamap

import "context"

// Family provides access to a persistent store for backed SHAMap instances.
// Each SHAMap independently fetches and deserializes nodes from the Family,
// ensuring no shared mutable state between SHAMap instances.
//
// Both Fetch and StoreBatch accept a context.Context so that callers
// driven by an RPC, peer request, or other cancellable operation can
// abort slow storage I/O instead of blocking a SHAMap mutator
// indefinitely. Existing internal call sites without a real ctx pass
// context.Background().
type Family interface {
	// Fetch retrieves a node's serialized data (prefix format) by its SHAMap hash.
	// Returns nil, nil if the node is not found. Returns ctx.Err() if
	// the context is cancelled.
	Fetch(ctx context.Context, hash [32]byte) ([]byte, error)

	// StoreBatch persists a batch of serialized nodes. Returns
	// ctx.Err() if the context is cancelled.
	StoreBatch(ctx context.Context, entries []FlushEntry) error
}
