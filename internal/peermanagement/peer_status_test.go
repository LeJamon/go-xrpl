package peermanagement

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeer_LastStatus_DefaultZero asserts a freshly constructed peer
// reports nsUNKNOWN — rippled's "no status reported" state.
func TestPeer_LastStatus_DefaultZero(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	assert.Equal(t, message.NodeStatus(0), p.LastStatus())
	assert.Equal(t, message.NodeStatus(0), p.Info().Status)
}

// TestPeer_ApplyStatusChange_StoresNewStatus checks that each rippled
// NodeStatus enum value is recorded verbatim by applyStatusChange and
// surfaced via LastStatus / PeerInfo.Status.
func TestPeer_ApplyStatusChange_StoresNewStatus(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	cases := []message.NodeStatus{
		message.NodeStatusConnecting,
		message.NodeStatusConnected,
		message.NodeStatusMonitoring,
		message.NodeStatusValidating,
		message.NodeStatusShutting,
	}
	for _, ns := range cases {
		p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
		p.applyStatusChange(nil, nil, false, nil, nil, ns)
		assert.Equal(t, ns, p.LastStatus())
		assert.Equal(t, ns, p.Info().Status)
	}
}

// TestPeer_ApplyStatusChange_StatusRetention covers rippled's
// last_status_ retention semantics (PeerImp.cpp:1799-1810):
//   - a non-zero NewStatus overwrites the prior value
//   - a TMStatusChange that omits new_status preserves the
//     previously-recorded enum (the "preserve old status" branch)
func TestPeer_ApplyStatusChange_StatusRetention(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	p.applyStatusChange(nil, nil, false, nil, nil, message.NodeStatusConnecting)
	require.Equal(t, message.NodeStatusConnecting, p.LastStatus())

	p.applyStatusChange(nil, nil, false, nil, nil, message.NodeStatusValidating)
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus())

	// rippled PeerImp.cpp:1801-1809: when the inbound TMStatusChange
	// has no newstatus, last_status_.newstatus() is preserved.
	p.applyStatusChange(nil, nil, false, nil, nil, 0)
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus(),
		"absent new_status must preserve prior value (rippled sticky retention)")
}

// TestPeer_ApplyStatusChange_StatusRecordedOnLostSync covers the
// rippled invariant that last_status_ is replaced by the entire
// inbound TMStatusChange — including a NewStatus carried alongside a
// neLOST_SYNC event.
func TestPeer_ApplyStatusChange_StatusRecordedOnLostSync(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	p.applyStatusChange(nil, nil, true, nil, nil, message.NodeStatusValidating)
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus())
}

// TestOverlay_handleStatusChange_PropagatesNewStatus verifies that the
// overlay decodes TMStatusChange.new_status and writes it to the peer.
func TestOverlay_handleStatusChange_PropagatesNewStatus(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	sc := &message.StatusChange{
		NewStatus:  message.NodeStatusMonitoring,
		LedgerSeq:  100,
		LedgerHash: make([]byte, 32),
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})
	assert.Equal(t, message.NodeStatusMonitoring, peer.LastStatus())
}

// TestOverlay_handleStatusChange_PublishesPeerStatus mirrors rippled's
// pubPeerStatus callback at PeerImp.cpp:1892-1963. The publisher must
// receive UPPERCASE status / action strings, the wire ledger fields,
// and a hex-encoded ledger_hash sourced from the peer's stored
// closedLedger (PeerImp.cpp:1941-1948 re-reads under recentLock_).
func TestOverlay_handleStatusChange_PublishesPeerStatus(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var got PeerStatusUpdate
	var fired int
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) {
		fired++
		got = u
	})

	closed := make([]byte, 32)
	for i := range closed {
		closed[i] = byte(0xAB)
	}
	first, last := uint32(100), uint32(200)
	sc := &message.StatusChange{
		NewStatus:   message.NodeStatusValidating,
		NewEvent:    message.NodeEventAcceptedLedger,
		LedgerSeq:   150,
		LedgerHash:  closed,
		NetworkTime: 700_000_000,
		FirstSeq:    &first,
		LastSeq:     &last,
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})

	require.Equal(t, 1, fired, "publisher must fire exactly once for non-lostSync")
	assert.Equal(t, "VALIDATING", got.Status, "rippled PeerImp.cpp:1908 — UPPERCASE")
	assert.Equal(t, "ACCEPTED_LEDGER", got.Action, "rippled PeerImp.cpp:1924")
	assert.Equal(t, uint32(150), got.LedgerIndex)
	assert.Equal(t, uint32(700_000_000), got.Date)
	assert.Equal(t, uint32(100), got.LedgerIndexMin)
	assert.Equal(t, uint32(200), got.LedgerIndexMax)
	assert.Equal(t,
		"ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB",
		got.LedgerHash,
		"PeerImp.cpp:1948 hex-encodes peer.closedLedgerHash_, not the wire bytes")
}

// TestOverlay_handleStatusChange_LostSyncSuppressesPublish covers
// PeerImp.cpp:1812-1830 — rippled's lostSync branch returns before
// pubPeerStatus is invoked, so subscribers never see a LOST_SYNC
// peer_status event.
func TestOverlay_handleStatusChange_LostSyncSuppressesPublish(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var fired int
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) { fired++ })

	sc := &message.StatusChange{
		NewEvent:  message.NodeEventLostSync,
		LedgerSeq: 0,
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})
	assert.Equal(t, 0, fired,
		"lostSync must not publish (PeerImp.cpp:1830 returns before pubPeerStatus)")
}

// TestOverlay_PeersJSON_StatusField mirrors PeerImp.cpp:463-491 — the
// `status` field is emitted with the rippled spelling for each known
// NodeStatus, and omitted when the peer has not reported one.
func TestOverlay_PeersJSON_StatusField(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	cases := []struct {
		name   string
		status message.NodeStatus
		want   string // empty => field must be absent
	}{
		{"omitted_when_unknown", 0, ""},
		{"connecting", message.NodeStatusConnecting, "connecting"},
		{"connected", message.NodeStatusConnected, "connected"},
		{"monitoring", message.NodeStatusMonitoring, "monitoring"},
		{"validating", message.NodeStatusValidating, "validating"},
		{"shutting", message.NodeStatusShutting, "shutting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPeer(PeerID(1), Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
			if tc.status != 0 {
				p.applyStatusChange(nil, nil, false, nil, nil, tc.status)
			}

			o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
			entries := o.PeersJSON()
			require.Len(t, entries, 1)

			got, present := entries[0]["status"]
			if tc.want == "" {
				assert.False(t, present, "expected `status` to be absent")
				return
			}
			require.True(t, present, "expected `status` field")
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestOverlay_PeersJSON_StatusOmittedForOutOfRangeEnum guards against
// surfacing future / unknown enum values as "unknown" or similar
// fall-through strings — rippled's switch only handles the five named
// statuses and silently drops anything else.
func TestOverlay_PeersJSON_StatusOmittedForOutOfRangeEnum(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
	p.applyStatusChange(nil, nil, false, nil, nil, message.NodeStatus(99))

	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
	entries := o.PeersJSON()
	require.Len(t, entries, 1)
	_, present := entries[0]["status"]
	assert.False(t, present, "unknown enum values must not surface as `status`")
}
