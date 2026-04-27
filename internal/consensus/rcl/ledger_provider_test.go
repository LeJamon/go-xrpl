package rcl

import (
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// fakeHeader is a minimal LedgerHeader for unit tests. We build a
// contiguous chain genesis(seq 1) → seq 2 → ... by feeding the parent
// hash forward; each child picks a synthetic hash derived from the
// parent hash and the sequence.
type fakeHeader struct {
	seq    uint32
	hash   [32]byte
	parent [32]byte
}

func (h *fakeHeader) Sequence() uint32     { return h.seq }
func (h *fakeHeader) Hash() [32]byte       { return h.hash }
func (h *fakeHeader) ParentHash() [32]byte { return h.parent }

// buildChain produces headers seq 1..n, each with a deterministic
// hash (byte 0 = seq, byte 1 = tag to distinguish forks). Returns
// the tip header and the full {hash → header} map.
func buildChain(n uint32, tag byte) (*fakeHeader, map[[32]byte]LedgerHeader) {
	byHash := make(map[[32]byte]LedgerHeader)
	var prevHash [32]byte // zero = pre-genesis
	var tip *fakeHeader
	for s := uint32(1); s <= n; s++ {
		h := &fakeHeader{seq: s, parent: prevHash}
		h.hash[0] = byte(s)
		h.hash[1] = tag
		byHash[h.hash] = h
		prevHash = h.hash
		tip = h
	}
	return tip, byHash
}

// newTestProvider constructs a provider backed by a byHash map. Any
// missing lookup returns a sentinel error.
func newTestProvider(byHash map[[32]byte]LedgerHeader) *LedgerProvider {
	return newLedgerProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
		if lh, ok := byHash[h]; ok {
			return lh, nil
		}
		return nil, errors.New("not found")
	})
}

func TestLedgerProvider_BuildsFullAncestry(t *testing.T) {
	tip, byHash := buildChain(5, 'a')
	p := newTestProvider(byHash)

	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("LedgerByID should succeed for tip of complete chain")
	}
	if lgr.Seq() != 5 {
		t.Errorf("Seq: got %d, want 5", lgr.Seq())
	}
	if lgr.ID() != consensus.LedgerID(tip.hash) {
		t.Errorf("ID mismatch")
	}
	// Ancestor(0) is the pre-genesis zero.
	var zero consensus.LedgerID
	if lgr.Ancestor(0) != zero {
		t.Errorf("Ancestor(0): want zero, got %x", lgr.Ancestor(0))
	}
	// Ancestor(N) is the ledger itself.
	if lgr.Ancestor(5) != consensus.LedgerID(tip.hash) {
		t.Errorf("Ancestor(5) should equal tip ID")
	}
	// Mid-chain seqs check out.
	for s := uint32(1); s <= 5; s++ {
		got := lgr.Ancestor(s)
		if got[0] != byte(s) || got[1] != 'a' {
			t.Errorf("Ancestor(%d): got %x, expected byte0=%d byte1='a'", s, got, s)
		}
	}
}

func TestLedgerProvider_MissingLinkTruncates(t *testing.T) {
	// When the walk-back hits a missing parent, buildChain returns a
	// partial chain rather than failing. MinSeq advances to the lowest
	// seq still reachable; below that Ancestor returns zero. Mirrors
	// rippled's behaviour for ledgers older than the keylet::skip
	// window (RCLValidations.cpp:79-95 / 99-114).
	tip, byHash := buildChain(5, 'b')
	// Delete the seq-3 header. The walk captures seq-3's hash from
	// seq-4's ParentHash, then tries to load seq-3's record to read
	// its own ParentHash — that lookup fails and the walk truncates.
	// Result: ancestors cover seqs [3,4], MinSeq=3.
	var s3Hash [32]byte
	for h, lh := range byHash {
		if lh.Sequence() == 3 {
			s3Hash = h
			break
		}
	}
	delete(byHash, s3Hash)

	p := newTestProvider(byHash)
	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("LedgerByID should succeed for partial chain")
	}
	if lgr.Seq() != 5 {
		t.Errorf("Seq: got %d, want 5", lgr.Seq())
	}
	if lgr.MinSeq() != 3 {
		t.Errorf("MinSeq: got %d, want 3 (truncated at seq-3 — seq-3 record lookup failed)", lgr.MinSeq())
	}
	if lgr.Ancestor(2) != (consensus.LedgerID{}) {
		t.Errorf("Ancestor(2) below MinSeq should be zero")
	}
	if lgr.Ancestor(5) != consensus.LedgerID(tip.hash) {
		t.Errorf("Ancestor(5) should equal tip ID")
	}
	// Within [MinSeq, Seq] the entries match.
	if lgr.Ancestor(3)[1] != 'b' {
		t.Errorf("Ancestor(3) tag should be 'b'")
	}
}

func TestLedgerProvider_BoundedAtMaxAncestors(t *testing.T) {
	// Walk depth is capped at maxProviderAncestors (256). For a tip at
	// seq 1000, MinSeq must be 1000-256=744 — not 0 — and the cache
	// entry must hold exactly 256 ancestors regardless of available
	// chain depth.
	const tipSeq = uint32(1000)
	tip, byHash := buildChain(tipSeq, 'e')

	p := newTestProvider(byHash)
	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("LedgerByID should succeed")
	}
	if lgr.Seq() != tipSeq {
		t.Errorf("Seq: got %d, want %d", lgr.Seq(), tipSeq)
	}
	wantMin := tipSeq - maxProviderAncestors
	if lgr.MinSeq() != wantMin {
		t.Errorf("MinSeq: got %d, want %d (256-ancestor cap)", lgr.MinSeq(), wantMin)
	}
	// Below MinSeq Ancestor returns zero — no panic.
	if lgr.Ancestor(100) != (consensus.LedgerID{}) {
		t.Errorf("Ancestor(100) below MinSeq should be zero")
	}
	// Within range entries are real.
	if lgr.Ancestor(wantMin)[1] != 'e' {
		t.Errorf("Ancestor(%d) tag should be 'e'", wantMin)
	}
	if lgr.Ancestor(tipSeq - 1)[1] != 'e' {
		t.Errorf("Ancestor(%d) tag should be 'e'", tipSeq-1)
	}
}

func TestLedgerProvider_AncestorOutOfRangeReturnsZero(t *testing.T) {
	// Defensive parity with rippled's RCLValidatedLedger::operator[]
	// (RCLValidations.cpp:79-95): Ancestor of an out-of-range seq must
	// return the zero LedgerID rather than panicking.
	tip, byHash := buildChain(5, 'f')
	p := newTestProvider(byHash)
	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("LedgerByID should succeed")
	}
	if lgr.Ancestor(999) != (consensus.LedgerID{}) {
		t.Errorf("Ancestor(s > seq) should return zero")
	}
}

func TestLedgerProvider_LRUEvicts(t *testing.T) {
	// Filling the cache beyond providerCacheCapacity must evict the
	// oldest entries; the cache stays bounded.
	tag := byte('g')
	p := newLedgerProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
		// Synthesize headers on demand so we don't need to materialize
		// thousands up front.
		seq := uint32(h[0]) | uint32(h[1])<<8 | uint32(h[2])<<16
		if seq == 0 || h[31] != tag {
			return nil, errors.New("not found")
		}
		var parent [32]byte
		if seq > 1 {
			parent[0] = byte((seq - 1) & 0xff)
			parent[1] = byte(((seq - 1) >> 8) & 0xff)
			parent[2] = byte(((seq - 1) >> 16) & 0xff)
			parent[31] = tag
		}
		return &fakeHeader{seq: seq, hash: h, parent: parent}, nil
	})

	// Insert providerCacheCapacity+50 distinct ledger IDs.
	makeID := func(seq uint32) consensus.LedgerID {
		var id consensus.LedgerID
		id[0] = byte(seq & 0xff)
		id[1] = byte((seq >> 8) & 0xff)
		id[2] = byte((seq >> 16) & 0xff)
		id[31] = tag
		return id
	}
	for s := uint32(1); s <= providerCacheCapacity+50; s++ {
		if _, ok := p.LedgerByID(makeID(s)); !ok {
			t.Fatalf("insert seq=%d should succeed", s)
		}
	}

	p.mu.Lock()
	cacheLen := p.lru.Len()
	mapLen := len(p.cache)
	p.mu.Unlock()

	if cacheLen > providerCacheCapacity {
		t.Errorf("LRU not bounded: got %d entries, want ≤%d", cacheLen, providerCacheCapacity)
	}
	if cacheLen != mapLen {
		t.Errorf("LRU/map size mismatch: %d vs %d", cacheLen, mapLen)
	}
	// First-inserted entry should have been evicted.
	if _, ok := p.cacheGet(makeID(1)); ok {
		t.Errorf("oldest entry (seq=1) should have been evicted")
	}
}

func TestLedgerProvider_CachesRepeatedQueries(t *testing.T) {
	tip, byHash := buildChain(10, 'c')

	var calls int
	p := newLedgerProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
		calls++
		if lh, ok := byHash[h]; ok {
			return lh, nil
		}
		return nil, errors.New("not found")
	})

	_, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("first lookup should succeed")
	}
	firstCalls := calls

	// Second query for the same ID should be a pure cache hit.
	_, ok = p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("second lookup should succeed")
	}
	if calls != firstCalls {
		t.Errorf("second lookup should not call into svc: got %d extra calls", calls-firstCalls)
	}
}

func TestLedgerProvider_SplicesCachedPrefix(t *testing.T) {
	_, byHash := buildChain(10, 'd')

	// Locate seq-5 as an intermediate tip we'll warm the cache with.
	var seq5 *fakeHeader
	for _, lh := range byHash {
		if lh.Sequence() == 5 {
			seq5 = lh.(*fakeHeader)
			break
		}
	}

	var calls int
	p := newLedgerProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
		calls++
		if lh, ok := byHash[h]; ok {
			return lh, nil
		}
		return nil, errors.New("not found")
	})

	// Warm cache at seq 5: five lookups (tip + 4 walk-back steps).
	if _, ok := p.LedgerByID(consensus.LedgerID(seq5.hash)); !ok {
		t.Fatal("warm lookup should succeed")
	}
	warmCalls := calls

	// Lookup seq 10. The walk must halt at seq 5 (whose ancestors are
	// already cached) and splice the prefix instead of walking 9 steps.
	// Expected extra calls: 1 (tip) + 5 (walk back to seq 5) = 6.
	// Without the splice it would be 1 + 9 = 10.
	var seq10 *fakeHeader
	for _, lh := range byHash {
		if lh.Sequence() == 10 {
			seq10 = lh.(*fakeHeader)
			break
		}
	}
	if _, ok := p.LedgerByID(consensus.LedgerID(seq10.hash)); !ok {
		t.Fatal("cold lookup should succeed")
	}
	extra := calls - warmCalls
	if extra > 6 {
		t.Errorf("splice didn't short-circuit: extra lookups = %d (expected ≤6)", extra)
	}
}

func TestLedgerProvider_NilServiceDisables(t *testing.T) {
	p := NewLedgerProvider(nil)
	if _, ok := p.LedgerByID(consensus.LedgerID{0x01}); ok {
		t.Fatal("nil-service provider should always return false")
	}
}

func TestLedgerProvider_NilReceiverSafe(t *testing.T) {
	var p *LedgerProvider
	if _, ok := p.LedgerByID(consensus.LedgerID{0x01}); ok {
		t.Fatal("nil receiver should return false without panicking")
	}
}
