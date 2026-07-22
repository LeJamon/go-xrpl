package peermanagement

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeer_Send_DropPolicy pins finding 9: a full bounded send queue drops
// the frame (returning ErrSendBufferFull) and counts it per peer, and the
// large-send-queue intervals reset as soon as a new send observes recovery.
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

	// Queue full: further sends drop and count without accelerating the
	// timer-based disconnect policy.
	require.ErrorIs(t, peer.Send([]byte{0xFF}), ErrSendBufferFull)
	require.ErrorIs(t, peer.Send([]byte{0xFE}), ErrSendBufferFull)
	assert.Equal(t, uint64(2), peer.SendDrops(), "each dropped frame must be counted")
	assert.Zero(t, peer.largeSendQ.Load())

	// Drain a single frame (queue still well above target) and re-enqueue.
	<-peer.send
	require.NoError(t, peer.Send([]byte{0x01}))
	assert.Zero(t, peer.largeSendQ.Load())

	peer.largeSendQ.Store(3)
	// A send after the queue drains below target clears prior timer strikes.
	for len(peer.send) > 0 {
		<-peer.send
	}
	require.NoError(t, peer.Send([]byte{0x02}))
	assert.Zero(t, peer.largeSendQ.Load())
}

func TestPeerPrioritySendHasIndependentCapacity(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)

	results := make(chan error, DefaultSendBufferSize*2)
	var wg sync.WaitGroup
	for range DefaultSendBufferSize * 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- peer.Send([]byte("gossip"))
		}()
	}
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, ErrSendBufferFull)
		}
	}
	require.Equal(t, DefaultSendBufferSize, succeeded)
	require.Len(t, peer.send, DefaultSendBufferSize)

	for range acquisitionSendBufferSize {
		require.NoError(t, peer.SendPriority([]byte("acquisition")))
	}
	require.ErrorIs(t, peer.SendPriority([]byte("acquisition")), ErrSendBufferFull)
	assert.Len(t, peer.send, DefaultSendBufferSize)
	assert.Len(t, peer.prioritySend, acquisitionSendBufferSize)
	assert.Equal(t, DefaultSendBufferSize+acquisitionSendBufferSize, peer.SendQueueLen())
}

func TestPeerPingQueuePressureIsNotFatal(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
	for range DefaultSendBufferSize {
		require.NoError(t, peer.Send([]byte{1}))
	}

	now := time.Unix(1_000, 0)
	require.NoError(t, peer.runPingTick(now))
	assert.Equal(t, uint64(1), peer.SendDrops())
	peer.latencyMu.RLock()
	inFlight := len(peer.pingsInFlight)
	peer.latencyMu.RUnlock()
	assert.Zero(t, inFlight, "a ping that was not queued must not start its timeout")
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

	for len(peer.send) >= targetSendQueue {
		<-peer.send
	}
	require.NoError(t, peer.Send([]byte{1}))
	assert.Zero(t, peer.largeSendQ.Load())
	for len(peer.send) < targetSendQueue {
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
