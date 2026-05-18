//go:build !cgo

// Package peertls provides the XRPL session-signature TLS wrapper used
// by the peer overlay. The handshake binds the session's TLS Finished
// bytes into a peer-protocol signature; the only available
// implementation is the OpenSSL shim under the cgo build tag.
//
// This !cgo file is a fail-loud stub: a production build MUST enable
// cgo and link OpenSSL.
package peertls

import (
	"context"
	"log/slog"
	"net"
	"time"
)

func Client(_ net.Conn, _ *Config) (PeerConn, error) {
	return nil, ErrSessionSigUnsupported
}

// NewListener under !cgo logs a hard error and closes the inner
// listener so the overlay's acceptLoop sees net.ErrClosed on the very
// first Accept rather than spinning on ErrSessionSigUnsupported.
func NewListener(inner net.Listener, _ *Config) net.Listener {
	slog.Error("peertls: NewListener called in !cgo build; XRPL peer protocol requires the cgo OpenSSL shim — closing listener",
		"t", "peertls/stub", "addr", inner.Addr())
	_ = inner.Close()
	return &stubListener{inner: inner}
}

type stubListener struct{ inner net.Listener }

var _ net.Listener = (*stubListener)(nil)

func (s *stubListener) Accept() (net.Conn, error) {
	c, err := s.inner.Accept()
	if err != nil {
		return nil, err
	}
	_ = c.Close()
	return nil, ErrSessionSigUnsupported
}

func (s *stubListener) Close() error   { return s.inner.Close() }
func (s *stubListener) Addr() net.Addr { return s.inner.Addr() }

type stubConn struct{}

var _ PeerConn = (*stubConn)(nil)

func (s *stubConn) Read([]byte) (int, error)               { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Write([]byte) (int, error)              { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Close() error                           { return nil }
func (s *stubConn) LocalAddr() net.Addr                    { return nil }
func (s *stubConn) RemoteAddr() net.Addr                   { return nil }
func (s *stubConn) SetDeadline(time.Time) error            { return ErrSessionSigUnsupported }
func (s *stubConn) SetReadDeadline(time.Time) error        { return ErrSessionSigUnsupported }
func (s *stubConn) SetWriteDeadline(time.Time) error       { return ErrSessionSigUnsupported }
func (s *stubConn) HandshakeContext(context.Context) error { return ErrSessionSigUnsupported }
func (s *stubConn) SharedValue() ([]byte, error)           { return nil, ErrSessionSigUnsupported }
