package peermanagement

import (
	"errors"
	"fmt"
)

// Sentinel errors for peer management operations.
var (
	// Connection errors
	ErrMaxPeersReached    = errors.New("maximum peers reached")
	ErrMaxInboundReached  = errors.New("maximum inbound connections reached")
	ErrMaxOutboundReached = errors.New("maximum outbound connections reached")
	ErrAlreadyConnected   = errors.New("already connected to peer")
	ErrSelfConnection     = errors.New("cannot connect to self")
	ErrSlotUnavailable    = errors.New("no connection slot available")
	ErrConnectionClosed   = errors.New("connection closed")
	ErrPingTimeout        = errors.New("peer ping timeout")

	// Handshake errors
	ErrHandshakeFailed  = errors.New("handshake failed")
	ErrInvalidHandshake = errors.New("invalid handshake data")
	ErrHandshakeTimeout = errors.New("handshake timeout")
	ErrProtocolMismatch = errors.New("protocol version mismatch")
	ErrInvalidPublicKey = errors.New("invalid public key")
	ErrInvalidSignature = errors.New("invalid signature")
	ErrNetworkMismatch  = errors.New("network ID mismatch")

	// Discovery errors
	ErrPeerNotFound    = errors.New("peer not found")
	ErrInvalidEndpoint = errors.New("invalid endpoint")
	ErrEndpointBanned  = errors.New("endpoint is banned")

	// Message errors
	ErrInvalidMessage   = errors.New("invalid message")
	ErrMessageTooLarge  = errors.New("message too large")
	ErrUnknownMessage   = errors.New("unknown message type")
	ErrDecodeFailed     = errors.New("failed to decode message")
	ErrEncodeFailed     = errors.New("failed to encode message")
	ErrDecompressFailed = errors.New("failed to decompress message")

	// Lifecycle errors
	ErrNotRunning = errors.New("overlay not running")
	ErrShutdown   = errors.New("overlay is shutting down")
)

// PeerError wraps an error with peer context.
type PeerError struct {
	PeerID   PeerID
	Endpoint Endpoint
	Op       string
	Err      error
}

// Error returns the error message.
func (e *PeerError) Error() string {
	if e.PeerID != 0 {
		return fmt.Sprintf("peer %d: %s: %v", e.PeerID, e.Op, e.Err)
	}
	if e.Endpoint.Host != "" {
		return fmt.Sprintf("peer %s: %s: %v", e.Endpoint.String(), e.Op, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *PeerError) Unwrap() error {
	return e.Err
}

// NewPeerError creates a new PeerError.
func NewPeerError(peerID PeerID, op string, err error) *PeerError {
	return &PeerError{
		PeerID: peerID,
		Op:     op,
		Err:    err,
	}
}

// NewEndpointError creates a new PeerError with endpoint context.
func NewEndpointError(endpoint Endpoint, op string, err error) *PeerError {
	return &PeerError{
		Endpoint: endpoint,
		Op:       op,
		Err:      err,
	}
}

// HandshakeError provides detailed handshake failure information.
type HandshakeError struct {
	Endpoint Endpoint
	Stage    string
	Err      error
}

// Error returns the error message.
func (e *HandshakeError) Error() string {
	return fmt.Sprintf("handshake with %s failed at %s: %v", e.Endpoint.String(), e.Stage, e.Err)
}

// Unwrap returns the underlying error.
func (e *HandshakeError) Unwrap() error {
	return e.Err
}

// NewHandshakeError creates a new HandshakeError.
func NewHandshakeError(endpoint Endpoint, stage string, err error) *HandshakeError {
	return &HandshakeError{
		Endpoint: endpoint,
		Stage:    stage,
		Err:      err,
	}
}
