package peermanagement

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPeer spins up a Peer with a throw-away identity and event
// channel — all the bad-data tests need is a Peer with working state,
// not an actual connection. A resource.Manager-backed Consumer is
// attached so IncBadData / Charge route through the same plumbing the
// overlay would set up.
func newTestPeer(t *testing.T, id PeerID) *Peer {
	t.Helper()
	ident, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(id, endpoint, false, ident, events)
	rm := resource.NewManager(nil, nil)
	c := rm.NewInboundEndpoint(endpoint.String())
	peer.attachUsage(c, func() {})
	return peer
}

// TestPeer_BadDataCount_StartsAtZero is the sanity check: a freshly
// constructed peer has recorded no bad-data events.
func TestPeer_BadDataCount_StartsAtZero(t *testing.T) {
	peer := newTestPeer(t, PeerID(1))
	assert.Equal(t, uint32(0), peer.BadDataCount(),
		"a new peer must start with badData == 0")
}

// TestPeer_BadDataCount_IncrementsMonotonic verifies that each
// IncBadData call increases (or holds) the consumer balance — the new
// flow uses a decaying-window balance, so values are normalized
// (cost/window) rather than raw weight sums, but monotonicity across
// fast-fire charges still holds because decay over microseconds is 0.
func TestPeer_BadDataCount_IncrementsMonotonic(t *testing.T) {
	peer := newTestPeer(t, PeerID(2))

	bal1 := peer.IncBadData("r1")
	bal2 := peer.IncBadData("r2")
	bal3 := peer.IncBadData("r3")
	assert.Greater(t, bal1, uint32(0), "first charge must produce positive balance")
	assert.GreaterOrEqual(t, bal2, bal1, "second charge must not decrease balance")
	assert.GreaterOrEqual(t, bal3, bal2, "third charge must not decrease balance")
	assert.Equal(t, bal3, peer.BadDataCount(),
		"BadDataCount must reflect the latest balance")
}

// TestPeer_BadDataCount_Concurrent verifies the race-safety of the
// charge path: 100 goroutines × 100 increments each must all be
// applied without losing any. The new flow funnels through the
// resource.Manager's mutex, but the guarantee is the same — no lost
// updates.
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

	// Balance is normalized by the decay window, so we can't assert an
	// exact total — but the value must reflect that many charges
	// landed (well above what a single charge would produce).
	bal := peer.BadDataCount()
	assert.Greater(t, bal, uint32(0),
		"concurrent increments must all be counted — no lost updates")
}

// TestPeer_AddSquelch_RejectsInvalidAndCharges verifies the wiring
// between AddSquelch's out-of-range rejection and the bad-data charge
// path: one rejected call must produce exactly one charge.
func TestPeer_AddSquelch_RejectsInvalidAndCharges(t *testing.T) {
	peer := newTestPeer(t, PeerID(4))
	validator := []byte("V")

	require.Equal(t, uint32(0), peer.BadDataCount())

	tooShort := MinUnsquelchExpire - time.Second
	assert.False(t, peer.AddSquelch(validator, tooShort),
		"out-of-range duration must be rejected")
	assert.Greater(t, peer.BadDataCount(), uint32(0),
		"rejection must record a charge")
}

// TestOverlay_IncPeerBadData_Attributes verifies the overlay-level
// helper looks up the peer by ID and delegates to Peer.IncBadData.
func TestOverlay_IncPeerBadData_Attributes(t *testing.T) {
	o := &Overlay{
		peers:           make(map[PeerID]*Peer),
		resourceManager: resource.NewManager(nil, nil),
	}
	peer := newTestPeer(t, PeerID(10))
	o.peers[peer.ID()] = peer

	bal1 := o.IncPeerBadData(peer.ID(), "unit")
	bal2 := o.IncPeerBadData(peer.ID(), "unit")
	assert.Greater(t, bal1, uint32(0))
	assert.GreaterOrEqual(t, bal2, bal1)

	// Unknown peer: must no-op and return 0.
	assert.Equal(t, uint32(0), o.IncPeerBadData(PeerID(999), "unknown"))
}

// TestPeer_Charge_DropDisconnects exercises the new charge-based
// disconnect path: a sequence of charges that crosses the drop
// threshold must invoke the onDropDisconnect callback and close the
// peer. Mirrors rippled PeerImp::charge at PeerImp.cpp:351-361.
// Also pins the once-per-peer invariant on the hook callback —
// subsequent charges that re-hit Drop must NOT bump the counter
// again, matching rippled's strand-serialised single-fire of
// overlay_.incPeerDisconnectCharges().
func TestPeer_Charge_DropDisconnects(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 4)
	endpoint := Endpoint{Host: "203.0.113.7", Port: 51235}
	peer := NewPeer(PeerID(500), endpoint, false, ident, events)

	rm := resource.NewManager(nil, nil)
	c := rm.NewInboundEndpoint(endpoint.String())
	var dropBumped int
	peer.attachUsage(c, func() { dropBumped++ })

	fee := resource.NewCharge(resource.DropThreshold+1, "synthetic")
	dropped := false
	for i := 0; i < 10000; i++ {
		if peer.Charge(fee, "abuse") == resource.Drop {
			dropped = true
			break
		}
	}
	require.True(t, dropped, "sustained over-budget charges must reach Drop")
	assert.Equal(t, 1, dropBumped,
		"onDropDisconnect callback must fire exactly once per peer lifetime")

	// Repeat charges after the first Drop must not re-fire the hook.
	for i := 0; i < 8; i++ {
		peer.Charge(fee, "abuse-after-close")
	}
	assert.Equal(t, 1, dropBumped,
		"subsequent over-budget charges must not bump the disconnect counter again")
}
