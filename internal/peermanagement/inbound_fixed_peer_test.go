package peermanagement

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteAddrPeerConn struct {
	*deadlinePeerConn
	remote *net.TCPAddr
}

func (c remoteAddrPeerConn) RemoteAddr() net.Addr { return c.remote }

// runAdmissionHandshake drives a fully signed inbound handshake from remote
// at endpoint through admission, returning the bytes written back and the
// handshake error.
func runAdmissionHandshake(t *testing.T, o *Overlay, id PeerID, endpoint Endpoint, remote *Identity) (*Peer, string, error) {
	t.Helper()
	sharedValue := make([]byte, 32)
	req, err := BuildHandshakeRequest(remote, sharedValue, DefaultHandshakeConfig())
	require.NoError(t, err)
	var wire bytes.Buffer
	require.NoError(t, WriteRawHandshakeRequest(&wire, req))

	peer := NewPeer(id, endpoint, true, o.identity, nil)
	conn := remoteAddrPeerConn{
		deadlinePeerConn: &deadlinePeerConn{data: bytes.NewReader(wire.Bytes())},
		remote:           &net.TCPAddr{IP: net.ParseIP(endpoint.Host), Port: int(endpoint.Port)},
	}
	err = o.performInboundHandshake(context.Background(), peer, conn)
	t.Cleanup(func() { o.releasePeerKey(peer) })
	return peer, conn.writes.String(), err
}

func TestInboundFixedPeerBypassesInboundLimit(t *testing.T) {
	requirePeerTLSSupported(t)
	clusterKey, err := NewIdentity()
	require.NoError(t, err)
	reservedKey, err := NewIdentity()
	require.NoError(t, err)

	o := newLifecycleTestOverlay(t,
		WithListenAddr(""),
		WithMaxInbound(0),
		WithFixedPeers("192.0.2.60:51235", "[2001:db8::60]:51235"),
		WithClusterNodes(clusterKey.EncodedPublicKey()+" cluster"),
	)
	_, err = o.PeerReservations().Insert(&PeerReservation{NodeID: reservedKey.EncodedPublicKey()})
	require.NoError(t, err)

	tests := []struct {
		name     string
		endpoint Endpoint
		remote   *Identity
		admitted bool
	}{
		{name: "fixed address from ephemeral port", endpoint: Endpoint{Host: "192.0.2.60", Port: 40001}, admitted: true},
		{name: "fixed address on configured port", endpoint: Endpoint{Host: "192.0.2.60", Port: 51235}, admitted: true},
		{name: "fixed IPv6 address", endpoint: Endpoint{Host: "2001:db8::60", Port: 40002}, admitted: true},
		{name: "IPv4-mapped fixed address", endpoint: Endpoint{Host: "::ffff:192.0.2.60", Port: 40003}, admitted: true},
		{name: "non-fixed address", endpoint: Endpoint{Host: "192.0.2.61", Port: 51235}},
		{name: "cluster member", endpoint: Endpoint{Host: "192.0.2.62", Port: 40004}, remote: clusterKey, admitted: true},
		{name: "reserved peer", endpoint: Endpoint{Host: "192.0.2.63", Port: 40005}, remote: reservedKey, admitted: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := tt.remote
			if remote == nil {
				remote, err = NewIdentity()
				require.NoError(t, err)
			}
			_, written, err := runAdmissionHandshake(t, o, PeerID(i+1), tt.endpoint, remote)
			if tt.admitted {
				require.NoError(t, err)
				assert.Contains(t, written, "HTTP/1.1 101 Switching Protocols")
				return
			}
			require.ErrorIs(t, err, errInboundRejected)
			assert.Contains(t, written, "HTTP/1.1 503 Service Unavailable")
		})
	}
}

func TestInboundSlotExemptPeersDoNotUseInboundSlots(t *testing.T) {
	o := newLifecycleTestOverlay(t, WithListenAddr(""), WithMaxInbound(1))
	inboundPeer := func(id PeerID, host string) *Peer {
		remote, err := NewIdentity()
		require.NoError(t, err)
		peer := NewPeer(id, Endpoint{Host: host, Port: 51235}, true, o.identity, o.events)
		peer.remotePubKey = NewPublicKeyTokenFromBytes(remote.PublicKey())
		return peer
	}

	exempt := inboundPeer(1, "192.0.2.70")
	require.NoError(t, o.reservePeerKey(exempt, true))
	require.NoError(t, o.addPeer(exempt))

	ordinary := inboundPeer(2, "192.0.2.71")
	require.NoError(t, o.reservePeerKey(ordinary, false),
		"an exempt inbound peer must not use up the only inbound slot")
	require.NoError(t, o.addPeer(ordinary))

	overflow := inboundPeer(3, "192.0.2.72")
	assert.ErrorIs(t, o.reservePeerKey(overflow, false), ErrMaxPeersReached)
	assert.NoError(t, o.reservePeerKey(inboundPeer(4, "192.0.2.73"), true))
}

func TestDiscoveryIsFixedHostIgnoresPort(t *testing.T) {
	d := NewDiscovery(&Config{
		FixedPeers: []string{
			"192.0.2.80:51235",
			"[2001:db8::80]:51235",
			"fixed.example:51235",
		},
	}, make(chan Event, 1))
	d.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "fixed.example" {
			return nil, errors.New("unexpected lookup")
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.81")}}, nil
	}
	require.NoError(t, d.Start(t.Context()))
	t.Cleanup(d.Stop)

	tests := []struct {
		host  string
		fixed bool
	}{
		{host: "192.0.2.80", fixed: true},
		{host: "::ffff:192.0.2.80", fixed: true},
		{host: "2001:db8::80", fixed: true},
		{host: "192.0.2.81", fixed: true},
		{host: "192.0.2.82"},
		{host: "fixed.example"},
		{host: ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.fixed, d.IsFixedHost(tt.host), tt.host)
	}
}
