package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBroadcastExceptSet_PartialExclusion confirms the normal path: peers
// in the exclusion set are skipped, the rest receive the frame.
func TestBroadcastExceptSet_PartialExclusion(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{peers: make(map[PeerID]*Peer)}
	for id := PeerID(1); id <= 4; id++ {
		o.peers[id] = relayTestPeer(t, ident, id, true)
	}

	excluded := map[PeerID]bool{1: true, 2: true}
	require.NoError(t, o.BroadcastExceptSet(excluded, []byte{0xAA}))

	assert.False(t, gotFrame(o.peers[1]), "excluded peer 1 must be skipped")
	assert.False(t, gotFrame(o.peers[2]), "excluded peer 2 must be skipped")
	assert.True(t, gotFrame(o.peers[3]), "eligible peer 3 must receive")
	assert.True(t, gotFrame(o.peers[4]), "eligible peer 4 must receive")
}

// TestBroadcastExceptSet_AllExcludedFallsBackToAll is the issue #724
// regression guard: when every connected peer is excluded, the request
// must NOT silently reach no one (which wedges tx-set acquisition in
// wrongLedger until the TTL sweep). The exclusion is ignored for that
// round and the frame goes to all connected peers, matching rippled's
// "peer stays eligible for the next request" semantics.
func TestBroadcastExceptSet_AllExcludedFallsBackToAll(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{peers: make(map[PeerID]*Peer)}
	for id := PeerID(1); id <= 4; id++ {
		o.peers[id] = relayTestPeer(t, ident, id, true)
	}

	excluded := map[PeerID]bool{1: true, 2: true, 3: true, 4: true}
	require.NoError(t, o.BroadcastExceptSet(excluded, []byte{0xAA}))

	for id := PeerID(1); id <= 4; id++ {
		assert.Truef(t, gotFrame(o.peers[id]),
			"peer %d must receive the frame when exclusion would starve the broadcast", id)
	}
}

// TestBroadcastExceptSet_AllExcludedStillSkipsDisconnected confirms the
// starvation fallback only reaches CONNECTED peers — a disconnected peer
// is never sent to, even when the exclusion set covers every peer.
func TestBroadcastExceptSet_AllExcludedStillSkipsDisconnected(t *testing.T) {
	ident, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{peers: make(map[PeerID]*Peer)}
	o.peers[1] = relayTestPeer(t, ident, 1, true)
	o.peers[2] = relayTestPeer(t, ident, 2, true)
	disconnected := relayTestPeer(t, ident, 3, true)
	disconnected.setState(PeerStateDisconnected)
	o.peers[3] = disconnected

	excluded := map[PeerID]bool{1: true, 2: true, 3: true}
	require.NoError(t, o.BroadcastExceptSet(excluded, []byte{0xAA}))

	assert.True(t, gotFrame(o.peers[1]), "connected peer 1 must receive the fallback frame")
	assert.True(t, gotFrame(o.peers[2]), "connected peer 2 must receive the fallback frame")
	assert.False(t, gotFrame(o.peers[3]), "disconnected peer must never receive")
}
