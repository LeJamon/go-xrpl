// Package peertls provides the TLS 1.2 transport used by XRPL peers.
package peertls

import (
	"context"
	"errors"
	"net"
)

// PeerConn is an XRPL TLS connection that exposes the session-signature
// material produced by its handshake.
type PeerConn interface {
	net.Conn
	HandshakeContext(ctx context.Context) error
	// SharedValue returns the 32-byte XRPL session-signature input.
	SharedValue() ([]byte, error)
}

// GracefulConn is a peer connection that can send TLS close_notify before
// closing its transport. Close remains available for abortive teardown.
type GracefulConn interface {
	PeerConn
	ShutdownContext(ctx context.Context) error
}

type Config struct {
	CertPEM []byte
	KeyPEM  []byte
}

// ErrSessionSigUnsupported reports that the build cannot provide XRPL's
// OpenSSL Finished-message access.
var ErrSessionSigUnsupported = errors.New(
	"peertls: session-signature TLS requires CGO + OpenSSL; rebuild with CGO_ENABLED=1")

var ErrHandshakeIncomplete = errors.New("peertls: handshake not complete")
