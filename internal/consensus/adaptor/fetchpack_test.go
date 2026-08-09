package adaptor

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchPackCache_AddGetConsumeExpiry(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	h := [32]byte{1, 2, 3}
	data := []byte{9, 8, 7}
	t0 := time.Unix(1000, 0)

	// A successful get returns the node and consumes it: a fetch-pack node is
	// single-use, so a second get of the same hash misses.
	require.True(t, c.add(h, data, t0))
	got, ok := c.get(h, t0)
	require.True(t, ok)
	require.Equal(t, []byte{9, 8, 7}, got)
	if _, ok := c.get(h, t0); ok {
		t.Error("get did not consume the entry on retrieval")
	}

	// The cache copies on insert so a caller mutating its buffer can't corrupt
	// the stored node.
	require.True(t, c.add(h, data, t0))
	data[0] = 0xFF
	got2, ok := c.get(h, t0)
	require.True(t, ok)
	require.Equal(t, byte(9), got2[0], "cache did not copy data on add")

	// An entry whose age has reached the TTL is not returned (inclusive
	// boundary), and either way the read consumes it.
	require.True(t, c.add(h, data, t0))
	if _, ok := c.get(h, t0.Add(fetchPackCacheTTL)); ok {
		t.Error("entry at the TTL boundary was returned")
	}
	require.True(t, c.add(h, data, t0))
	if _, ok := c.get(h, t0.Add(fetchPackCacheTTL+time.Second)); ok {
		t.Error("expired entry returned")
	}
}

func TestMakeFetchPack_DiffsHaveAndTraversesHistory(t *testing.T) {
	genesisLedger := makeGenesisLedger(t)
	first, err := ledger.NewOpen(genesisLedger, time.Now())
	require.NoError(t, err)
	require.NoError(t, first.Close(time.Now(), 0))
	second, err := ledger.NewOpen(first, time.Now())
	require.NoError(t, err)
	require.NoError(t, second.Close(time.Now(), 0))
	have, err := ledger.NewOpen(second, time.Now())
	require.NoError(t, err)
	require.NoError(t, have.Close(time.Now(), 0))

	lookup := newFakeLookup()
	lookup.add(genesisLedger)
	lookup.add(first)
	lookup.add(second)
	lookup.add(have)
	p := newLedgerProviderForTest(lookup)
	objects, err := p.MakeFetchPack(have.Hash(), 4)
	require.NoError(t, err)
	require.NotEmpty(t, objects)
	assert.Equal(t, second.Sequence(), objects[0].LedgerSeq)
	foundFirst := false
	for _, object := range objects[1:] {
		if object.LedgerSeq == first.Sequence() {
			foundFirst = true
			break
		}
	}
	assert.True(t, foundFirst, "historical traversal should include the older predecessor")
}

func TestMakeFetchPack_GuardsAndCancellation(t *testing.T) {
	want := makeGenesisLedger(t)
	have, err := ledger.NewOpen(want, time.Now())
	require.NoError(t, err)
	require.NoError(t, have.Close(time.Now(), 0))
	lookup := newFakeLookup()
	lookup.add(want)
	lookup.add(have)
	p := newLedgerProviderForTest(lookup)

	p.SetFetchPackGuards(func() bool { return true }, nil)
	objects, err := p.MakeFetchPack(have.Hash(), 0)
	require.ErrorIs(t, err, peermanagement.ErrFetchPackBusy)
	assert.Nil(t, objects)
	p.SetFetchPackGuards(nil, func() time.Duration { return 41 * time.Second })
	objects, err = p.MakeFetchPack(have.Hash(), 0)
	require.ErrorIs(t, err, peermanagement.ErrFetchPackBusy)
	assert.Nil(t, objects)

	p.SetFetchPackGuards(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	objects, err = p.MakeFetchPackContext(ctx, have.Hash(), 0)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, objects)
}

func TestMakeFetchPack_LookupCancellationWinsOverStorageError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lookup := &contextErrorLookup{
		fakeLookup:   newFakeLookup(),
		err:          errors.New("storage unavailable"),
		beforeReturn: cancel,
	}
	objects, err := newLedgerProviderForTest(lookup).MakeFetchPackContext(ctx, [32]byte{1}, 0)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, objects)
}

func TestMakeFetchPack_ExactObjectCap(t *testing.T) {
	want := makeGenesisLedger(t)
	have, err := ledger.NewOpen(want, time.Now())
	require.NoError(t, err)
	require.NoError(t, have.Close(time.Now(), 0))
	lookup := newFakeLookup()
	lookup.add(want)
	lookup.add(have)
	p := newLedgerProviderForTest(lookup)
	objects, err := p.MakeFetchPack(have.Hash(), 1)
	require.NoError(t, err)
	assert.Len(t, objects, 1, "the header consumes the exact one-object cap")
}

func TestFetchPackCache_Sweep(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	t0 := time.Unix(1000, 0)
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0.Add(40*time.Second))
	c.sweep(t0.Add(fetchPackCacheTTL + time.Second))
	require.Equal(t, 1, c.Size(), "sweep should drop only the expired entry")
	if _, ok := c.get([32]byte{2}, t0.Add(40*time.Second)); !ok {
		t.Error("sweep dropped a still-fresh entry")
	}
	require.Zero(t, c.Bytes())
}

func TestFetchPackCache_ByteBoundEvictsOldest(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxBytes = 6
	c.maxNode = 4
	t0 := time.Unix(1000, 0)
	require.True(t, c.add([32]byte{1}, []byte{1, 1, 1}, t0))
	require.True(t, c.add([32]byte{2}, []byte{2, 2, 2}, t0.Add(time.Second)))
	require.True(t, c.add([32]byte{3}, []byte{3, 3, 3}, t0.Add(2*time.Second)))

	require.Equal(t, int64(6), c.Bytes())
	require.Equal(t, 2, c.Size())
	_, ok := c.get([32]byte{1}, t0.Add(2*time.Second))
	require.False(t, ok)
	_, ok = c.get([32]byte{2}, t0.Add(2*time.Second))
	require.True(t, ok)
	require.Equal(t, int64(3), c.Bytes())
}

func TestFetchPackCache_EntryBoundEvictsOldest(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxEntries = 2
	t0 := time.Unix(1000, 0)
	require.True(t, c.add([32]byte{1}, []byte{1}, t0))
	require.True(t, c.add([32]byte{2}, []byte{2}, t0.Add(time.Second)))
	require.True(t, c.add([32]byte{3}, []byte{3}, t0.Add(2*time.Second)))

	require.Equal(t, 2, c.Size())
	_, ok := c.get([32]byte{1}, t0.Add(2*time.Second))
	require.False(t, ok)
	_, ok = c.get([32]byte{3}, t0.Add(2*time.Second))
	require.True(t, ok)
}

func TestFetchPackCache_RejectsOversizedNode(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxBytes = 8
	c.maxNode = 4
	require.False(t, c.add([32]byte{1}, make([]byte, 5), time.Unix(1000, 0)))
	require.Zero(t, c.Size())
	require.Zero(t, c.Bytes())
}

func TestFetchPackCache_ReplacementAccounting(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxBytes = 8
	c.maxNode = 8
	h := [32]byte{1}
	t0 := time.Unix(1000, 0)
	require.True(t, c.add(h, []byte{1, 2, 3}, t0))
	require.True(t, c.add(h, []byte{4, 5, 6, 7, 8}, t0.Add(time.Second)))
	require.Equal(t, int64(5), c.Bytes())
	require.Equal(t, 1, c.Size())
}

func TestFetchPackCache_OrderCompactionBoundsReplacementTombstones(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxEntries = 2
	c.maxBytes = 8
	c.maxNode = 8
	h := [32]byte{1}
	for i := range 100 {
		require.True(t, c.add(h, []byte{byte(i)}, time.Unix(int64(i), 0)))
	}
	require.LessOrEqual(t, len(c.order), 2*c.maxEntries)
	require.Equal(t, 1, c.Size())
}

func TestFetchPackCache_StaleOrderDoesNotEvictReinsertedEntry(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxEntries = 2
	t0 := time.Unix(1000, 0)
	h1 := [32]byte{1}
	h2 := [32]byte{2}
	h3 := [32]byte{3}
	require.True(t, c.add(h1, []byte{1}, t0))
	require.True(t, c.add(h2, []byte{2}, t0.Add(time.Second)))
	_, ok := c.get(h1, t0.Add(2*time.Second))
	require.True(t, ok)
	require.True(t, c.add(h1, []byte{3}, t0.Add(3*time.Second)))
	require.True(t, c.add(h3, []byte{4}, t0.Add(4*time.Second)))

	_, ok = c.get(h2, t0.Add(4*time.Second))
	require.False(t, ok)
	got, ok := c.get(h1, t0.Add(4*time.Second))
	require.True(t, ok)
	require.Equal(t, []byte{3}, got)
}

func TestFetchPackCache_AcceptsNewcomersOverTarget(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.targetSize = 2
	t0 := time.Unix(1000, 0)
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0)
	c.add([32]byte{3}, []byte{3}, t0)
	require.Equal(t, 3, c.Size(), "newcomer over the target was refused")
	got, ok := c.get([32]byte{3}, t0)
	require.True(t, ok, "a fresh, useful node over the target must remain available")
	require.Equal(t, byte(3), got[0])
}

func TestFetchPackCache_SweepShrinksProportionallyOverTarget(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.targetSize = 2
	t0 := time.Unix(1000, 0)
	// Four entries over a target of two ⇒ a 2× overflow shrinks the eviction
	// window to ttl/2 (22.5s). Two entries aged 30s fall outside it; the two
	// aged 10s survive. Under the full TTL (45s) all four would survive, so the
	// drop is the proportional shrink, not plain TTL expiry.
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0)
	c.add([32]byte{3}, []byte{3}, t0.Add(20*time.Second))
	c.add([32]byte{4}, []byte{4}, t0.Add(20*time.Second))
	c.sweep(t0.Add(30 * time.Second))
	require.Equal(t, 2, c.Size(), "oversized cache should age out the older half")
	for _, h := range [][32]byte{{1}, {2}} {
		if _, ok := c.get(h, t0.Add(30*time.Second)); ok {
			t.Errorf("entry %v older than the shrunk window survived sweep", h[0])
		}
	}
	for _, h := range [][32]byte{{3}, {4}} {
		if _, ok := c.get(h, t0.Add(30*time.Second)); !ok {
			t.Errorf("entry %v within the shrunk window was swept", h[0])
		}
	}
}

func TestFetchPackCache_EffectiveMaxAge(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.targetSize = 100
	c.ttl = 40 * time.Second

	// Within the target: entries linger the full TTL.
	require.Equal(t, 40*time.Second, c.effectiveMaxAge(50))
	require.Equal(t, 40*time.Second, c.effectiveMaxAge(100))
	// 2× over the target: the window is halved.
	require.Equal(t, 20*time.Second, c.effectiveMaxAge(200))
	// Far over the target: the window is floored at one second.
	require.Equal(t, time.Second, c.effectiveMaxAge(1_000_000))
}

func TestFetchPackCache_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *fetchPackCache
	require.False(t, c.add([32]byte{1}, []byte{1}, time.Unix(1, 0)))
	if _, ok := c.get([32]byte{1}, time.Unix(1, 0)); ok {
		t.Error("nil cache returned a hit")
	}
	c.sweep(time.Unix(1, 0))
	require.Equal(t, 0, c.Size())
	require.Zero(t, c.Bytes())
	require.Equal(t, uint32(0), (*Router)(nil).FetchPackCacheSize())
}

// TestMakeFetchPack_PacksParentTree verifies the serve side packs the parent
// ("want") of the requested ledger: a header object whose hash is want's
// ledger hash, followed by SHAMap nodes that each verify against their hash,
// all tagged with want's sequence.
func TestMakeFetchPack_PacksParentTree(t *testing.T) {
	t.Parallel()
	want := makeGenesisLedger(t) // the ledger we expect to be packed
	open, err := ledger.NewOpen(want, time.Now())
	require.NoError(t, err)
	require.NoError(t, open.Close(time.Now(), 0))
	have := open // child of want
	require.True(t, have.IsImmutable())
	require.Equal(t, want.Hash(), have.Header().ParentHash)

	lookup := newFakeLookup()
	lookup.add(want)
	lookup.add(have)
	p := newLedgerProviderForTest(lookup)

	objs, err := p.MakeFetchPack(have.Hash(), 0)
	require.NoError(t, err)
	require.NotEmpty(t, objs)

	wantHash := want.Hash()
	assert.Equal(t, wantHash[:], objs[0].Hash, "first object must be want's header node")
	assert.Equal(t, want.Sequence(), objs[0].LedgerSeq)

	for i := 1; i < len(objs); i++ {
		var h [32]byte
		copy(h[:], objs[i].Hash)
		assert.Truef(t, shamap.VerifyFetchPackNode(h, objs[i].Data), "object %d does not verify", i)
		assert.Equal(t, want.Sequence(), objs[i].LedgerSeq, "object %d carries the wrong ledger seq", i)
	}
}

func TestMakeFetchPack_UnknownOrOpenHaveYieldsNoPack(t *testing.T) {
	t.Parallel()
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	// Unknown have.
	objs, err := p.MakeFetchPack([32]byte{0xAB}, 0)
	require.NoError(t, err)
	require.Nil(t, objs)

	// Open (not immutable) have is refused.
	open := makeOpenLedger(t)
	synthetic := [32]byte{0xCD}
	lookup.byHash[synthetic] = open
	objs, err = p.MakeFetchPack(synthetic, 0)
	require.ErrorIs(t, err, peermanagement.ErrFetchPackOpen)
	require.Nil(t, objs)
}

func TestMakeFetchPack_TargetStateCapDoesNotLimitTargetTxOrHistory(t *testing.T) {
	genesis := makeGenesisLedger(t)
	state, err := genesis.StateMapSnapshot()
	require.NoError(t, err)
	for i := 0; i < fetchPackMaxObjects+128; i++ {
		var key [32]byte
		key[0] = byte(i)
		key[1] = byte(i >> 8)
		key[31] = 0xA5
		require.NoError(t, state.Put(key, bytes.Repeat([]byte{byte(i)}, 12)))
	}
	txMap, err := genesis.TxMapSnapshot()
	require.NoError(t, err)
	want, err := ledger.NewOpenWithHeader(genesis.Header(), state, txMap, genesis.Fees())
	require.NoError(t, err)
	require.NoError(t, want.Close(time.Now(), 0))
	haveHeader := want.Header()
	haveHeader.ParentHash = want.Hash()
	haveHeader.LedgerIndex++
	have, err := ledger.NewOpenWithHeader(
		haveHeader,
		shamap.New(shamap.TypeState),
		shamap.New(shamap.TypeTransaction),
		want.Fees(),
	)
	require.NoError(t, err)
	require.NoError(t, have.Close(time.Now(), 0))

	lookup := newFakeLookup()
	lookup.add(want)
	lookup.add(have)
	objects, err := newLedgerProviderForTest(lookup).MakeFetchPack(have.Hash(), 0)
	require.NoError(t, err)
	assert.Greater(t, len(objects), fetchPackMaxObjects,
		"the target ledger's state map may exceed the historical continuation gate")
	for _, object := range objects {
		assert.Equal(t, want.Sequence(), object.LedgerSeq,
			"a pack over the gate must not continue to an older ledger")
	}
}

func TestMakeFetchPack_RespectsConfiguredFetchDepth(t *testing.T) {
	want := makeGenesisLedger(t)
	have, err := ledger.NewOpen(want, time.Now())
	require.NoError(t, err)
	require.NoError(t, have.Close(time.Now(), 0))

	lookup := newFakeLookup()
	lookup.add(want)
	lookup.add(have)
	p := newLedgerProviderForTest(lookup)

	lookup.earliestFetch = have.Sequence() + 1
	objects, err := p.MakeFetchPack(have.Hash(), 0)
	require.ErrorIs(t, err, peermanagement.ErrFetchPackTooEarly)
	assert.Nil(t, objects)

	lookup.earliestFetch = have.Sequence()
	objects, err = p.MakeFetchPack(have.Hash(), 0)
	require.NoError(t, err)
	assert.NotEmpty(t, objects)
}

// TestHandleFetchPackReply_VerifiesAndCaches drives a fetch-pack reply through
// the router and asserts only verifiable SHAMap nodes are cached — the leading
// header object and a tampered node are dropped.
func TestHandleFetchPackReply_VerifiesAndCaches(t *testing.T) {
	t.Parallel()
	source := shamap.New(shamap.TypeState)
	for i := byte(1); i <= 12; i++ {
		var key [32]byte
		key[0] = i
		key[31] = 0xA5
		require.NoError(t, source.Put(key, []byte{i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB}))
	}
	_, err := source.Hash()
	require.NoError(t, err)
	valid, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(valid), 2)

	objects := make([]message.IndexedObject, 0, len(valid)+2)
	// A header-like object: hash != sha512Half(data) → must be dropped.
	objects = append(objects, message.IndexedObject{
		Hash: bytes.Repeat([]byte{0xEE}, 32),
		Data: []byte{0x01, 0x02, 0x03},
	})
	for _, n := range valid {
		objects = append(objects, message.IndexedObject{
			Hash: append([]byte(nil), n.Hash[:]...),
			Data: n.Data,
		})
	}
	// A tampered node: valid hash, corrupted data → must be dropped.
	tampered := append([]byte(nil), valid[len(valid)-1].Data...)
	tampered[len(tampered)-1] ^= 0xFF
	objects = append(objects, message.IndexedObject{
		Hash: append([]byte(nil), valid[len(valid)-1].Hash[:]...),
		Data: tampered,
	})

	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType: message.ObjectTypeFetchPack,
		Query:   false,
		Objects: objects,
	})
	require.NoError(t, err)

	r := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	armFetchAcquisition(r) // a pack is only processed while an acquisition is in flight
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(5),
		Type:    message.TypeGetObjects,
		Payload: payload,
	})

	assert.Equal(t, len(valid), r.fetchPacks.Size(), "only verifiable SHAMap nodes should be cached")
	assert.Equal(t, uint32(len(valid)), r.FetchPackCacheSize())
	for _, n := range valid {
		if _, ok := r.fetchPacks.get(n.Hash, time.Now()); !ok {
			t.Errorf("valid node %x not cached", n.Hash[:8])
		}
	}
	var headerHash [32]byte
	copy(headerHash[:], bytes.Repeat([]byte{0xEE}, 32))
	if _, ok := r.fetchPacks.get(headerHash, time.Now()); ok {
		t.Error("non-SHAMap header object was cached")
	}
}

func TestHaveLedgerSeqRequiresCompleteLedger(t *testing.T) {
	adaptor := newTestAdaptor(t)
	r := newTestRouter(nil, adaptor, make(chan *peermanagement.InboundMessage, 1))
	open := adaptor.LedgerService().GetOpenLedger()
	require.NotNil(t, open)

	assert.False(t, r.haveLedgerSeq(open.Sequence()))
}

// TestTryFetchPackEscalation_NoChildIsNoOp confirms the escalation is a no-op
// (and does not consume its one-shot flag) when no child ledger is known to key
// the pack request on — the common forward-tip case.
func TestTryFetchPackEscalation_NoChildIsNoOp(t *testing.T) {
	t.Parallel()
	r := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	il := inbound.New([32]byte{0x7A, 0x7B}, 999999, 3, serveTestLogger())
	if r.tryFetchPackEscalation(il) {
		t.Fatal("escalation reported a request sent without a known child ledger")
	}
	if il.FetchPackRequested() {
		t.Error("escalation consumed the one-shot flag despite sending nothing")
	}
}

type fetchPackPrioritySender struct {
	noopSender
	ordinary int
	priority int
}

func (s *fetchPackPrioritySender) SendToPeer(uint64, []byte) error {
	s.ordinary++
	return nil
}

func (s *fetchPackPrioritySender) SendPriorityToPeer(uint64, []byte) error {
	s.priority++
	return nil
}

func TestTryFetchPackEscalationUsesAcquisitionLane(t *testing.T) {
	adaptor := newTestAdaptor(t)
	sender := &fetchPackPrioritySender{}
	adaptor.sender = sender
	svc := adaptor.LedgerService()
	child := svc.GetClosedLedger()
	require.NotNil(t, child)
	parent, err := svc.GetLedgerByHash(child.ParentHash())
	require.NoError(t, err)
	require.NotNil(t, parent)

	r := newTestRouter(nil, adaptor, make(chan *peermanagement.InboundMessage, 1))
	il := inbound.New(parent.Hash(), parent.Sequence(), 3, serveTestLogger())

	require.True(t, r.tryFetchPackEscalation(il))
	assert.Equal(t, 1, sender.priority)
	assert.Zero(t, sender.ordinary)
}

// armFetchAcquisition registers one in-flight acquisition so the fetch-pack
// reply handler's solicitation gate lets a pack through.
func armFetchAcquisition(r *Router) {
	r.fetchTracker.GetOrCreate([32]byte{0xAC}, func() *inbound.Ledger {
		return inbound.New([32]byte{0xAC}, 1, 0, serveTestLogger())
	})
}

// validFetchPackNodes builds a small state SHAMap and returns its fetch-pack
// nodes, each of which verifies against its hash.
func validFetchPackNodes(t *testing.T) []shamap.FetchPackNode {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for i := byte(1); i <= 12; i++ {
		var key [32]byte
		key[0] = i
		key[31] = 0xA5
		require.NoError(t, source.Put(key, []byte{i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB}))
	}
	_, err := source.Hash()
	require.NoError(t, err)
	nodes, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(nodes), 2)
	return nodes
}

// manyValidFetchPackNodes builds a state SHAMap large enough to yield at least
// minCount fetch-pack nodes, each of which verifies against its hash.
func manyValidFetchPackNodes(t *testing.T, minCount int) []shamap.FetchPackNode {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	// A 16-way SHAMap yields a few more nodes than leaves, so a leaf count
	// comfortably above minCount clears it.
	leaves := minCount + minCount/8 + 16
	for i := range leaves {
		var key [32]byte
		key[0] = byte(i)
		key[1] = byte(i >> 8)
		key[2] = byte(i >> 16)
		key[31] = 0xA5
		require.NoError(t, source.Put(key, []byte{byte(i), byte(i >> 8), 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB}))
	}
	_, err := source.Hash()
	require.NoError(t, err)
	nodes, err := source.WalkFetchPackNodes(1 << 24)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(nodes), minCount,
		"need at least minCount nodes to exercise the cap")
	return nodes
}

func nodesToObjects(nodes []shamap.FetchPackNode) []message.IndexedObject {
	objects := make([]message.IndexedObject, 0, len(nodes))
	for _, n := range nodes {
		objects = append(objects, message.IndexedObject{
			Hash: append([]byte(nil), n.Hash[:]...),
			Data: n.Data,
		})
	}
	return objects
}

func encodeFetchPack(t *testing.T, objects []message.IndexedObject) []byte {
	t.Helper()
	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType: message.ObjectTypeFetchPack,
		Query:   false,
		Objects: objects,
	})
	require.NoError(t, err)
	return payload
}

// TestHandleFetchPackReply_NoActiveAcquisitionDropped confirms a pack arriving
// with no acquisition in flight is dropped before any hashing, and without
// charging the sender (an unsolicited pack is a benign race, not misbehavior).
func TestHandleFetchPackReply_NoActiveAcquisitionDropped(t *testing.T) {
	t.Parallel()
	payload := encodeFetchPack(t, nodesToObjects(validFetchPackNodes(t)))

	r, rs := makeRouterWithBadDataRecorder(t)
	// No acquisition armed.
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(5),
		Type:    message.TypeGetObjects,
		Payload: payload,
	})

	assert.Equal(t, 0, r.fetchPacks.Size(), "unsolicited pack must not be cached")
	assert.Empty(t, rs.getBadDataCalls(), "an unsolicited pack is benign, not a charge")
}

// TestHandleFetchPackReply_ProcessesAllWireValidObjects confirms the receiver
// does not apply the local serving cap to an inbound pack. A valid sender may
// return the larger state-tree allowance, and every object in the bounded wire
// frame must be available to the acquisition.
func TestHandleFetchPackReply_ProcessesAllWireValidObjects(t *testing.T) {
	t.Parallel()
	nodes := manyValidFetchPackNodes(t, fetchPackMaxObjects+1)
	payload := encodeFetchPack(t, nodesToObjects(nodes))

	r, rs := makeRouterWithBadDataRecorder(t)
	armFetchAcquisition(r)
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(8),
		Type:    message.TypeGetObjects,
		Payload: payload,
	})

	assert.Equal(t, len(nodes), r.fetchPacks.Size(),
		"every wire-valid object must be cached")
	assert.Empty(t, rs.getBadDataCalls(),
		"an over-cap reply from an honest peer must not be charged")
}

// TestHandleFetchPackReply_PoisonCharged confirms a blob that does not hash to
// its claimed key is dropped and the sender is charged, while the verifiable
// nodes in the same reply are still cached.
func TestHandleFetchPackReply_PoisonCharged(t *testing.T) {
	t.Parallel()
	nodes := validFetchPackNodes(t)
	objects := nodesToObjects(nodes)
	// A tampered node: valid hash, corrupted data → poison.
	tampered := append([]byte(nil), nodes[len(nodes)-1].Data...)
	tampered[len(tampered)-1] ^= 0xFF
	objects = append(objects, message.IndexedObject{
		Hash: append([]byte(nil), nodes[len(nodes)-1].Hash[:]...),
		Data: tampered,
	})
	payload := encodeFetchPack(t, objects)

	r, rs := makeRouterWithBadDataRecorder(t)
	armFetchAcquisition(r)
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(11),
		Type:    message.TypeGetObjects,
		Payload: payload,
	})

	assert.Equal(t, len(nodes), r.fetchPacks.Size(), "verifiable nodes must still be cached")
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(11), calls[0].peerID)
	assert.Equal(t, "fetch-pack-poison", calls[0].reason)
}

// TestHandleFetchPackReply_HeaderObjectNotCharged confirms the pack's leading
// ledger-header object (which never verifies as a SHAMap node) is dropped
// without charging the sender as poison.
func TestHandleFetchPackReply_HeaderObjectNotCharged(t *testing.T) {
	t.Parallel()
	nodes := validFetchPackNodes(t)
	header := message.IndexedObject{
		Hash: bytes.Repeat([]byte{0xEE}, 32),
		Data: append(protocol.HashPrefixLedgerMaster().Bytes(), 0xDE, 0xAD, 0xBE, 0xEF),
	}
	objects := append([]message.IndexedObject{header}, nodesToObjects(nodes)...)
	payload := encodeFetchPack(t, objects)

	r, rs := makeRouterWithBadDataRecorder(t)
	armFetchAcquisition(r)
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(13),
		Type:    message.TypeGetObjects,
		Payload: payload,
	})

	assert.Equal(t, len(nodes), r.fetchPacks.Size(), "valid nodes cached; header dropped")
	var headerHash [32]byte
	copy(headerHash[:], bytes.Repeat([]byte{0xEE}, 32))
	if _, ok := r.fetchPacks.get(headerHash, time.Now()); ok {
		t.Error("ledger-header object was cached")
	}
	assert.Empty(t, rs.getBadDataCalls(), "a well-formed header is expected to fail verification, not poison")
}
