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
	// finishedBufSize is the working buffer size for SSL_get_finished /
	// SSL_get_peer_finished. TLS 1.2 verify_data is 12 bytes for every
	// cipher suite the shim's pinned cipher list negotiates
	// (TLSv1.2:!CBC:!DSS:!PSK:!eNULL:!aNULL); 64 is comfortable headroom
	// and OpenSSL truncates / reports the actual length anyway.
	finishedBufSize = 64
	// pumpBufSize sizes the working buffers for moving TLS records
	// between the network BIO and the underlying net.Conn. 16 KiB is
	// the maximum TLS record size.
	pumpBufSize = 16 * 1024
)

// Client wraps an existing net.Conn (typically a *net.TCPConn) as the
// client side of a peertls connection. Caller must subsequently invoke
// HandshakeContext.
func Client(inner net.Conn, cfg *Config) (PeerConn, error) {
	return newConn(inner, cfg, false)
}

// NewListener returns a net.Listener whose Accept produces server-side
// PeerConns.
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

// conn is the OpenSSL-backed PeerConn implementation.
//
// Concurrency model:
//
//   - sslMu serializes every operation on the underlying SSL_*
//     (SSL_read, SSL_write, SSL_do_handshake, BIO drain/fill, free).
//     OpenSSL is not goroutine-safe at the SSL object level. This
//     mutex is NEVER held across a blocking inner.Read/Write so a
//     stalled Read can't starve a concurrent Write.
//   - inMu serializes Read callers; only one goroutine ever calls
//     c.inner.Read at a time. Mirrors crypto/tls.Conn.in.
//   - outMu serializes Write callers and inner.Write across the
//     Read-drain path (which may emit alerts) and the Write-drain
//     path (which emits encrypted records). Mirrors
//     crypto/tls.Conn.out.
//   - closed is set under sslMu by Close before SSL/CTX are freed;
//     every sslMu critical section that touches c.ssl checks it
//     after locking to avoid use-after-free.
type conn struct {
	inner net.Conn

	sslMu     sync.Mutex
	ctx       *shim.Ctx
	ssl       *shim.SSL
	handshake bool

	inMu  sync.Mutex
	outMu sync.Mutex

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

// HandshakeContext drives the TLS handshake. Idempotent. ctx
// cancellation is wired into the underlying conn via SetDeadline:
// crossing the deadline interrupts any blocked inner I/O so the
// handshake can return promptly.
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
			// Drained above; loop and retry.
			continue
		case errors.Is(err, shim.ErrWantRead):
			// Need bytes from the wire — fall through to pump.
		default:
			return fmt.Errorf("peertls: handshake: %w", err)
		}

		if err := c.pumpInboundLocked(); err != nil {
			return fmt.Errorf("peertls: handshake: %w", err)
		}
	}
}

// handshakeStep performs one SSL_do_handshake call under sslMu and
// drains any output bytes the call produced. Caller writes out to
// inner outside sslMu.
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

// pumpInboundLocked reads one chunk from inner and feeds it into the
// BIO. The "Locked" suffix denotes the caller owns inMu (so only one
// goroutine ever reads from inner). sslMu is acquired internally only
// for the BIO_write — never held across inner.Read.
func (c *conn) pumpInboundLocked() error {
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
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	_, werr := c.ssl.BIOWrite(buf[:n])
	return werr
}

// drainBIOLocked reads everything currently pending on the network
// BIO into a fresh slice. Caller must hold sslMu.
func (c *conn) drainBIOLocked() []byte {
	var out []byte
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
			// Drained above; loop.
			continue
		case errors.Is(err, shim.ErrZeroRet):
			return 0, io.EOF
		case err == nil:
			// Spurious 0-byte SSL_read; treat as WantRead.
			if perr := c.pumpInboundLocked(); perr != nil {
				return 0, perr
			}
		default:
			return 0, err
		}
	}
}

// sslReadStep runs SSL_read under sslMu and drains any output.
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
			// Output drained above; loop.
			continue
		case errors.Is(err, shim.ErrWantRead):
			// SSL_write only returns WANT_READ during renegotiation,
			// which is disabled via SSL_OP_NO_RENEGOTIATION. Treat as
			// a protocol error.
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

// writeToInner writes p to inner serialized with all other writers.
// Used by Read's drain path; Write itself already holds outMu.
func (c *conn) writeToInner(p []byte) error {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	_, err := c.inner.Write(p)
	return err
}

// handshakeReady reports whether the handshake completed. Safe to call
// after either inMu or outMu is held: HandshakeContext sets
// c.handshake while holding both, so the happens-before edge propagates
// to subsequent Read/Write callers via either mutex.
func (c *conn) handshakeReady() bool {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	return c.handshake
}

// Close tears down the connection. The underlying net.Conn is closed
// FIRST so any goroutine blocked in inner.Read/Write returns
// immediately with an error; SSL/CTX are then freed under sslMu. The
// closed flag prevents any in-flight SSL operation that re-acquires
// sslMu from touching freed memory.
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

// SharedValue computes the rippled-compatible 32-byte shared value:
// sha512Half(sha512(local_finished) XOR sha512(peer_finished)).
func (c *conn) SharedValue() ([]byte, error) {
	c.sslMu.Lock()
	defer c.sslMu.Unlock()
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	if !c.handshake {
		return nil, ErrHandshakeIncomplete
	}
	local := make([]byte, finishedBufSize)
	peer := make([]byte, finishedBufSize)

	ln := c.ssl.GetFinished(local)
	if ln < 12 {
		return nil, fmt.Errorf("peertls: local Finished too short (%d bytes)", ln)
	}
	pn := c.ssl.GetPeerFinished(peer)
	if pn < 12 {
		return nil, fmt.Errorf("peertls: peer Finished too short (%d bytes)", pn)
	}
	return computeSharedValue(local[:ln], peer[:pn])
}
