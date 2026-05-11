package adaptor

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
)

// TxSetImpl implements consensus.TxSet backed by a SHAMap of transaction
// blobs keyed by txID. This mirrors rippled's InboundTransactions: the
// SHAMap is the canonical storage, and Txs/TxIDs/Bytes derive from it.
//
// Complexity profile (N = set size):
//   - Add/Remove/Contains: O(log N) via SHAMap descent + incremental
//     hash propagation up the dirty path.
//   - ID():                O(1) — the SHAMap caches the root hash and
//     refreshes it on every mutation.
//   - Txs/TxIDs:           O(N) walk of the leaves in canonical key order.
//     The two methods walk identically, so callers can zip them.
//   - Bytes:               O(N) walk.
type TxSetImpl struct {
	txMap *shamap.SHAMap
	count int
}

// NewTxSet creates a TxSet from raw transaction blobs. The ID is the
// SHAMap root hash, matching rippled's canonical tx-set hashing.
//
// Panics if the backing SHAMap cannot be constructed — mirrors
// rippled's XRPL_ASSERT(map_) in RCLTxSet (RCLCxTx.h:111). A nil
// tx-set is unrecoverable: consensus would silently no-op on every
// subsequent Add/Contains/ID call.
func NewTxSet(txBlobs [][]byte) *TxSetImpl {
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		panic(fmt.Errorf("NewTxSet: shamap.New(TypeTransaction): %w", err))
	}
	ts := &TxSetImpl{txMap: txMap}
	for _, blob := range txBlobs {
		_ = ts.Add(blob)
	}
	return ts
}

func (ts *TxSetImpl) ID() consensus.TxSetID {
	h, err := ts.txMap.Hash()
	if err != nil {
		return consensus.TxSetID{}
	}
	return consensus.TxSetID(h)
}

// Txs returns every transaction blob in canonical key order. The
// ordering matches TxIDs() so callers can zip the two slices.
func (ts *TxSetImpl) Txs() [][]byte {
	result := make([][]byte, 0, ts.count)
	_ = ts.txMap.ForEach(func(it *shamap.Item) bool {
		result = append(result, it.Data())
		return true
	})
	return result
}

// TxIDs returns every txID in canonical key order, parallel to Txs().
func (ts *TxSetImpl) TxIDs() []consensus.TxID {
	result := make([]consensus.TxID, 0, ts.count)
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

// Add inserts a transaction blob into the set. Mirrors rippled's
// RCLTxSet::MutableTxSet::insert which uses tnTRANSACTION_NM only
// (RCLCxTx.h:90) — there is no untyped fallback.
func (ts *TxSetImpl) Add(tx []byte) error {
	txID := computeTxID(tx)
	key := [32]byte(txID)
	if ok, _ := ts.txMap.Has(key); ok {
		return nil
	}
	if err := ts.txMap.PutWithNodeType(key, tx, shamap.NodeTypeTransactionNoMeta); err != nil {
		return err
	}
	ts.count++
	return nil
}

func (ts *TxSetImpl) Remove(id consensus.TxID) error {
	key := [32]byte(id)
	ok, _ := ts.txMap.Has(key)
	if !ok {
		return nil
	}
	if err := ts.txMap.Delete(key); err != nil {
		return err
	}
	ts.count--
	return nil
}

func (ts *TxSetImpl) Size() int {
	return ts.count
}

// Bytes returns the tx blobs concatenated with a 4-byte length prefix
// each, walked in canonical SHAMap key order.
func (ts *TxSetImpl) Bytes() []byte {
	var buf bytes.Buffer
	_ = ts.txMap.ForEach(func(it *shamap.Item) bool {
		blob := it.DataUnsafe()
		l := uint32(len(blob))
		buf.Write([]byte{byte(l >> 24), byte(l >> 16), byte(l >> 8), byte(l)})
		buf.Write(blob)
		return true
	})
	return buf.Bytes()
}

// SHAMap returns the canonical tx-set SHAMap. Callers must treat it
// as read-only — mutating it directly bypasses count tracking.
func (ts *TxSetImpl) SHAMap() *shamap.SHAMap {
	return ts.txMap
}

// computeTxID computes the SHA-512Half of a transaction blob with the
// HashPrefix for transactions (TXN\x00). Matches rippled's
// sha512Half(HashPrefix::transactionID, txBlob).
func computeTxID(blob []byte) consensus.TxID {
	return consensus.TxID(common.Sha512Half(protocol.HashPrefixTransactionID[:], blob))
}

// TxSetCache is a thread-safe cache for transaction sets.
type TxSetCache struct {
	mu    sync.RWMutex
	cache map[consensus.TxSetID]*TxSetImpl
}

// NewTxSetCache creates a new TxSetCache.
func NewTxSetCache() *TxSetCache {
	return &TxSetCache{
		cache: make(map[consensus.TxSetID]*TxSetImpl),
	}
}

// Get retrieves a TxSet by ID.
func (c *TxSetCache) Get(id consensus.TxSetID) (*TxSetImpl, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ts, ok := c.cache[id]
	return ts, ok
}

// Put stores a TxSet in the cache.
func (c *TxSetCache) Put(ts *TxSetImpl) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[ts.ID()] = ts
}

// Remove deletes a TxSet from the cache.
func (c *TxSetCache) Remove(id consensus.TxSetID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, id)
}
