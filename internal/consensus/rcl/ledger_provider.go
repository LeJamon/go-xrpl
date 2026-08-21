package rcl

import (
	"container/list"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

// maxProviderAncestors mirrors rippled's keylet::skip window — ledgers
// further back are treated as diverging post-genesis.
const maxProviderAncestors = uint32(256)

const providerCacheCapacity = 1024

// ledgerHeader is the narrow slice of *ledger.Ledger the provider needs.
type ledgerHeader interface {
	Sequence() uint32
	Hash() [32]byte
	ParentHash() [32]byte
}

var _ ledgerHeader = (*ledger.Ledger)(nil)

type hashLookupFunc func(hash [32]byte) (ledgerHeader, error)

type hashOfSeqHeader interface {
	HashOfSeq(seq uint32) ([32]byte, bool, error)
}

// AncestryProvider satisfies LedgerAncestryProvider. It materialises a
// ledger's ancestor slice once per LedgerID and caches it in an LRU,
// avoiding O(depth²) ParentHash walks across the trie's many
// Ancestor(s) calls. Partial chains carry a retry marker and are rechecked
// before reuse so acquisition can repair them in place.
type AncestryProvider struct {
	lookup hashLookupFunc

	mu       sync.Mutex
	maxItems int
	cache    map[consensus.LedgerID]*list.Element
	lru      *list.List // front=most recent, back=least recent
	inflight map[consensus.LedgerID]*ancestryFlight
}

type cacheEntry struct {
	id consensus.LedgerID
	pl *providerLedger
}

type ancestryFlight struct {
	done     chan struct{}
	result   *providerLedger
	fallback *providerLedger
}

// NewAncestryProvider wraps the ledger service. A nil svc returns a
// disabled provider that always reports (nil, false).
func NewAncestryProvider(svc *service.Service) *AncestryProvider {
	if svc == nil {
		return newAncestryProviderFromLookup(nil)
	}
	return newAncestryProviderFromLookup(func(hash [32]byte) (ledgerHeader, error) {
		l, err := svc.GetLedgerByHash(hash)
		if err != nil {
			return nil, err
		}
		return l, nil
	})
}

func newAncestryProviderFromLookup(fn hashLookupFunc) *AncestryProvider {
	return &AncestryProvider{
		lookup:   fn,
		maxItems: providerCacheCapacity,
		cache:    make(map[consensus.LedgerID]*list.Element),
		lru:      list.New(),
		inflight: make(map[consensus.LedgerID]*ancestryFlight),
	}
}

// LedgerByID implements LedgerAncestryProvider.
func (p *AncestryProvider) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	if p == nil || p.lookup == nil {
		return nil, false
	}
	var fallback *providerLedger
	if cached, ok := p.cacheGet(id); ok {
		if !cached.partial {
			return cached, true
		}
		// A failed parent lookup is a transient acquisition state, not a
		// permanent property of this ledger. Recheck the exact missing hash
		// before reusing the truncated suffix; once it arrives, rebuild the
		// chain so MinSeq/Ancestor and trie support recover automatically.
		if !p.headerAvailable(cached.retryHash, cached.retrySeq) {
			return cached, true
		}
		fallback = cached
	}

	p.mu.Lock()
	if cached, ok := p.cacheGetLocked(id); ok {
		if fallback == nil || cached != fallback {
			p.mu.Unlock()
			return cached, true
		}
		elem := p.cache[id]
		delete(p.cache, id)
		p.lru.Remove(elem)
	}
	if flight, ok := p.inflight[id]; ok {
		if flight.fallback == nil {
			flight.fallback = fallback
		}
		p.mu.Unlock()
		<-flight.done
		if flight.result == nil {
			return nil, false
		}
		return flight.result, true
	}
	if p.inflight == nil {
		p.inflight = make(map[consensus.LedgerID]*ancestryFlight)
	}
	flight := &ancestryFlight{done: make(chan struct{}), fallback: fallback}
	p.inflight[id] = flight
	p.mu.Unlock()

	built := p.buildAndFinish(id, flight)
	return built, built != nil
}

func (p *AncestryProvider) buildAndFinish(
	id consensus.LedgerID,
	flight *ancestryFlight,
) (built *providerLedger) {
	defer func() {
		p.mu.Lock()
		if built == nil ||
			(built.partial && flight.fallback != nil &&
				(built.id != flight.fallback.id || built.seq != flight.fallback.seq ||
					built.minSeq > flight.fallback.minSeq)) {
			built = flight.fallback
		}
		if built != nil {
			p.cachePutLocked(id, built)
		}
		flight.result = built
		delete(p.inflight, id)
		close(flight.done)
		p.mu.Unlock()
	}()

	built = p.buildChain(id)
	return built
}

func (p *AncestryProvider) headerAvailable(hash [32]byte, seq uint32) bool {
	header, err := p.lookup(hash)
	return err == nil && !isNilInterface(header) && header.Hash() == hash && header.Sequence() == seq
}

func (p *AncestryProvider) cacheGet(id consensus.LedgerID) (*providerLedger, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cacheGetLocked(id)
}

func (p *AncestryProvider) cacheGetLocked(id consensus.LedgerID) (*providerLedger, bool) {
	elem, ok := p.cache[id]
	if !ok {
		return nil, false
	}
	p.lru.MoveToFront(elem)
	return elem.Value.(*cacheEntry).pl, true
}

func (p *AncestryProvider) cachePutLocked(id consensus.LedgerID, pl *providerLedger) {
	if elem, ok := p.cache[id]; ok {
		// Keep the complete chain when a concurrent lookup only observed a
		// transient gap. A complete result must be allowed to replace a
		// previously cached partial result so recovery is not lost to a race.
		entry := elem.Value.(*cacheEntry)
		if entry.pl.partial && !pl.partial {
			elem.Value = &cacheEntry{id: id, pl: pl}
		}
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
// partial chains are returned with a higher minSeq and marked retryable.
func (p *AncestryProvider) buildChain(id consensus.LedgerID) *providerLedger {
	tip, err := p.lookup([32]byte(id))
	if err != nil || isNilInterface(tip) {
		return nil
	}
	if consensus.LedgerID(tip.Hash()) != id {
		return nil
	}
	tipSeq := tip.Sequence()
	if tipSeq == 0 {
		return nil
	}

	targetDepth := min(tipSeq-1, maxProviderAncestors)
	if targetDepth == 0 {
		return &providerLedger{id: id, seq: tipSeq, minSeq: tipSeq}
	}

	// ancestors[i] is the ID at seq (tipSeq - targetDepth + i);
	// ancestors[targetDepth-1] is the immediate parent.
	ancestors := make([]consensus.LedgerID, targetDepth)
	curr := tip
	filled := uint32(0)
	myMinSeq := tipSeq - targetDepth
	var retryHash [32]byte
	var retrySeq uint32
	partial := false

	// Embedded ancestry remains usable when intervening ledger objects are
	// temporarily absent from the service.
	if resolver, ok := tip.(hashOfSeqHeader); ok {
		resolved := true
		for seq := myMinSeq; seq < tipSeq; seq++ {
			hash, available, hashErr := resolver.HashOfSeq(seq)
			if hashErr != nil || !available {
				resolved = false
				break
			}
			ancestors[seq-myMinSeq] = consensus.LedgerID(hash)
		}
		if resolved {
			return &providerLedger{
				id:        id,
				seq:       tipSeq,
				minSeq:    myMinSeq,
				ancestors: ancestors,
			}
		}
	}

	for filled < targetDepth {
		parentHash := consensus.LedgerID(curr.ParentHash())
		currSeq := curr.Sequence()
		idx := targetDepth - 1 - filled
		ancestors[idx] = parentHash
		filled++

		if filled >= targetDepth {
			break
		}

		// If the exact parent's chain is already cached, borrow its entries.
		// A stale or corrupted cache entry must fall through to the validated
		// direct lookup below rather than poisoning this chain.
		expectedSeq := currSeq - 1
		if cached, hit := p.cacheGet(parentHash); hit && !cached.partial &&
			cached.ID() == parentHash && currSeq != 0 && cached.Seq() == expectedSeq {
			for j := range idx {
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
		if currSeq == 0 || err != nil || isNilInterface(parent) ||
			parent.Hash() != [32]byte(parentHash) || parent.Sequence() != expectedSeq {
			// Partial chain — truncate to the populated suffix.
			ancestors = ancestors[idx:]
			myMinSeq = tipSeq - filled
			retryHash = [32]byte(parentHash)
			retrySeq = expectedSeq
			partial = true
			break
		}
		curr = parent
	}

	return &providerLedger{
		id:        id,
		seq:       tipSeq,
		minSeq:    myMinSeq,
		ancestors: ancestors,
		retryHash: retryHash,
		retrySeq:  retrySeq,
		partial:   partial,
	}
}

// providerLedger satisfies ledgertrie.Ledger. ancestors[i] is the ID
// at seq (minSeq + i); the ledger's own ID at seq=tipSeq is not stored.
type providerLedger struct {
	id        consensus.LedgerID
	seq       uint32
	minSeq    uint32
	ancestors []consensus.LedgerID

	// retryHash and retrySeq identify the unavailable or malformed header that
	// truncated a partial suffix.
	retryHash [32]byte
	retrySeq  uint32
	partial   bool
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

func (l *providerLedger) retryable() bool { return l.partial }
