package peermanagement

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	events <- Event{Type: EventMessageReceived, MessageType: message.TypeValidation}
	acquisition := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	peer.SetAcquisitionEvents(acquisition)

	first := Event{Type: EventMessageReceived, MessageType: message.TypeLedgerData}
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

func TestPeer_DispatchEvent_BackpressuresConsensusSeparately(t *testing.T) {
	for _, msgType := range []message.MessageType{
		message.TypeProposeLedger,
		message.TypeValidation,
		message.TypeValidatorList,
		message.TypeValidatorListCollection,
	} {
		t.Run(msgType.String(), func(t *testing.T) {
			id, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			consensus := make(chan Event, 1)
			peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
			peer.SetConsensusEvents(consensus)

			evt := Event{Type: EventMessageReceived, MessageType: msgType}
			require.True(t, peer.dispatchEvent(evt))
			done := make(chan bool, 1)
			go func() { done <- peer.dispatchEvent(evt) }()
			select {
			case <-done:
				t.Fatal("full consensus lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}

			<-consensus
			select {
			case delivered := <-done:
				require.True(t, delivered)
			case <-time.After(time.Second):
				t.Fatal("consensus dispatch did not resume")
			}
			assert.Empty(t, events)
		})
	}
}

func TestPeer_DispatchEvent_BackpressuresConsensusControlSeparately(t *testing.T) {
	for _, msgType := range []message.MessageType{
		message.TypeStatusChange,
		message.TypeHaveSet,
	} {
		t.Run(msgType.String(), func(t *testing.T) {
			id, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			control := make(chan Event, 1)
			peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
			peer.SetConsensusControlEvents(control)

			evt := Event{Type: EventMessageReceived, MessageType: msgType}
			require.True(t, peer.dispatchEvent(evt))
			done := make(chan bool, 1)
			go func() { done <- peer.dispatchEvent(evt) }()
			select {
			case <-done:
				t.Fatal("full consensus control lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}

			<-control
			select {
			case delivered := <-done:
				require.True(t, delivered)
			case <-time.After(time.Second):
				t.Fatal("consensus control dispatch did not resume")
			}
			assert.Empty(t, events)
		})
	}
}

func TestPeer_DispatchEvent_ConsensusBackpressureReleasesOnClose(t *testing.T) {
	tests := []struct {
		name    string
		msgType message.MessageType
		wire    func(*Peer, chan<- Event)
	}{
		{"priority", message.TypeValidation, (*Peer).SetConsensusEvents},
		{"control", message.TypeStatusChange, (*Peer).SetConsensusControlEvents},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := NewIdentity()
			require.NoError(t, err)
			lane := make(chan Event, 1)
			peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, make(chan Event, 1))
			tc.wire(peer, lane)
			evt := Event{Type: EventMessageReceived, MessageType: tc.msgType}
			require.True(t, peer.dispatchEvent(evt))

			done := make(chan bool, 1)
			go func() { done <- peer.dispatchEvent(evt) }()
			select {
			case <-done:
				t.Fatal("full consensus lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}
			require.NoError(t, peer.Close())
			select {
			case delivered := <-done:
				assert.False(t, delivered)
			case <-time.After(time.Second):
				t.Fatal("consensus dispatch did not release when the peer closed")
			}
			assert.Len(t, lane, 1)
		})
	}
}

func TestConsensusMessageClassification(t *testing.T) {
	for _, msgType := range []message.MessageType{
		message.TypeProposeLedger,
		message.TypeValidation,
		message.TypeValidatorList,
		message.TypeValidatorListCollection,
	} {
		assert.True(t, isConsensusPriorityMessageType(msgType), msgType.String())
		assert.False(t, isConsensusControlMessageType(msgType), msgType.String())
	}
	for _, msgType := range []message.MessageType{
		message.TypeStatusChange,
		message.TypeHaveSet,
	} {
		assert.False(t, isConsensusPriorityMessageType(msgType), msgType.String())
		assert.True(t, isConsensusControlMessageType(msgType), msgType.String())
	}
	for _, msgType := range []message.MessageType{
		message.TypeGetLedger,
		message.TypeTransaction,
		message.TypeLedgerData,
	} {
		assert.False(t, isConsensusPriorityMessageType(msgType), msgType.String())
		assert.False(t, isConsensusControlMessageType(msgType), msgType.String())
	}
}

func TestConsensusPriorityLanePreservesValidatorListValidationOrder(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	o := &Overlay{
		consensusEvents:   make(chan Event, 2),
		consensusMessages: make(chan *InboundMessage, 2),
		stopCh:            make(chan struct{}),
	}
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, make(chan Event, 1))
	peer.SetConsensusEvents(o.consensusEvents)

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- o.consensusEventLoop(ctx) }()

	require.True(t, peer.dispatchEvent(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: message.TypeValidatorList,
		Payload:     []byte{0x01},
	}))
	require.True(t, peer.dispatchEvent(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: message.TypeValidation,
		Payload:     []byte{0x02},
	}))

	first := <-o.consensusMessages
	second := <-o.consensusMessages
	assert.Equal(t, message.TypeValidatorList, first.Type)
	assert.Equal(t, []byte{0x01}, first.Payload)
	assert.Equal(t, message.TypeValidation, second.Type)
	assert.Equal(t, []byte{0x02}, second.Payload)

	cancel()
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("consensus event loop did not stop")
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
