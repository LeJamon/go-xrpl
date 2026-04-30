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

// TestPeer_ApplyStatusChange_StatusOverwritten verifies that a
// subsequent TMStatusChange replaces the prior status — rippled's
// last_status_ tracks only the latest message.
func TestPeer_ApplyStatusChange_StatusOverwritten(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	p.applyStatusChange(nil, nil, false, nil, nil, message.NodeStatusConnecting)
	require.Equal(t, message.NodeStatusConnecting, p.LastStatus())

	p.applyStatusChange(nil, nil, false, nil, nil, message.NodeStatusValidating)
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus())

	// A subsequent TMStatusChange that omits new_status (proto3 default)
	// must reset the recorded status — rippled re-evaluates
	// has_newstatus() against the latest message only.
	p.applyStatusChange(nil, nil, false, nil, nil, 0)
	assert.Equal(t, message.NodeStatus(0), p.LastStatus())
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
