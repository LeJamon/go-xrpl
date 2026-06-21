package adaptor

import (
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	// fetchPackCacheTargetSize is the soft target for the fetch-pack node
	// cache.
	fetchPackCacheTargetSize = 65536
	// fetchPackCacheTTL bounds how long an inbound fetch-pack node lingers
	// while the cache is within its target size.
	fetchPackCacheTTL = 45 * time.Second
)

// fetchPackCache holds inbound fetch-pack SHAMap nodes keyed by node hash,
// briefly, so a stalled acquisition can complete locally via
// inbound.Ledger.CheckLocal. New nodes are always stored; the router sweeps on
// its maintenance tick, ageing entries out at the full TTL while the cache is
// within targetSize and proportionally faster once it grows past it, so a flood
// of cheap nodes shrinks the eviction window rather than locking newcomers out.
type fetchPackCache struct {
	mu         sync.Mutex
	nodes      map[[32]byte]fetchPackEntry
	targetSize int
	ttl        time.Duration
}

type fetchPackEntry struct {
	data []byte
	at   time.Time
}

func newFetchPackCache() *fetchPackCache {
	return &fetchPackCache{
		nodes:      make(map[[32]byte]fetchPackEntry),
		targetSize: fetchPackCacheTargetSize,
		ttl:        fetchPackCacheTTL,
	}
}

// add stores a node blob keyed by its hash, stamping it with now. A newcomer is
// always accepted, even past the target size, so a fresh, immediately-useful
// node is never refused; the cache is bounded by sweep, not at insert.
func (c *fetchPackCache) add(hash [32]byte, data []byte, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[hash] = fetchPackEntry{data: append([]byte(nil), data...), at: now}
}

// get returns the cached blob for hash when present and unexpired, deleting it
// when expired.
func (c *fetchPackCache) get(hash [32]byte, now time.Time) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.nodes[hash]
	if !ok {
		return nil, false
	}
	if now.Sub(e.at) > c.ttl {
		delete(c.nodes, hash)
		return nil, false
	}
	return e.data, true
}

// sweep drops entries older than the effective max age for the current size:
// the full TTL while within the target, and a proportionally shorter window
// once over it. The age is computed once against the pre-sweep size so a single
// pass bounds an oversized cache.
func (c *fetchPackCache) sweep(now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	maxAge := c.effectiveMaxAge(len(c.nodes))
	for h, e := range c.nodes {
		if now.Sub(e.at) > maxAge {
			delete(c.nodes, h)
		}
	}
}

// effectiveMaxAge is how long an entry may linger before the next sweep drops
// it: the full TTL while size is within targetSize, otherwise the TTL scaled by
// targetSize/size and floored at one second. The further the cache grows past
// its target, the faster entries age out, mirroring rippled's TaggedCache sweep.
func (c *fetchPackCache) effectiveMaxAge(size int) time.Duration {
	if c.targetSize <= 0 || size <= c.targetSize {
		return c.ttl
	}
	age := time.Duration(int64(c.ttl) * int64(c.targetSize) / int64(size))
	return max(age, time.Second)
}

// handleFetchPackReply consumes an inbound mtGET_OBJECTS reply carrying SHAMap
// nodes — either an otFETCH_PACK bulk pack or the otSTATE_NODE/otTRANSACTION_NODE
// nodes served for a by-hash acquisition escalation. It caches each verified
// node by its hash, then gives every in-flight acquisition a chance to complete
// locally from the cache via CheckLocal. A pack's leading ledger-header object
// and any node that fails hash verification are dropped.
func (r *Router) handleFetchPackReply(msg *peermanagement.InboundMessage) {
	if r.fetchPacks == nil {
		return
	}
	decoded, err := message.Decode(message.TypeGetObjects, msg.Payload)
	if err != nil {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "fetch-pack-decode")
		return
	}
	gob, ok := decoded.(*message.GetObjectByHash)
	if !ok || gob.Query {
		return
	}
	switch gob.ObjType {
	case message.ObjectTypeFetchPack, message.ObjectTypeStateNode, message.ObjectTypeTransactionNode:
	default:
		return
	}

	now := time.Now()
	stored := 0
	// Per-ledgerseq "late pack" short-circuit: skip caching nodes for a
	// ledger we already hold. go-xrpl packs are single-ledger, but track
	// per-object so a multi-seq pack is handled too.
	var pLSeq uint32
	pLDo := true
	for i := range gob.Objects {
		obj := &gob.Objects[i]
		if len(obj.Hash) != 32 || len(obj.Data) == 0 {
			continue
		}
		if obj.LedgerSeq != 0 && obj.LedgerSeq != pLSeq {
			pLSeq = obj.LedgerSeq
			pLDo = !r.haveLedgerSeq(pLSeq)
		}
		if !pLDo {
			continue
		}
		var hash [32]byte
		copy(hash[:], obj.Hash)
		// Only SHAMap tree nodes are useful for completing an acquisition;
		// the leading header object (hash == ledger hash) is not a SHAMap
		// node and is expected to fail verification, as is any poisoned blob.
		if !shamap.VerifyFetchPackNode(hash, obj.Data) {
			continue
		}
		r.fetchPacks.add(hash, obj.Data, now)
		stored++
	}
	if stored == 0 {
		return
	}
	r.tryCompleteFromFetchPack(now)
}

// haveLedgerSeq reports whether a ledger at seq is already in our store, so a
// late fetch-pack for an already-acquired ledger is not cached.
func (r *Router) haveLedgerSeq(seq uint32) bool {
	if seq == 0 {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	l, err := svc.GetLedgerBySequence(seq)
	return err == nil && l != nil
}

// tryCompleteFromFetchPack runs CheckLocal against the fetch-pack cache for
// every in-flight acquisition, finalizing any that complete.
func (r *Router) tryCompleteFromFetchPack(now time.Time) {
	if r.fetchPacks == nil {
		return
	}
	fetch := func(hash [32]byte) ([]byte, bool) { return r.fetchPacks.get(hash, now) }
	for _, il := range r.fetchTracker.Active() {
		if il.CheckLocal(fetch) && il.IsComplete() {
			r.completeInboundLedger(il)
		}
	}
}

// tryFetchPackEscalation attempts, at most once per acquisition, to recover a
// stalled legacy acquisition via a fetch-pack instead of reaping it. The
// requester must name a ledger it HAS whose PARENT is the ledger it wants, so
// we locate the child of the stalled ledger (the ledger at il.Seq()+1 whose
// ParentHash links back to it) and send its hash.
//
// Returns true when a request was sent and the acquisition's deadline was
// extended for the reply, so the caller leaves it in flight; false when no
// fetch-pack is possible — the common case for a forward tip acquisition whose
// child does not exist yet — or one was already tried, so the caller reaps it.
func (r *Router) tryFetchPackEscalation(il *inbound.Ledger) bool {
	if r.fetchPacks == nil || il.FetchPackRequested() {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return false
	}

	wantHash := il.Hash()
	child, err := svc.GetLedgerBySequence(il.Seq() + 1)
	if err != nil || child == nil || child.Header().ParentHash != wantHash {
		// No known child to key the pack on: nothing to request.
		return false
	}
	childHash := child.Hash()

	req := &message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      true,
		LedgerHash: childHash[:],
	}
	frame, err := encodeFrame(message.TypeGetObjects, req)
	if err != nil {
		return false
	}
	if err := r.adaptor.SendToPeer(il.PeerID(), frame); err != nil {
		r.logger.Debug("fetch-pack request send failed",
			"seq", il.Seq(), "err", err)
		return false
	}

	il.MarkFetchPackRequested()
	r.logger.Info("requested fetch-pack for stalled acquisition",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", wantHash[:4]),
		"child", fmt.Sprintf("%x", childHash[:4]),
		"peer", il.PeerID(),
	)
	return true
}
