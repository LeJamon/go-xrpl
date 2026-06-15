package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeer_Send_DropPolicy pins finding 9: a full bounded send queue drops
// the frame (returning ErrSendBufferFull) and counts it per peer, and the
// large-send-queue strike count is cleared only once the queue drains back
// below targetSendQueue — not on any single successful enqueue.
func TestPeer_Send_DropPolicy(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	// nil events channel: Send never touches it. No writer drains p.send,
	// so the buffer fills deterministically.
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)

	// Fill the bounded send buffer.
	for i := range DefaultSendBufferSize {
		require.NoError(t, peer.Send([]byte{byte(i)}), "enqueue %d should succeed", i)
	}
	require.Zero(t, peer.SendDrops(), "no drops while the queue has room")

	// Queue full: further sends drop, count, and accumulate strikes.
	require.ErrorIs(t, peer.Send([]byte{0xFF}), ErrSendBufferFull)
	require.ErrorIs(t, peer.Send([]byte{0xFE}), ErrSendBufferFull)
	assert.Equal(t, uint64(2), peer.SendDrops(), "each dropped frame must be counted")
	assert.GreaterOrEqual(t, peer.largeSendQ.Load(), uint32(2), "drops accumulate strikes")

	// Drain a single frame (queue still well above target) and re-enqueue:
	// the strike count must NOT reset — this is the tightened semantics.
	<-peer.send
	strikes := peer.largeSendQ.Load()
	require.NoError(t, peer.Send([]byte{0x01}))
	assert.Equal(t, strikes, peer.largeSendQ.Load(),
		"a successful enqueue with the queue still above target must not clear strikes")

	// Drain fully below target, then a successful enqueue clears strikes.
	for len(peer.send) > 0 {
		<-peer.send
	}
	require.NoError(t, peer.Send([]byte{0x02}))
	assert.Zero(t, peer.largeSendQ.Load(),
		"draining below target then enqueueing must clear the strike count")
}
