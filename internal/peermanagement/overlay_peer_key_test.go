package peermanagement

import (
	"net"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPeerWithRemoteKey(t *testing.T, o *Overlay, id PeerID, endpoint Endpoint, key *Identity) *Peer {
	t.Helper()
	peer := NewPeer(id, endpoint, false, o.identity, o.events)
	peer.remotePubKey = NewPublicKeyTokenFromBtcec(key.BtcecPublicKey())
	peer.setState(PeerStateConnected)
	return peer
}

func TestOverlayRejectsDuplicateRemotePublicKey(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	remote, err := GenerateIdentity()
	require.NoError(t, err)

	first := newPeerWithRemoteKey(t, o, 1, Endpoint{Host: "192.0.2.30", Port: 51235}, remote)
	duplicate := newPeerWithRemoteKey(t, o, 2, Endpoint{Host: "198.51.100.30", Port: 51235}, remote)
	require.NoError(t, o.addPeer(first))
	assert.ErrorIs(t, o.addPeer(duplicate), ErrAlreadyConnected)
	assert.Len(t, o.Peers(), 1)

	o.removePeer(first.ID())
	require.NoError(t, o.addPeer(duplicate))
	o.removePeer(duplicate.ID())
}

func TestOverlayRemotePublicKeyReservationIsAtomic(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	remote, err := GenerateIdentity()
	require.NoError(t, err)
	peers := []*Peer{
		newPeerWithRemoteKey(t, o, 1, Endpoint{Host: "192.0.2.31", Port: 51235}, remote),
		newPeerWithRemoteKey(t, o, 2, Endpoint{Host: "198.51.100.31", Port: 51235}, remote),
	}
	results := make([]error, len(peers))
	var wg sync.WaitGroup
	for i, peer := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = o.reservePeerKey(peer, false)
		}()
	}
	wg.Wait()

	winner := -1
	for i, err := range results {
		if err == nil {
			require.Equal(t, -1, winner)
			winner = i
		} else {
			assert.ErrorIs(t, err, ErrAlreadyConnected)
		}
	}
	require.NotEqual(t, -1, winner)
	for _, peer := range peers {
		o.releasePeerKey(peer)
	}
}

func TestOverlayAllowsDistinctKeysOnSameIP(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	firstKey, err := GenerateIdentity()
	require.NoError(t, err)
	secondKey, err := GenerateIdentity()
	require.NoError(t, err)

	first := newPeerWithRemoteKey(t, o, 1, Endpoint{Host: "192.0.2.32", Port: 51235}, firstKey)
	second := newPeerWithRemoteKey(t, o, 2, Endpoint{Host: "192.0.2.32", Port: 51236}, secondKey)
	require.NoError(t, o.addPeer(first))
	require.NoError(t, o.addPeer(second))
	assert.True(t, o.isConnectedTo(first.Endpoint()))
	assert.True(t, o.isConnectedTo(second.Endpoint()))
	assert.False(t, o.isConnectedTo(Endpoint{Host: "192.0.2.32", Port: 51237}))
	o.removePeer(first.ID())
	o.removePeer(second.ID())
}

func TestOverlayRejectsDuplicateResolvedEndpoint(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	firstKey, err := GenerateIdentity()
	require.NoError(t, err)
	secondKey, err := GenerateIdentity()
	require.NoError(t, err)
	first := newPeerWithRemoteKey(t, o, 1, Endpoint{Host: "alias-a.example", Port: 51235}, firstKey)
	first.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.34"), Port: 51235}}
	second := newPeerWithRemoteKey(t, o, 2, Endpoint{Host: "alias-b.example", Port: 51235}, secondKey)
	second.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.34"), Port: 51235}}

	require.NoError(t, o.addPeer(first))
	assert.ErrorIs(t, o.addPeer(second), ErrAlreadyConnected)
	o.removePeer(first.ID())
}

func TestAddPeerCarriesOutboundResourceHandle(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)
	remote, err := GenerateIdentity()
	require.NoError(t, err)
	endpoint := Endpoint{Host: "192.0.2.35", Port: 51235}
	peer := newPeerWithRemoteKey(t, o, 9, endpoint, remote)
	usage := o.resourceManager.NewOutboundEndpoint(endpoint.String())
	require.NotNil(t, usage)
	usage.Charge(resource.NewCharge(resource.DecayWindowSeconds*100, "pre-dial"), "")

	require.NoError(t, o.addPeerWithUsage(peer, usage))
	entries := o.resourceManager.Snapshot(0)
	require.Len(t, entries, 1)
	assert.Equal(t, "outbound", entries[0].Type)
	assert.Equal(t,
		"IP Address: 192.0.2.35:51235, Public Key: "+remote.EncodedPublicKey(),
		entries[0].Address,
	)
	assert.Positive(t, entries[0].Local)

	o.removePeer(peer.ID())
	assert.Equal(t, 1, o.resourceManager.Stats().Retained)
	reacquired := o.resourceManager.NewOutboundEndpoint(endpoint.String())
	require.NotNil(t, reacquired)
	assert.Positive(t, reacquired.Balance())
	reacquired.Release()
}

func TestOverlayInboundIPLimit(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()), WithIPLimit(2))
	require.NoError(t, err)
	assert.True(t, o.reserveInboundIP("55.104.0.2"))
	assert.True(t, o.reserveInboundIP("55.104.0.2"))
	assert.False(t, o.reserveInboundIP("55.104.0.2"))
	o.releaseInboundIP("55.104.0.2")
	assert.True(t, o.reserveInboundIP("55.104.0.2"))
}

func TestOverlayReleasesInboundIPAfterPeerClose(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()), WithIPLimit(1))
	require.NoError(t, err)
	remote, err := GenerateIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "55.104.0.3", Port: 51235}, true, o.identity, o.events)
	peer.remotePubKey = NewPublicKeyTokenFromBtcec(remote.BtcecPublicKey())

	require.True(t, o.reserveInboundIP(peer.Endpoint().Host))
	require.NoError(t, o.addPeer(peer))
	require.NoError(t, peer.Close())
	o.removePeer(peer.ID())
	assert.True(t, o.reserveInboundIP(peer.Endpoint().Host))
}
