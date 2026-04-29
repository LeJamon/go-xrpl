//go:build !cgo

package peertls

import (
	"context"
	"net"
	"time"
)

func Client(_ net.Conn, _ *Config) (PeerConn, error) {
	return nil, ErrSessionSigUnsupported
}

func NewListener(inner net.Listener, _ *Config) net.Listener {
	return &stubListener{inner: inner}
}

type stubListener struct{ inner net.Listener }

func (s *stubListener) Accept() (net.Conn, error) { return nil, ErrSessionSigUnsupported }
func (s *stubListener) Close() error              { return s.inner.Close() }
func (s *stubListener) Addr() net.Addr            { return s.inner.Addr() }

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
