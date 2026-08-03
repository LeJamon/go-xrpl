package node

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConnectionLimiterEnforcesExactConfiguredMaximum(t *testing.T) {
	limiter := newConnectionLimiter(3)
	require.True(t, limiter.tryAcquire("http", 2))
	require.True(t, limiter.tryAcquire("http", 2))
	require.False(t, limiter.tryAcquire("http", 2))
	require.True(t, limiter.tryAcquire("ws", 2))
	require.False(t, limiter.tryAcquire("other", 0))

	limiter.release("http")
	require.True(t, limiter.tryAcquire("other", 0))
}

func TestConnectionLimiterDefaultAndDisabledGlobalBounds(t *testing.T) {
	bounded := newConnectionLimiter(0)
	for range defaultMaxRPCConnections {
		require.True(t, bounded.tryAcquire("http", 0))
	}
	require.False(t, bounded.tryAcquire("http", 0))

	unbounded := newConnectionLimiter(-1)
	for range defaultMaxRPCConnections + 1 {
		require.True(t, unbounded.tryAcquire("http", 0))
	}
}

func TestLimitedConnReleasesExactlyOnce(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	var releases atomic.Int32
	conn := &limitedConn{
		Conn:           server,
		releaseOnClose: &releaseOnClose{release: func() { releases.Add(1) }},
	}

	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, releases.Load())
}

func TestLimitedConnConcurrentCloseReleasesExactlyOnce(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	var releases atomic.Int32
	conn := &limitedConn{
		Conn:           server,
		releaseOnClose: &releaseOnClose{release: func() { releases.Add(1) }},
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = conn.Close()
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, releases.Load())
}

func TestLimitedConnReleasesWhenUnderlyingCloseFails(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	wantErr := errors.New("injected close failure")
	var releases atomic.Int32
	conn := &limitedConn{
		Conn:           &errorCloseConn{Conn: server, err: wantErr},
		releaseOnClose: &releaseOnClose{release: func() { releases.Add(1) }},
	}

	require.ErrorIs(t, conn.Close(), wantErr)
	require.EqualValues(t, 1, releases.Load())
}

func TestLimitedListenerPreservesTCPOptionalInterfaces(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	limited := &limitedListener{
		Listener:  listener,
		limiter:   newConnectionLimiter(-1),
		portName:  "http",
		portLimit: 1,
	}
	t.Cleanup(func() { _ = limited.Close() })

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	conn, err := limited.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.IsType(t, &limitedTCPConn{}, conn)
	require.Implements(t, (*io.ReaderFrom)(nil), conn)
	require.Implements(t, (*interface{ CloseWrite() error })(nil), conn)
	require.Implements(t, (*syscall.Conn)(nil), conn)
}

func TestLimitedListenerRejectsAtCapacityAndAdmitsAfterClose(t *testing.T) {
	listener := newQueuedListener()
	t.Cleanup(func() { _ = listener.Close() })
	limited := &limitedListener{
		Listener:  listener,
		limiter:   newConnectionLimiter(-1),
		portName:  "http",
		portLimit: 1,
	}

	firstServer, firstClient := net.Pipe()
	t.Cleanup(func() { _ = firstClient.Close() })
	listener.enqueue(firstServer)
	first, err := limited.Accept()
	require.NoError(t, err)

	secondServer, secondClient := net.Pipe()
	t.Cleanup(func() { _ = secondClient.Close() })
	listener.enqueue(secondServer)

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, acceptError := limited.Accept()
		if acceptError != nil {
			acceptErr <- acceptError
			return
		}
		accepted <- conn
	}()

	require.NoError(t, secondClient.SetReadDeadline(time.Now().Add(time.Second)))
	buffer := make([]byte, 1)
	_, err = secondClient.Read(buffer)
	require.Error(t, err)

	require.NoError(t, first.Close())
	thirdServer, thirdClient := net.Pipe()
	t.Cleanup(func() { _ = thirdClient.Close() })
	listener.enqueue(thirdServer)

	select {
	case conn := <-accepted:
		require.NoError(t, conn.Close())
	case err := <-acceptErr:
		t.Fatalf("Accept returned error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("listener did not admit a connection after the slot was released")
	}
}

type queuedListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

type errorCloseConn struct {
	net.Conn
	err error
}

func (c *errorCloseConn) Close() error {
	_ = c.Conn.Close()
	return c.err
}

func newQueuedListener() *queuedListener {
	return &queuedListener{
		connections: make(chan net.Conn, 4),
		closed:      make(chan struct{}),
	}
}

func (l *queuedListener) enqueue(conn net.Conn) {
	l.connections <- conn
}

func (l *queuedListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *queuedListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *queuedListener) Addr() net.Addr {
	return testAddr("queued")
}

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

var _ net.Listener = (*queuedListener)(nil)
var _ net.Conn = (*limitedConn)(nil)
