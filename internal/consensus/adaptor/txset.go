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
//
// SHAMap errors. The shamap package only returns errors when the map is
// in StateInvalid (a corrupted/poisoned state we never put it into
// here) — Has/Hash/Delete are otherwise infallible. We treat any error
// as "set is broken, fail open": Contains returns false, ID returns the
// zero hash. The zero hash collides with the canonical hash of the
// empty SHAMap, which is acceptable because reaching StateInvalid is a
// programmer-error path that rippled covers with XRPL_ASSERT.
//
// Aliasing. SHAMap() exposes the live backing map. Add and Remove
// mutate it in place — there is no copy-on-write. Callers that hold a
// pointer returned by SHAMap() across an Add/Remove see the mutation;
// the only production caller (router.go serveTxSet) takes the pointer
// and walks it synchronously, so the aliasing is dormant. Rippled
// avoids this by snapshotting on every MutableTxSet round-trip
// (RCLCxTx.h:78,119); replicating that would require SHAMap COW which
// we do not yet support.
type TxSetImpl struct {
	txMap *shamap.SHAMap
	count int
}

// NewTxSet creates a TxSet from raw transaction blobs. The ID is the
// SHAMap root hash, matching rippled's canonical tx-set hashing.
//
// Returns an error if any blob is rejected by the backing SHAMap
// (e.g. < 12 bytes — see shamap/leaf_node.go). Rippled never silently
// shrinks a tx-set during construction (RCLCxTx.h:87-91), and a
// truncated set would compute the wrong root hash and break consensus.
//
// Panics if the backing SHAMap cannot even be constructed — mirrors
// rippled's XRPL_ASSERT(map_) in RCLTxSet (RCLCxTx.h:111). A nil
// tx-set is unrecoverable; consensus would silently no-op on every
// subsequent Add/Contains/ID call.
func NewTxSet(txBlobs [][]byte) (*TxSetImpl, error) {
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		panic(fmt.Errorf("NewTxSet: shamap.New(TypeTransaction): %w", err))
	}
	ts := &TxSetImpl{txMap: txMap}
	for i, blob := range txBlobs {
		if err := ts.Add(blob); err != nil {
			return nil, fmt.Errorf("NewTxSet: blob %d (%d bytes): %w", i, len(blob), err)
		}
	}
	return ts, nil
}

func (ts *TxSetImpl) ID() consensus.TxSetID {
	h, err := ts.txMap.Hash()
	if err != nil {
		return consensus.TxSetID{}
	}
	return consensus.TxSetID(h)
}

// Txs returns every transaction blob in canonical key order. The
// ordering matches TxIDs() so callers can zip the two slices. Each
// blob is a defensive copy (shamap.Item.Data()) — callers may retain
// or mutate the returned slices safely; see Bytes() for the zero-copy
// counterpart used internally.
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
//
// Invalidates any pointer previously returned by SHAMap(): the
// underlying tree is mutated in place. See the TxSetImpl doc comment
// for the aliasing contract.
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

// Remove deletes a transaction by ID. Invalidates any pointer
// previously returned by SHAMap(); see the TxSetImpl doc comment.
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
//
// Uses Item.DataUnsafe() (zero-copy) because each blob is written into
// our own owned buffer before the next ForEach iteration — the unsafe
// alias never escapes. Txs(), by contrast, uses Item.Data() because
// the slices are handed back to callers who may mutate or retain them.
// Do not "normalize" this asymmetry without thinking through both
// invariants.
//
// goXRPL-specific helper with no rippled counterpart; not currently
// wired to the wire or to disk. If it ever is, consumers must accept
// the canonical-order framing (insertion order is no longer
// observable).
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

// SHAMap returns the canonical tx-set SHAMap. The pointer aliases the
// live backing store: subsequent Add/Remove calls on this TxSetImpl
// mutate the returned map in place. Callers must treat it as
// read-only and finish any walk before mutating the parent TxSetImpl.
// See the TxSetImpl doc comment for the broader aliasing contract.
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
