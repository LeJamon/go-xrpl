package shamap

// Family provides access to a persistent store for backed SHAMap instances.
// Each SHAMap independently fetches and deserializes nodes from the Family,
// ensuring no shared mutable state between SHAMap instances.
type Family interface {
	// Fetch retrieves a node's serialized data (prefix format) by its SHAMap hash.
	// Returns nil, nil if the node is not found.
	Fetch(hash [32]byte) ([]byte, error)

	// StoreBatch persists a batch of serialized nodes.
	StoreBatch(entries []FlushEntry) error
}
