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
	for i := range ordinarySendMaximum {
		require.NoError(t, full.Send([]byte{byte(i)}))
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: full, 2: healthy})
	frame := []byte{0xAA, 0xBB, 0xCC}
	err := overlay.Broadcast(frame)
	require.ErrorIs(t, err, ErrSendBufferFull)
	var fanoutErr *FanoutError
	require.ErrorAs(t, err, &fanoutErr)
	require.Equal(t, 2, fanoutErr.Attempted)
	require.Equal(t, 1, fanoutErr.Failed)
	require.Zero(t, fanoutErr.Critical)

	require.Equal(t, uint64(1), full.SendDrops())
	require.Zero(t, full.largeSendQ.Load())
	require.Equal(t, ordinarySendMaximum, full.SendQueueLen())
	require.Equal(t, frame, requireOutboundFrame(t, healthy))
}

func TestBroadcastFanoutErrorPreservesMixedFailureCategories(t *testing.T) {
	closed := newTestPeer(t, 1)
	closed.setState(PeerStateConnected)
	closed.closed.Store(true)
	full := newTestPeer(t, 2)
	full.setState(PeerStateConnected)
	for range ordinarySendMaximum {
		require.NoError(t, full.Send([]byte{0xAA}))
	}
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: closed, 2: full})

	err := overlay.Broadcast([]byte{0xBB})
	require.ErrorIs(t, err, ErrConnectionClosed)
	require.ErrorIs(t, err, ErrSendBufferFull)
	var fanoutErr *FanoutError
	require.ErrorAs(t, err, &fanoutErr)
	require.Equal(t, 2, fanoutErr.Attempted)
	require.Equal(t, 2, fanoutErr.Failed)
}

func TestBroadcastPriorityUsesProtectedAdmission(t *testing.T) {
	peer := newTestPeer(t, 1)
	peer.setState(PeerStateConnected)
	for i := range ordinarySendMaximum {
		require.NoError(t, peer.Send([]byte{byte(i)}))
	}
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: peer})
	frame := []byte{0xAA}

	require.NoError(t, overlay.BroadcastPriority(frame))
	snapshot := peer.outbound.snapshot()
	require.Equal(t, ordinarySendMaximum, snapshot.ClassFrames[OutboundClassOrdinary])
	require.Equal(t, 1, snapshot.ClassFrames[OutboundClassAcquisition])
	require.Zero(t, peer.SendDrops())
}

func TestBroadcastPriorityExceptSetUsesProtectedAdmission(t *testing.T) {
	excluded := newTestPeer(t, 1)
	eligible := newTestPeer(t, 2)
	excluded.setState(PeerStateConnected)
	eligible.setState(PeerStateConnected)
	for i := range ordinarySendMaximum {
		require.NoError(t, excluded.Send([]byte{byte(i)}))
		require.NoError(t, eligible.Send([]byte{byte(i)}))
	}
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: excluded, 2: eligible})
	frame := []byte{0xAA}

	require.NoError(t, overlay.BroadcastPriorityExceptSet(map[PeerID]bool{1: true}, frame))
	excludedSnapshot := excluded.outbound.snapshot()
	require.Equal(t, ordinarySendMaximum, excludedSnapshot.ClassFrames[OutboundClassOrdinary])
	require.Zero(t, excludedSnapshot.ClassFrames[OutboundClassAcquisition])
	eligibleSnapshot := eligible.outbound.snapshot()
	require.Equal(t, ordinarySendMaximum, eligibleSnapshot.ClassFrames[OutboundClassOrdinary])
	require.Equal(t, 1, eligibleSnapshot.ClassFrames[OutboundClassAcquisition])
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
	require.Zero(t, source.outbound.snapshot().BulkSequences)
	require.Equal(t, 1, other.outbound.snapshot().BulkSequences)
	require.Equal(t, frames[0], requireOutboundFrame(t, other))
	require.Equal(t, frames[1], requireOutboundFrame(t, other))
}

func TestBroadcastExceptDoesNotEchoToSource(t *testing.T) {
	source := newTestPeer(t, 1)
	other := newTestPeer(t, 2)
	source.setState(PeerStateConnected)
	other.setState(PeerStateConnected)
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{1: source, 2: other})
	frame := []byte{0xAA, 0xBB, 0xCC}

	require.NoError(t, overlay.BroadcastExcept(source.ID(), frame))
	require.Zero(t, source.SendQueueLen())
	require.Equal(t, frame, requireOutboundFrame(t, other))
}
