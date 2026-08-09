package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerSendOrdinaryHardLimitReturnsTypedOutcome(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)

	for i := range ordinarySendMaximum {
		require.NoError(t, peer.Send([]byte{byte(i)}), "enqueue %d should succeed", i)
	}
	require.Zero(t, peer.SendDrops(), "no drops while the queue has room")

	err = peer.Send([]byte{0xFF})
	require.ErrorIs(t, err, ErrSendBufferFull)
	var queueErr *SendQueueError
	require.ErrorAs(t, err, &queueErr)
	assert.Equal(t, OutboundClassOrdinary, queueErr.Class)
	assert.Equal(t, SendQueueFrameLimit, queueErr.Reason)
	assert.Equal(t, ordinarySendMaximum, queueErr.RetainedFrames)
	assert.Equal(t, uint64(1), peer.SendDrops())
	assert.Equal(t, uint64(1), peer.SendDropsByClass(OutboundClassOrdinary))
	assert.Zero(t, peer.largeSendQ.Load())

	requireOutboundFrame(t, peer)
	require.NoError(t, peer.Send([]byte{0x01}))

	peer.largeSendQ.Store(3)
	for peer.SendQueueLen() > 0 {
		requireOutboundFrame(t, peer)
	}
	require.NoError(t, peer.Send([]byte{0x02}))
	assert.Zero(t, peer.largeSendQ.Load())
}

func TestPeerAcquisitionAdmissionCannotConsumeCriticalMinima(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)

	for range ordinarySendMaximum {
		require.NoError(t, peer.Send([]byte("gossip")))
	}
	acquisitionAccepted := 0
	for {
		err = peer.SendPriority([]byte("acquisition"))
		if err != nil {
			break
		}
		acquisitionAccepted++
	}
	require.ErrorIs(t, err, ErrSendBufferFull)
	assert.Equal(t, 120, acquisitionAccepted)
	snapshot := peer.outbound.snapshot()
	assert.Equal(t, ordinarySendMaximum, snapshot.ClassFrames[OutboundClassOrdinary])
	assert.Equal(t, acquisitionAccepted, snapshot.ClassFrames[OutboundClassAcquisition])

	for range consensusSendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassConsensus, []byte("validation")))
	}
	for range controlSendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassControl, []byte("control")))
	}
	assert.Equal(t, reliableSendBufferSize, peer.SendQueueLen())
}

func TestPeerPingUsesProtectedControlAdmission(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	for range ordinarySendMaximum {
		require.NoError(t, peer.Send([]byte{1}))
	}
	for range 120 {
		require.NoError(t, peer.SendPriority([]byte("acquisition")))
	}
	for range consensusSendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassConsensus, []byte("validation")))
	}

	now := time.Unix(1_000, 0)
	require.NoError(t, peer.runPingTick(now))
	peer.latencyMu.RLock()
	inFlight := len(peer.pingsInFlight)
	peer.latencyMu.RUnlock()
	assert.Equal(t, 1, inFlight)
	assert.Equal(t, controlSendMinimum-1, reliableSendBufferSize-peer.SendQueueLen())
	assert.Zero(t, peer.SendDrops())
	assert.Equal(t, uint32(1), peer.largeSendQ.Load())
}

func TestPeerLargeSendQueueCountsTimerIntervals(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	for range targetSendQueue {
		require.NoError(t, peer.Send([]byte{1}))
	}

	start := time.Unix(1_000, 0)
	for interval := uint32(1); interval <= sendqIntervals+1; interval++ {
		now := start.Add(time.Duration(interval-1) * pingTimeout)
		err := peer.runPingTick(now)
		if interval <= sendqIntervals {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrLargeSendQueue)
		}
		assert.Equal(t, interval, peer.largeSendQ.Load())
		clearPeerPings(peer)
	}
}

func clearPeerPings(peer *Peer) {
	peer.latencyMu.Lock()
	clear(peer.pingsInFlight)
	peer.latencyMu.Unlock()
}

func TestPeerLargeSendQueueIgnoresProbeSubintervals(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	for range targetSendQueue {
		require.NoError(t, peer.Send([]byte{1}))
	}

	start := time.Unix(1_000, 0)
	require.NoError(t, peer.runPingTick(start))
	clearPeerPings(peer)
	assert.Equal(t, uint32(1), peer.largeSendQ.Load())
	for elapsed := pingProbeInterval; elapsed < pingTimeout; elapsed += pingProbeInterval {
		require.NoError(t, peer.runPingTick(start.Add(elapsed)))
		assert.Equal(t, uint32(1), peer.largeSendQ.Load())
	}
}

func TestPeerSendRecoveryResetsTimerStrikesBeforeQueueRegrows(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	for range targetSendQueue {
		require.NoError(t, peer.Send([]byte{1}))
	}

	start := time.Unix(1_000, 0)
	require.NoError(t, peer.runPingTick(start))
	clearPeerPings(peer)
	require.NoError(t, peer.runPingTick(start.Add(pingTimeout)))
	clearPeerPings(peer)
	assert.Equal(t, uint32(2), peer.largeSendQ.Load())

	for peer.SendQueueLen() >= targetSendQueue {
		requireOutboundFrame(t, peer)
	}
	require.NoError(t, peer.Send([]byte{1}))
	assert.Zero(t, peer.largeSendQ.Load())
	for peer.SendQueueLen() < targetSendQueue {
		require.NoError(t, peer.Send([]byte{1}))
	}

	require.NoError(t, peer.runPingTick(start.Add(2*pingTimeout)))
	assert.Equal(t, uint32(1), peer.largeSendQ.Load())
}

func TestPeerDeferredPingWithRecoveredQueueResetsTimerStrikes(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	peer.largeSendQ.Store(sendqIntervals)

	now := time.Unix(1_000, 0)
	peer.recordPingSent(8, now.Add(-pingTimeout))
	peer.readProgressMu.Lock()
	peer.readProgress = inboundFrameProgress{
		active:       true,
		deadline:     now.Add(time.Minute),
		lastProgress: now,
	}
	peer.readProgressMu.Unlock()

	require.NoError(t, peer.runPingTick(now))
	assert.Zero(t, peer.largeSendQ.Load())
}
