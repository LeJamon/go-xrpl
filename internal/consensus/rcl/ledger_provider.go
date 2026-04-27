package rcl

import (
	"container/list"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
)

// maxProviderAncestors mirrors rippled's keylet::skip window — ledgers
// further back are treated as diverging post-genesis.
const maxProviderAncestors = uint32(256)

const providerCacheCapacity = 1024

// LedgerHeader is the narrow slice of *ledger.Ledger the provider needs.
type LedgerHeader interface {
	Sequence() uint32
	Hash() [32]byte
	ParentHash() [32]byte
}

var _ LedgerHeader = (*ledger.Ledger)(nil)

type hashLookupFunc func(hash [32]byte) (LedgerHeader, error)

// LedgerProvider satisfies LedgerAncestryProvider. It materialises a
// ledger's ancestor slice once per LedgerID and caches it in an LRU,
// avoiding O(depth²) ParentHash walks across the trie's many
// Ancestor(s) calls. Cached chains are immutable so never invalidated.
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

// NewLedgerProvider wraps the ledger service. A nil svc returns a
// disabled provider that always reports (nil, false).
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
		// Race: another goroutine populated concurrently.
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

// buildChain walks parent hashes backwards from id, up to
// maxProviderAncestors links. Returns nil if the tip is unresolvable;
// partial chains are returned with a higher minSeq.
func (p *LedgerProvider) buildChain(id consensus.LedgerID) *providerLedger {
	tip, err := p.lookup([32]byte(id))
	if err != nil || tip == nil {
		return nil
	}
	tipSeq := tip.Sequence()
	if tipSeq == 0 {
		return nil
	}

	targetDepth := tipSeq - 1
	if targetDepth > maxProviderAncestors {
		targetDepth = maxProviderAncestors
	}
	if targetDepth == 0 {
		return &providerLedger{id: id, seq: tipSeq, minSeq: tipSeq}
	}

	// ancestors[i] is the ID at seq (tipSeq - targetDepth + i);
	// ancestors[targetDepth-1] is the immediate parent.
	ancestors := make([]consensus.LedgerID, targetDepth)
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

		// If parent's chain is already cached, borrow its entries.
		if cached, hit := p.cacheGet(parentHash); hit {
			for j := uint32(0); j < idx; j++ {
				wantSeq := myMinSeq + j
				if wantSeq >= cached.minSeq && wantSeq < cached.seq {
					ancestors[j] = cached.ancestors[wantSeq-cached.minSeq]
				}
			}
			if cached.minSeq > myMinSeq {
				gap := cached.minSeq - myMinSeq
				ancestors = ancestors[gap:]
				myMinSeq = cached.minSeq
			}
			break
		}

		parent, err := p.lookup([32]byte(parentHash))
		if err != nil || parent == nil {
			// Partial chain — truncate to the populated suffix.
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

// providerLedger satisfies ledgertrie.Ledger. ancestors[i] is the ID
// at seq (minSeq + i); the ledger's own ID at seq=tipSeq is not stored.
type providerLedger struct {
	id        consensus.LedgerID
	seq       uint32
	minSeq    uint32
	ancestors []consensus.LedgerID
}

func (l *providerLedger) ID() consensus.LedgerID { return l.id }
func (l *providerLedger) Seq() uint32            { return l.seq }
func (l *providerLedger) MinSeq() uint32         { return l.minSeq }

func (l *providerLedger) Ancestor(s uint32) consensus.LedgerID {
	if s == l.seq {
		return l.id
	}
	if s < l.minSeq || s > l.seq {
		return consensus.LedgerID{}
	}
	return l.ancestors[s-l.minSeq]
}
