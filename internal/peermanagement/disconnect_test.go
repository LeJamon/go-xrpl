package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverlay_Disconnect_SingleEmission pins finding 2: a single peer
// teardown must emit exactly one EventPeerDisconnected. The normal path
// (peer.Run → Peer.Close, then the run-watcher goroutine → removePeer)
// previously dispatched the event from BOTH Peer.Close and removePeer, so
// peer_disconnects double-counted and the higher-layer disconnect callback
// fired twice. removePeer is now the single emission site, gated on
// peer-map membership.
func TestOverlay_Disconnect_SingleEmission(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)

	var cbCalls int
	o.SetPeerDisconnectCallback(func(PeerID) { cbCalls++ })

	peer := NewPeer(PeerID(42), Endpoint{Host: "127.0.0.1", Port: 51235}, true, o.identity, o.events)
	peer.setState(PeerStateConnected)
	o.addPeer(peer)

	// Mirror the production teardown ordering: Run calls Close on the peer,
	// then the run-watcher goroutine calls removePeer.
	peer.Close()
	o.removePeer(peer.ID())

	// Drain the lifecycle channel and dispatch synchronously so the
	// event-loop handlers run deterministically without a live eventLoop
	// goroutine.
	var connected, disconnected int
	for {
		select {
		case e := <-o.lifecycle:
			switch e.Type {
			case EventPeerConnected:
				connected++
			case EventPeerDisconnected:
				disconnected++
			}
			o.handleEvent(e)
			continue
		default:
		}
		break
	}

	assert.Equal(t, 1, connected, "exactly one connect event per peer")
	assert.Equal(t, 1, disconnected,
		"exactly one disconnect event per teardown — Peer.Close must not double-dispatch")
	assert.Equal(t, uint64(1), o.PeerDisconnects(), "peer_disconnects must increment exactly once")
	assert.Equal(t, 1, cbCalls, "disconnect callback must fire exactly once")
}

// TestOverlay_Disconnect_NeverAddedPeer pins the second half of finding 2:
// a peer that lost the post-handshake duplicate race and was closed without
// ever being added to the map must NOT emit a disconnect event. Before the
// fix Peer.Close dispatched one for a peer that was never connected.
func TestOverlay_Disconnect_NeverAddedPeer(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 51235}, false, o.identity, o.events)
	peer.setState(PeerStateConnected)

	// Never added to o.peers (the duplicate-race path in Connect closes the
	// peer directly).
	peer.Close()

	var disconnected int
	for {
		select {
		case e := <-o.lifecycle:
			if e.Type == EventPeerDisconnected {
				disconnected++
			}
			continue
		default:
		}
		break
	}
	assert.Equal(t, 0, disconnected,
		"closing a never-added peer must not emit a disconnect event")
}
