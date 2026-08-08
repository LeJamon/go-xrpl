// Package peermanagement implements XRPL peer-to-peer networking.
package peermanagement

import (
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// PeerID is a unique identifier for a connected peer.
type PeerID uint64

// Endpoint represents a network address for a peer.
type Endpoint struct {
	Host string
	Port uint16
}

// String returns the endpoint as "host:port".
func (e Endpoint) String() string {
	return net.JoinHostPort(e.Host, fmt.Sprintf("%d", e.Port))
}

// ParseEndpoint parses an endpoint from "host:port" string.
func ParseEndpoint(s string) (Endpoint, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return Endpoint{}, err
	}
	if strings.TrimSpace(host) == "" {
		return Endpoint{}, ErrInvalidEndpoint
	}
	port, err := parsePort(portStr)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: host, Port: port}, nil
}

func parsePort(s string) (uint16, error) {
	if s == "" {
		return 0, ErrInvalidEndpoint
	}
	var p int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrInvalidEndpoint
		}
		p = p*10 + int(c-'0')
		if p > 65535 {
			return 0, ErrInvalidEndpoint
		}
	}
	return uint16(p), nil
}

// EventType represents the type of peer management event.
type EventType int

const (
	// EventPeerConnected is emitted when a peer has completed its
	// handshake and been added to the overlay (lifecycle channel).
	EventPeerConnected EventType = iota

	// EventPeerDisconnected is emitted when peer disconnects (lifecycle).
	EventPeerDisconnected

	// EventPeerFailed is emitted when a connection attempt fails (lifecycle).
	EventPeerFailed

	// EventMessageReceived is emitted when a message is received from a peer.
	EventMessageReceived

	// EventLedgerResponse is emitted when ledger data needs to be sent to a peer.
	EventLedgerResponse
)

// String returns the string representation of an EventType.
func (e EventType) String() string {
	switch e {
	case EventPeerConnected:
		return "PeerConnected"
	case EventPeerDisconnected:
		return "PeerDisconnected"
	case EventPeerFailed:
		return "PeerFailed"
	case EventMessageReceived:
		return "MessageReceived"
	case EventLedgerResponse:
		return "LedgerResponse"
	default:
		return "Unknown"
	}
}

// Event represents a peer management event for internal coordination.
type Event struct {
	// Type is the event type.
	Type EventType

	// PeerID is the peer this event relates to (if applicable).
	PeerID PeerID

	// Endpoint is the peer's endpoint (for connection events).
	Endpoint Endpoint

	// PublicKey is the peer's public key (after handshake).
	PublicKey []byte

	// MessageType is the type of message (for MessageReceived events).
	MessageType message.MessageType

	// Payload is the message payload (for MessageReceived events).
	Payload []byte

	// ManifestFrame carries an oversized manifest payload spooled to disk.
	ManifestFrame *ManifestFrame

	// WireSize is the on-wire payload size in bytes (compressed if the
	// frame was compressed, excluding the header) for MessageReceived
	// events. Mirrors rippled's header.payload_wire_size passed to
	// onMessageBegin (ProtocolMessage.h:311); used by the tx_reduce_relay
	// metrics so byte counts match what crossed the wire, not the
	// post-decompression payload.
	WireSize uint64

	// Inbound indicates if this is an inbound connection.
	Inbound bool

	// Error is set for failure events.
	Error error

	reservation *inboundReservation
	charge      *messageCharge
}

func (e *Event) release() {
	if e == nil {
		return
	}
	if e.ManifestFrame != nil {
		_ = e.ManifestFrame.Close()
		e.ManifestFrame = nil
	}
	if e.reservation != nil {
		e.reservation.release()
		e.reservation = nil
	}
	if e.charge != nil {
		e.charge.finish()
		e.charge = nil
	}
}

func (e *Event) inboundMessage() *InboundMessage {
	msg := &InboundMessage{
		PeerID:        e.PeerID,
		Type:          e.MessageType,
		Payload:       e.Payload,
		ManifestFrame: e.ManifestFrame,
		reservation:   e.reservation,
		charge:        e.charge,
	}
	e.reservation = nil
	e.charge = nil
	e.ManifestFrame = nil
	return msg
}

func (e *Event) manifestInboundMessage() *InboundMessage {
	return e.inboundMessage()
}

func (e *Event) selectCharge(fee resource.Charge, chargeContext string) bool {
	if e == nil || e.charge == nil {
		return false
	}
	e.charge.update(fee, chargeContext)
	return true
}

func (e *Event) retainedInboundMessage() *InboundMessage {
	return &InboundMessage{
		PeerID:        e.PeerID,
		Type:          e.MessageType,
		Payload:       e.Payload,
		ManifestFrame: e.ManifestFrame,
		reservation:   e.reservation.retain(),
	}
}

// InboundMessage represents a message received from a peer.
// This is exposed to consumers of the Overlay.
type InboundMessage struct {
	// PeerID identifies the sender.
	PeerID PeerID

	// Type is the message type.
	Type message.MessageType

	// Payload is the raw message payload.
	Payload []byte

	// ManifestFrame carries an oversized manifest payload spooled to disk.
	ManifestFrame *ManifestFrame

	// Tx carries an already-decoded transaction for inner frames fanned
	// out from a TMTransactions batch, so the consumer can skip decoding
	// Payload. It is nil for messages read off the wire, whose decoded
	// form lives in Payload.
	Tx *message.Transaction

	reservation *inboundReservation
	charge      *messageCharge
	closeOnce   sync.Once
	closeErr    error
}

// Close releases retained inbound bytes and removes any spool file.
func (m *InboundMessage) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		if m.ManifestFrame != nil {
			m.closeErr = m.ManifestFrame.Close()
		}
		m.reservation.release()
		m.reservation = nil
		m.charge.finish()
		m.charge = nil
	})
	return m.closeErr
}

func (m *InboundMessage) SelectPeerCharge(fee resource.Charge, chargeContext string) bool {
	if m == nil || m.charge == nil {
		return false
	}
	m.charge.update(fee, chargeContext)
	return true
}

// CompletePeerCharge applies the selected per-message charge exactly once.
func (m *InboundMessage) CompletePeerCharge() {
	if m != nil {
		m.charge.finish()
	}
}
