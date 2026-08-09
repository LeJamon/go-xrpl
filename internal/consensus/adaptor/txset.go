package adaptor

import (
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// TxSetImpl implements consensus.TxSet backed by an immutable SHAMap of
// transaction blobs keyed by txID.
//
// Complexity profile (N = set size):
//   - Contains:            O(log N) via SHAMap descent.
//   - ID/Size:             O(1).
//   - Txs/TxIDs:           O(N) walk of the leaves in canonical key order.
//     The two methods walk identically, so callers can zip them.
type TxSetImpl struct {
	txMap *shamap.SHAMap
	id    consensus.TxSetID
	count int
}

var _ consensus.TxSet = (*TxSetImpl)(nil)

// NewTxSet creates a TxSet from raw transaction blobs. The ID is the
// canonical SHAMap root hash.
//
// Construction fails instead of publishing a partial set when the backing
// SHAMap rejects a blob.
func NewTxSet(txBlobs [][]byte) (*TxSetImpl, error) {
	txMap := shamap.New(shamap.TypeTransaction)
	count := 0
	for i, blob := range txBlobs {
		txID := computeTxID(blob)
		key := [32]byte(txID)
		exists, err := txMap.Has(key)
		if err != nil {
			return nil, fmt.Errorf("NewTxSet: blob %d (%d bytes): %w", i, len(blob), err)
		}
		if exists {
			continue
		}
		if err := txMap.PutWithNodeType(key, blob, shamap.NodeTypeTransactionNoMeta); err != nil {
			return nil, fmt.Errorf("NewTxSet: blob %d (%d bytes): %w", i, len(blob), err)
		}
		count++
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("NewTxSet: freeze: %w", err)
	}
	hash, err := txMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("NewTxSet: hash: %w", err)
	}
	return &TxSetImpl{
		txMap: txMap,
		id:    consensus.TxSetID(hash),
		count: count,
	}, nil
}

func (ts *TxSetImpl) ID() consensus.TxSetID {
	return ts.id
}

// Txs returns every transaction blob in canonical key order. The
// ordering matches TxIDs() so callers can zip the two slices. Each
// blob is a defensive copy (shamap.Item.Data()) — callers may retain
// or mutate the returned slices safely.
func (ts *TxSetImpl) Txs() [][]byte {
	result := make([][]byte, 0, ts.Size())
	_ = ts.txMap.ForEach(func(it *shamap.Item) bool {
		result = append(result, it.Data())
		return true
	})
	return result
}

// TxIDs returns every txID in canonical key order, parallel to Txs().
func (ts *TxSetImpl) TxIDs() []consensus.TxID {
	result := make([]consensus.TxID, 0, ts.Size())
	_ = ts.txMap.ForEach(func(it *shamap.Item) bool {
		key := it.Key()
		result = append(result, consensus.TxID(key))
		return true
	})
	return result
}

func (ts *TxSetImpl) Contains(id consensus.TxID) bool {
	ok, err := ts.txMap.Has([32]byte(id))
	return err == nil && ok
}

func (ts *TxSetImpl) Size() int {
	return ts.count
}

// shamap returns the immutable canonical transaction tree used by the serving
// path.
func (ts *TxSetImpl) shamap() *shamap.SHAMap {
	return ts.txMap
}

// computeTxID computes the SHA-512Half of a transaction blob with the
// HashPrefix for transactions (TXN\x00).
func computeTxID(blob []byte) consensus.TxID {
	return consensus.TxID(sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), blob))
}

// txSetCacheTTL bounds how long a transaction set is retained. A set is
// needed only while its consensus round is live plus a margin to serve
// catching-up peers; beyond that it is dead weight. The window spans many
// rounds (mainnet cadence ~4s) so an in-flight round never loses its set,
// while keeping the cache bounded to a small constant. Without it the map
// grows by one full SHAMap of tx blobs per BuildTxSet — our own each
// round plus one per acquired peer set, >20k/day at mainnet — for the
// process lifetime.
const txSetCacheTTL = 5 * time.Minute

// TxSetCache is a thread-safe, TTL-expiring cache for transaction sets.
type TxSetCache struct {
	mu    sync.RWMutex
	cache map[consensus.TxSetID]*TxSetImpl
	added map[consensus.TxSetID]time.Time
	// now is the clock used for expiry; tests override it. Defaults to
	// time.Now, matching the router's txSetAcquire sweep.
	now func() time.Time
}

func NewTxSetCache() *TxSetCache {
	zero := &TxSetImpl{txMap: shamap.New(shamap.TypeTransaction)}
	return &TxSetCache{
		cache: map[consensus.TxSetID]*TxSetImpl{
			{}: zero,
		},
		added: make(map[consensus.TxSetID]time.Time),
		now:   time.Now,
	}
}

func (c *TxSetCache) Get(id consensus.TxSetID) (*TxSetImpl, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ts, ok := c.cache[id]
	return ts, ok
}

func (c *TxSetCache) Put(ts *TxSetImpl) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := ts.ID()
	if id == (consensus.TxSetID{}) {
		return
	}
	c.cache[id] = ts
	c.added[id] = c.now()
	c.sweepLocked()
}

func (c *TxSetCache) Remove(id consensus.TxSetID) {
	if id == (consensus.TxSetID{}) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, id)
	delete(c.added, id)
}

// sweepLocked evicts entries older than txSetCacheTTL. Runs opportunistically
// on Put — frequent enough at round cadence to keep the map bounded without a
// dedicated timer. Caller holds c.mu.
func (c *TxSetCache) sweepLocked() {
	cutoff := c.now().Add(-txSetCacheTTL)
	for id, t := range c.added {
		if t.Before(cutoff) {
			delete(c.cache, id)
			delete(c.added, id)
		}
	}
}
