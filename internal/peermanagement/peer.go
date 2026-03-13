package peermanagement

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// PeerState represents the peer connection state.
type PeerState int

const (
	PeerStateDisconnected PeerState = iota
	PeerStateConnecting
	PeerStateConnected
	PeerStateClosing
)

// String returns the string representation of PeerState.
func (s PeerState) String() string {
	switch s {
	case PeerStateDisconnected:
		return "disconnected"
	case PeerStateConnecting:
		return "connecting"
	case PeerStateConnected:
		return "connected"
	case PeerStateClosing:
		return "closing"
	default:
		return "unknown"
	}
}

// Peer represents a connection to an XRPL peer node.
type Peer struct {
	mu sync.RWMutex

	id       PeerID
	endpoint Endpoint
	inbound  bool

	identity     *Identity
	remotePubKey *PublicKeyToken
	capabilities *PeerCapabilities

	conn  net.Conn
	state PeerState

	send   chan []byte
	events chan<- Event

	score   *PeerScore
	traffic *TrafficCounter

	createdAt time.Time
	closeCh   chan struct{}
	closed    atomic.Bool
}

// PeerConfig holds peer connection configuration.
type PeerConfig struct {
	SendBufferSize int
	TLSConfig      *tls.Config
}

// DefaultPeerConfig returns the default peer configuration.
func DefaultPeerConfig() PeerConfig {
	return PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
	}
}

// NewPeer creates a new peer.
func NewPeer(id PeerID, endpoint Endpoint, inbound bool, identity *Identity, events chan<- Event) *Peer {
	return &Peer{
		id:        id,
		endpoint:  endpoint,
		inbound:   inbound,
		identity:  identity,
		state:     PeerStateDisconnected,
		send:      make(chan []byte, DefaultSendBufferSize),
		events:    events,
		score:     NewPeerScore(),
		traffic:   NewTrafficCounter(),
		createdAt: time.Now(),
		closeCh:   make(chan struct{}),
	}
}

// ID returns the peer's unique identifier.
func (p *Peer) ID() PeerID {
	return p.id
}

// Endpoint returns the peer's endpoint.
func (p *Peer) Endpoint() Endpoint {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.endpoint
}

// Inbound returns true if this is an inbound connection.
func (p *Peer) Inbound() bool {
	return p.inbound
}

// State returns the current connection state.
func (p *Peer) State() PeerState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// RemotePublicKey returns the peer's public key after handshake.
func (p *Peer) RemotePublicKey() *PublicKeyToken {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.remotePubKey
}

// Capabilities returns the peer's negotiated capabilities.
func (p *Peer) Capabilities() *PeerCapabilities {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.capabilities
}

// Connect establishes connection to the peer (outbound).
func (p *Peer) Connect(ctx context.Context, cfg PeerConfig) error {
	p.mu.Lock()
	if p.state != PeerStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PeerStateConnecting
	p.mu.Unlock()

	addr := p.endpoint.String()

	dialer := &net.Dialer{Timeout: DefaultConnectTimeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		p.setState(PeerStateDisconnected)
		return NewEndpointError(p.endpoint, "connect", err)
	}

	tlsConfig := cfg.TLSConfig
	if tlsConfig == nil {
		tlsConfig = DefaultPeerConfig().TLSConfig
	}

	tlsConn := tls.Client(tcpConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tcpConn.Close()
		p.setState(PeerStateDisconnected)
		return NewHandshakeError(p.endpoint, "tls", err)
	}

	p.mu.Lock()
	p.conn = tlsConn
	p.mu.Unlock()

	if err := p.performHandshake(ctx, tlsConn); err != nil {
		tlsConn.Close()
		p.setState(PeerStateDisconnected)
		return err
	}

	p.setState(PeerStateConnected)
	return nil
}

// AcceptConnection sets the connection for an inbound peer.
func (p *Peer) AcceptConnection(conn net.Conn) {
	p.mu.Lock()
	p.conn = conn
	p.state = PeerStateConnecting
	p.mu.Unlock()
}

// performHandshake performs the XRPL HTTP upgrade handshake.
func (p *Peer) performHandshake(ctx context.Context, tlsConn *tls.Conn) error {
	sharedValue, err := MakeSharedValue(tlsConn)
	if err != nil {
		return NewHandshakeError(p.endpoint, "shared_value", err)
	}

	cfg := DefaultHandshakeConfig()
	req, err := BuildHandshakeRequest(p.identity, sharedValue, cfg)
	if err != nil {
		return NewHandshakeError(p.endpoint, "build_request", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultHandshakeTimeout)
	}
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	if err := req.Write(tlsConn); err != nil {
		return NewHandshakeError(p.endpoint, "send_request", err)
	}

	// Read response (simplified - in practice need full HTTP parsing)
	buf := make([]byte, 4096)
	n, err := tlsConn.Read(buf)
	if err != nil && err != io.EOF {
		return NewHandshakeError(p.endpoint, "read_response", err)
	}

	// Verify the response contains upgrade
	response := string(buf[:n])
	if len(response) < 12 {
		return NewHandshakeError(p.endpoint, "verify", ErrInvalidHandshake)
	}

	p.mu.Lock()
	p.capabilities = NewPeerCapabilities()
	p.mu.Unlock()

	return nil
}

// Run starts the peer's read/write loops.
func (p *Peer) Run(ctx context.Context) error {
	p.mu.RLock()
	if p.state != PeerStateConnected {
		p.mu.RUnlock()
		return ErrConnectionClosed
	}
	p.mu.RUnlock()

	errCh := make(chan error, 2)

	go func() {
		errCh <- p.readLoop(ctx)
	}()

	go func() {
		errCh <- p.writeLoop(ctx)
	}()

	select {
	case <-ctx.Done():
		p.Close()
		return ctx.Err()
	case err := <-errCh:
		p.Close()
		return err
	}
}

func (p *Peer) readLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closeCh:
			return nil
		default:
		}

		p.mu.RLock()
		conn := p.conn
		p.mu.RUnlock()

		if conn == nil {
			return ErrConnectionClosed
		}

		header, payload, err := ReadMessage(conn)
		if err != nil {
			if p.closed.Load() {
				return nil
			}
			return err
		}

		if header.Compressed {
			payload, err = DecompressLZ4(payload, int(header.UncompressedSize))
			if err != nil {
				continue
			}
		}

		p.traffic.AddCount(CategorizeMessage(uint16(header.MessageType)), true, len(payload))

		if p.events != nil {
			p.events <- Event{
				Type:        EventMessageReceived,
				PeerID:      p.id,
				MessageType: uint16(header.MessageType),
				Payload:     payload,
			}
		}
	}
}

func (p *Peer) writeLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closeCh:
			return nil
		case data := <-p.send:
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()

			if conn == nil {
				return ErrConnectionClosed
			}

			_, err := conn.Write(data)
			if err != nil {
				return err
			}
		}
	}
}

// Send queues data to be sent to the peer.
func (p *Peer) Send(data []byte) error {
	if p.closed.Load() {
		return ErrConnectionClosed
	}

	select {
	case p.send <- data:
		return nil
	default:
		return ErrConnectionClosed
	}
}

// Close closes the peer connection.
func (p *Peer) Close() error {
	if p.closed.Swap(true) {
		return nil
	}

	p.mu.Lock()
	p.state = PeerStateClosing
	close(p.closeCh)
	conn := p.conn
	p.conn = nil
	p.mu.Unlock()

	var err error
	if conn != nil {
		err = conn.Close()
	}

	p.setState(PeerStateDisconnected)

	if p.events != nil {
		p.events <- Event{
			Type:     EventPeerDisconnected,
			PeerID:   p.id,
			Endpoint: p.endpoint,
		}
	}

	return err
}

func (p *Peer) setState(state PeerState) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

// PeerInfo provides read-only information about a peer.
type PeerInfo struct {
	ID          PeerID
	Endpoint    Endpoint
	Inbound     bool
	State       PeerState
	PublicKey   string
	ConnectedAt time.Time
	MessagesIn  uint64
	MessagesOut uint64
}

// Info returns read-only information about the peer.
func (p *Peer) Info() PeerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var pubKey string
	if p.remotePubKey != nil {
		pubKey = p.remotePubKey.Encode()
	}

	stats := p.traffic.GetTotalStats()

	return PeerInfo{
		ID:          p.id,
		Endpoint:    p.endpoint,
		Inbound:     p.inbound,
		State:       p.state,
		PublicKey:   pubKey,
		ConnectedAt: p.createdAt,
		MessagesIn:  stats.MessagesIn,
		MessagesOut: stats.MessagesOut,
	}
}
