package adaptor

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// TxSetImpl implements consensus.TxSet backed by a SHAMap of transaction
// blobs keyed by txID. The SHAMap is the canonical storage, and
// Txs/TxIDs/Bytes derive from it.
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
// SHAMap errors. For an unbacked SHAMap that stays in StateModifying —
// which is how we use it here — the only error source on Has/Hash is
// StateInvalid, which TxSetImpl never enters. (Backed maps additionally
// propagate I/O errors from descend(); mutators reject StateImmutable
// and StateSyncing.) Contains treats any such error as "fail open"
// (false); ID treats it as the zero hash, which collides with the
// canonical hash of the empty SHAMap. Reaching that path is a
// programmer-error condition.
//
// Aliasing. shamap() (package-internal) exposes the live backing map.
// Add and Remove mutate it in place — there is no copy-on-write. The
// only production caller (router.go serveTxSet) takes the pointer and
// walks it synchronously, so the aliasing is dormant. A snapshot on
// every round-trip would avoid it but requires O(1) SHAMap COW, which
// the unbacked path here does not yet have (Snapshot does a deep
// clone). The accessor stays unexported so the aliasing concern cannot
// leak out of this package.
//
// Concurrency. The backing *shamap.SHAMap is internally lock-protected,
// but TxSetImpl maintains a shadow `count` field for O(1) Size(); the
// mutex below brackets every count read (Size, Txs, TxIDs) against
// Add/Remove writers. The extra field exists because our consensus
// engine logs and gates on tx counts from hot paths, so we pay for it
// rather than walk the tree.
type TxSetImpl struct {
	txMap *shamap.SHAMap
	mu    sync.Mutex
	count int
}

// NewTxSet creates a TxSet from raw transaction blobs. The ID is the
// canonical SHAMap root hash.
//
// Returns an error if shamap.New fails or any blob is rejected by the
// backing SHAMap. Neither path is reachable today — shamap.New is
// unconditional and the only blob gate is the <12-byte rejection, far
// below any real transaction blob. We propagate both as errors rather
// than panicking or silently dropping the blob: a truncated set
// computes the wrong root hash and would break consensus, and keeping
// a single error-return contract (no mixed panic/error) means callers
// who already check err do not have to also wrap recover().
func NewTxSet(txBlobs [][]byte) (*TxSetImpl, error) {
	txMap := shamap.New(shamap.TypeTransaction)
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

// Add inserts a transaction blob into the set, always with the
// no-metadata node type; there is no untyped fallback.
//
// Has errors are propagated (unlike Contains, which fails open) — the
// mutation path is where SHAMap state transitions surface, and a
// surprised Has on a corrupted root is more useful as a diagnostic
// than as a silent fall-through into PutWithNodeType.
//
// Invalidates any pointer previously returned by shamap(): the
// underlying tree is mutated in place. See the TxSetImpl doc comment
// for the aliasing contract.
func (ts *TxSetImpl) Add(tx []byte) error {
	txID := computeTxID(tx)
	key := [32]byte(txID)
	ok, err := ts.txMap.Has(key)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if err := ts.txMap.PutWithNodeType(key, tx, shamap.NodeTypeTransactionNoMeta); err != nil {
		return err
	}
	ts.mu.Lock()
	ts.count++
	ts.mu.Unlock()
	return nil
}

// Remove deletes a transaction by ID. Propagates Has errors for the
// same reason Add does. Invalidates any pointer previously returned
// by shamap(); see the TxSetImpl doc comment.
func (ts *TxSetImpl) Remove(id consensus.TxID) error {
	key := [32]byte(id)
	ok, err := ts.txMap.Has(key)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := ts.txMap.Delete(key); err != nil {
		return err
	}
	ts.mu.Lock()
	ts.count--
	ts.mu.Unlock()
	return nil
}

func (ts *TxSetImpl) Size() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
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
// Not currently wired to the wire or to disk. If it ever is,
// consumers must accept the canonical-order framing (insertion order
// is no longer observable).
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

// shamap returns the canonical tx-set SHAMap. Package-internal: the
// pointer aliases the live backing store, so subsequent Add/Remove
// calls on this TxSetImpl mutate the returned map in place. Callers
// must treat it as read-only and finish any walk before mutating the
// parent TxSetImpl. See the TxSetImpl doc comment for the broader
// aliasing contract.
func (ts *TxSetImpl) shamap() *shamap.SHAMap {
	return ts.txMap
}

// computeTxID computes the SHA-512Half of a transaction blob with the
// HashPrefix for transactions (TXN\x00).
func computeTxID(blob []byte) consensus.TxID {
	return consensus.TxID(common.Sha512Half(protocol.HashPrefixTransactionID[:], blob))
}

// TxSetCache is a thread-safe cache for transaction sets.
type TxSetCache struct {
	mu    sync.RWMutex
	cache map[consensus.TxSetID]*TxSetImpl
}

func NewTxSetCache() *TxSetCache {
	return &TxSetCache{
		cache: make(map[consensus.TxSetID]*TxSetImpl),
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
	c.cache[ts.ID()] = ts
}

func (c *TxSetCache) Remove(id consensus.TxSetID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, id)
}
