package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPeer tests creating a new peer
func TestNewPeer(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	require.NotNil(t, peer)
	assert.Equal(t, PeerStateDisconnected, peer.State())
	assert.Equal(t, PeerID(1), peer.ID())
	assert.Equal(t, endpoint, peer.Endpoint())
	assert.False(t, peer.Inbound())
}

// TestPeerState_String tests the PeerState String method
func TestPeerState_String(t *testing.T) {
	tests := []struct {
		state    PeerState
		expected string
	}{
		{PeerStateDisconnected, "disconnected"},
		{PeerStateConnecting, "connecting"},
		{PeerStateConnected, "connected"},
		{PeerStateClosing, "closing"},
		{PeerState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.String())
		})
	}
}

// TestPeer_Close tests closing a peer
func TestPeer_Close(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	// Close should succeed even when disconnected
	err = peer.Close()
	assert.NoError(t, err)

	// Multiple closes should be safe (idempotent)
	err = peer.Close()
	assert.NoError(t, err)
}

// TestPeer_StateTransitions tests state transitions
func TestPeer_StateTransitions(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	// Initial state
	assert.Equal(t, PeerStateDisconnected, peer.State())

	// Simulate state transitions
	peer.setState(PeerStateConnecting)
	assert.Equal(t, PeerStateConnecting, peer.State())

	peer.setState(PeerStateConnected)
	assert.Equal(t, PeerStateConnected, peer.State())

	peer.setState(PeerStateClosing)
	assert.Equal(t, PeerStateClosing, peer.State())

	peer.setState(PeerStateDisconnected)
	assert.Equal(t, PeerStateDisconnected, peer.State())
}

// TestPeer_Accessors tests getter methods
func TestPeer_Accessors(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "test.example.com", Port: 51235}
	peer := NewPeer(PeerID(42), endpoint, true, id, events)

	assert.Equal(t, PeerID(42), peer.ID())
	assert.Equal(t, endpoint, peer.Endpoint())
	assert.True(t, peer.Inbound())
	assert.Nil(t, peer.RemotePublicKey())
	assert.Nil(t, peer.Capabilities())
}

// TestPeer_Info tests the Info method
func TestPeer_Info(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	info := peer.Info()

	assert.Equal(t, PeerID(1), info.ID)
	assert.Equal(t, endpoint, info.Endpoint)
	assert.False(t, info.Inbound)
	assert.Equal(t, PeerStateDisconnected, info.State)
	assert.Empty(t, info.PublicKey)
}

// TestPeer_Send tests sending data to closed peer
func TestPeer_Send(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	// Newly created peer can buffer sends even when disconnected
	err = peer.Send([]byte("test"))
	assert.NoError(t, err)

	// After closing, send should fail
	peer.Close()
	err = peer.Send([]byte("test2"))
	assert.ErrorIs(t, err, ErrConnectionClosed)
}

// TestPeer_SendBufferFull tests send buffer behavior
func TestPeer_SendBufferFull(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}
	peer := NewPeer(PeerID(1), endpoint, false, id, events)

	// Manually set to connected state and fill buffer
	peer.setState(PeerStateConnected)
	peer.closed.Store(false)

	// Fill the send buffer
	for i := 0; i < DefaultSendBufferSize; i++ {
		select {
		case peer.send <- []byte("data"):
		default:
			// Buffer full
			break
		}
	}

	// Next send should fail with buffer full
	err = peer.Send([]byte("overflow"))
	assert.ErrorIs(t, err, ErrConnectionClosed)
}

// TestPeer_InboundOutbound tests inbound/outbound distinction
func TestPeer_InboundOutbound(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	events := make(chan Event, 10)
	endpoint := Endpoint{Host: "192.168.1.1", Port: 51235}

	inboundPeer := NewPeer(PeerID(1), endpoint, true, id, events)
	outboundPeer := NewPeer(PeerID(2), endpoint, false, id, events)

	assert.True(t, inboundPeer.Inbound())
	assert.False(t, outboundPeer.Inbound())
}

// TestPeerConfig tests peer configuration
func TestPeerConfig(t *testing.T) {
	cfg := DefaultPeerConfig()

	assert.Equal(t, DefaultSendBufferSize, cfg.SendBufferSize)
	// PeerTLSConfig is intentionally nil in the default — callers must
	// supply CertPEM/KeyPEM derived from the local Identity.
	assert.Nil(t, cfg.PeerTLSConfig)
}

// TestPeerConstants tests that constants have expected values
func TestPeerConstants(t *testing.T) {
	assert.Equal(t, 10*time.Second, DefaultConnectTimeout)
	assert.Equal(t, 5*time.Second, DefaultHandshakeTimeout)
	assert.Equal(t, 64, DefaultSendBufferSize)
}

// TestEndpoint tests Endpoint type
func TestEndpoint(t *testing.T) {
	e := Endpoint{Host: "192.168.1.1", Port: 51235}

	assert.Equal(t, "192.168.1.1:51235", e.String())
}

// TestParseEndpoint tests endpoint parsing
func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		input    string
		expected Endpoint
		hasError bool
	}{
		{"192.168.1.1:51235", Endpoint{Host: "192.168.1.1", Port: 51235}, false},
		{"localhost:8080", Endpoint{Host: "localhost", Port: 8080}, false},
		{"[::1]:51235", Endpoint{Host: "::1", Port: 51235}, false},
		{"invalid", Endpoint{}, true},
		{"192.168.1.1", Endpoint{}, true},
		{"192.168.1.1:99999", Endpoint{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseEndpoint(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestPeerInfo tests PeerInfo structure
func TestPeerInfo(t *testing.T) {
	now := time.Now()
	info := PeerInfo{
		ID:          PeerID(1),
		Endpoint:    Endpoint{Host: "192.168.1.1", Port: 51235},
		Inbound:     true,
		State:       PeerStateConnected,
		PublicKey:   "n123...",
		ConnectedAt: now,
		MessagesIn:  100,
		MessagesOut: 50,
	}

	assert.Equal(t, PeerID(1), info.ID)
	assert.True(t, info.Inbound)
	assert.Equal(t, PeerStateConnected, info.State)
	assert.Equal(t, uint64(100), info.MessagesIn)
	assert.Equal(t, uint64(50), info.MessagesOut)
}
