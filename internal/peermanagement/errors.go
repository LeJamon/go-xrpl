package peermanagement

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// Sentinel errors for peer management operations.
var (
	// Connection errors
	ErrMaxPeersReached       = errors.New("maximum peers reached")
	ErrAlreadyConnected      = errors.New("already connected to peer")
	ErrSelfConnection        = errors.New("cannot connect to self")
	ErrConnectionClosed      = errors.New("connection closed")
	ErrSendBufferFull        = errors.New("peer send buffer full")
	ErrCriticalSendQueueFull = errors.New("critical peer send queue exhausted")
	ErrPingTimeout           = errors.New("peer ping timeout")
	ErrLargeSendQueue        = errors.New("peer send queue saturated; closing")
	ErrReadIdle              = errors.New("peer read idle deadline exceeded")
	ErrFrameReadTooSlow      = errors.New("peer frame exceeded its read progress budget")
	ErrWriteIdle             = errors.New("peer write idle deadline exceeded")

	// Handshake errors
	ErrHandshakeFailed          = errors.New("handshake failed")
	ErrInvalidHandshake         = errors.New("invalid handshake data")
	ErrHandshakeHeadersTooLarge = errors.New("handshake headers too large")
	ErrHandshakeBodyTooLarge    = errors.New("handshake body too large")
	ErrHandshakeTimeout         = errors.New("handshake timeout")
	ErrProtocolMismatch         = errors.New("protocol version mismatch")
	ErrInvalidPublicKey         = errors.New("invalid public key")
	ErrInvalidSignature         = errors.New("invalid signature")
	ErrNetworkMismatch          = errors.New("network ID mismatch")

	// Discovery errors
	ErrPeerNotFound    = errors.New("peer not found")
	ErrInvalidEndpoint = errors.New("invalid endpoint")
	ErrEndpointBanned  = errors.New("endpoint is banned")

	// Message errors
	ErrInvalidMessage  = errors.New("invalid message")
	ErrMessageTooLarge = errors.New("message too large")
	ErrUnknownMessage  = errors.New("unknown message type")
	ErrDecodeFailed    = errors.New("failed to decode message")
	ErrEncodeFailed    = errors.New("failed to encode message")
	// Lifecycle errors
	ErrNotRunning     = errors.New("overlay not running")
	ErrAlreadyRunning = errors.New("overlay already running")
	ErrShutdown       = errors.New("overlay is shutting down")
)

type SendQueueError struct {
	Class           OutboundSendClass
	Reason          SendQueueFailureReason
	AttemptedFrames int
	AttemptedBytes  int64
	RetainedFrames  int
	RetainedBytes   int64
}

func (e *SendQueueError) Error() string {
	base := ErrSendBufferFull
	if e.Reason == SendQueueClosed {
		base = ErrConnectionClosed
	}
	return fmt.Sprintf(
		"%v: class=%s reason=%s attempted_frames=%d attempted_bytes=%d retained_frames=%d retained_bytes=%d",
		base,
		e.Class,
		e.Reason,
		e.AttemptedFrames,
		e.AttemptedBytes,
		e.RetainedFrames,
		e.RetainedBytes,
	)
}

func (e *SendQueueError) Unwrap() error {
	switch {
	case e.Reason == SendQueueClosed:
		return ErrConnectionClosed
	case e.Class == OutboundClassControl || e.Class == OutboundClassConsensus:
		return errors.Join(ErrSendBufferFull, ErrCriticalSendQueueFull)
	default:
		return ErrSendBufferFull
	}
}

// FanoutError summarizes enqueue failures from a broadcast and preserves every
// cause for errors.Is and errors.As.
type FanoutError struct {
	Operation string
	Attempted int
	Failed    int
	Critical  int
	Err       error
}

func (e *FanoutError) Error() string {
	return fmt.Sprintf(
		"%s fanout: attempted=%d accepted=%d failed=%d critical=%d: %v",
		e.Operation,
		e.Attempted,
		e.Attempted-e.Failed,
		e.Failed,
		e.Critical,
		e.Err,
	)
}

func (e *FanoutError) Unwrap() error {
	return e.Err
}

var errCompressionUnnegotiated = errors.New("compressed frame without negotiated compression")
var errBootstrapManifestDropped = errors.New("bootstrap manifests could not be delivered")

// FrameReadError describes a failed payload transfer after its header was read.
type FrameReadError struct {
	MessageType message.MessageType
	WireSize    uint32
	Compressed  bool
	BytesRead   uint64
	Elapsed     time.Duration
	Err         error
}

func (e *FrameReadError) Error() string {
	var rate uint64
	if e.Elapsed > 0 {
		rate = e.BytesRead * uint64(time.Second) / uint64(e.Elapsed)
	}
	return fmt.Sprintf("failed to read %s payload: wire_size=%d compressed=%t bytes_read=%d elapsed=%s rate=%dB/s: %v",
		e.MessageType, e.WireSize, e.Compressed, e.BytesRead, e.Elapsed, rate, e.Err)
}

func (e *FrameReadError) Unwrap() error {
	return e.Err
}

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
