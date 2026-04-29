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

	"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls/shim"
)

const (
	finishedBufSize = 1024
	pumpBufSize     = 16 * 1024 // max TLS record size
)

func Client(inner net.Conn, cfg *Config) (PeerConn, error) {
	return newConn(inner, cfg, false)
}

func NewListener(inner net.Listener, cfg *Config) net.Listener {
	return &listener{inner: inner, cfg: cfg}
}

type listener struct {
	inner net.Listener
	cfg   *Config
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	pc, err := newConn(c, l.cfg, true)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return pc, nil
}

func (l *listener) Close() error   { return l.inner.Close() }
func (l *listener) Addr() net.Addr { return l.inner.Addr() }

// conn is the OpenSSL-backed PeerConn.
//
// Locks:
//   - sslMu: every SSL_* / BIO_* call. OpenSSL is not goroutine-safe at
//     the SSL level. Never held across inner.Read/Write.
//   - inMu / outMu: serialize Read and Write callers respectively, and
//     guard inner.Read / inner.Write so concurrent Read+Write can run
//     without re-entering the wire from two goroutines.
//   - closed: set by Close before SSL/CTX are freed; sslMu critical
//     sections check it to avoid use-after-free.
type conn struct {
	inner net.Conn

	sslMu     sync.Mutex
	ctx       *shim.Ctx
	ssl       *shim.SSL
	handshake bool

	inMu  sync.Mutex
	outMu sync.Mutex

	// pendingIn buffers bytes the BIO ring couldn't accept on the last
	// pump. Owned by inMu.
	pendingIn []byte

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func newConn(inner net.Conn, cfg *Config, isServer bool) (*conn, error) {
	if cfg == nil || len(cfg.CertPEM) == 0 || len(cfg.KeyPEM) == 0 {
		return nil, errors.New("peertls: Config requires CertPEM and KeyPEM")
	}
	ctx, err := shim.NewCtx(isServer)
	if err != nil {
		return nil, err
	}
	if err := ctx.UseCertPEM(cfg.CertPEM, cfg.KeyPEM); err != nil {
		ctx.Free()
		return nil, fmt.Errorf("peertls: load cert: %w", err)
	}
	s, err := ctx.NewSSL()
	if err != nil {
		ctx.Free()
		return nil, err
	}
	return &conn{inner: inner, ctx: ctx, ssl: s}, nil
}

// HandshakeContext is idempotent; ctx cancellation is propagated to the
// inner conn via SetDeadline so a blocked Read/Write returns promptly.
func (c *conn) HandshakeContext(ctx context.Context) error {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	c.outMu.Lock()
	defer c.outMu.Unlock()

	if dl, ok := ctx.Deadline(); ok {
		if err := c.inner.SetDeadline(dl); err != nil {
			return err
		}
		defer func() { _ = c.inner.SetDeadline(time.Time{}) }()
	}
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() {
			_ = c.inner.SetDeadline(time.Unix(1, 0))
		})
		defer stop()
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		out, done, err := c.handshakeStep()
		if len(out) > 0 {
			if _, werr := c.inner.Write(out); werr != nil {
				return werr
			}
		}
		if done {
			return nil
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			continue
		case errors.Is(err, shim.ErrWantRead):
			// fall through to pump
		default:
			return fmt.Errorf("peertls: handshake: %w", err)
		}

		if err := c.pumpInboundLocked(); err != nil {
			return fmt.Errorf("peertls: handshake: %w", err)
		}
	}
}

func (c *conn) handshakeStep() (out []byte, done bool, err error) {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return nil, false, net.ErrClosed
	}
	if c.handshake {
		return nil, true, nil
	}
	err = c.ssl.Handshake()
	out = c.drainBIOLocked()
	if err == nil {
		c.handshake = true
		return out, true, nil
	}
	return out, false, err
}

// pumpInboundLocked reads one chunk from inner into the BIO. Caller owns
// inMu. BIO_write has partial-accept semantics: any tail is buffered on
// pendingIn and drained on the next call so pipelined records don't drop.
func (c *conn) pumpInboundLocked() error {
	if len(c.pendingIn) > 0 {
		return c.bioWriteAllLocked()
	}

	buf := make([]byte, pumpBufSize)
	n, err := c.inner.Read(buf)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if n == 0 {
		return nil
	}

	c.sslMu.Lock()
	if c.closed.Load() {
		c.sslMu.Unlock()
		return net.ErrClosed
	}
	w, werr := c.ssl.BIOWrite(buf[:n])
	c.sslMu.Unlock()
	if werr != nil {
		return werr
	}
	if w < n {
		c.pendingIn = append(c.pendingIn[:0], buf[w:n]...)
	}
	return nil
}

func (c *conn) bioWriteAllLocked() error {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	w, werr := c.ssl.BIOWrite(c.pendingIn)
	if werr != nil {
		return werr
	}
	c.pendingIn = c.pendingIn[w:]
	return nil
}

// drainBIOLocked drains pending BIO output. Caller holds sslMu.
func (c *conn) drainBIOLocked() []byte {
	out := make([]byte, 0, pumpBufSize)
	buf := make([]byte, pumpBufSize)
	for {
		n, err := c.ssl.BIORead(buf)
		if err != nil || n == 0 {
			return out
		}
		out = append(out, buf[:n]...)
	}
}

func (c *conn) Read(b []byte) (int, error) {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	if !c.handshakeReady() {
		return 0, ErrHandshakeIncomplete
	}
	if len(b) == 0 {
		return 0, nil
	}

	for {
		n, out, err := c.sslReadStep(b)
		if len(out) > 0 {
			if werr := c.writeToInner(out); werr != nil {
				return 0, werr
			}
		}
		if n > 0 {
			return n, nil
		}
		switch {
		case errors.Is(err, shim.ErrWantRead):
			if perr := c.pumpInboundLocked(); perr != nil {
				return 0, perr
			}
		case errors.Is(err, shim.ErrWantWrite):
			continue
		case errors.Is(err, shim.ErrZeroRet):
			return 0, io.EOF
		case err == nil:
			// (0, nil) violates the shim contract; surface it.
			return 0, errors.New("peertls: SSL_read returned 0 bytes with no error")
		default:
			return 0, err
		}
	}
}

func (c *conn) sslReadStep(b []byte) (n int, out []byte, err error) {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return 0, nil, net.ErrClosed
	}
	n, err = c.ssl.Read(b)
	out = c.drainBIOLocked()
	return
}

func (c *conn) Write(b []byte) (int, error) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if !c.handshakeReady() {
		return 0, ErrHandshakeIncomplete
	}

	written := 0
	for written < len(b) {
		n, out, err := c.sslWriteStep(b[written:])
		if len(out) > 0 {
			if _, werr := c.inner.Write(out); werr != nil {
				return written, werr
			}
		}
		if err == nil {
			written += n
			continue
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			continue
		case errors.Is(err, shim.ErrWantRead):
			// Renegotiation is disabled, so this is a protocol error.
			return written, errors.New("peertls: unexpected WANT_READ from SSL_write (renegotiation?)")
		default:
			return written, err
		}
	}
	return written, nil
}

func (c *conn) sslWriteStep(b []byte) (n int, out []byte, err error) {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return 0, nil, net.ErrClosed
	}
	n, err = c.ssl.Write(b)
	out = c.drainBIOLocked()
	return
}

// writeToInner serializes inner.Write with Write callers (used by the
// Read drain path; Write holds outMu itself).
func (c *conn) writeToInner(p []byte) error {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	_, err := c.inner.Write(p)
	return err
}

func (c *conn) handshakeReady() bool {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	return c.handshake
}

// Close closes inner first to unblock any pending Read/Write, then frees
// SSL/CTX under sslMu. The closed flag guards against use-after-free.
func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeErr = c.inner.Close()

		c.sslMu.Lock()
		defer c.sslMu.Unlock()
		if c.ssl != nil {
			c.ssl.Free()
			c.ssl = nil
		}
		if c.ctx != nil {
			c.ctx.Free()
			c.ctx = nil
		}
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
	if c.closed.Load() {
		return nil, nil, net.ErrClosed
	}
	if !c.handshake {
		return nil, nil, ErrHandshakeIncomplete
	}
	localBuf := make([]byte, finishedBufSize)
	peerBuf := make([]byte, finishedBufSize)

	// SSL_get_finished returns the FULL length even on truncation —
	// reject ln > buf to match rippled instead of silently hashing a
	// short copy.
	ln := c.ssl.GetFinished(localBuf)
	if ln < 12 || ln > len(localBuf) {
		return nil, nil, fmt.Errorf("peertls: local Finished length %d", ln)
	}
	pn := c.ssl.GetPeerFinished(peerBuf)
	if pn < 12 || pn > len(peerBuf) {
		return nil, nil, fmt.Errorf("peertls: peer Finished length %d", pn)
	}
	return localBuf[:ln:ln], peerBuf[:pn:pn], nil
}
