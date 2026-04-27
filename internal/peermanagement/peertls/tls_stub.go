//go:build !cgo

package peertls

import (
	"context"
	"net"
	"time"
)

// Client returns ErrSessionSigUnsupported. Non-CGO builds cannot perform
// the rippled session-signature handshake.
func Client(_ net.Conn, _ *Config) (PeerConn, error) {
	return nil, ErrSessionSigUnsupported
}

// NewListener wraps inner but every Accept returns ErrSessionSigUnsupported.
func NewListener(inner net.Listener, _ *Config) net.Listener {
	return &stubListener{inner: inner}
}

type stubListener struct{ inner net.Listener }

func (s *stubListener) Accept() (net.Conn, error) { return nil, ErrSessionSigUnsupported }
func (s *stubListener) Close() error              { return s.inner.Close() }
func (s *stubListener) Addr() net.Addr            { return s.inner.Addr() }

// stubConn is unused at runtime in a non-CGO build (constructors fail
// first), but kept to ensure the type system has a PeerConn implementation
// the rest of the codebase can reference under !cgo.
type stubConn struct{}

var _ PeerConn = (*stubConn)(nil)

func (s *stubConn) Read([]byte) (int, error)              { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Write([]byte) (int, error)             { return 0, ErrSessionSigUnsupported }
func (s *stubConn) Close() error                          { return ErrSessionSigUnsupported }
func (s *stubConn) LocalAddr() net.Addr                   { return nil }
func (s *stubConn) RemoteAddr() net.Addr                  { return nil }
func (s *stubConn) SetDeadline(time.Time) error           { return ErrSessionSigUnsupported }
func (s *stubConn) SetReadDeadline(time.Time) error       { return ErrSessionSigUnsupported }
func (s *stubConn) SetWriteDeadline(time.Time) error      { return ErrSessionSigUnsupported }
func (s *stubConn) HandshakeContext(context.Context) error { return ErrSessionSigUnsupported }
func (s *stubConn) SharedValue() ([]byte, error)          { return nil, ErrSessionSigUnsupported }
