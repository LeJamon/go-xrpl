//go:build cgo

package peertls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls/shim"
)

const (
	finishedBufSize = 1024
	pumpBufSize     = 16 * 1024
	bioOutBufSize   = 32 * 1024
)

// pumpBufPool recycles scratch space used to move encrypted input into the BIO.
var pumpBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, pumpBufSize)
		return &buf
	},
}

func getPumpBuf() []byte {
	return *(pumpBufPool.Get().(*[]byte))
}

func putPumpBuf(buf []byte) {
	if cap(buf) != pumpBufSize {
		return
	}
	buf = buf[:pumpBufSize]
	pumpBufPool.Put(&buf)
}

var bioOutBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, bioOutBufSize)
		return &buf
	},
}

func getBIOOutBuf() []byte {
	return *(bioOutBufPool.Get().(*[]byte))
}

func putBIOOutBuf(buf []byte) {
	if cap(buf) != bioOutBufSize {
		return
	}
	buf = buf[:bioOutBufSize]
	bioOutBufPool.Put(&buf)
}

type tlsEngine interface {
	Handshake() error
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Shutdown() error
	BIORead([]byte) (int, error)
	BIOWrite([]byte) (int, error)
	GetFinished([]byte) int
	GetPeerFinished([]byte) int
	Free()
}

type connState uint8

const (
	stateNew connState = iota
	stateHandshaking
	stateReady
	stateFailed
	stateShuttingDown
	stateClosed
)

var (
	_ PeerConn     = (*conn)(nil)
	_ net.Listener = (*listener)(nil)
)

// Supported reports whether XRPL session-signature TLS is available in this
// build.
func Supported() bool { return true }

// Client wraps inner as an XRPL TLS client. It does not close inner when
// construction fails.
func Client(inner net.Conn, cfg *Config) (PeerConn, error) {
	if inner == nil {
		return nil, errors.New("peertls: nil connection")
	}
	ctx, err := newContext(cfg, false)
	if err != nil {
		return nil, err
	}
	ssl, err := ctx.NewSSL()
	ctx.Free()
	if err != nil {
		return nil, err
	}
	return newConnWithEngine(inner, ssl), nil
}

// NewListener wraps inner as an XRPL TLS server listener. The configuration is
// validated and copied into a reusable OpenSSL context before it returns.
func NewListener(inner net.Listener, cfg *Config) (net.Listener, error) {
	if inner == nil {
		return nil, errors.New("peertls: nil listener")
	}
	ctx, err := newContext(cfg, true)
	if err != nil {
		return nil, err
	}
	return &listener{inner: inner, ctx: ctx}, nil
}

func newContext(cfg *Config, isServer bool) (*shim.Ctx, error) {
	if cfg == nil || len(cfg.CertPEM) == 0 || len(cfg.KeyPEM) == 0 {
		return nil, errors.New("peertls: Config requires CertPEM and KeyPEM")
	}
	ctx, err := shim.NewCtx(isServer)
	if err != nil {
		return nil, err
	}
	cert := append([]byte(nil), cfg.CertPEM...)
	key := append([]byte(nil), cfg.KeyPEM...)
	if err := ctx.UseCertPEM(cert, key); err != nil {
		ctx.Free()
		return nil, fmt.Errorf("peertls: load cert: %w", err)
	}
	return ctx, nil
}

type listener struct {
	inner net.Listener

	mu     sync.Mutex
	ctx    *shim.Ctx
	closed bool
}

func (l *listener) Accept() (net.Conn, error) {
	l.mu.Lock()
	closed := l.closed || l.ctx == nil
	l.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}

	raw, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	if l.closed || l.ctx == nil {
		l.mu.Unlock()
		_ = raw.Close()
		return nil, net.ErrClosed
	}
	ssl, err := l.ctx.NewSSL()
	l.mu.Unlock()
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return newConnWithEngine(raw, ssl), nil
}

func (l *listener) Close() error {
	err := l.inner.Close()
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		if l.ctx != nil {
			l.ctx.Free()
			l.ctx = nil
		}
	}
	l.mu.Unlock()
	return err
}

func (l *listener) Addr() net.Addr { return l.inner.Addr() }

// Locks:
//   - sslMu owns the engine, TLS state, and terminal TLS error.
//   - inMu owns raw reads, pendingIn, and pendingReadErr.
//   - outMu owns raw writes and pendingOut.
//   - queuedOutMu owns TLS records waiting for the raw writer.
//
// No lock is held across raw I/O except its directional lock. Close closes the
// raw connection before taking sslMu so blocked operations are interrupted.
type conn struct {
	inner net.Conn

	sslMu       sync.Mutex
	ssl         tlsEngine
	state       connState
	terminalErr error

	inMu           sync.Mutex
	pendingIn      []byte
	pendingReadErr error

	outMu      sync.Mutex
	pendingOut []byte

	queuedOutMu sync.Mutex
	queuedOut   []byte

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func newConn(inner net.Conn, cfg *Config, isServer bool) (*conn, error) {
	if inner == nil {
		return nil, errors.New("peertls: nil connection")
	}
	ctx, err := newContext(cfg, isServer)
	if err != nil {
		return nil, err
	}
	ssl, err := ctx.NewSSL()
	ctx.Free()
	if err != nil {
		return nil, err
	}
	return newConnWithEngine(inner, ssl), nil
}

func newConnWithEngine(inner net.Conn, ssl tlsEngine) *conn {
	return &conn{inner: inner, ssl: ssl, state: stateNew}
}

// HandshakeContext performs the TLS handshake. A successful handshake is
// idempotent; any failed attempt leaves the connection terminal.
func (c *conn) HandshakeContext(ctx context.Context) error {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	c.outMu.Lock()
	defer c.releaseOutputLock()

	if err := c.handshakeStatus(); err != ErrHandshakeIncomplete {
		return err
	}

	cleanup, err := c.installContextDeadline(ctx)
	if err != nil {
		return c.failHandshake(err)
	}
	err = c.handshakeLoop(ctx)
	cleanupErr := cleanup()
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	} else if err == nil {
		err = cleanupErr
	}
	if err != nil {
		return c.failHandshake(err)
	}

	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.state == stateClosed || c.closed.Load() {
		return net.ErrClosed
	}
	if c.state == stateFailed {
		return c.terminalErr
	}
	c.state = stateReady
	return nil
}

func (c *conn) handshakeStatus() error {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	switch c.state {
	case stateClosed:
		return net.ErrClosed
	case stateReady:
		return nil
	case stateFailed:
		return c.terminalErr
	case stateShuttingDown:
		return net.ErrClosed
	default:
		c.state = stateHandshaking
		return ErrHandshakeIncomplete
	}
}

func (c *conn) handshakeLoop(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		c.sslMu.Lock()
		if c.state == stateClosed || c.closed.Load() || c.ssl == nil {
			c.sslMu.Unlock()
			return net.ErrClosed
		}
		err := c.ssl.Handshake()
		out, drainErr := c.takeOutputLocked()
		c.queueOutputLocked(out)
		c.sslMu.Unlock()

		if flushErr := c.flushStepOutputLocked(drainErr); flushErr != nil {
			return flushErr
		}
		if err == nil {
			return nil
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			if len(out) == 0 {
				return io.ErrNoProgress
			}
			continue
		case errors.Is(err, shim.ErrWantRead):
			if err := c.pumpInboundLocked(); err != nil {
				return fmt.Errorf("peertls: handshake: %w", err)
			}
		default:
			return fmt.Errorf("peertls: handshake: %w", err)
		}
	}
}

func (c *conn) failHandshake(err error) error {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.state == stateClosed || c.closed.Load() {
		return net.ErrClosed
	}
	if c.state == stateFailed && c.terminalErr != nil {
		return c.terminalErr
	}
	c.state = stateFailed
	c.terminalErr = err
	return err
}

func (c *conn) installContextDeadline(ctx context.Context) (func() error, error) {
	deadline, hasDeadline := ctx.Deadline()
	cancelable := ctx.Done() != nil
	if !hasDeadline && !cancelable {
		return func() error { return nil }, nil
	}
	if hasDeadline {
		if err := c.inner.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	var injected atomic.Bool
	var callbackDone chan struct{}
	var stop func() bool
	if cancelable {
		callbackDone = make(chan struct{})
		stop = context.AfterFunc(ctx, func() {
			if c.inner.SetDeadline(time.Unix(1, 0)) == nil {
				injected.Store(true)
			}
			close(callbackDone)
		})
	}
	return func() error {
		if stop != nil && !stop() {
			<-callbackDone
		}
		if !hasDeadline && !injected.Load() {
			return nil
		}
		// net.Conn exposes no deadline getter. A context deadline necessarily
		// overwrites any prior deadline and is cleared after this operation.
		return c.inner.SetDeadline(time.Time{})
	}, nil
}

// pumpInboundLocked feeds buffered ciphertext before surfacing a raw terminal
// read error. The caller holds inMu.
func (c *conn) pumpInboundLocked() error {
	if len(c.pendingIn) > 0 {
		return c.feedPendingInputLocked()
	}
	if c.pendingReadErr != nil {
		err := c.pendingReadErr
		c.pendingReadErr = nil
		return deferredReadError(err)
	}

	buf := getPumpBuf()
	defer putPumpBuf(buf)
	n, readErr := c.inner.Read(buf)
	if n < 0 || n > len(buf) {
		return c.failTLS(errors.New("peertls: invalid raw read count"))
	}
	if n > 0 {
		c.pendingIn = append(c.pendingIn[:0], buf[:n]...)
		c.pendingReadErr = readErr
		return c.feedPendingInputLocked()
	}
	if readErr != nil {
		return deferredReadError(readErr)
	}
	return io.ErrNoProgress
}

func (c *conn) feedPendingInputLocked() error {
	c.sslMu.Lock()
	if c.state == stateClosed || c.state == stateShuttingDown || c.closed.Load() || c.ssl == nil {
		c.sslMu.Unlock()
		return net.ErrClosed
	}
	n, err := c.ssl.BIOWrite(c.pendingIn)
	c.sslMu.Unlock()
	if n < 0 || n > len(c.pendingIn) {
		return c.failTLS(errors.New("peertls: invalid BIO write count"))
	}
	if n > 0 {
		c.pendingIn = c.pendingIn[n:]
	}
	if err != nil {
		return c.failTLS(err)
	}
	if n == 0 {
		return c.failTLS(io.ErrNoProgress)
	}
	return nil
}

func deferredReadError(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// flushOutputLocked writes queued OpenSSL output without dropping short-write
// tails. The caller holds outMu.
func (c *conn) flushOutputLocked() error {
	for {
		if len(c.pendingOut) == 0 {
			c.queuedOutMu.Lock()
			c.pendingOut = append(c.pendingOut, c.queuedOut...)
			c.queuedOut = c.queuedOut[:0]
			c.queuedOutMu.Unlock()
			if len(c.pendingOut) == 0 {
				return nil
			}
		}
		n, err := c.inner.Write(c.pendingOut)
		if n < 0 || n > len(c.pendingOut) {
			return c.poisonOutput(errors.New("peertls: invalid raw write count"))
		}
		if n > 0 {
			c.pendingOut = c.pendingOut[n:]
		}
		if err != nil {
			return c.poisonOutput(err)
		}
		if n == 0 {
			return c.poisonOutput(io.ErrNoProgress)
		}
	}
}

// takeOutputLocked drains output generated by the immediately preceding SSL
// operation. Keeping the operation and drain in one sslMu critical section
// prevents another goroutine from claiming its ciphertext. The caller holds
// sslMu and owns the returned slice.
func (c *conn) takeOutputLocked() ([]byte, error) {
	buf := getBIOOutBuf()
	defer putBIOOutBuf(buf)
	var out []byte
	for {
		if c.state == stateClosed || c.closed.Load() || c.ssl == nil {
			return out, net.ErrClosed
		}
		n, err := c.ssl.BIORead(buf)
		if n < 0 || n > len(buf) {
			return out, errors.New("peertls: invalid BIO read count")
		}
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			return out, err
		}
		if n == 0 {
			return out, nil
		}
	}
}

// queueOutputLocked runs before releasing sslMu so TLS records retain engine
// operation order even when a raw write is already in progress.
func (c *conn) queueOutputLocked(out []byte) {
	if len(out) == 0 {
		return
	}
	c.queuedOutMu.Lock()
	c.queuedOut = append(c.queuedOut, out...)
	c.queuedOutMu.Unlock()
}

// flushStepOutputLocked flushes queued TLS records through the single
// raw-write path. The caller holds outMu.
func (c *conn) flushStepOutputLocked(drainErr error) error {
	if err := c.flushOutputLocked(); err != nil {
		return err
	}
	if drainErr != nil {
		return c.failTLS(drainErr)
	}
	return nil
}

// releaseOutputLock drains records queued by a concurrent Read before the
// active raw writer relinquishes ownership.
func (c *conn) releaseOutputLock() {
	for {
		if err := c.flushOutputLocked(); err != nil {
			c.outMu.Unlock()
			return
		}
		c.queuedOutMu.Lock()
		if len(c.queuedOut) == 0 {
			c.outMu.Unlock()
			c.queuedOutMu.Unlock()
			return
		}
		c.queuedOutMu.Unlock()
	}
}

func (c *conn) poisonOutput(err error) error {
	terminal := fmt.Errorf("peertls: raw TLS write: %w", err)
	terminal = c.failTLS(terminal)
	_ = c.inner.Close()
	return terminal
}

func (c *conn) failTLS(err error) error {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.state == stateClosed || c.closed.Load() {
		return net.ErrClosed
	}
	if c.state == stateFailed && c.terminalErr != nil {
		return c.terminalErr
	}
	c.state = stateFailed
	c.terminalErr = err
	return err
}

func (c *conn) operationStatus() error {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	return c.operationStatusLocked()
}

func (c *conn) operationStatusLocked() error {
	if c.state == stateClosed || c.state == stateShuttingDown || c.closed.Load() || c.ssl == nil {
		return net.ErrClosed
	}
	if c.state == stateFailed {
		return c.terminalErr
	}
	if c.state != stateReady {
		return ErrHandshakeIncomplete
	}
	return nil
}

func (c *conn) Read(b []byte) (int, error) {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	if err := c.operationStatus(); err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}

	for {
		c.sslMu.Lock()
		if err := c.operationStatusLocked(); err != nil {
			c.sslMu.Unlock()
			return 0, err
		}
		n, err := c.ssl.Read(b)
		out, drainErr := c.takeOutputLocked()
		c.queueOutputLocked(out)
		c.sslMu.Unlock()
		if n < 0 || n > len(b) {
			return 0, c.failTLS(errors.New("peertls: invalid SSL read count"))
		}

		var flushErr error
		if len(out) > 0 || drainErr != nil {
			if c.outMu.TryLock() {
				flushErr = c.flushStepOutputLocked(drainErr)
				c.outMu.Unlock()
			} else {
				if drainErr != nil {
					flushErr = c.failTLS(drainErr)
				}
			}
		}
		if flushErr != nil {
			return n, flushErr
		}
		if n > 0 {
			return n, err
		}
		switch {
		case errors.Is(err, shim.ErrWantRead):
			if err := c.pumpInboundLocked(); err != nil {
				var timeoutErr net.Error
				if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
					return 0, err
				}
				return 0, c.failTLS(err)
			}
		case errors.Is(err, shim.ErrWantWrite):
			if len(out) == 0 {
				return 0, c.failTLS(io.ErrNoProgress)
			}
			continue
		case errors.Is(err, shim.ErrZeroRet):
			return 0, io.EOF
		case err == nil:
			return 0, errors.New("peertls: SSL_read returned 0 bytes with no error")
		default:
			return 0, c.failTLS(err)
		}
	}
}

func (c *conn) Write(b []byte) (int, error) {
	c.outMu.Lock()
	defer c.releaseOutputLock()
	if err := c.operationStatus(); err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	if err := c.flushOutputLocked(); err != nil {
		return 0, err
	}

	written := 0
	for written < len(b) {
		c.sslMu.Lock()
		if err := c.operationStatusLocked(); err != nil {
			c.sslMu.Unlock()
			return written, err
		}
		n, err := c.ssl.Write(b[written:])
		out, drainErr := c.takeOutputLocked()
		c.queueOutputLocked(out)
		c.sslMu.Unlock()
		if n < 0 || n > len(b)-written {
			return written, c.failTLS(errors.New("peertls: invalid SSL write count"))
		}
		written += n

		if flushErr := c.flushStepOutputLocked(drainErr); flushErr != nil {
			return written, flushErr
		}
		if err == nil {
			if n == 0 {
				return written, c.failTLS(io.ErrNoProgress)
			}
			continue
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			if n == 0 && len(out) == 0 {
				return written, c.failTLS(io.ErrNoProgress)
			}
			continue
		case errors.Is(err, shim.ErrWantRead):
			return written, c.failTLS(errors.New("peertls: unexpected WANT_READ from SSL_write"))
		default:
			return written, c.failTLS(err)
		}
	}
	return written, nil
}

// ShutdownContext sends TLS close_notify, bounds raw I/O with ctx, and then
// closes the underlying transport. Close remains the abortive teardown path.
func (c *conn) ShutdownContext(ctx context.Context) (retErr error) {
	lockWaitDone := make(chan struct{})
	stopLockWait := context.AfterFunc(ctx, func() {
		_ = c.Close()
		close(lockWaitDone)
	})
	c.outMu.Lock()
	defer c.releaseOutputLock()
	defer func() {
		if closeErr := c.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	if !stopLockWait() {
		<-lockWaitDone
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.operationStatus(); err != nil {
		return err
	}

	cleanup, err := c.installContextDeadline(ctx)
	if err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		c.sslMu.Lock()
		switch {
		case c.state == stateClosed || c.closed.Load() || c.ssl == nil:
			err = net.ErrClosed
		case c.state == stateFailed:
			err = c.terminalErr
		case c.state == stateReady:
			c.state = stateShuttingDown
		case c.state != stateShuttingDown:
			err = ErrHandshakeIncomplete
		}
		if err != nil {
			c.sslMu.Unlock()
			break
		}
		err = c.ssl.Shutdown()
		out, drainErr := c.takeOutputLocked()
		c.queueOutputLocked(out)
		c.sslMu.Unlock()
		if flushErr := c.flushStepOutputLocked(drainErr); flushErr != nil {
			err = flushErr
			break
		}
		if err == nil || errors.Is(err, shim.ErrWantRead) {
			err = nil
			break
		}
		if errors.Is(err, shim.ErrWantWrite) && len(out) == 0 {
			err = io.ErrNoProgress
			break
		}
		if !errors.Is(err, shim.ErrWantWrite) {
			break
		}
	}
	cleanupErr := cleanup()
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	} else if err == nil {
		err = cleanupErr
	}
	if err != nil {
		return err
	}
	return nil
}

// Close aborts the TLS session and unblocks concurrent raw I/O.
func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeErr = c.inner.Close()

		c.sslMu.Lock()
		c.state = stateClosed
		if c.ssl != nil {
			c.ssl.Free()
			c.ssl = nil
		}
		c.sslMu.Unlock()
	})
	return c.closeErr
}

func (c *conn) LocalAddr() net.Addr  { return c.inner.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr { return c.inner.RemoteAddr() }

func (c *conn) SetDeadline(t time.Time) error      { return c.inner.SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.inner.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.inner.SetWriteDeadline(t) }

func (c *conn) SharedValue() ([]byte, error) {
	localCopy, peerCopy, err := c.snapshotFinishedLocked()
	if err != nil {
		return nil, err
	}
	return computeSharedValue(localCopy, peerCopy)
}

func (c *conn) snapshotFinishedLocked() (local, peer []byte, err error) {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.state == stateClosed || c.state == stateShuttingDown || c.closed.Load() || c.ssl == nil {
		return nil, nil, net.ErrClosed
	}
	if c.state == stateFailed {
		return nil, nil, c.terminalErr
	}
	if c.state != stateReady {
		return nil, nil, ErrHandshakeIncomplete
	}
	localBuf := make([]byte, finishedBufSize)
	peerBuf := make([]byte, finishedBufSize)

	ln := c.ssl.GetFinished(localBuf)
	if ln < 12 || ln > len(localBuf) {
		return nil, nil, fmt.Errorf("peertls: local Finished length %d", ln)
	}
	if c.state == stateClosed || c.closed.Load() || c.ssl == nil {
		return nil, nil, net.ErrClosed
	}
	pn := c.ssl.GetPeerFinished(peerBuf)
	if pn < 12 || pn > len(peerBuf) {
		return nil, nil, fmt.Errorf("peertls: peer Finished length %d", pn)
	}
	return localBuf[:ln:ln], peerBuf[:pn:pn], nil
}
