package peermanagement

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchEvent must never block — read hot path and Close path
// would otherwise deadlock against an event loop holding peersMu.
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

// Nil counter must still not block on a full channel.
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

// Nil events channel must silently discard rather than panic.
func TestPeer_DispatchEvent_NilChannel(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
}

// Overlay-side dispatchEvent is non-blocking and surfaces drops via
// DroppedEvents.
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
