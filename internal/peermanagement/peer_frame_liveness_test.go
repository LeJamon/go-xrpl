package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
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

func (c *chunkedLivenessConn) Write(src []byte) (int, error) { return len(src), nil }
func (c *chunkedLivenessConn) Close() error                  { return nil }
func (c *chunkedLivenessConn) LocalAddr() net.Addr           { return livenessAddr("local") }
func (c *chunkedLivenessConn) RemoteAddr() net.Addr          { return livenessAddr("remote") }
func (c *chunkedLivenessConn) SetDeadline(time.Time) error   { return nil }
func (c *chunkedLivenessConn) SetWriteDeadline(time.Time) error {
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
	assert.Empty(t, peer.send)
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
