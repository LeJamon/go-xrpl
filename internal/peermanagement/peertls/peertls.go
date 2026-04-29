// Package peertls is the TLS 1.2 transport for XRPL peer connections.
// It exposes the post-handshake Finished bytes via SharedValue so the
// session-signature can be computed on both sides. OpenSSL-backed under
// cgo; non-cgo builds get a stub that fails closed.
package peertls

import (
	"context"
	"errors"
	"net"
)

type PeerConn interface {
	net.Conn
	HandshakeContext(ctx context.Context) error
	// SharedValue returns the 32-byte session-signature input.
	SharedValue() ([]byte, error)
}

type Config struct {
	CertPEM []byte
	KeyPEM  []byte
}

var ErrSessionSigUnsupported = errors.New(
	"peertls: session-signature TLS requires CGO + OpenSSL; rebuild with CGO_ENABLED=1")

var ErrHandshakeIncomplete = errors.New("peertls: handshake not complete")
