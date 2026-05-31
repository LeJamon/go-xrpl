package adaptor

import (
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

const (
	// maxRecentTxs is the maximum number of recently seen transaction IDs to track.
	maxRecentTxs = 10000
)

// TxPool manages pending transactions with deduplication.
// It wraps the adaptor's pending tx map and adds a recently-seen cache
// to avoid processing duplicate transactions from multiple peers.
type TxPool struct {
	mu sync.RWMutex

	// pending transactions awaiting consensus inclusion
	pending map[consensus.TxID][]byte

	// recently seen tx IDs (for deduplication)
	recent    []consensus.TxID
	recentSet map[consensus.TxID]struct{}
}

// NewTxPool creates a new TxPool.
func NewTxPool() *TxPool {
	return &TxPool{
		pending:   make(map[consensus.TxID][]byte),
		recent:    make([]consensus.TxID, 0, maxRecentTxs),
		recentSet: make(map[consensus.TxID]struct{}, maxRecentTxs),
	}
}

// Add adds a transaction to the pending pool.
// Returns true if the transaction is new (not a duplicate).
func (p *TxPool) Add(blob []byte) bool {
	txID := computeTxID(blob)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Check recently seen cache
	if _, seen := p.recentSet[txID]; seen {
		return false
	}

	// Check pending
	if _, exists := p.pending[txID]; exists {
		return false
	}

	p.pending[txID] = blob
	p.addRecentLocked(txID)

	return true
}

// Has returns true if we have the transaction (pending or recently seen).
func (p *TxPool) Has(id consensus.TxID) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.pending[id]; ok {
		return true
	}
	_, ok := p.recentSet[id]
	return ok
}

// Get returns a transaction blob by ID, or nil if not found.
func (p *TxPool) Get(id consensus.TxID) []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pending[id]
}

// GetAll returns all pending transaction blobs.
func (p *TxPool) GetAll() [][]byte {
	p.mu.RLock()
	defer p.mu.RUnlock()

	blobs := make([][]byte, 0, len(p.pending))
	for _, blob := range p.pending {
		blobs = append(blobs, blob)
	}
	return blobs
}

// Remove removes specific transactions from the pending pool
// (e.g., after they've been included in a consensus ledger).
func (p *TxPool) Remove(ids []consensus.TxID) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, id := range ids {
		delete(p.pending, id)
	}
}

// Clear removes all pending transactions.
func (p *TxPool) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = make(map[consensus.TxID][]byte)
}

// Size returns the number of pending transactions.
func (p *TxPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pending)
}

// addRecentLocked adds a txID to the recently-seen ring buffer.
// Caller must hold the write lock.
func (p *TxPool) addRecentLocked(id consensus.TxID) {
	if len(p.recent) >= maxRecentTxs {
		// Evict oldest entry
		oldest := p.recent[0]
		p.recent = p.recent[1:]
		delete(p.recentSet, oldest)
	}
	p.recent = append(p.recent, id)
	p.recentSet[id] = struct{}{}
}
