package manager

import (
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/hashicorp/golang-lru/v2"
)

// LedgerCache provides fast access to recently used ledgers and tracks
// which ledgers are complete locally
type LedgerCache struct {
	mu sync.RWMutex

	// In-memory cache of recently accessed ledgers
	// Key: ledger sequence number
	recentBySeq *lru.Cache[uint32, *ledger.Ledger]

	// Cache by hash for faster hash-based lookups
	// Key: ledger hash as string (hex)
	recentByHash *lru.Cache[[32]byte, *ledger.Ledger]

	// Track which ledgers we have complete locally
	completeness *CompleteLedgerSet

	// Metrics
	hits   uint64
	misses uint64
}

// LedgerCacheConfig holds configuration for the cache
type LedgerCacheConfig struct {
	// MaxRecentLedgers is the number of ledgers to keep in memory
	MaxRecentLedgers int
}

// NewLedgerCache creates a new ledger cache
func NewLedgerCache(config LedgerCacheConfig) (*LedgerCache, error) {
	if config.MaxRecentLedgers <= 0 {
		config.MaxRecentLedgers = 256 // Default cache size
	}

	seqCache, err := lru.New[uint32, *ledger.Ledger](config.MaxRecentLedgers)
	if err != nil {
		return nil, err
	}

	hashCache, err := lru.New[[32]byte, *ledger.Ledger](config.MaxRecentLedgers)
	if err != nil {
		return nil, err
	}

	return &LedgerCache{
		recentBySeq:  seqCache,
		recentByHash: hashCache,
		completeness: NewCompleteLedgerSet(),
	}, nil
}

// Get retrieves a ledger by sequence number from cache
func (c *LedgerCache) Get(seq uint32) (*ledger.Ledger, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ledgerValue, found := c.recentBySeq.Get(seq)
	if found {
		c.hits++
		return ledgerValue, true
	}

	c.misses++
	return nil, false
}

// GetByHash retrieves a ledger by hash from cache
func (c *LedgerCache) GetByHash(hash [32]byte) (*ledger.Ledger, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ledgerValue, found := c.recentByHash.Get(hash)
	if found {
		c.hits++
		return ledgerValue, true
	}

	c.misses++
	return nil, false
}

// Put stores a ledger in cache
func (c *LedgerCache) Put(ledger *ledger.Ledger) {
	c.mu.Lock()
	defer c.mu.Unlock()

	seq := ledger.Sequence()
	hash := ledger.Hash()

	c.recentBySeq.Add(seq, ledger)
	c.recentByHash.Add(hash, ledger)
}

// Remove removes a ledger from cache
func (c *LedgerCache) Remove(seq uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Get the ledger first to remove from hash cache too
	if ledgerValue, found := c.recentBySeq.Peek(seq); found {
		hash := ledgerValue.Hash()
		c.recentByHash.Remove(hash)
	}

	c.recentBySeq.Remove(seq)
}

// MarkComplete marks a ledger sequence as complete locally
func (c *LedgerCache) MarkComplete(seq uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completeness.Add(seq)
}

// MarkCompleteRange marks a range of ledger sequences as complete
func (c *LedgerCache) MarkCompleteRange(start, end uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completeness.AddRange(start, end)
}

// IsComplete checks if we have a ledger sequence complete locally
func (c *LedgerCache) IsComplete(seq uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.completeness.Contains(seq)
}

// GetCompleteRange returns the range of complete ledgers
func (c *LedgerCache) GetCompleteRange() (min, max uint32, hasAny bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.completeness.Range()
}

// FindMissingInRange finds missing ledger sequences in a range
func (c *LedgerCache) FindMissingInRange(start, end uint32) []uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.completeness.FindMissing(start, end)
}

// Clear removes all cached ledgers (but keeps completeness tracking)
func (c *LedgerCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.recentBySeq.Purge()
	c.recentByHash.Purge()
}

// ClearCompleteness clears the completeness tracking
func (c *LedgerCache) ClearCompleteness() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completeness.Clear()
}

// Stats returns cache statistics
func (c *LedgerCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := float64(0)
	if total > 0 {
		hitRate = float64(c.hits) / float64(total)
	}

	return CacheStats{
		Hits:         c.hits,
		Misses:       c.misses,
		HitRate:      hitRate,
		SeqCacheLen:  c.recentBySeq.Len(),
		HashCacheLen: c.recentByHash.Len(),
	}
}

// CacheStats holds cache performance metrics
type CacheStats struct {
	Hits         uint64
	Misses       uint64
	HitRate      float64
	SeqCacheLen  int
	HashCacheLen int
}

// Hash256 interface for ledger hashes
type Hash256 [32]byte
