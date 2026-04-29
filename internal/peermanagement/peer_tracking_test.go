package peermanagement

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTrackingPeer(t *testing.T) *Peer {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)
	return NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
}

func TestPeerTracking_DefaultUnknown(t *testing.T) {
	p := newTrackingPeer(t)
	assert.Equal(t, PeerTrackingUnknown, p.Tracking())
	assert.Equal(t, PeerTrackingUnknown, p.Info().Tracking)
}

// CheckTracking mirrors PeerImp::checkTracking (PeerImp.cpp:1986-2005).
func TestPeerTracking_CheckTracking(t *testing.T) {
	cases := []struct {
		name     string
		peerSeq  uint32
		validSeq uint32
		initial  PeerTracking
		want     PeerTracking
	}{
		{"converged_within_limit", 1000, 1000, PeerTrackingUnknown, PeerTrackingConverged},
		{"converged_at_boundary_minus_one", 1000, 1023, PeerTrackingUnknown, PeerTrackingConverged},
		{"diverged_above_limit", 1000, 1200, PeerTrackingUnknown, PeerTrackingDiverged},
		{"between_limits_no_change", 1000, 1050, PeerTrackingUnknown, PeerTrackingUnknown},
		{"converged_overrides_previous_unknown", 5000, 5005, PeerTrackingUnknown, PeerTrackingConverged},
		{"diverged_sticky_no_reflip_within_window", 1000, 1100, PeerTrackingDiverged, PeerTrackingDiverged},
		{"converged_can_overwrite_diverged", 1000, 1010, PeerTrackingDiverged, PeerTrackingConverged},
		{"zero_peer_seq_no_op", 0, 1000, PeerTrackingUnknown, PeerTrackingUnknown},
		{"zero_valid_seq_no_op", 1000, 0, PeerTrackingUnknown, PeerTrackingUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTrackingPeer(t)
			p.SetTracking(tc.initial)
			p.CheckTracking(tc.peerSeq, tc.validSeq)
			assert.Equal(t, tc.want, p.Tracking())
		})
	}
}

func TestOverlay_handleStatusChange_UpdatesTracking(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})
	o.SetValidLedgerProvider(func() (uint32, time.Duration, bool) {
		return 5000, 30 * time.Second, true
	})

	sc := &message.StatusChange{
		LedgerSeq:  4995,
		LedgerHash: make([]byte, 32),
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})
	assert.Equal(t, PeerTrackingConverged, peer.Tracking())

	sc2 := &message.StatusChange{
		LedgerSeq:  4500,
		LedgerHash: make([]byte, 32),
	}
	encoded2, err := message.Encode(sc2)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded2})
	assert.Equal(t, PeerTrackingDiverged, peer.Tracking())
}

func TestOverlay_handleStatusChange_StaleValidLedgerSkipsTracking(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: peer})
	// Validated ledger is older than rippled's 2-min freshness gate.
	o.SetValidLedgerProvider(func() (uint32, time.Duration, bool) {
		return 5000, 5 * time.Minute, true
	})

	sc := &message.StatusChange{
		LedgerSeq:  5000,
		LedgerHash: make([]byte, 32),
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 1, Payload: encoded})
	assert.Equal(t, PeerTrackingUnknown, peer.Tracking())
}

func TestOverlay_handleStatusChange_NoProviderLeavesUnknown(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: peer})

	sc := &message.StatusChange{
		LedgerSeq:  5000,
		LedgerHash: make([]byte, 32),
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 1, Payload: encoded})
	assert.Equal(t, PeerTrackingUnknown, peer.Tracking())
}

func TestOverlay_handleStatusChange_ZeroLedgerSeqSkipsTracking(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: peer})
	o.SetValidLedgerProvider(func() (uint32, time.Duration, bool) {
		return 5000, 30 * time.Second, true
	})

	// LedgerSeq=0 — peer reported no ledger info; tracking must not flip.
	sc := &message.StatusChange{
		LedgerSeq:  0,
		LedgerHash: make([]byte, 32),
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 1, Payload: encoded})
	assert.Equal(t, PeerTrackingUnknown, peer.Tracking())
}

// PeersJSON's track field mirrors PeerImp::json (PeerImp.cpp:437-450):
// "diverged" / "unknown" emit; "converged" omits.
func TestOverlay_PeersJSON_TrackField(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	mk := func(state PeerTracking) *Peer {
		p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
		p.SetTracking(state)
		return p
	}

	cases := []struct {
		name      string
		state     PeerTracking
		wantKey   bool
		wantValue string
	}{
		{"unknown_emits_unknown", PeerTrackingUnknown, true, "unknown"},
		{"diverged_emits_diverged", PeerTrackingDiverged, true, "diverged"},
		{"converged_omits_field", PeerTrackingConverged, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newTestOverlayWithPeers(map[PeerID]*Peer{1: mk(tc.state)})
			out := o.PeersJSON()
			require.Len(t, out, 1)
			v, hasKey := out[0]["track"]
			assert.Equal(t, tc.wantKey, hasKey)
			if tc.wantKey {
				assert.Equal(t, tc.wantValue, v)
			}
		})
	}
}
