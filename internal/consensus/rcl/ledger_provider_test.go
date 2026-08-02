package rcl

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
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
func newTestProvider(byHash map[[32]byte]LedgerHeader) *AncestryProvider {
	return newAncestryProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
		if lh, ok := byHash[h]; ok {
			return lh, nil
		}
		return nil, errors.New("not found")
	})
}

func TestAncestryProvider_BuildsFullAncestry(t *testing.T) {
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

func TestAncestryProvider_MissingLinkTruncates(t *testing.T) {
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

func TestAncestryProvider_MissingLinkRepairsAfterArrival(t *testing.T) {
	tip, byHash := buildChain(5, 'r')
	var missingHash [32]byte
	for hash, header := range byHash {
		if header.Sequence() == 3 {
			missingHash = hash
			break
		}
	}
	missing := byHash[missingHash]
	delete(byHash, missingHash)

	p := newTestProvider(byHash)
	first, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("partial lookup should resolve the tip suffix")
	}
	if first.MinSeq() != 3 {
		t.Fatalf("partial MinSeq = %d, want 3", first.MinSeq())
	}

	byHash[missingHash] = missing
	second, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("repaired lookup should resolve")
	}
	if second.MinSeq() != 1 {
		t.Fatalf("repaired MinSeq = %d, want 1", second.MinSeq())
	}
	if got := second.Ancestor(2); got[1] != 'r' {
		t.Fatalf("repaired Ancestor(2) = %x, want chain tag r", got)
	}
}

func TestAncestryProvider_RetainsPartialWhenRepairIsTransientlyUnavailable(t *testing.T) {
	tip, byHash := buildChain(5, 'u')
	var missingHash [32]byte
	var unstableHash [32]byte
	for hash, header := range byHash {
		switch header.Sequence() {
		case 3:
			missingHash = hash
		case 4:
			unstableHash = hash
		}
	}
	missing := byHash[missingHash]
	unstable := byHash[unstableHash]
	delete(byHash, missingHash)

	failTip := false
	p := newAncestryProviderFromLookup(func(hash [32]byte) (LedgerHeader, error) {
		if failTip && hash == tip.hash {
			failTip = false
			return nil, errors.New("transient tip failure")
		}
		if header, ok := byHash[hash]; ok {
			return header, nil
		}
		return nil, errors.New("not found")
	})
	partial, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("partial lookup should resolve the tip suffix")
	}
	if got := partial.MinSeq(); got != 3 {
		t.Fatalf("partial MinSeq = %d, want 3", got)
	}

	byHash[missingHash] = missing
	failTip = true
	retained, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("transient repair failure should retain the cached suffix")
	}
	if got := retained.MinSeq(); got != 3 {
		t.Fatalf("retained MinSeq = %d, want 3", got)
	}

	delete(byHash, unstableHash)
	retained, ok = p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("poorer partial repair should retain the cached suffix")
	}
	if got := retained.MinSeq(); got != 3 {
		t.Fatalf("retained MinSeq after poorer repair = %d, want 3", got)
	}
	byHash[unstableHash] = unstable

	repaired, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("subsequent repair should resolve")
	}
	if got := repaired.MinSeq(); got != 1 {
		t.Fatalf("repaired MinSeq = %d, want 1", got)
	}
}

func TestAncestryProvider_EvictedPartialJoinsInflightFallback(t *testing.T) {
	tip, byHash := buildChain(5, 'v')
	var missingHash [32]byte
	for hash, header := range byHash {
		if header.Sequence() == 3 {
			missingHash = hash
			break
		}
	}
	missing := byHash[missingHash]
	delete(byHash, missingHash)

	p := newTestProvider(byHash)
	partial, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("partial lookup should resolve the tip suffix")
	}
	byHash[missingHash] = missing

	retryProbe := make(chan struct{})
	releaseRetryProbe := make(chan struct{})
	buildTip := make(chan struct{})
	releaseBuildTip := make(chan struct{})
	p.lookup = func(hash [32]byte) (LedgerHeader, error) {
		switch hash {
		case missingHash:
			close(retryProbe)
			<-releaseRetryProbe
			return missing, nil
		case tip.hash:
			close(buildTip)
			<-releaseBuildTip
			return nil, errors.New("transient tip failure")
		default:
			return byHash[hash], nil
		}
	}

	type result struct {
		ledger ledgertrie.Ledger
		ok     bool
	}
	id := consensus.LedgerID(tip.hash)
	first := make(chan result, 1)
	go func() {
		ledger, resolved := p.LedgerByID(id)
		first <- result{ledger: ledger, ok: resolved}
	}()
	<-retryProbe

	p.mu.Lock()
	elem := p.cache[id]
	delete(p.cache, id)
	p.lru.Remove(elem)
	p.mu.Unlock()

	second := make(chan result, 1)
	go func() {
		ledger, resolved := p.LedgerByID(id)
		second <- result{ledger: ledger, ok: resolved}
	}()
	<-buildTip
	close(releaseRetryProbe)

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		flight := p.inflight[id]
		hasFallback := flight != nil && flight.fallback != nil
		p.mu.Unlock()
		if hasFallback {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("retrying caller did not attach its cached fallback to the in-flight build")
		default:
			runtime.Gosched()
		}
	}
	close(releaseBuildTip)

	for _, got := range []result{<-first, <-second} {
		if !got.ok {
			t.Fatal("shared transient failure should retain the cached suffix")
		}
		if minSeq := got.ledger.MinSeq(); minSeq != partial.MinSeq() {
			t.Fatalf("retained MinSeq = %d, want %d", minSeq, partial.MinSeq())
		}
	}
}

func TestAncestryProvider_RejectsParentHashMismatchAndRepairs(t *testing.T) {
	tip, byHash := buildChain(5, 'm')
	var parentHash [32]byte
	var correct LedgerHeader
	for hash, header := range byHash {
		if header.Sequence() == 4 {
			parentHash = hash
			correct = header
			break
		}
	}
	wrongHash := parentHash
	wrongHash[0]++
	malformed := &fakeHeader{seq: 4, hash: wrongHash, parent: correct.ParentHash()}
	byHash[parentHash] = malformed

	p := newTestProvider(byHash)
	partial, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("hash-mismatched parent should leave a retryable suffix")
	}
	pl, ok := partial.(*providerLedger)
	if !ok || !pl.retryable() {
		t.Fatalf("hash-mismatched parent returned non-retryable ledger: %#v", partial)
	}
	if got := partial.MinSeq(); got != 4 {
		t.Fatalf("hash-mismatched parent MinSeq = %d, want 4", got)
	}

	byHash[parentHash] = correct
	repaired, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("corrected parent did not rebuild full chain")
	}
	if got := repaired.MinSeq(); got != 1 {
		t.Fatalf("corrected parent MinSeq = %d, want 1", got)
	}
}

func TestAncestryProvider_RejectsParentSequenceGapAndRepairs(t *testing.T) {
	tip, byHash := buildChain(5, 'n')
	var parentHash [32]byte
	var correct LedgerHeader
	for hash, header := range byHash {
		if header.Sequence() == 4 {
			parentHash = hash
			correct = header
			break
		}
	}
	byHash[parentHash] = &fakeHeader{seq: 2, hash: parentHash, parent: correct.ParentHash()}

	p := newTestProvider(byHash)
	partial, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("sequence-gap parent should leave a retryable suffix")
	}
	pl, ok := partial.(*providerLedger)
	if !ok || !pl.retryable() {
		t.Fatalf("sequence-gap parent returned non-retryable ledger: %#v", partial)
	}
	if got := partial.MinSeq(); got != 4 {
		t.Fatalf("sequence-gap parent MinSeq = %d, want 4", got)
	}

	byHash[parentHash] = correct
	repaired, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("corrected sequence gap did not rebuild full chain")
	}
	if got := repaired.MinSeq(); got != 1 {
		t.Fatalf("corrected sequence gap MinSeq = %d, want 1", got)
	}
}

func TestAncestryProvider_NilHeadersAreUnknown(t *testing.T) {
	tip, byHash := buildChain(3, 'z')
	var typedNil *fakeHeader
	byHash[tip.hash] = typedNil
	p := newTestProvider(byHash)
	if _, ok := p.LedgerByID(consensus.LedgerID(tip.hash)); ok {
		t.Fatal("typed-nil tip header must not resolve")
	}

	tip, byHash = buildChain(3, 'y')
	var parentHash [32]byte
	for hash, header := range byHash {
		if header.Sequence() == 2 {
			parentHash = hash
			break
		}
	}
	byHash[parentHash] = typedNil
	p = newTestProvider(byHash)
	partial, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok || partial.MinSeq() != 2 {
		t.Fatalf("typed-nil parent should be retryable suffix: ok=%v ledger=%v", ok, partial)
	}
}

func TestAncestryProvider_SameTipConcurrentLookupIsBounded(t *testing.T) {
	tip, byHash := buildChain(12, 'q')
	const workers = 32
	var calls atomic.Int32
	start := make(chan struct{})
	p := newAncestryProviderFromLookup(func(hash [32]byte) (LedgerHeader, error) {
		calls.Add(1)
		if lh, ok := byHash[hash]; ok {
			return lh, nil
		}
		return nil, errors.New("not found")
	})

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
			if !ok {
				t.Errorf("concurrent lookup returned ok=false")
				return
			}
			if lgr.MinSeq() != 1 {
				t.Errorf("concurrent lookup returned min=%d, want 1", lgr.MinSeq())
			}
		}()
	}
	close(start)
	wg.Wait()

	// The shared in-flight result keeps cold fan-in close to one ancestry
	// walk. Allow a small scheduling tail of late callers to begin a second
	// flight, but reject the full workers-times-depth hammering this test
	// exposes without the gate.
	if got, want := calls.Load(), int32(workers*2); got > want {
		t.Fatalf("same-tip lookup duplicated service work: got %d calls, want <=%d", got, want)
	}
}

func TestAncestryProvider_UsesEmbeddedHashOfSeq(t *testing.T) {
	const tipSeq = uint32(5)
	tip, byHash := buildChain(tipSeq, 's')

	hashes := make(map[uint32][32]byte, tipSeq)
	for hash, header := range byHash {
		hashes[header.Sequence()] = hash
	}
	byHash[tip.hash] = &skipListHeader{fakeHeader: *tip, ancestors: hashes}
	for hash, header := range byHash {
		if header.Sequence() < tipSeq {
			delete(byHash, hash)
		}
	}

	p := newTestProvider(byHash)
	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("embedded ancestry should resolve without parent records")
	}
	if lgr.MinSeq() != 1 {
		t.Fatalf("embedded ancestry MinSeq = %d, want 1", lgr.MinSeq())
	}
	for seq := uint32(1); seq < tipSeq; seq++ {
		if got, want := lgr.Ancestor(seq), consensus.LedgerID(hashes[seq]); got != want {
			t.Fatalf("Ancestor(%d) = %x, want %x", seq, got, want)
		}
	}
}

type skipListHeader struct {
	fakeHeader
	ancestors map[uint32][32]byte
}

func (h *skipListHeader) HashOfSeq(seq uint32) ([32]byte, bool, error) {
	hash, ok := h.ancestors[seq]
	return hash, ok, nil
}

func TestAncestryProvider_BoundedAtMaxAncestors(t *testing.T) {
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

func TestAncestryProvider_AncestorOutOfRangeReturnsZero(t *testing.T) {
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

func TestAncestryProvider_LRUEvicts(t *testing.T) {
	// Filling the cache beyond providerCacheCapacity must evict the
	// oldest entries; the cache stays bounded.
	tag := byte('g')
	p := newAncestryProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
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

func TestAncestryProvider_CachesRepeatedQueries(t *testing.T) {
	tip, byHash := buildChain(10, 'c')

	var calls int
	p := newAncestryProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
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

func TestAncestryProvider_SplicesCachedPrefix(t *testing.T) {
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
	p := newAncestryProviderFromLookup(func(h [32]byte) (LedgerHeader, error) {
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

func TestAncestryProvider_BypassesWrongSequenceCachedParent(t *testing.T) {
	tip, byHash := buildChain(6, 'w')
	var parentHash [32]byte
	for hash, header := range byHash {
		if header.Sequence() == tip.seq-1 {
			parentHash = hash
			break
		}
	}
	if parentHash == ([32]byte{}) {
		t.Fatal("test chain did not expose the child parent")
	}

	p := newTestProvider(byHash)
	p.cachePut(consensus.LedgerID(parentHash), &providerLedger{
		id:        consensus.LedgerID(parentHash),
		seq:       tip.seq - 2,
		minSeq:    tip.seq - 2,
		ancestors: nil,
	})

	lgr, ok := p.LedgerByID(consensus.LedgerID(tip.hash))
	if !ok {
		t.Fatal("child lookup failed with wrong-sequence cached parent")
	}
	if lgr.MinSeq() != 1 {
		t.Fatalf("child MinSeq = %d, want 1", lgr.MinSeq())
	}
	if got := lgr.Ancestor(tip.seq - 1); got != consensus.LedgerID(parentHash) {
		t.Fatalf("child parent = %x, want %x", got, parentHash)
	}
}

func TestAncestryProvider_NilServiceDisables(t *testing.T) {
	p := NewAncestryProvider(nil)
	if _, ok := p.LedgerByID(consensus.LedgerID{0x01}); ok {
		t.Fatal("nil-service provider should always return false")
	}
}

func TestAncestryProvider_NilReceiverSafe(t *testing.T) {
	var p *AncestryProvider
	if _, ok := p.LedgerByID(consensus.LedgerID{0x01}); ok {
		t.Fatal("nil receiver should return false without panicking")
	}
}
