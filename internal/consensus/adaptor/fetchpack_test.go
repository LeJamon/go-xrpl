package adaptor

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
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

func TestFetchPackCache_AcceptsNewcomersOverTarget(t *testing.T) {
	t.Parallel()
	c := newFetchPackCache()
	c.targetSize = 2
	t0 := time.Unix(1000, 0)
	c.add([32]byte{1}, []byte{1}, t0)
	c.add([32]byte{2}, []byte{2}, t0)
	c.add([32]byte{3}, []byte{3}, t0) // over target → still accepted
	require.Equal(t, 3, c.size(), "newcomer over the target was refused")
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
	require.Equal(t, 2, c.size(), "oversized cache should age out the older half")
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
