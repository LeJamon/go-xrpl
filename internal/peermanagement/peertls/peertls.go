// Package peertls provides the TLS 1.2 transport for XRPL peer-to-peer
// connections. Unlike crypto/tls, it exposes the post-handshake Finished
// bytes via SharedValue() so the rippled session-signature verification
// can succeed both client- and server-side. The OpenSSL-backed
// implementation lives in tls_openssl.go (build tag: cgo); a stub
// returning ErrSessionSigUnsupported lives in tls_stub.go (build tag:
// !cgo).
package peertls

import (
	"context"
	"errors"
	"net"
)

// PeerConn is the TLS connection abstraction used by the peer subsystem.
// All methods of net.Conn are passed through to the underlying TLS
// stream. HandshakeContext drives the handshake explicitly.
// SharedValue returns the 32-byte sha512Half(c1 ^ c2) of the local and
// peer Finished messages, matching rippled's makeSharedValue.
type PeerConn interface {
	net.Conn
	HandshakeContext(ctx context.Context) error
	SharedValue() ([]byte, error)
}

// Config is the per-connection TLS configuration. It deliberately omits
// CA pools and verification toggles: rippled peers do not trust certs,
// they trust the public key advertised in the Public-Key header. The
// shim sets SSL_VERIFY_NONE.
type Config struct {
	// CertPEM is the PEM-encoded x509 certificate (concatenated chain
	// allowed). Required.
	CertPEM []byte
	// KeyPEM is the PEM-encoded private key. Required.
	KeyPEM []byte
}

// ErrSessionSigUnsupported is returned by every constructor in non-CGO
// builds. The peer subsystem cannot operate without it; RPC, WebSocket,
// tx, and other subsystems are unaffected.
var ErrSessionSigUnsupported = errors.New(
	"peertls: session-signature TLS requires CGO + OpenSSL; rebuild with CGO_ENABLED=1")

// ErrHandshakeIncomplete is returned by SharedValue if called before a
// successful HandshakeContext.
var ErrHandshakeIncomplete = errors.New("peertls: handshake not complete")
