package peermanagement

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bootstrapConnection struct {
	overlay *Overlay
	peerID  PeerID
}

func startBootstrapTestOverlay(t *testing.T, opts ...Option) *Overlay {
	t.Helper()
	base := []Option{
		WithListenAddr("127.0.0.1:0"),
		WithDataDir(t.TempDir()),
		WithMaxPeers(4),
		WithMaxInbound(2),
		WithMaxOutbound(0),
		WithPrivateMode(true),
		WithCompression(false),
	}
	overlay, err := New(append(base, opts...)...)
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() {
		runDone <- overlay.Run(context.Background())
	}()
	select {
	case <-overlay.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before its listener became ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}
	t.Cleanup(func() {
		require.NoError(t, overlay.Stop())
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("overlay did not stop")
		}
	})
	return overlay
}

func TestBootstrapGovernorSerializesColdConnections(t *testing.T) {
	var governor bootstrapGovernor
	first, ok := governor.tryReserve()
	require.True(t, ok)
	require.NotNil(t, first)

	_, ok = governor.tryReserve()
	assert.False(t, ok)

	first.release()
	second, ok := governor.tryReserve()
	require.True(t, ok)
	require.NotNil(t, second)
	second.markReady()

	assert.True(t, governor.isReady())
	lease, ok := governor.tryReserve()
	assert.True(t, ok)
	assert.Nil(t, lease)
}

func TestDiscoveryBootstrapSelectionLimitsFixedPeers(t *testing.T) {
	now := time.Unix(1_000, 0)
	d := NewDiscovery(&Config{
		MaxOutbound: 3,
		FixedPeers: []string{
			"192.0.2.1:51235",
			"192.0.2.2:51235",
		},
		Clock: func() time.Time { return now },
	}, nil)
	for _, address := range []string{
		"192.0.2.1:51235",
		"192.0.2.2:51235",
		"192.0.2.3:51235",
	} {
		d.AddPeer(address, 0, 0)
	}

	candidates := d.selectPeersToConnect(1, true)
	require.Len(t, candidates, 1)
	assert.True(t, d.IsFixed(candidates[0]))
}

func TestDiscoveryBootstrapSelectionPreservesFixedPeersAtZeroOrdinaryCapacity(t *testing.T) {
	d := NewDiscovery(&Config{
		MaxOutbound: 0,
		FixedPeers: []string{
			"192.0.2.1:51235",
			"192.0.2.2:51235",
		},
	}, nil)
	d.AddPeer("192.0.2.1:51235", 0, 0)
	d.AddPeer("192.0.2.2:51235", 0, 0)

	candidates := d.selectPeersToConnect(0, true)
	require.Len(t, candidates, 1)
	assert.True(t, d.IsFixed(candidates[0]))
}

func TestDiscoveryBootstrapSelectionPrefersKnownCompression(t *testing.T) {
	now := time.Unix(2_000, 0)
	d := NewDiscovery(&Config{
		MaxOutbound: 3,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{
		"192.0.2.1:51235",
		"192.0.2.2:51235",
		"192.0.2.3:51235",
	} {
		d.AddPeer(address, 0, 0)
	}
	d.markNegotiatedCompression("192.0.2.1:51235", false)
	d.markNegotiatedCompression("192.0.2.2:51235", true)

	candidates := d.selectPeersToConnect(1, true)
	require.Equal(t, []string{"192.0.2.2:51235"}, candidates)
}

func TestDiscoveryBootstrapSelectionPrefersCompressionOverFixedPeer(t *testing.T) {
	now := time.Unix(2_500, 0)
	fixed := "192.0.2.1:51235"
	compressed := "192.0.2.2:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 2,
		FixedPeers:  []string{fixed},
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(fixed, 0, 0)
	d.AddPeer(compressed, 0, 0)
	d.markNegotiatedCompression(fixed, false)
	d.markNegotiatedCompression(compressed, true)

	assert.Equal(t, []string{compressed}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapRetryDelayStartsAtDisconnect(t *testing.T) {
	now := time.Unix(3_000, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)
	d.MarkConnected(address, 1)
	d.delayBootstrapRetry(address)
	d.MarkDisconnected(1)

	assert.Empty(t, d.selectPeersToConnect(1, true))
	now = now.Add(recentConnectAttempt)
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
}

func TestPeerBootstrapAcknowledgementFiresOnce(t *testing.T) {
	called := 0
	peer := &Peer{onBootstrapReady: func() { called++ }}

	peer.acknowledgeBootstrap()
	peer.acknowledgeBootstrap()
	assert.Equal(t, 1, called)
}

func TestOverlayMalformedPingDoesNotAcknowledgeBootstrap(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	overlay := &Overlay{peers: map[PeerID]*Peer{
		1: {id: 1, onBootstrapReady: lease.markReady},
	}}

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: uint16(message.TypePing),
		Payload:     []byte("not-a-ping"),
	})

	assert.False(t, governor.isReady())
}

func TestOverlayPingDoesNotBypassSeenManifest(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	peer := &Peer{id: 1, onBootstrapReady: lease.markReady}
	peer.bootstrapManifest.Store(true)
	overlay := &Overlay{peers: map[PeerID]*Peer{1: peer}}
	payload, err := message.Encode(&message.Ping{PType: message.PingTypePong, Seq: 1})
	require.NoError(t, err)

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: uint16(message.TypePing),
		Payload:     payload,
	})

	assert.False(t, governor.isReady())
}

func TestOverlayDroppedManifestDoesNotAcknowledgeBootstrap(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	peer := &Peer{id: 1, onBootstrapReady: lease.markReady, closeCh: make(chan struct{})}
	peer.bootstrapManifest.Store(true)
	messages := make(chan *InboundMessage, 1)
	messages <- &InboundMessage{}
	overlay := &Overlay{
		peers:    map[PeerID]*Peer{1: peer},
		messages: messages,
	}
	payload, err := message.Encode(&message.Manifests{
		List: []message.Manifest{{STObject: []byte{1}}},
	})
	require.NoError(t, err)

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: uint16(message.TypeManifests),
		Payload:     payload,
	})

	assert.False(t, governor.isReady())
	select {
	case <-peer.closeCh:
	default:
		t.Fatal("dropped bootstrap manifests did not close the peer")
	}
}

func TestOverlayColdBootstrapAdmitsOnePeerUntilStartupFrameCompletes(t *testing.T) {
	connections := make(chan bootstrapConnection, 2)
	first := startBootstrapTestOverlay(t)
	second := startBootstrapTestOverlay(t, WithListenAddr("[::1]:0"))
	first.SetPeerConnectCallback(func(peerID PeerID) {
		connections <- bootstrapConnection{overlay: first, peerID: peerID}
	})
	second.SetPeerConnectCallback(func(peerID PeerID) {
		connections <- bootstrapConnection{overlay: second, peerID: peerID}
	})

	client := startBootstrapTestOverlay(t,
		WithMaxOutbound(2),
		WithFixedPeers(first.ListenAddr(), second.ListenAddr()),
	)

	var connected bootstrapConnection
	select {
	case connected = <-connections:
	case <-time.After(10 * time.Second):
		t.Fatal("first bootstrap peer did not connect")
	}

	client.autoconnect(context.Background())
	select {
	case <-connections:
		t.Fatal("second bootstrap peer connected before the first startup frame completed")
	case <-time.After(250 * time.Millisecond):
	}

	payload, err := message.Encode(&message.Manifests{
		List: []message.Manifest{{STObject: []byte{1}}},
	})
	require.NoError(t, err)
	var frame bytes.Buffer
	require.NoError(t, message.WriteMessage(&frame, message.TypeManifests, payload))
	require.NoError(t, connected.overlay.Send(connected.peerID, frame.Bytes()))
	select {
	case inbound := <-client.Messages():
		_, err := message.Decode(message.TypeManifests, inbound.Payload)
		require.NoError(t, err)
		client.AcknowledgePeerBootstrap(inbound.PeerID)
	case <-time.After(5 * time.Second):
		t.Fatal("startup manifests did not reach the protocol consumer")
	}
	require.Eventually(t, client.bootstrap.isReady, 5*time.Second, 10*time.Millisecond)

	client.autoconnect(context.Background())
	select {
	case <-connections:
	case <-time.After(10 * time.Second):
		t.Fatal("second bootstrap peer did not connect after the startup frame completed")
	}
}
