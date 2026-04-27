//go:build cgo

package peertls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls/shim"
)

const (
	// TLS 1.2 verify_data is exactly 12 bytes for the default PRF; we
	// allocate a comfortable upper bound (RFC 5246 ¶7.4.9 lets cipher
	// suites override but none in our pinned TLS_*WITH_*_GCM* set do).
	finishedBufSize = 64
	// Pump buffer for moving TLS records between BIO and net.Conn.
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

type conn struct {
	inner net.Conn

	mu        sync.Mutex // guards ssl + handshakeDone
	ctx       *shim.Ctx
	ssl       *shim.SSL
	handshake bool

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

func (c *conn) HandshakeContext(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handshake {
		return nil
	}
	if err := c.pumpUntil(ctx, c.ssl.Handshake); err != nil {
		return fmt.Errorf("peertls: handshake: %w", err)
	}
	c.handshake = true
	return nil
}

// pumpUntil runs op repeatedly, moving bytes between the BIO and the
// underlying net.Conn whenever op returns ErrWantRead / ErrWantWrite.
// Honors ctx cancellation by setting a short read deadline on the
// underlying conn.
func (c *conn) pumpUntil(ctx context.Context, op func() error) error {
	for {
		err := op()
		if err == nil {
			// Even on success, drain any final bytes (e.g. last
			// Finished record on the server side) to the wire.
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
			return nil
		}
		switch {
		case errors.Is(err, shim.ErrWantWrite):
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
		case errors.Is(err, shim.ErrWantRead):
			// First flush anything pending we owe the peer (TLS
			// record we just produced), then read.
			if drainErr := c.drain(ctx); drainErr != nil {
				return drainErr
			}
			if fillErr := c.fill(ctx); fillErr != nil {
				return fillErr
			}
		default:
			return err
		}
	}
}

func (c *conn) drain(ctx context.Context) error {
	buf := make([]byte, pumpBufSize)
	for {
		n, err := c.ssl.BIORead(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := c.applyContextDeadline(ctx); err != nil {
			return err
		}
		if _, werr := c.inner.Write(buf[:n]); werr != nil {
			return werr
		}
	}
}

func (c *conn) fill(ctx context.Context) error {
	buf := make([]byte, pumpBufSize)
	if err := c.applyContextDeadline(ctx); err != nil {
		return err
	}
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
	if _, werr := c.ssl.BIOWrite(buf[:n]); werr != nil {
		return werr
	}
	return nil
}

func (c *conn) applyContextDeadline(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.inner.SetDeadline(dl)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *conn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handshake {
		return 0, ErrHandshakeIncomplete
	}
	for {
		n, err := c.ssl.Read(b)
		if err == nil {
			return n, nil
		}
		if errors.Is(err, shim.ErrWantRead) {
			if ferr := c.fill(nil); ferr != nil {
				return 0, ferr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantWrite) {
			if derr := c.drain(nil); derr != nil {
				return 0, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrZeroRet) {
			return 0, io.EOF
		}
		return 0, err
	}
}

func (c *conn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handshake {
		return 0, ErrHandshakeIncomplete
	}
	written := 0
	for written < len(b) {
		n, err := c.ssl.Write(b[written:])
		if err == nil {
			written += n
			// Flush the encrypted record(s) to the wire.
			if derr := c.drain(nil); derr != nil {
				return written, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantWrite) {
			if derr := c.drain(nil); derr != nil {
				return written, derr
			}
			continue
		}
		if errors.Is(err, shim.ErrWantRead) {
			if ferr := c.fill(nil); ferr != nil {
				return written, ferr
			}
			continue
		}
		return written, err
	}
	return written, nil
}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		// Best-effort close_notify; ignore errors.
		if c.handshake {
			_ = c.ssl.Shutdown()
			_ = c.drain(nil)
		}
		if c.ssl != nil {
			c.ssl.Free()
			c.ssl = nil
		}
		if c.ctx != nil {
			c.ctx.Free()
			c.ctx = nil
		}
		c.mu.Unlock()
		c.closeErr = c.inner.Close()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
