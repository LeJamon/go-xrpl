package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type livenessClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *livenessClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *livenessClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

type connReadStep struct {
	after time.Duration
	data  []byte
	err   error
}

type chunkedLivenessConn struct {
	clock *livenessClock
	steps []connReadStep

	mu           sync.Mutex
	readDeadline time.Time
	deadlines    []time.Time
	writeCalls   int
	writeLimits  int
	writeErr     error
	writes       [][]byte
}

type partialWriteConn struct {
	chunkedLivenessConn
	limit int
	wire  []byte
}

func (c *partialWriteConn) Write(src []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeCalls++
	n := min(c.limit, len(src))
	c.wire = append(c.wire, src[:n]...)
	return n, nil
}

func (c *chunkedLivenessConn) Read(dst []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.steps) == 0 {
		return 0, io.EOF
	}
	step := &c.steps[0]
	next := c.clock.current().Add(step.after)
	if !c.readDeadline.IsZero() && !next.Before(c.readDeadline) {
		c.clock.advance(c.readDeadline.Sub(c.clock.current()))
		return 0, livenessTimeoutError{}
	}
	c.clock.advance(step.after)
	step.after = 0
	n := copy(dst, step.data)
	step.data = step.data[n:]
	if len(step.data) > 0 {
		return n, nil
	}
	err := step.err
	c.steps = c.steps[1:]
	return n, err
}

func (c *chunkedLivenessConn) Write(src []byte) (int, error) {
	c.mu.Lock()
	c.writeCalls++
	c.writes = append(c.writes, append([]byte(nil), src...))
	err := c.writeErr
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(src), nil
}

func TestPeerWriteLoopPreservesReliableAdmissionOrder(t *testing.T) {
	clock := &livenessClock{now: time.Unix(5_900, 0)}
	conn := &chunkedLivenessConn{clock: clock}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	require.NoError(t, peer.Send([]byte("gossip")))
	require.NoError(t, peer.SendPriority([]byte("acquisition")))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.writeLoop(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.writes) == 2
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Equal(t, []byte("gossip"), conn.writes[0])
	require.Equal(t, []byte("acquisition"), conn.writes[1])
}

func TestPeerWriteLoopBoundsReliableBurstBeforeBulk(t *testing.T) {
	clock := &livenessClock{now: time.Unix(5_950, 0)}
	conn := &chunkedLivenessConn{clock: clock}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	for i := range reliableFramesPerBulkFrame + 1 {
		require.NoError(t, peer.Send([]byte{0x52, byte(i)}))
	}
	require.NoError(t, peer.SendManifestFrames([][]byte{{0x42, 0}, {0x42, 1}}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.writeLoop(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.writes) == reliableFramesPerBulkFrame+3
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for i := range reliableFramesPerBulkFrame {
		require.Equal(t, []byte{0x52, byte(i)}, conn.writes[i])
	}
	require.Equal(t, []byte{0x42, 0}, conn.writes[reliableFramesPerBulkFrame])
	require.Equal(t, []byte{0x52, reliableFramesPerBulkFrame}, conn.writes[reliableFramesPerBulkFrame+1])
	require.Equal(t, []byte{0x42, 1}, conn.writes[reliableFramesPerBulkFrame+2])
}

func TestPeerBulkSequenceQueueIsBounded(t *testing.T) {
	peer := newLatencyTestPeer(t)
	for i := range bulkSequenceBufferSize {
		require.NoError(t, peer.SendManifestFrames([][]byte{{0x4d, byte(i)}}))
	}
	require.Equal(t, bulkSequenceBufferSize, peer.outbound.snapshot().BulkSequences)
	require.ErrorIs(t, peer.SendManifestFrames([][]byte{{0x4d, 0xff}}), ErrSendBufferFull)
	require.Equal(t, uint64(1), peer.SendDrops())
	require.Equal(t, uint64(1), peer.SendDropsByClass(OutboundClassBulk))
}

func TestPeerManifestDispatchBackpressuresUntilCapacity(t *testing.T) {
	peer := newLatencyTestPeer(t)
	manifestMessages := make(chan *InboundMessage, 1)
	peer.SetManifestMessages(manifestMessages)
	manifestMessages <- &InboundMessage{}

	delivered := make(chan bool, 1)
	go func() {
		delivered <- peer.dispatchEvent(Event{
			Type: EventMessageReceived, PeerID: peer.ID(), MessageType: uint16(message.TypeManifests), Payload: []byte("manifest"),
		})
	}()
	select {
	case <-delivered:
		t.Fatal("manifest dispatch did not apply backpressure")
	case <-time.After(25 * time.Millisecond):
	}

	<-manifestMessages
	require.True(t, <-delivered)
	inbound := <-manifestMessages
	assert.Equal(t, peer.ID(), inbound.PeerID)
	assert.Equal(t, uint16(message.TypeManifests), inbound.Type)
	assert.Equal(t, []byte("manifest"), inbound.Payload)
}

type countingReader struct {
	reader *bytes.Reader
	read   atomic.Int64
}

func (r *countingReader) Read(dst []byte) (int, error) {
	n, err := r.reader.Read(dst)
	r.read.Add(int64(n))
	return n, err
}

func TestPeerManifestReadBudgetBoundsPayloadAllocation(t *testing.T) {
	payload := bytes.Repeat([]byte{0x4d}, 32*1024)
	frame := manifestFrame(t, payload)
	budget := newReadBudget(int64(len(payload)))

	firstQueue := make(chan *InboundMessage, 1)
	firstQueue <- &InboundMessage{}
	first := newLatencyTestPeer(t)
	first.bufReader = bufio.NewReader(bytes.NewReader(frame))
	first.SetManifestMessages(firstQueue)
	first.SetInboundReadBudget(budget)
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.readLoop(context.Background()) }()
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.used == int64(len(payload))
	}, time.Second, time.Millisecond)

	secondReader := &countingReader{reader: bytes.NewReader(frame)}
	secondQueue := make(chan *InboundMessage, 1)
	second := newLatencyTestPeer(t)
	second.bufReader = bufio.NewReader(secondReader)
	second.SetManifestMessages(secondQueue)
	second.SetInboundReadBudget(budget)
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.readLoop(context.Background()) }()

	require.Eventually(t, func() bool { return secondReader.read.Load() > 0 }, time.Second, time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	require.Less(t, secondReader.read.Load(), int64(len(frame)))

	queued := <-firstQueue
	require.NoError(t, queued.Close())
	firstInbound := <-firstQueue
	require.NoError(t, firstInbound.Close())
	require.Eventually(t, func() bool { return secondReader.read.Load() == int64(len(frame)) }, time.Second, time.Millisecond)
	secondInbound := <-secondQueue
	require.Equal(t, payload, secondInbound.Payload)
	require.NoError(t, secondInbound.Close())
	require.ErrorIs(t, <-firstDone, io.EOF)
	require.ErrorIs(t, <-secondDone, io.EOF)
}

func (c *chunkedLivenessConn) Close() error                { return nil }
func (c *chunkedLivenessConn) LocalAddr() net.Addr         { return livenessAddr("local") }
func (c *chunkedLivenessConn) RemoteAddr() net.Addr        { return livenessAddr("remote") }
func (c *chunkedLivenessConn) SetDeadline(time.Time) error { return nil }
func (c *chunkedLivenessConn) SetWriteDeadline(time.Time) error {
	c.mu.Lock()
	c.writeLimits++
	c.mu.Unlock()
	return nil
}
func (c *chunkedLivenessConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = deadline
	c.deadlines = append(c.deadlines, deadline)
	return nil
}

type livenessAddr string

func (a livenessAddr) Network() string { return string(a) }
func (a livenessAddr) String() string  { return string(a) }

type livenessTimeoutError struct{}

func (livenessTimeoutError) Error() string   { return "i/o timeout" }
func (livenessTimeoutError) Timeout() bool   { return true }
func (livenessTimeoutError) Temporary() bool { return true }

func newFrameLivenessPeer(t *testing.T, clock *livenessClock, conn net.Conn) (*Peer, chan Event) {
	t.Helper()
	identity, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 4)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, identity, events)
	peer.conn = conn
	peer.bufReader = bufio.NewReader(conn)
	peer.readPolicy.now = clock.current
	return peer, events
}

func manifestFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	require.NoError(t, message.WriteMessage(&frame, message.TypeManifests, payload))
	return frame.Bytes()
}

func TestPeerReadLoopAllowsProgressBeyondIdleInterval(t *testing.T) {
	clock := &livenessClock{now: time.Unix(1_000, 0)}
	frame := manifestFrame(t, []byte("manifest"))
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{
			{after: 3 * time.Second, data: frame[:3]},
			{after: 3 * time.Second, data: frame[3:6]},
			{after: 3 * time.Second, data: frame[6:10]},
			{after: 3 * time.Second, data: frame[10:]},
			{err: io.EOF},
		},
	}
	peer, events := newFrameLivenessPeer(t, clock, conn)
	peer.readPolicy.idleTimeout = 5 * time.Second
	peer.readPolicy.minimumFrameRate = 1

	err := peer.readLoop(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, io.EOF))
	require.Len(t, events, 1)
	event := <-events
	assert.Equal(t, uint16(message.TypeManifests), event.MessageType)
	assert.Equal(t, []byte("manifest"), event.Payload)
	assert.Equal(t, 12*time.Second, clock.current().Sub(time.Unix(1_000, 0)))
	require.GreaterOrEqual(t, len(conn.deadlines), 4)
	for i := 1; i < 4; i++ {
		assert.True(t, conn.deadlines[i].After(conn.deadlines[i-1]))
	}
}

func TestPeerReadLoopClassifiesWrappedMidFrameTimeoutAsReadIdle(t *testing.T) {
	clock := &livenessClock{now: time.Unix(2_000, 0)}
	frame := manifestFrame(t, []byte("manifest"))
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{
			{data: frame[:6]},
			{after: time.Second, data: frame[6:10]},
			{after: 10 * time.Second, data: frame[10:]},
		},
	}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	peer.readPolicy.idleTimeout = 5 * time.Second
	peer.readPolicy.minimumFrameRate = 1

	err := peer.readLoop(context.Background())
	require.ErrorIs(t, err, ErrReadIdle)
}

func TestPeerReadLoopReportsPartialFrameProgress(t *testing.T) {
	clock := &livenessClock{now: time.Unix(2_500, 0)}
	payload := []byte("manifest")
	frame := manifestFrame(t, payload)
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{
			{data: frame[:HeaderSizeUncompressed]},
			{after: 2 * time.Second, data: frame[HeaderSizeUncompressed : HeaderSizeUncompressed+3], err: io.ErrUnexpectedEOF},
		},
	}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	peer.readPolicy.minimumFrameRate = 1
	bootstrapReady := false
	peer.onBootstrapReady = func() { bootstrapReady = true }

	err := peer.readLoop(context.Background())
	var frameErr *FrameReadError
	require.ErrorAs(t, err, &frameErr)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, TypeManifests, frameErr.MessageType)
	assert.Equal(t, uint32(len(payload)), frameErr.WireSize)
	assert.False(t, frameErr.Compressed)
	assert.Equal(t, uint64(3), frameErr.BytesRead)
	assert.Equal(t, 2*time.Second, frameErr.Elapsed)
	assert.Contains(t, frameErr.Error(), "rate=1B/s")
	assert.False(t, bootstrapReady)
}

func TestPeerReadLoopRejectsDroppedBootstrapManifest(t *testing.T) {
	clock := &livenessClock{now: time.Unix(2_750, 0)}
	frame := manifestFrame(t, []byte("manifest"))
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{{data: frame}},
	}
	peer, events := newFrameLivenessPeer(t, clock, conn)
	for range cap(events) {
		events <- Event{}
	}
	bootstrapReady := false
	peer.onBootstrapReady = func() { bootstrapReady = true }

	err := peer.readLoop(context.Background())
	require.ErrorIs(t, err, errBootstrapManifestDropped)
	assert.False(t, bootstrapReady)
}

func TestPeerReadLoopRejectsTricklePastFrameBudget(t *testing.T) {
	clock := &livenessClock{now: time.Unix(3_000, 0)}
	frame := manifestFrame(t, []byte("slow"))
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{
			{data: frame[:6]},
			{after: time.Second, data: frame[6:7]},
			{after: time.Second, data: frame[7:8]},
			{after: time.Second, data: frame[8:9]},
			{after: time.Second, data: frame[9:]},
		},
	}
	peer, events := newFrameLivenessPeer(t, clock, conn)
	peer.readPolicy.idleTimeout = 2 * time.Second
	peer.readPolicy.minimumFrameRate = 2

	err := peer.readLoop(context.Background())
	require.ErrorIs(t, err, ErrFrameReadTooSlow)
	assert.Empty(t, events)
}

func TestPeerPingTimeoutDefersOnlyForBlockingFrame(t *testing.T) {
	clock := &livenessClock{now: time.Unix(4_000, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1
	reader := peer.newFrameProgressReader(bytes.NewReader([]byte{1, 2}), nil)
	require.NoError(t, reader.setHeader(MessageHeader{PayloadSize: 100}))

	clock.advance(time.Second)
	buf := make([]byte, 1)
	_, err := reader.Read(buf)
	require.NoError(t, err)
	sentAt := clock.current()
	peer.recordPingSent(7, sentAt)

	clock.advance(time.Second)
	_, err = reader.Read(buf)
	require.NoError(t, err)
	clock.advance(pingTimeout - time.Second)

	require.NoError(t, peer.runPingTick(clock.current()))
	assert.Zero(t, peer.SendQueueLen())
	reader.finish(true, clock.current())
	require.NoError(t, peer.runPingTick(clock.current()))

	peer.OnPong(7, clock.current().Add(time.Millisecond))
	_, _, stale := peer.staleInFlightPing(clock.current().Add(time.Hour), pingTimeout)
	assert.False(t, stale)
}

func TestPeerPingSentBeforeFrameUsesNextFrameProgress(t *testing.T) {
	clock := &livenessClock{now: time.Unix(4_500, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1
	sentAt := clock.current()
	peer.recordPingSent(8, sentAt)

	reader := peer.newFrameProgressReader(bytes.NewReader([]byte{1}), nil)
	require.NoError(t, reader.setHeader(MessageHeader{PayloadSize: 100}))
	clock.advance(time.Second)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	clock.advance(pingTimeout - time.Second)

	_, _, stale, deferred := peer.pingTimeoutStatus(clock.current(), pingTimeout)
	assert.False(t, stale)
	assert.True(t, deferred)
}

func TestPeerStoppedFrameProgressNoLongerDefersPing(t *testing.T) {
	clock := &livenessClock{now: time.Unix(4_600, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1
	sentAt := clock.current()
	peer.recordPingSent(8, sentAt)

	reader := peer.newFrameProgressReader(bytes.NewReader([]byte{1}), nil)
	require.NoError(t, reader.setHeader(MessageHeader{PayloadSize: 100}))
	clock.advance(time.Second)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	clock.advance(readIdleDeadline)

	_, _, stale, deferred := peer.pingTimeoutStatus(clock.current(), pingTimeout)
	assert.True(t, stale)
	assert.False(t, deferred)
}

func TestPeerCompletedFrameGraceExpiresWithoutPong(t *testing.T) {
	clock := &livenessClock{now: time.Unix(4_700, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1
	sentAt := clock.current()
	peer.recordPingSent(8, sentAt)

	reader := peer.newFrameProgressReader(bytes.NewReader([]byte{1}), nil)
	require.NoError(t, reader.setHeader(MessageHeader{PayloadSize: 100}))
	clock.advance(time.Second)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	clock.advance(pingTimeout - time.Second)
	reader.finish(true, clock.current())

	_, _, stale, deferred := peer.pingTimeoutStatus(clock.current(), pingTimeout)
	assert.False(t, stale)
	assert.True(t, deferred)
	clock.advance(peer.readPolicy.pingDispatchGrace)
	_, _, stale, deferred = peer.pingTimeoutStatus(clock.current(), pingTimeout)
	assert.True(t, stale)
	assert.False(t, deferred)
}

func TestPeerReadLoopDispatchesPongBehindSlowFrame(t *testing.T) {
	clock := &livenessClock{now: time.Unix(4_800, 0)}
	manifest := manifestFrame(t, []byte("manifest"))
	pong, err := message.EncodeFrame(&message.Ping{PType: message.PingTypePong, Seq: 11})
	require.NoError(t, err)
	conn := &chunkedLivenessConn{
		clock: clock,
		steps: []connReadStep{
			{data: manifest[:6]},
			{after: 30 * time.Second, data: manifest[6:10]},
			{after: 30 * time.Second, data: manifest[10:]},
			{after: time.Second, data: pong},
			{err: io.EOF},
		},
	}
	peer, events := newFrameLivenessPeer(t, clock, conn)
	peer.readPolicy.minimumFrameRate = 1
	peer.recordPingSent(11, clock.current())

	err = peer.readLoop(context.Background())
	require.Error(t, err)
	require.Len(t, events, 2)
	manifestEvent := <-events
	pongEvent := <-events
	assert.Equal(t, uint16(message.TypeManifests), manifestEvent.MessageType)
	assert.Equal(t, uint16(message.TypePing), pongEvent.MessageType)
	decoded, err := message.Decode(message.TypePing, pongEvent.Payload)
	require.NoError(t, err)
	peer.OnPong(decoded.(*message.Ping).Seq, clock.current())
	_, _, stale := peer.staleInFlightPing(clock.current().Add(time.Hour), pingTimeout)
	assert.False(t, stale)
}

func TestPeerConsecutiveFramesDeferPingWithinProgressBudget(t *testing.T) {
	clock := &livenessClock{now: time.Unix(5_000, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1

	first := peer.newFrameProgressReader(bytes.NewReader([]byte{1}), nil)
	require.NoError(t, first.setHeader(MessageHeader{PayloadSize: 1}))
	sentAt := clock.current()
	peer.recordPingSent(9, sentAt)
	clock.advance(time.Second)
	_, err := first.Read(make([]byte, 1))
	require.NoError(t, err)
	first.finish(true, clock.current())

	second := peer.newFrameProgressReader(bytes.NewReader([]byte{2}), nil)
	require.NoError(t, second.setHeader(MessageHeader{PayloadSize: 100}))
	clock.advance(pingTimeout)
	_, err = second.Read(make([]byte, 1))
	require.NoError(t, err)

	_, _, stale, deferred := peer.pingTimeoutStatus(clock.current(), pingTimeout)
	assert.False(t, stale)
	assert.True(t, deferred)
}

func TestPeerConsecutiveFramesCannotExtendPingPastProgressBudget(t *testing.T) {
	clock := &livenessClock{now: time.Unix(5_500, 0)}
	peer := newLatencyTestPeer(t)
	peer.readPolicy.now = clock.current
	peer.readPolicy.idleTimeout = readIdleDeadline
	peer.readPolicy.minimumFrameRate = 1
	sentAt := clock.current()
	peer.recordPingSent(10, sentAt)

	budget := peer.frameReadBudget(MaxMessageSize)
	clock.advance(budget - time.Second)
	reader := peer.newFrameProgressReader(bytes.NewReader([]byte{1}), nil)
	require.NoError(t, reader.setHeader(MessageHeader{PayloadSize: MaxMessageSize}))
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	clock.advance(time.Second)

	seq, _, stale, deferred := peer.pingTimeoutStatus(clock.current(), pingTimeout)
	require.True(t, stale)
	assert.False(t, deferred)
	assert.Equal(t, uint32(10), seq)
}

func TestPeerWriteLoopUsesSizeAwarePerFrameDeadline(t *testing.T) {
	clock := &livenessClock{now: time.Unix(6_000, 0)}
	conn := &chunkedLivenessConn{clock: clock}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.writeLoop(ctx) }()

	require.NoError(t, peer.Send([]byte("large ordered frame")))
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.writeCalls == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	conn.mu.Lock()
	defer conn.mu.Unlock()
	assert.Equal(t, 1, conn.writeLimits)
}

func TestPeerWriteLoopMapsWriteTimeout(t *testing.T) {
	clock := &livenessClock{now: time.Unix(6_100, 0)}
	conn := &chunkedLivenessConn{clock: clock, writeErr: livenessTimeoutError{}}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	require.NoError(t, peer.Send([]byte("stalled frame")))
	err := peer.writeLoop(t.Context())
	require.ErrorIs(t, err, ErrWriteIdle)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	assert.Equal(t, 1, conn.writeLimits)
}

func TestPeerWriteLoopCompletesPartialWritesAndReleasesInFlight(t *testing.T) {
	clock := &livenessClock{now: time.Unix(6_200, 0)}
	conn := &partialWriteConn{
		chunkedLivenessConn: chunkedLivenessConn{clock: clock},
		limit:               3,
	}
	peer, _ := newFrameLivenessPeer(t, clock, conn)
	frame := []byte("frame requiring complete writes")
	require.NoError(t, peer.Send(frame))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.writeLoop(ctx) }()
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.wire) == len(frame)
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	conn.mu.Lock()
	assert.Equal(t, frame, conn.wire)
	assert.Greater(t, conn.writeCalls, 1)
	conn.mu.Unlock()
	snapshot := peer.outbound.snapshot()
	assert.Zero(t, snapshot.TotalFrames)
	assert.Zero(t, snapshot.TotalBytes)
	assert.Zero(t, snapshot.InFlight)
}
