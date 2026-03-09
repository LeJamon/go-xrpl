package shamap

import "sync"

// MemoryFamily is an in-memory implementation of the Family interface.
// It stores serialized nodes in a map keyed by their SHAMap hash.
// Suitable for testing and small datasets.
type MemoryFamily struct {
	mu    sync.RWMutex
	store map[[32]byte][]byte
}

// NewMemoryFamily creates a new in-memory Family.
func NewMemoryFamily() *MemoryFamily {
	return &MemoryFamily{
		store: make(map[[32]byte][]byte),
	}
}

// Fetch retrieves a node's serialized data by its hash.
// Returns nil, nil if the node is not found.
func (f *MemoryFamily) Fetch(hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, ok := f.store[hash]
	if !ok {
		return nil, nil
	}
	// Return a copy to avoid shared mutable state between SHAMap instances
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// StoreBatch persists a batch of serialized nodes.
func (f *MemoryFamily) StoreBatch(entries []FlushEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range entries {
		cp := make([]byte, len(e.Data))
		copy(cp, e.Data)
		f.store[e.Hash] = cp
	}
	return nil
}

// Len returns the number of stored nodes.
func (f *MemoryFamily) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.store)
}
