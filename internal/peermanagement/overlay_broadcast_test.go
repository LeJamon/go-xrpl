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
	require.Equal(t, uint32(1), full.largeSendQ.Load())
	require.Len(t, full.send, DefaultSendBufferSize)
	require.Equal(t, frame, <-healthy.send)
}
