package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fatalRunConn struct {
	closed chan struct{}
	once   sync.Once
}

func newFatalRunConn() *fatalRunConn {
	return &fatalRunConn{closed: make(chan struct{})}
}

func (c *fatalRunConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *fatalRunConn) Write([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *fatalRunConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (*fatalRunConn) LocalAddr() net.Addr              { return livenessAddr("local") }
func (*fatalRunConn) RemoteAddr() net.Addr             { return livenessAddr("remote") }
func (*fatalRunConn) SetDeadline(time.Time) error      { return nil }
func (*fatalRunConn) SetReadDeadline(time.Time) error  { return nil }
func (*fatalRunConn) SetWriteDeadline(time.Time) error { return nil }

func TestOutboundQueueReliableFIFOAndBulkFairness(t *testing.T) {
	queue := newOutboundQueue()
	for i := range reliableFramesPerBulkFrame + 1 {
		class := OutboundClassOrdinary
		if i%2 == 1 {
			class = OutboundClassAcquisition
		}
		require.NoError(t, queue.enqueueReliable(class, []byte{0x52, byte(i)}))
	}
	require.NoError(t, queue.enqueueBulk([][]byte{{0x42, 0}, {0x42, 1}}))

	for i := range reliableFramesPerBulkFrame {
		token, err := queue.next(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []byte{0x52, byte(i)}, token.data)
		snapshot := queue.snapshot()
		assert.Equal(t, 1, snapshot.InFlight)
		assert.Equal(t, reliableFramesPerBulkFrame+3-i, snapshot.TotalFrames)
		queue.complete(token)
	}

	token, err := queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte{0x42, 0}, token.data)
	queue.complete(token)

	token, err = queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte{0x52, reliableFramesPerBulkFrame}, token.data)
	queue.complete(token)

	token, err = queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte{0x42, 1}, token.data)
	queue.complete(token)
	assert.Zero(t, queue.snapshot().TotalFrames)
}

func TestOutboundQueueProtectedMinimaAndHardMaxima(t *testing.T) {
	queue := newOutboundQueue()
	for range ordinarySendMinimum {
		require.NoError(t, queue.enqueueReliable(OutboundClassOrdinary, []byte{1}))
	}
	for range acquisitionSendMinimum {
		require.NoError(t, queue.enqueueReliable(OutboundClassAcquisition, []byte{1}))
	}
	for range consensusSendMinimum {
		require.NoError(t, queue.enqueueReliable(OutboundClassConsensus, []byte{1}))
	}
	for range controlSendMinimum {
		require.NoError(t, queue.enqueueReliable(OutboundClassControl, []byte{1}))
	}

	for range consensusSendMaximum - consensusSendMinimum {
		require.NoError(t, queue.enqueueReliable(OutboundClassConsensus, []byte{1}))
	}
	err := queue.enqueueReliable(OutboundClassConsensus, []byte{1})
	require.ErrorIs(t, err, ErrSendBufferFull)
	require.ErrorIs(t, err, ErrCriticalSendQueueFull)

	var queueErr *SendQueueError
	require.ErrorAs(t, err, &queueErr)
	assert.Equal(t, OutboundClassConsensus, queueErr.Class)
	assert.Equal(t, SendQueueFrameLimit, queueErr.Reason)
	select {
	case fatal := <-queue.fatalSignal():
		assert.True(t, errors.Is(fatal, ErrCriticalSendQueueFull))
	default:
		t.Fatal("critical admission exhaustion did not signal the peer lifecycle")
	}
}

func TestOutboundQueueRetainedByteLimitsIncludeInFlight(t *testing.T) {
	queue := newOutboundQueue()
	queue.mu.Lock()
	queue.totalBytes = int64(outboundNonCriticalByteMaximum - 1)
	queue.mu.Unlock()

	require.NoError(t, queue.enqueueReliable(OutboundClassOrdinary, []byte{1}))
	token, err := queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(outboundNonCriticalByteMaximum), queue.snapshot().TotalBytes)

	err = queue.enqueueReliable(OutboundClassOrdinary, []byte{1})
	require.ErrorIs(t, err, ErrSendBufferFull)
	var queueErr *SendQueueError
	require.ErrorAs(t, err, &queueErr)
	assert.Equal(t, SendQueueByteLimit, queueErr.Reason)

	require.NoError(t, queue.enqueueReliable(
		OutboundClassConsensus,
		make([]byte, outboundCriticalByteReserve),
	))
	err = queue.enqueueReliable(OutboundClassControl, []byte{1})
	require.ErrorIs(t, err, ErrCriticalSendQueueFull)

	queue.complete(token)
}

func TestOutboundQueueCloseReleasesQueuedAndInFlightAccounting(t *testing.T) {
	queue := newOutboundQueue()
	require.NoError(t, queue.enqueueReliable(OutboundClassOrdinary, []byte("reliable")))
	require.NoError(t, queue.enqueueBulk([][]byte{[]byte("bulk-1"), []byte("bulk-2")}))
	token, err := queue.next(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, token.data)
	require.Positive(t, queue.snapshot().TotalBytes)

	queue.close()
	snapshot := queue.snapshot()
	assert.Zero(t, snapshot.TotalFrames)
	assert.Zero(t, snapshot.TotalBytes)
	assert.Zero(t, snapshot.InFlight)
	assert.Zero(t, snapshot.BulkSequences)
	assert.Nil(t, queue.reliable)
	assert.Nil(t, queue.bulk)
	require.ErrorIs(t, queue.enqueueReliable(OutboundClassOrdinary, []byte{1}), ErrConnectionClosed)

	queue.complete(token)
	assert.Zero(t, queue.snapshot().TotalFrames)
}

func TestOutboundBudgetIsSharedAndRetainsInFlightReservations(t *testing.T) {
	maxBytes := int64(outboundNonCriticalByteMaximum + 2*outboundCriticalByteReserve)
	budget := newOutboundBudget(maxBytes, 2)
	first := newOutboundQueue()
	second := newOutboundQueue()
	first.setBudget(budget)
	second.setBudget(budget)
	preseed := budget.generalLimit - 1
	require.True(t, first.budget.reserve(preseed, false))

	require.NoError(t, first.enqueueReliable(OutboundClassOrdinary, []byte{1}))
	token, err := first.next(context.Background())
	require.NoError(t, err)

	err = second.enqueueReliable(OutboundClassOrdinary, []byte{1})
	require.ErrorIs(t, err, ErrSendBufferFull)
	var queueErr *SendQueueError
	require.ErrorAs(t, err, &queueErr)
	assert.Equal(t, SendQueueSharedByteLimit, queueErr.Reason)

	require.NoError(t, second.enqueueReliable(
		OutboundClassConsensus,
		make([]byte, outboundCriticalByteReserve),
	))
	first.complete(token)
	require.NoError(t, second.enqueueReliable(OutboundClassOrdinary, []byte{1}))

	first.close()
	second.close()
	retained, general := budget.snapshot()
	assert.Zero(t, retained)
	assert.Zero(t, general)
}

func TestOutboundBudgetProtectsEveryHealthyPeersCriticalSlice(t *testing.T) {
	const peers = 3
	maxBytes := int64(outboundNonCriticalByteMaximum + peers*outboundCriticalByteReserve)
	budget := newOutboundBudget(maxBytes, peers)
	queues := []*outboundQueue{newOutboundQueue(), newOutboundQueue(), newOutboundQueue()}
	for _, queue := range queues {
		queue.setBudget(budget)
		t.Cleanup(queue.close)
	}

	require.True(t, queues[0].budget.reserve(budget.generalLimit, false))
	require.NoError(t, queues[0].enqueueReliable(
		OutboundClassConsensus,
		make([]byte, outboundCriticalByteReserve),
	))
	err := queues[0].enqueueReliable(OutboundClassConsensus, []byte{1})
	require.ErrorIs(t, err, ErrCriticalSendQueueFull)
	var queueErr *SendQueueError
	require.ErrorAs(t, err, &queueErr)
	assert.Equal(t, SendQueueSharedByteLimit, queueErr.Reason)

	for _, queue := range queues[1:] {
		require.NoError(t, queue.enqueueReliable(
			OutboundClassConsensus,
			make([]byte, outboundCriticalByteReserve),
		))
	}
	assert.Equal(t, maxBytes, func() int64 {
		retained, _ := budget.snapshot()
		return retained
	}())
}

func TestOutboundBudgetAdditionalReservedPeerDoesNotPanic(t *testing.T) {
	maxBytes := int64(outboundNonCriticalByteMaximum + outboundCriticalByteReserve)
	budget := newOutboundBudget(maxBytes, 1)
	first := newOutboundQueue()
	second := newOutboundQueue()
	first.setBudget(budget)
	second.setBudget(budget)

	assert.True(t, first.budget.hasReserve)
	assert.False(t, second.budget.hasReserve)
	require.True(t, first.budget.reserve(budget.generalLimit, false))
	require.NoError(t, first.enqueueReliable(
		OutboundClassConsensus,
		make([]byte, outboundCriticalByteReserve),
	))
	require.ErrorIs(t,
		second.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)

	first.close()
	second.close()
	budget.mu.Lock()
	assert.Zero(t, budget.activeAccounts)
	assert.Zero(t, budget.reservedAccounts)
	budget.mu.Unlock()
}

func TestOutboundRetainedBytesConfigDefaultsAndValidates(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, DefaultOutboundRetainedBytes, cfg.OutboundRetainedBytes)

	cfg.OutboundRetainedBytes = 0
	require.NoError(t, cfg.Validate())
	assert.Equal(t, DefaultOutboundRetainedBytes, cfg.OutboundRetainedBytes)

	cfg = DefaultConfig()
	cfg.OutboundRetainedBytes = int64(outboundNonCriticalByteMaximum) +
		int64(cfg.MaxPeers)*int64(outboundCriticalByteReserve) - 1
	require.Error(t, cfg.Validate())
}

func TestOutboundBudgetAttachesProductionPeersToOneSharedInstance(t *testing.T) {
	overlay := &Overlay{
		outboundBudget: newOutboundBudget(DefaultOutboundRetainedBytes, DefaultMaxPeers),
	}
	first := newLatencyTestPeer(t)
	second := newLatencyTestPeer(t)
	overlay.attachOutboundBudget(first)
	overlay.attachOutboundBudget(second)
	assert.Same(t, overlay.outboundBudget, first.outbound.budget.budget)
	assert.Same(t, first.outbound.budget.budget, second.outbound.budget.budget)
	assert.NotSame(t, first.outbound.budget, second.outbound.budget)
}

func TestOverlayCountsOneShotLocalAndSharedCriticalFailures(t *testing.T) {
	const peers = 2
	overlay := &Overlay{
		outboundBudget: newOutboundBudget(
			int64(outboundNonCriticalByteMaximum+peers*outboundCriticalByteReserve),
			peers,
		),
	}
	localPeer := newLatencyTestPeer(t)
	sharedPeer := newLatencyTestPeer(t)
	overlay.attachOutboundBudget(localPeer)
	overlay.attachOutboundBudget(sharedPeer)

	for range controlSendMinimum {
		require.NoError(t, localPeer.outbound.enqueueReliable(OutboundClassControl, []byte{1}))
	}
	for range acquisitionSendMinimum {
		require.NoError(t, localPeer.outbound.enqueueReliable(OutboundClassAcquisition, []byte{1}))
	}
	for range ordinarySendMinimum {
		require.NoError(t, localPeer.outbound.enqueueReliable(OutboundClassOrdinary, []byte{1}))
	}
	for range consensusSendMaximum {
		require.NoError(t, localPeer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}))
	}
	require.ErrorIs(t,
		localPeer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)
	require.ErrorIs(t,
		localPeer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)

	_, generalUsed := overlay.outboundBudget.snapshot()
	require.True(t, sharedPeer.outbound.budget.reserve(
		overlay.outboundBudget.generalLimit-generalUsed,
		false,
	))
	require.NoError(t, sharedPeer.outbound.enqueueReliable(
		OutboundClassConsensus,
		make([]byte, outboundCriticalByteReserve),
	))
	require.ErrorIs(t,
		sharedPeer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)
	require.ErrorIs(t,
		sharedPeer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)

	local, shared := overlay.OutboundCriticalQueueFailures()
	assert.Equal(t, uint64(1), local)
	assert.Equal(t, uint64(1), shared)
}

func TestPeerRunTerminatesOnCriticalQueueExhaustion(t *testing.T) {
	peer := newLatencyTestPeer(t)
	conn := newFatalRunConn()
	peer.conn = conn
	peer.bufReader = bufio.NewReader(conn)
	peer.setState(PeerStateConnected)

	for range controlSendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassControl, []byte{1}))
	}
	for range acquisitionSendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassAcquisition, []byte{1}))
	}
	for range ordinarySendMinimum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassOrdinary, []byte{1}))
	}
	for range consensusSendMaximum {
		require.NoError(t, peer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}))
	}
	require.ErrorIs(t,
		peer.outbound.enqueueReliable(OutboundClassConsensus, []byte{1}),
		ErrCriticalSendQueueFull,
	)

	done := make(chan error, 1)
	go func() { done <- peer.Run(context.Background()) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrCriticalSendQueueFull)
	case <-time.After(time.Second):
		t.Fatal("Peer.Run did not terminate on critical queue exhaustion")
	}
	assert.True(t, peer.closed.Load())
	assert.Equal(t, PeerStateDisconnected, peer.State())
	assert.Zero(t, peer.SendQueueLen())
}

func TestOutboundQueueOwnsAcceptedFrameBytes(t *testing.T) {
	queue := newOutboundQueue()
	reliable := []byte("reliable")
	bulkFirst := []byte("bulk-first")
	bulkSecond := []byte("bulk-second")
	require.NoError(t, queue.enqueueReliable(OutboundClassOrdinary, reliable))
	require.NoError(t, queue.enqueueBulk([][]byte{bulkFirst, bulkSecond}))

	copy(reliable, []byte("mutated!"))
	copy(bulkFirst, []byte("changed-one"))
	copy(bulkSecond, []byte("changed-two"))

	token, err := queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("reliable"), token.data)
	queue.complete(token)
	token, err = queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("bulk-first"), token.data)
	queue.complete(token)
	token, err = queue.next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("bulk-second"), token.data)
	queue.complete(token)
}

func TestValidationBurstBlockedWritersPreservesEveryAcceptedFrame(t *testing.T) {
	first := newLatencyTestPeer(t)
	second := newLatencyTestPeer(t)
	peers := []*Peer{first, second}

	expected := make([][]byte, 0, 257)
	for i := 0; i < 255; i++ {
		frame := outboundTestFrame(t, message.TypeValidation, []byte{byte(i), byte(i >> 8)})
		expected = append(expected, frame)
		for _, peer := range peers {
			require.NoError(t, peer.Send(frame), "validation %d", i)
		}
	}
	transaction := outboundTestFrame(t, message.TypeTransaction, []byte("transaction"))
	acquisition := outboundTestFrame(t, message.TypeGetLedger, []byte("acquisition"))
	expected = append(expected, transaction, acquisition)
	for _, peer := range peers {
		require.NoError(t, peer.Send(transaction))
		require.NoError(t, peer.SendPriority(acquisition))
		snapshot := peer.outbound.snapshot()
		assert.Equal(t, 255, snapshot.ClassFrames[OutboundClassConsensus])
		assert.Equal(t, 1, snapshot.ClassFrames[OutboundClassOrdinary])
		assert.Equal(t, 1, snapshot.ClassFrames[OutboundClassAcquisition])
		assert.Equal(t, len(expected), snapshot.TotalFrames)
	}

	for _, peer := range peers {
		for index, want := range expected {
			got, ok := takeOutboundFrame(peer)
			require.True(t, ok, "accepted frame %d disappeared", index)
			assert.Equal(t, want, got, "FIFO mismatch at frame %d", index)
		}
		assert.Zero(t, peer.SendQueueLen())
	}
}

func outboundTestFrame(t *testing.T, messageType message.MessageType, payload []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	require.NoError(t, message.WriteMessage(&frame, messageType, payload))
	return frame.Bytes()
}
