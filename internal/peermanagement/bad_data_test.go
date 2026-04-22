package peermanagement

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPeer spins up a Peer with a throw-away identity and event
// channel — all the bad-data tests need is a Peer with working state,
// not an actual connection.
func newTestPeer(t *testing.T, id PeerID) *Peer {
	t.Helper()
	ident, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	return NewPeer(id, endpoint, false, ident, events)
}

// TestPeer_BadDataCount_StartsAtZero is the sanity check: a freshly
// constructed peer has recorded no bad-data events.
func TestPeer_BadDataCount_StartsAtZero(t *testing.T) {
	peer := newTestPeer(t, PeerID(1))
	assert.Equal(t, uint32(0), peer.BadDataCount(),
		"a new peer must start with badData == 0")
}

// TestPeer_BadDataCount_IncrementsMonotonic verifies that three sequential
// IncBadData calls land the count at 3. Also checks that IncBadData
// returns the new cumulative count each time (not the previous one),
// matching the documented contract and rippled's fee accumulator shape.
func TestPeer_BadDataCount_IncrementsMonotonic(t *testing.T) {
	peer := newTestPeer(t, PeerID(2))

	// r1/r2/r3 are unrecognized reasons, so each charges
	// weightDefaultBadData. IncBadData returns the running balance.
	w := uint32(weightDefaultBadData)
	assert.Equal(t, w, peer.IncBadData("r1"),
		"first increment returns the post-increment balance")
	assert.Equal(t, 2*w, peer.IncBadData("r2"))
	assert.Equal(t, 3*w, peer.IncBadData("r3"))

	assert.Equal(t, 3*w, peer.BadDataCount(),
		"BadDataCount must reflect all increments")
}

// TestPeer_BadDataCount_Concurrent verifies the atomic semantics of the
// counter: 100 goroutines × 100 increments each must land exactly at
// 10000 with no lost updates. Guards against a regression to a
// sync.Mutex-gated counter or a naive read-modify-write.
func TestPeer_BadDataCount_Concurrent(t *testing.T) {
	peer := newTestPeer(t, PeerID(3))

	const goroutines = 100
	const perG = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				peer.IncBadData("concurrent")
			}
		}()
	}
	wg.Wait()

	// "concurrent" is unrecognized, so each call charges
	// weightDefaultBadData. The atomic balance must equal the total
	// weight without any lost updates.
	expected := uint32(goroutines * perG * weightDefaultBadData)
	assert.Equal(t, expected, peer.BadDataCount(),
		"concurrent increments must all be counted — no lost updates")
}

// TestPeer_AddSquelch_RejectsInvalidAndIncrementsBadData verifies the
// wiring between AddSquelch's out-of-range rejection and the bad-data
// counter: one rejected call must produce exactly one increment (not
// zero, not two). This is the canonical single-location-per-offense
// test; overlay's handleSquelchMessage must NOT also increment.
func TestPeer_AddSquelch_RejectsInvalidAndIncrementsBadData(t *testing.T) {
	peer := newTestPeer(t, PeerID(4))
	validator := []byte("V")

	require.Equal(t, uint32(0), peer.BadDataCount())

	tooShort := MinUnsquelchExpire - time.Second
	assert.False(t, peer.AddSquelch(validator, tooShort),
		"out-of-range duration must be rejected")

	// "squelch-duration" is charged at the feeInvalidData tier per
	// BadDataWeight, so one rejection adds weightInvalidData.
	assert.Equal(t, uint32(weightInvalidData), peer.BadDataCount(),
		"rejection must record exactly one feeInvalidData charge")
}

// TestOverlay_IncPeerBadData_Attributes verifies the overlay-level
// helper: it looks up the peer by ID and delegates to Peer.IncBadData.
// A second call increments again; an unknown PeerID no-ops and returns
// 0. This is the surface higher layers (consensus router) use — keeping
// the behavior explicit here prevents a refactor from silently
// swallowing increments.
func TestOverlay_IncPeerBadData_Attributes(t *testing.T) {
	o := &Overlay{
		peers: make(map[PeerID]*Peer),
	}
	peer := newTestPeer(t, PeerID(10))
	o.peers[peer.ID()] = peer

	// Each "unit" call adds weightDefaultBadData to the balance.
	assert.Equal(t, uint32(weightDefaultBadData), o.IncPeerBadData(peer.ID(), "unit"))
	assert.Equal(t, uint32(2*weightDefaultBadData), o.IncPeerBadData(peer.ID(), "unit"))
	assert.Equal(t, uint32(2*weightDefaultBadData), peer.BadDataCount())

	// Unknown peer: must no-op and return 0, not panic or insert.
	assert.Equal(t, uint32(0), o.IncPeerBadData(PeerID(999), "unknown"))
}

// TestOverlay_EvictBadDataPeers_RemovesOffendersOnly seeds two peers,
// charges one past the threshold, runs the maintenance tick, and
// asserts the offender was disconnected while the well-behaved peer
// remains. This exercises the eviction path that the periodic
// maintenanceLoop relies on, without needing a running overlay.
func TestOverlay_EvictBadDataPeers_RemovesOffendersOnly(t *testing.T) {
	o := &Overlay{
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	offender := newTestPeer(t, PeerID(100))
	good := newTestPeer(t, PeerID(200))
	o.peers[offender.ID()] = offender
	o.peers[good.ID()] = good

	// Charge the offender past the threshold via weighted IncBadData.
	// "unit" is unrecognized so it falls back to weightDefaultBadData
	// per BadDataWeight; we need ceil(threshold/weight) calls to cross.
	hitsToEvict := (EvictBadDataThreshold + weightDefaultBadData - 1) / weightDefaultBadData
	for i := 0; i < hitsToEvict; i++ {
		offender.IncBadData("unit")
	}
	require.GreaterOrEqual(t, offender.BadDataCount(), uint32(EvictBadDataThreshold),
		"offender must be at or past threshold after %d charges", hitsToEvict)
	require.Equal(t, uint32(0), good.BadDataCount())

	o.evictBadDataPeers()

	o.peersMu.RLock()
	_, offenderStillThere := o.peers[offender.ID()]
	_, goodStillThere := o.peers[good.ID()]
	o.peersMu.RUnlock()

	assert.False(t, offenderStillThere,
		"offender must be removed after eviction tick")
	assert.True(t, goodStillThere,
		"well-behaved peer must not be evicted")

	// Below-threshold peers don't get evicted even on subsequent ticks.
	// Stop one charge short of crossing.
	for i := 0; i < hitsToEvict-1; i++ {
		good.IncBadData("unit")
	}
	o.evictBadDataPeers()

	o.peersMu.RLock()
	_, goodAfter := o.peers[good.ID()]
	o.peersMu.RUnlock()
	assert.True(t, goodAfter,
		"peer below EvictBadDataThreshold must survive an eviction tick")
}

// TestOverlay_NoEviction_AfterSingleInvalidData guards against a
// regression to the historic 1000-threshold, which was 25× more
// aggressive than rippled and evicted honest peers after a single
// malformed message + a handful of decode misses. At the rippled-parity
// 25000 threshold a single weightInvalidData (400) charge — the
// heaviest bad-data weight — must NOT evict; a peer needs sustained
// abuse to cross.
func TestOverlay_NoEviction_AfterSingleInvalidData(t *testing.T) {
	o := &Overlay{
		peers:  make(map[PeerID]*Peer),
		events: make(chan Event, 8),
	}

	peer := newTestPeer(t, PeerID(300))
	o.peers[peer.ID()] = peer

	// One feeInvalidData-equivalent charge.
	peer.IncBadData("replay-delta-verify")
	require.Equal(t, uint32(weightInvalidData), peer.BadDataCount(),
		"single invalid-data charge should land at weightInvalidData")
	require.Less(t, uint32(weightInvalidData), uint32(EvictBadDataThreshold),
		"threshold must be well above a single invalid-data charge")

	o.evictBadDataPeers()

	o.peersMu.RLock()
	_, stillThere := o.peers[peer.ID()]
	o.peersMu.RUnlock()
	assert.True(t, stillThere,
		"peer charged one feeInvalidData must NOT be evicted (regression guard for threshold=1000)")
}
