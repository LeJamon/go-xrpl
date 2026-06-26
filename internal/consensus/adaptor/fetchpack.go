package adaptor

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
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

// get returns the cached blob for hash when present and unexpired, consuming the
// entry on retrieval: a fetch-pack node is single-use, so it is removed once
// handed to an acquisition, mirroring rippled's getFetchPack (retrieve + del).
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
	delete(c.nodes, hash)
	if now.Sub(e.at) >= c.ttl {
		return nil, false
	}
	return e.data, true
}

// sweep drops entries at least as old as the effective max age for the current
// size: the full TTL while within the target, and a proportionally shorter
// window once over it. The age is computed once against the pre-sweep size so a
// single pass bounds an oversized cache.
func (c *fetchPackCache) sweep(now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	maxAge := c.effectiveMaxAge(len(c.nodes))
	for h, e := range c.nodes {
		if now.Sub(e.at) >= maxAge {
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
//
// The handler runs on the consensus router goroutine, so it bounds the work an
// inbound reply can impose: replies are ignored unless an acquisition is in
// flight (an unsolicited pack can complete nothing), at most the serve cap of
// objects is hashed no matter how large the reply, and a peer that ships
// poisoned blobs is charged.
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

	// With no acquisition in flight there is nothing a pack can complete, so
	// drop it before any per-object hashing. The router is single-goroutine,
	// so this snapshot stays valid for the completion pass below.
	active := r.fetchTracker.Active()
	if len(active) == 0 {
		return
	}

	now := time.Now()
	stored := 0
	poisoned := 0
	// Hash-verify at most the serve cap of objects per reply: a peer can
	// legitimately serve a heavy-delta ledger above our own serve cap, so an
	// over-large reply is truncated rather than charged, while the work it
	// imposes on the consensus goroutine stays bounded.
	limit := min(len(gob.Objects), fetchPackMaxObjects)
	// Per-ledgerseq "late pack" short-circuit: skip caching nodes for a
	// ledger we already hold. go-xrpl packs are single-ledger, but track
	// per-object so a multi-seq pack is handled too.
	var pLSeq uint32
	pLDo := true
	for i := range limit {
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
		// The leading ledger-header object is not a SHAMap node and is
		// expected to fail verification; recognise it by its prefix and skip
		// it without hashing or counting it against the sender.
		if isLedgerHeaderObject(obj.Data) {
			continue
		}
		var hash [32]byte
		copy(hash[:], obj.Hash)
		// A blob that does not hash to its claimed key is poisoned; an honest
		// pack contains none, so a non-header verify failure is bad data.
		if !shamap.VerifyFetchPackNode(hash, obj.Data) {
			poisoned++
			continue
		}
		r.fetchPacks.add(hash, obj.Data, now)
		stored++
	}
	if poisoned > 0 {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "fetch-pack-poison")
	}
	if stored == 0 {
		return
	}
	r.tryCompleteFromFetchPack(active, now)
}

// isLedgerHeaderObject reports whether a fetch-pack object is the pack's leading
// ledger-header object rather than a SHAMap tree node. The header carries the
// ledgerMaster hash prefix, not a SHAMap node prefix, so it never verifies as a
// node and is dropped without being charged as poison.
func isLedgerHeaderObject(data []byte) bool {
	prefix := protocol.HashPrefixLedgerMaster.Bytes()
	return len(data) >= len(prefix) && bytes.Equal(data[:len(prefix)], prefix)
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
// each given in-flight acquisition, finalizing any that complete. The caller
// passes the active snapshot it already holds so the set is consistent with the
// pack just cached.
func (r *Router) tryCompleteFromFetchPack(active []*inbound.Ledger, now time.Time) {
	if r.fetchPacks == nil {
		return
	}
	fetch := func(hash [32]byte) ([]byte, bool) { return r.fetchPacks.get(hash, now) }
	for _, il := range active {
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
