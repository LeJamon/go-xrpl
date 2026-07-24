package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBroadcast_AttemptsBackpressuredPeerOnceAndContinues(t *testing.T) {
	full := newTestPeer(t, 1)
	healthy := newTestPeer(t, 2)
	full.setState(PeerStateConnected)
	healthy.setState(PeerStateConnected)
	for i := range DefaultSendBufferSize {
		require.NoError(t, full.Send([]byte{byte(i)}))
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: full, 2: healthy})
	frame := []byte{0xAA, 0xBB, 0xCC}
	require.NoError(t, overlay.Broadcast(frame))

	require.Equal(t, uint64(1), full.SendDrops())
	require.Zero(t, full.largeSendQ.Load())
	require.Len(t, full.send, DefaultSendBufferSize)
	require.Equal(t, frame, <-healthy.send)
}

func TestBroadcastPriorityUsesIndependentQueue(t *testing.T) {
	peer := newTestPeer(t, 1)
	peer.setState(PeerStateConnected)
	for i := range DefaultSendBufferSize {
		require.NoError(t, peer.Send([]byte{byte(i)}))
	}
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: peer})
	frame := []byte{0xAA}

	require.NoError(t, overlay.BroadcastPriority(frame))
	require.Len(t, peer.send, DefaultSendBufferSize)
	require.Equal(t, frame, <-peer.prioritySend)
	require.Zero(t, peer.SendDrops())
}

func TestBroadcastPriorityExceptSetUsesIndependentQueue(t *testing.T) {
	excluded := newTestPeer(t, 1)
	eligible := newTestPeer(t, 2)
	excluded.setState(PeerStateConnected)
	eligible.setState(PeerStateConnected)
	for i := range DefaultSendBufferSize {
		require.NoError(t, excluded.Send([]byte{byte(i)}))
		require.NoError(t, eligible.Send([]byte{byte(i)}))
	}
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: excluded, 2: eligible})
	frame := []byte{0xAA}

	require.NoError(t, overlay.BroadcastPriorityExceptSet(map[PeerID]bool{1: true}, frame))
	require.Len(t, excluded.send, DefaultSendBufferSize)
	require.Empty(t, excluded.prioritySend)
	require.Len(t, eligible.send, DefaultSendBufferSize)
	require.Equal(t, frame, <-eligible.prioritySend)
	require.Zero(t, excluded.SendDrops())
	require.Zero(t, eligible.SendDrops())
}

func TestBroadcastManifestFramesExceptSkipsSourcePeer(t *testing.T) {
	source := newTestPeer(t, 1)
	other := newTestPeer(t, 2)
	source.setState(PeerStateConnected)
	other.setState(PeerStateConnected)
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: source, 2: other})
	frames := [][]byte{{0xAA}, {0xBB}}

	require.NoError(t, overlay.BroadcastManifestFramesExcept(source.ID(), frames))
	require.Empty(t, source.manifestSend)
	require.Equal(t, frames, <-other.manifestSend)
}

func TestBroadcastExceptDoesNotEchoToSource(t *testing.T) {
	source := newTestPeer(t, 1)
	other := newTestPeer(t, 2)
	source.setState(PeerStateConnected)
	other.setState(PeerStateConnected)
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: source, 2: other})
	frame := []byte{0xAA, 0xBB, 0xCC}

	require.NoError(t, overlay.BroadcastExcept(source.ID(), frame))
	require.Empty(t, source.send)
	require.Equal(t, frame, <-other.send)
}
