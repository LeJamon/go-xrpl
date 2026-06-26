package manager

import (
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	lru "github.com/hashicorp/golang-lru/v2"
)

// LedgerCache caches recently used ledgers (by seq and by hash) and tracks
// which ledgers are complete locally.
//
// Locking:
//   - Get / GetByHash run lock-free on lru.Cache's internal synchronisation.
//   - writeMu serialises Put / Remove so the seq+hash double-write stays
//     atomic across the two underlying caches.
//   - completenessMu guards CompleteLedgerSet, which has no internal lock.
//   - hits / misses are atomic.
type LedgerCache struct {
	recentBySeq  *lru.Cache[uint32, *ledger.Ledger]
	recentByHash *lru.Cache[[32]byte, *ledger.Ledger]

	writeMu sync.Mutex

	completenessMu sync.RWMutex
	completeness   *CompleteLedgerSet

	hits   atomic.Uint64
	misses atomic.Uint64
}

type ledgerCacheConfig struct {
	MaxRecentLedgers int
}

func NewLedgerCache(config ledgerCacheConfig) (*LedgerCache, error) {
	if config.MaxRecentLedgers <= 0 {
		config.MaxRecentLedgers = 256
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
	ledgerValue, found := c.recentBySeq.Get(seq)
	if found {
		c.hits.Add(1)
		return ledgerValue, true
	}

	c.misses.Add(1)
	return nil, false
}

// GetByHash retrieves a ledger by hash from cache
func (c *LedgerCache) GetByHash(hash [32]byte) (*ledger.Ledger, bool) {
	ledgerValue, found := c.recentByHash.Get(hash)
	if found {
		c.hits.Add(1)
		return ledgerValue, true
	}

	c.misses.Add(1)
	return nil, false
}

// Put stores a ledger in cache
func (c *LedgerCache) Put(ledger *ledger.Ledger) {
	seq := ledger.Sequence()
	hash := ledger.Hash()

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.recentBySeq.Add(seq, ledger)
	c.recentByHash.Add(hash, ledger)
}

// Remove removes a ledger from cache
func (c *LedgerCache) Remove(seq uint32) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// Get the ledger first to remove from hash cache too
	if ledgerValue, found := c.recentBySeq.Peek(seq); found {
		hash := ledgerValue.Hash()
		c.recentByHash.Remove(hash)
	}

	c.recentBySeq.Remove(seq)
}

// MarkComplete marks a ledger sequence as complete locally
func (c *LedgerCache) MarkComplete(seq uint32) {
	c.completenessMu.Lock()
	defer c.completenessMu.Unlock()

	c.completeness.Add(seq)
}

// MarkCompleteRange marks a range of ledger sequences as complete
func (c *LedgerCache) MarkCompleteRange(start, end uint32) {
	c.completenessMu.Lock()
	defer c.completenessMu.Unlock()

	c.completeness.AddRange(start, end)
}

// IsComplete checks if we have a ledger sequence complete locally
func (c *LedgerCache) IsComplete(seq uint32) bool {
	c.completenessMu.RLock()
	defer c.completenessMu.RUnlock()

	return c.completeness.Contains(seq)
}

// GetCompleteRange returns the range of complete ledgers
func (c *LedgerCache) GetCompleteRange() (min, max uint32, hasAny bool) {
	c.completenessMu.RLock()
	defer c.completenessMu.RUnlock()

	return c.completeness.Range()
}

// FindMissingInRange finds missing ledger sequences in a range
func (c *LedgerCache) FindMissingInRange(start, end uint32) []uint32 {
	c.completenessMu.RLock()
	defer c.completenessMu.RUnlock()

	return c.completeness.FindMissing(start, end)
}

// Clear removes all cached ledgers (but keeps completeness tracking).
// Holds writeMu so a concurrent Put cannot leave one cache populated while
// the other is empty — the seq/hash double-write invariant Put/Remove rely on.
func (c *LedgerCache) Clear() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.recentBySeq.Purge()
	c.recentByHash.Purge()
}

// ClearCompleteness clears the completeness tracking
func (c *LedgerCache) ClearCompleteness() {
	c.completenessMu.Lock()
	defer c.completenessMu.Unlock()

	c.completeness.Clear()
}

// stats returns cache statistics
func (c *LedgerCache) stats() cacheStats {
	hits := c.hits.Load()
	misses := c.misses.Load()

	total := hits + misses
	hitRate := float64(0)
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	return cacheStats{
		Hits:         hits,
		Misses:       misses,
		HitRate:      hitRate,
		SeqCacheLen:  c.recentBySeq.Len(),
		HashCacheLen: c.recentByHash.Len(),
	}
}

// cacheStats holds cache performance metrics
type cacheStats struct {
	Hits         uint64
	Misses       uint64
	HitRate      float64
	SeqCacheLen  int
	HashCacheLen int
}
