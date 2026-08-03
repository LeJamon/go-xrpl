//go:build !cgo

package peertls

import "net"

// Supported reports whether XRPL session-signature TLS is available in this
// build.
func Supported() bool { return false }

// Client reports that XRPL session-signature TLS is unavailable in this build.
// It does not close inner.
func Client(_ net.Conn, _ *Config) (PeerConn, error) {
	return nil, ErrSessionSigUnsupported
}

// NewListener reports that XRPL session-signature TLS is unavailable in this
// build. It does not close inner.
func NewListener(_ net.Listener, _ *Config) (net.Listener, error) {
	return nil, ErrSessionSigUnsupported
}
