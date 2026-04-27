package rcl

import (
	"container/list"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
)

// maxProviderAncestors caps how far back buildChain walks. Mirrors
// rippled's RCLValidatedLedger which exposes only the past 256 hashes
// via the keylet::skip SLE — ledgers separated by more than this are
// treated as diverging post-genesis (RCLValidations.h:155).
const maxProviderAncestors = uint32(256)

// providerCacheCapacity caps the number of ledgers retained in the
// LRU. Each entry holds at most maxProviderAncestors hashes (32 bytes
// each), so the cache is bounded at ~8MB regardless of mainnet seq.
const providerCacheCapacity = 1024

// LedgerHeader is the narrow slice of *ledger.Ledger the provider
// reads. Exposed as an interface so unit tests can stub it without
// constructing a full ledger. *ledger.Ledger satisfies it.
type LedgerHeader interface {
	Sequence() uint32
	Hash() [32]byte
	ParentHash() [32]byte
}

// Static assertion that *ledger.Ledger satisfies LedgerHeader.
var _ LedgerHeader = (*ledger.Ledger)(nil)

// hashLookupFunc resolves a ledger hash to its header. Returning an
// interface (rather than the concrete *ledger.Ledger) is what lets
// tests inject fake lookups; the production constructor adapts
// *service.Service.GetLedgerByHash into this shape.
type hashLookupFunc func(hash [32]byte) (LedgerHeader, error)

// LedgerProvider satisfies LedgerAncestryProvider by resolving a
// LedgerID via the ledger service and materializing the ledger's
// recent ancestor chain on demand.
//
// The trie calls Ancestor(s) many times per insert (binary search in
// ledgertrie.Mismatch plus the span operations). Walking back via
// ParentHash on every call would be O(depth²) — so we materialize
// the ancestor slice once per (LedgerID) query and cache the result.
// Ancestor slices are immutable once a ledger is closed, so cached
// entries never need invalidation; the cache is sized purely to bound
// memory and is evicted in LRU order when full.
//
// Ancestry depth is capped at maxProviderAncestors (256). On a fresh
// node missing earlier history the walk truncates at the first
// missing parent, leaving a partial chain — Mismatch then treats the
// region below the truncated minSeq as unknown and returns 1, which
// is the same behaviour rippled exhibits when ledgers fall outside
// the keylet::skip window.
type LedgerProvider struct {
	lookup hashLookupFunc

	mu       sync.Mutex
	maxItems int
	cache    map[consensus.LedgerID]*list.Element
	lru      *list.List // front=most recent, back=least recent
}

type cacheEntry struct {
	id consensus.LedgerID
	pl *providerLedger
}

// NewLedgerProvider wraps the production ledger service into a
// provider suitable for ValidationTracker.SetLedgerAncestryProvider.
// nil svc yields a provider that always returns (nil, false) —
// useful as a disabled placeholder without special-casing at the
// call site.
func NewLedgerProvider(svc *service.Service) *LedgerProvider {
	if svc == nil {
		return newLedgerProviderFromLookup(nil)
	}
	return newLedgerProviderFromLookup(func(hash [32]byte) (LedgerHeader, error) {
		l, err := svc.GetLedgerByHash(hash)
		if err != nil {
			return nil, err
		}
		return l, nil
	})
}

// newLedgerProviderFromLookup is the internal constructor used by
// production (NewLedgerProvider wraps *service.Service) and by tests
// (pass a closure backed by a fake header map).
func newLedgerProviderFromLookup(fn hashLookupFunc) *LedgerProvider {
	return &LedgerProvider{
		lookup:   fn,
		maxItems: providerCacheCapacity,
		cache:    make(map[consensus.LedgerID]*list.Element),
		lru:      list.New(),
	}
}

// LedgerByID implements LedgerAncestryProvider.
func (p *LedgerProvider) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	if p == nil || p.lookup == nil {
		return nil, false
	}
	if cached, ok := p.cacheGet(id); ok {
		return cached, true
	}

	built := p.buildChain(id)
	if built == nil {
		return nil, false
	}

	p.cachePut(id, built)
	return built, true
}

func (p *LedgerProvider) cacheGet(id consensus.LedgerID) (*providerLedger, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	elem, ok := p.cache[id]
	if !ok {
		return nil, false
	}
	p.lru.MoveToFront(elem)
	return elem.Value.(*cacheEntry).pl, true
}

func (p *LedgerProvider) cachePut(id consensus.LedgerID, pl *providerLedger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if elem, ok := p.cache[id]; ok {
		// Race: another goroutine populated concurrently. Bump LRU and
		// drop the duplicate build; either is correct.
		p.lru.MoveToFront(elem)
		return
	}
	elem := p.lru.PushFront(&cacheEntry{id: id, pl: pl})
	p.cache[id] = elem
	for p.lru.Len() > p.maxItems {
		old := p.lru.Back()
		if old == nil {
			break
		}
		oldEntry := old.Value.(*cacheEntry)
		delete(p.cache, oldEntry.id)
		p.lru.Remove(old)
	}
}

// buildChain walks parent hashes backwards from `id`, collecting up
// to maxProviderAncestors links. Returns nil only when the tip itself
// is unresolvable; partial chains (walk failed midway) are returned
// with a higher minSeq, matching rippled's behaviour for ledgers
// older than the keylet::skip window.
func (p *LedgerProvider) buildChain(id consensus.LedgerID) *providerLedger {
	tip, err := p.lookup([32]byte(id))
	if err != nil || tip == nil {
		return nil
	}
	tipSeq := tip.Sequence()
	if tipSeq == 0 {
		// Seq-0 is our pre-genesis fiction; a real ledger must have
		// seq >= 1.
		return nil
	}

	// Target: ancestors at seqs [tipSeq-targetDepth, tipSeq-1].
	targetDepth := tipSeq - 1
	if targetDepth > maxProviderAncestors {
		targetDepth = maxProviderAncestors
	}
	if targetDepth == 0 {
		// tipSeq == 1: only the tip itself, no ancestors.
		return &providerLedger{id: id, seq: tipSeq, minSeq: tipSeq}
	}

	ancestors := make([]consensus.LedgerID, targetDepth)
	// ancestors[i] is the ID at seq (tipSeq - targetDepth + i) — i.e.
	// ancestors[targetDepth-1] is the immediate parent.

	curr := tip
	filled := uint32(0)
	myMinSeq := tipSeq - targetDepth

	for filled < targetDepth {
		parentHash := consensus.LedgerID(curr.ParentHash())
		idx := targetDepth - 1 - filled
		ancestors[idx] = parentHash
		filled++

		if filled >= targetDepth {
			break
		}

		// Cache splice: if the parent's chain is already cached,
		// borrow its ancestor entries for the seqs they cover.
		if cached, hit := p.cacheGet(parentHash); hit {
			for j := uint32(0); j < idx; j++ {
				wantSeq := myMinSeq + j
				if wantSeq >= cached.minSeq && wantSeq < cached.seq {
					ancestors[j] = cached.ancestors[wantSeq-cached.minSeq]
				}
			}
			// If the cached chain starts above our myMinSeq, the
			// leading slots are zero — narrow our minSeq to skip them.
			if cached.minSeq > myMinSeq {
				gap := cached.minSeq - myMinSeq
				ancestors = ancestors[gap:]
				myMinSeq = cached.minSeq
			}
			break
		}

		parent, err := p.lookup([32]byte(parentHash))
		if err != nil || parent == nil {
			// Partial chain — we couldn't reach further. Truncate to
			// the populated suffix and bump minSeq accordingly.
			ancestors = ancestors[idx:]
			myMinSeq = tipSeq - filled
			break
		}
		curr = parent
	}

	return &providerLedger{
		id:        id,
		seq:       tipSeq,
		minSeq:    myMinSeq,
		ancestors: ancestors,
	}
}

// providerLedger is the trie's view of a production ledger:
// (id, seq, minSeq, ancestors). Satisfies ledgertrie.Ledger.
//
// ancestors[i] is the ID at seq (minSeq + i). The ledger's own ID at
// seq=tipSeq is `id` and is NOT stored in the slice.
type providerLedger struct {
	id        consensus.LedgerID
	seq       uint32
	minSeq    uint32
	ancestors []consensus.LedgerID
}

func (l *providerLedger) ID() consensus.LedgerID { return l.id }
func (l *providerLedger) Seq() uint32            { return l.seq }
func (l *providerLedger) MinSeq() uint32         { return l.minSeq }

// Ancestor returns the ID of the ancestor at sequence s. For s
// outside [minSeq, seq] returns the zero LedgerID, matching rippled's
// RCLValidatedLedger::operator[] (RCLValidations.cpp:79-95).
func (l *providerLedger) Ancestor(s uint32) consensus.LedgerID {
	if s == l.seq {
		return l.id
	}
	if s < l.minSeq || s > l.seq {
		return consensus.LedgerID{}
	}
	return l.ancestors[s-l.minSeq]
}
