package adaptor

import (
	"bytes"
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

func TestFetchPackCache_AddGetExpiry(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	h := [32]byte{1, 2, 3}
	data := []byte{9, 8, 7}
	t0 := time.Unix(1000, 0)

	c.add(h, data, t0)
	got, ok := c.get(h, t0)
	require.True(t, ok)
	require.Equal(t, []byte{9, 8, 7}, got)

	// The cache must copy on insert so a caller mutating its buffer can't
	// corrupt the stored node.
	data[0] = 0xFF
	got2, _ := c.get(h, t0)
	require.Equal(t, byte(9), got2[0], "cache did not copy data on add")

	// Expired entries are not returned and are evicted on read.
	if _, ok := c.get(h, t0.Add(fetchPackCacheTTL+time.Second)); ok {
		t.Error("expired entry returned")
	}
	if _, ok := c.get(h, t0); ok {
		t.Error("expired entry not evicted on the expiring read")
	}
}

func TestFetchPackCache_Sweep(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	t0 := time.Unix(1000, 0)
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0.Add(40*time.Second))
	c.sweep(t0.Add(fetchPackCacheTTL + time.Second))
	require.Equal(t, 1, c.size(), "sweep should drop only the expired entry")
	if _, ok := c.get([32]byte{2}, t0.Add(40*time.Second)); !ok {
		t.Error("sweep dropped a still-fresh entry")
	}
}

func TestFetchPackCache_CapDropsNewcomers(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.maxSize = 2
	t0 := time.Unix(1000, 0)
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0)
	c.add([32]byte{3}, []byte{3}, t0) // over cap → dropped
	require.Equal(t, 2, c.size())
	if _, ok := c.get([32]byte{3}, t0); ok {
		t.Error("newcomer stored over the cap")
	}
	// Refreshing an existing key stays allowed even at the cap.
	c.add([32]byte{1}, []byte{0xAA}, t0)
	got, _ := c.get([32]byte{1}, t0)
	require.Equal(t, byte(0xAA), got[0], "refresh of an existing key was rejected at cap")
}

func TestFetchPackCache_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *fetchPackCache
	c.add([32]byte{1}, []byte{1}, time.Unix(1, 0))
	if _, ok := c.get([32]byte{1}, time.Unix(1, 0)); ok {
		t.Error("nil cache returned a hit")
	}
	c.sweep(time.Unix(1, 0))
	require.Equal(t, 0, c.size())
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
	require.NoError(t, err)
	require.Nil(t, objs)
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

	r := NewRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	armFetchAcquisition(r) // a pack is only processed while an acquisition is in flight
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(5),
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, len(valid), r.fetchPacks.size(), "only verifiable SHAMap nodes should be cached")
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

// TestTryFetchPackEscalation_NoChildIsNoOp confirms the escalation is a no-op
// (and does not consume its one-shot flag) when no child ledger is known to key
// the pack request on — the common forward-tip case.
func TestTryFetchPackEscalation_NoChildIsNoOp(t *testing.T) {
	t.Parallel()
	r := NewRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	il := inbound.New([32]byte{0x7A, 0x7B}, 999999, 3, serveTestLogger())
	if r.tryFetchPackEscalation(il) {
		t.Fatal("escalation reported a request sent without a known child ledger")
	}
	if il.FetchPackRequested() {
		t.Error("escalation consumed the one-shot flag despite sending nothing")
	}
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
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, 0, r.fetchPacks.size(), "unsolicited pack must not be cached")
	assert.Empty(t, rs.getBadDataCalls(), "an unsolicited pack is benign, not a charge")
}

// TestHandleFetchPackReply_OversizedTruncatedNotCharged confirms a reply
// carrying more objects than our serve cap is processed only up to the cap and
// the sender is NOT charged: a peer can legitimately serve a heavy-delta ledger
// above our cap, so the surplus is truncated — bounding the consensus-goroutine
// work without penalising an honest sender.
func TestHandleFetchPackReply_OversizedTruncatedNotCharged(t *testing.T) {
	t.Parallel()
	nodes := manyValidFetchPackNodes(t, fetchPackMaxObjects+1)
	payload := encodeFetchPack(t, nodesToObjects(nodes))

	r, rs := makeRouterWithBadDataRecorder(t)
	armFetchAcquisition(r)
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(8),
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, fetchPackMaxObjects, r.fetchPacks.size(),
		"processing is bounded to the serve cap; surplus objects are ignored")
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
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, len(nodes), r.fetchPacks.size(), "verifiable nodes must still be cached")
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
		Data: append(protocol.HashPrefixLedgerMaster.Bytes(), 0xDE, 0xAD, 0xBE, 0xEF),
	}
	objects := append([]message.IndexedObject{header}, nodesToObjects(nodes)...)
	payload := encodeFetchPack(t, objects)

	r, rs := makeRouterWithBadDataRecorder(t)
	armFetchAcquisition(r)
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(13),
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, len(nodes), r.fetchPacks.size(), "valid nodes cached; header dropped")
	var headerHash [32]byte
	copy(headerHash[:], bytes.Repeat([]byte{0xEE}, 32))
	if _, ok := r.fetchPacks.get(headerHash, time.Now()); ok {
		t.Error("ledger-header object was cached")
	}
	assert.Empty(t, rs.getBadDataCalls(), "a well-formed header is expected to fail verification, not poison")
}
