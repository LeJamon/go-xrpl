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

func TestLedgerProvider_MissingLinkFailsCleanly(t *testing.T) {
	tip, byHash := buildChain(5, 'b')
	// Delete the seq-3 header from the lookup so the walk-back fails
	// when it reaches the seq-4 ledger and tries to load its parent.
	var s3Hash [32]byte
	for h, lh := range byHash {
		if lh.Sequence() == 3 {
			s3Hash = h
			break
		}
	}
	delete(byHash, s3Hash)

	p := newTestProvider(byHash)
	if _, ok := p.LedgerByID(consensus.LedgerID(tip.hash)); ok {
		t.Fatal("LedgerByID should fail when chain has a gap")
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
