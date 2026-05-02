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
		p.applyStatusChange(&message.StatusChange{NewStatus: ns})
		assert.Equal(t, ns, p.LastStatus())
		assert.Equal(t, ns, p.Info().Status)
	}
}

// TestPeer_ApplyStatusChange_StatusRetention covers rippled's
// last_status_ retention semantics (PeerImp.cpp:1799-1810). Both
// branches end with `last_status_ = *m;`, so a TMStatusChange that
// omits new_status DROPS the stored value — the "preserve old status"
// comment refers only to the inherited value rippled mutates onto the
// local message `m` (consumed by pubPeerStatus, see
// TestPeer_ApplyStatusChange_StatusInheritedToPublished).
func TestPeer_ApplyStatusChange_StatusRetention(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	p.applyStatusChange(&message.StatusChange{NewStatus: message.NodeStatusConnecting})
	require.Equal(t, message.NodeStatusConnecting, p.LastStatus())

	p.applyStatusChange(&message.StatusChange{NewStatus: message.NodeStatusValidating})
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus())

	// PeerImp.cpp:1802 / 1807: `last_status_ = *m;` runs verbatim in
	// both branches. m has no newstatus → stored last_status_.newstatus()
	// becomes false, so subsequent `peers` RPC reads drop the field.
	p.applyStatusChange(&message.StatusChange{})
	assert.Equal(t, message.NodeStatus(0), p.LastStatus(),
		"absent new_status must drop the prior stored value (rippled `last_status_ = *m;`)")
}

// TestPeer_ApplyStatusChange_StatusInheritedToPublished covers
// rippled's PeerImp.cpp:1804-1808 — when the inbound message has no
// new_status, the local `m` is mutated to carry the prior enum so the
// pubPeerStatus callback (which reads `m`, not last_status_) emits the
// inherited value once. applyStatusChange's return value is the
// post-inheritance status the publisher must use; it differs from
// LastStatus() in exactly this case.
func TestPeer_ApplyStatusChange_StatusInheritedToPublished(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)

	got := p.applyStatusChange(&message.StatusChange{NewStatus: message.NodeStatusValidating})
	assert.Equal(t, message.NodeStatusValidating, got, "wire-set status returned verbatim")
	assert.Equal(t, message.NodeStatusValidating, p.LastStatus())

	// status-less follow-up: published value inherits the prior enum,
	// stored value is dropped.
	got = p.applyStatusChange(&message.StatusChange{})
	assert.Equal(t, message.NodeStatusValidating, got,
		"absent new_status must inherit prior for pubPeerStatus (PeerImp.cpp:1808)")
	assert.Equal(t, message.NodeStatus(0), p.LastStatus(),
		"but stored last_status_ is dropped (PeerImp.cpp:1807)")

	// second status-less message: nothing left to inherit.
	got = p.applyStatusChange(&message.StatusChange{})
	assert.Equal(t, message.NodeStatus(0), got,
		"after the prior is dropped, subsequent status-less messages publish no status")
}

// TestPeer_ApplyStatusChange_StatusRecordedOnLostSync covers the
// rippled invariant that last_status_ is replaced by the entire
// inbound TMStatusChange — including a NewStatus carried alongside a
// neLOST_SYNC event.
func TestPeer_ApplyStatusChange_StatusRecordedOnLostSync(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	p.applyStatusChange(&message.StatusChange{NewStatus: message.NodeStatusValidating, NewEvent: message.NodeEventLostSync})
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
	require.NotNil(t, got.LedgerIndex)
	assert.Equal(t, uint32(150), *got.LedgerIndex)
	require.NotNil(t, got.Date)
	assert.Equal(t, uint32(700_000_000), *got.Date)
	require.NotNil(t, got.LedgerIndexMin)
	assert.Equal(t, uint32(100), *got.LedgerIndexMin)
	require.NotNil(t, got.LedgerIndexMax)
	assert.Equal(t, uint32(200), *got.LedgerIndexMax)
	assert.Equal(t,
		"ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB",
		got.LedgerHash,
		"PeerImp.cpp:1948 hex-encodes peer.closedLedgerHash_, not the wire bytes")
}

// TestOverlay_handleStatusChange_PublishedStatusInheritsPrior covers
// rippled's PeerImp.cpp:1804-1808 carry-over: a status-less follow-up
// message must publish the prior status to subscribers (rippled's
// `m->set_newstatus(status)` mutation on the local message), even
// though `last_status_` itself has been overwritten with the new
// (status-less) wire value.
func TestOverlay_handleStatusChange_PublishedStatusInheritsPrior(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var got []PeerStatusUpdate
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) { got = append(got, u) })

	encode := func(sc *message.StatusChange) []byte {
		b, err := message.Encode(sc)
		require.NoError(t, err)
		return b
	}

	// Seed the peer with a known status.
	o.handleStatusChange(Event{PeerID: 7, Payload: encode(&message.StatusChange{
		NewStatus:   message.NodeStatusValidating,
		NewEvent:    message.NodeEventAcceptedLedger,
		LedgerSeq:   100,
		NetworkTime: 1,
	})})
	require.Len(t, got, 1)
	assert.Equal(t, "VALIDATING", got[0].Status)

	// Status-less follow-up. Published Status must inherit VALIDATING.
	o.handleStatusChange(Event{PeerID: 7, Payload: encode(&message.StatusChange{
		NewEvent:    message.NodeEventClosingLedger,
		LedgerSeq:   101,
		NetworkTime: 2,
	})})
	require.Len(t, got, 2)
	assert.Equal(t, "VALIDATING", got[1].Status,
		"PeerImp.cpp:1808 — pubPeerStatus reads the inherited m->newstatus()")
	assert.Equal(t, message.NodeStatus(0), peer.LastStatus(),
		"but stored last_status_ has been overwritten by the wire (PeerImp.cpp:1807)")

	// Second status-less message: nothing to inherit anymore.
	o.handleStatusChange(Event{PeerID: 7, Payload: encode(&message.StatusChange{
		NewEvent:    message.NodeEventClosingLedger,
		LedgerSeq:   102,
		NetworkTime: 3,
	})})
	require.Len(t, got, 3)
	assert.Equal(t, "", got[2].Status,
		"after the prior is dropped, subsequent publishes carry no status")
}

// TestOverlay_handleStatusChange_LedgerHashZerosOnMalformedWire covers
// PeerImp.cpp:1842-1851 + 1941-1948. When wire bytes for ledger_hash
// are present but not 32 bytes, applyStatusChange clears the peer's
// stored closedLedgerHash_; rippled still emits the field but with the
// 64-character zero hex string.
func TestOverlay_handleStatusChange_LedgerHashZerosOnMalformedWire(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var got PeerStatusUpdate
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) { got = u })

	sc := &message.StatusChange{
		NewStatus:   message.NodeStatusConnected,
		NewEvent:    message.NodeEventClosingLedger,
		LedgerSeq:   1,
		LedgerHash:  []byte{0x01, 0x02}, // 2 bytes ≠ 32 → malformed
		NetworkTime: 1,
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})

	assert.Equal(t,
		"0000000000000000000000000000000000000000000000000000000000000000",
		got.LedgerHash,
		"PeerImp.cpp:1948 emits hex of the cleared closedLedgerHash_, not the wire bytes")
}

// TestOverlay_handleStatusChange_AutoFillsDate covers
// PeerImp.cpp:1796-1797 — rippled stamps networktime with the local
// clock when the wire didn't carry it. The published Date must be
// non-nil even when the peer omitted network_time.
func TestOverlay_handleStatusChange_AutoFillsDate(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var got PeerStatusUpdate
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) { got = u })

	sc := &message.StatusChange{
		NewEvent:  message.NodeEventClosingLedger,
		LedgerSeq: 1,
		// NetworkTime omitted on purpose.
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})

	require.NotNil(t, got.Date, "PeerImp.cpp:1796-1797 — networktime auto-filled")
	assert.Greater(t, *got.Date, uint32(0))
}

// TestOverlay_SetPeerStatusPublisher_Disconnect verifies the doc
// comment: passing nil to SetPeerStatusPublisher silences the sink so
// subsequent handleStatusChange events emit no callbacks.
func TestOverlay_SetPeerStatusPublisher_Disconnect(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	o := newTestOverlayWithPeers(map[PeerID]*Peer{7: peer})

	var fired int
	o.SetPeerStatusPublisher(func(u PeerStatusUpdate) { fired++ })
	o.SetPeerStatusPublisher(nil)

	sc := &message.StatusChange{
		NewStatus: message.NodeStatusConnecting,
		NewEvent:  message.NodeEventClosingLedger,
		LedgerSeq: 1,
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)

	o.handleStatusChange(Event{PeerID: 7, Payload: encoded})
	assert.Equal(t, 0, fired, "SetPeerStatusPublisher(nil) must disconnect the sink")
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
				p.applyStatusChange(&message.StatusChange{NewStatus: tc.status})
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
	p.applyStatusChange(&message.StatusChange{NewStatus: message.NodeStatus(99)})

	o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
	entries := o.PeersJSON()
	require.Len(t, entries, 1)
	_, present := entries[0]["status"]
	assert.False(t, present, "unknown enum values must not surface as `status`")
}
