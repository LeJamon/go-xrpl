// Package peermanagement implements XRPL peer-to-peer networking.
package peermanagement

import (
	"fmt"
	"net"
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
	var port uint16
	_, err = parsePort(portStr, &port)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: host, Port: port}, nil
}

func parsePort(s string, port *uint16) (int, error) {
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
	*port = uint16(p)
	return p, nil
}

// EventType represents the type of peer management event.
type EventType int

const (
	// EventPeerConnecting is emitted when starting to connect to a peer.
	EventPeerConnecting EventType = iota

	// EventPeerConnected is emitted when TCP connection is established.
	EventPeerConnected

	// EventPeerHandshakeComplete is emitted when handshake succeeds.
	EventPeerHandshakeComplete

	// EventPeerActivated is emitted when peer becomes fully active.
	EventPeerActivated

	// EventPeerDisconnected is emitted when peer disconnects.
	EventPeerDisconnected

	// EventPeerFailed is emitted when connection attempt fails.
	EventPeerFailed

	// EventMessageReceived is emitted when a message is received from a peer.
	EventMessageReceived

	// EventEndpointsReceived is emitted when peer endpoints are received.
	EventEndpointsReceived

	// EventLedgerResponse is emitted when ledger data needs to be sent to a peer.
	EventLedgerResponse
)

// String returns the string representation of an EventType.
func (e EventType) String() string {
	switch e {
	case EventPeerConnecting:
		return "PeerConnecting"
	case EventPeerConnected:
		return "PeerConnected"
	case EventPeerHandshakeComplete:
		return "PeerHandshakeComplete"
	case EventPeerActivated:
		return "PeerActivated"
	case EventPeerDisconnected:
		return "PeerDisconnected"
	case EventPeerFailed:
		return "PeerFailed"
	case EventMessageReceived:
		return "MessageReceived"
	case EventEndpointsReceived:
		return "EndpointsReceived"
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
	MessageType uint16

	// Payload is the message payload (for MessageReceived events).
	Payload []byte

	// Endpoints is a list of endpoints (for EndpointsReceived events).
	Endpoints []Endpoint

	// Inbound indicates if this is an inbound connection.
	Inbound bool

	// Error is set for failure events.
	Error error
}

// InboundMessage represents a message received from a peer.
// This is exposed to consumers of the Overlay.
type InboundMessage struct {
	// PeerID identifies the sender.
	PeerID PeerID

	// Type is the message type.
	Type uint16

	// Payload is the raw message payload.
	Payload []byte
}
