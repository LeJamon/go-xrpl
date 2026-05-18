package peermanagement

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeer_DispatchEvent_NonBlocking confirms the helper used by both
// readLoop and Close never blocks when the events channel is full and
// instead bumps the dropped counter. This is the contract that
// prevents the read hot path from deadlocking against an event loop
// that is waiting on peersMu.
func TestPeer_DispatchEvent_NonBlocking(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	var dropped atomic.Uint64
	peer.SetDroppedEventsCounter(&dropped)

	peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
	assert.Equal(t, uint64(0), dropped.Load())

	for i := 0; i < 5; i++ {
		done := make(chan struct{})
		go func() {
			peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("dispatchEvent blocked on full channel (iter %d)", i)
		}
	}
	assert.Equal(t, uint64(5), dropped.Load())
}

// TestPeer_DispatchEvent_NilCounter ensures a peer with no wired
// counter (e.g., constructed in tests outside Overlay) still does not
// block on a full channel — only the counter side-effect is suppressed.
func TestPeer_DispatchEvent_NilCounter(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})

	done := make(chan struct{})
	go func() {
		peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatchEvent blocked with nil counter on full channel")
	}
}

// TestPeer_DispatchEvent_NilChannel ensures a peer constructed with no
// events channel at all silently discards events instead of panicking.
func TestPeer_DispatchEvent_NilChannel(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
}

// TestOverlay_DispatchEvent_NonBlocking confirms the overlay-side
// helper used by addPeer/removePeer/Connect/handleInbound is
// non-blocking and surfaces drops via DroppedEvents.
func TestOverlay_DispatchEvent_NonBlocking(t *testing.T) {
	o := &Overlay{
		events: make(chan Event, 1),
	}

	o.dispatchEvent(Event{Type: EventPeerConnected, PeerID: 1})
	assert.Equal(t, uint64(0), o.DroppedEvents())

	for i := 0; i < 3; i++ {
		done := make(chan struct{})
		go func() {
			o.dispatchEvent(Event{Type: EventPeerConnected, PeerID: 2})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("Overlay.dispatchEvent blocked on full channel (iter %d)", i)
		}
	}
	assert.Equal(t, uint64(3), o.DroppedEvents())
}
