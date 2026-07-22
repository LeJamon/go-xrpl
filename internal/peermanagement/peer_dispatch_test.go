package peermanagement

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ordinary dispatch must never block; only acquisition traffic intentionally
// propagates bounded backpressure to the peer read loop.
func TestPeer_DispatchEvent_NonBlocking(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	var dropped atomic.Uint64
	peer.SetDroppedEventsCounter(&dropped)

	peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1})
	assert.Equal(t, uint64(0), dropped.Load())

	for i := range 5 {
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

func TestPeer_DispatchEvent_BackpressuresAcquisitionSeparately(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	events <- Event{Type: EventMessageReceived, MessageType: uint16(message.TypeValidation)}
	acquisition := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	peer.SetAcquisitionEvents(acquisition)

	first := Event{Type: EventMessageReceived, MessageType: uint16(message.TypeLedgerData)}
	require.True(t, peer.dispatchEvent(first))
	done := make(chan bool, 1)
	go func() { done <- peer.dispatchEvent(first) }()
	select {
	case <-done:
		t.Fatal("full acquisition lane did not apply backpressure")
	case <-time.After(20 * time.Millisecond):
	}
	<-acquisition
	select {
	case delivered := <-done:
		require.True(t, delivered)
	case <-time.After(time.Second):
		t.Fatal("acquisition dispatch did not resume")
	}
}

// Overlay-side dispatchLifecycle delivers lifecycle events on a dedicated
// channel with blocking sends (finding 4) so a message burst can never
// drop a disconnect. A buffered slot accepts a send without a consumer;
// closing stopCh releases a send blocked on a full channel during
// shutdown so a run-watcher goroutine can't wedge.
func TestOverlay_DispatchLifecycle(t *testing.T) {
	o := &Overlay{
		lifecycle: make(chan Event, 1),
		stopCh:    make(chan struct{}),
	}

	// The buffered slot accepts the first send without a consumer.
	o.dispatchLifecycle(Event{Type: EventPeerConnected, PeerID: 1})
	got := <-o.lifecycle
	assert.Equal(t, EventPeerConnected, got.Type)

	// Fill the buffer, then a further send blocks until stopCh is closed.
	o.dispatchLifecycle(Event{Type: EventPeerConnected, PeerID: 1})
	close(o.stopCh)
	done := make(chan struct{})
	go func() {
		o.dispatchLifecycle(Event{Type: EventPeerDisconnected, PeerID: 2})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatchLifecycle did not release on stopCh close during shutdown")
	}
}
