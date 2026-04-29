package peermanagement

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls"
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

	conn         net.Conn
	bufReader    *bufio.Reader
	state        PeerState
	handshakeCfg HandshakeConfig

	send   chan []byte
	events chan<- Event

	score   *PeerScore
	traffic *TrafficCounter

	// squelchMap: per-validator squelch deadlines. Messages from a
	// squelched validator are not relayed to this peer until expiry.
	squelchMu  sync.RWMutex
	squelchMap map[string]time.Time

	createdAt time.Time
	closeCh   chan struct{}
	closed    atomic.Bool

	// badDataBalance: weighted invalid-data fee, halved on a fixed
	// cadence by the overlay so transient errors decay. int64 because
	// decay can overshoot zero.
	badDataBalance atomic.Int64

	// closedLedger / previousLedger refresh on the inbound
	// mtSTATUS_CHANGE handler too.
	serverDomain      string
	closedLedger      [32]byte
	previousLedger    [32]byte
	hasClosedLedger   bool
	hasPreviousLedger bool
}

type PeerConfig struct {
	SendBufferSize int
	PeerTLSConfig  *peertls.Config
}

// DefaultPeerConfig returns defaults; callers must set PeerTLSConfig
// before Connect.
func DefaultPeerConfig() PeerConfig {
	return PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
	}
}

func NewPeer(id PeerID, endpoint Endpoint, inbound bool, identity *Identity, events chan<- Event) *Peer {
	return &Peer{
		id:         id,
		endpoint:   endpoint,
		inbound:    inbound,
		identity:   identity,
		state:      PeerStateDisconnected,
		send:       make(chan []byte, DefaultSendBufferSize),
		events:     events,
		score:      NewPeerScore(),
		traffic:    NewTrafficCounter(),
		squelchMap: make(map[string]time.Time),
		createdAt:  time.Now(),
		closeCh:    make(chan struct{}),
	}
}

func (p *Peer) ID() PeerID {
	return p.id
}

func (p *Peer) Endpoint() Endpoint {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.endpoint
}

// RemoteIP is the IP from the actual TCP connection (not the self-reported header).
func (p *Peer) RemoteIP() string {
	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()

	if conn == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return ""
	}
	return host
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

func (p *Peer) RemotePublicKey() *PublicKeyToken {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.remotePubKey
}

func (p *Peer) Capabilities() *PeerCapabilities {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.capabilities
}

func (p *Peer) applyHandshakeExtras(x HandshakeExtras) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serverDomain = x.ServerDomain
	if x.HasClosedLedger {
		p.closedLedger = x.ClosedLedger
		p.hasClosedLedger = true
	} else {
		p.closedLedger = [32]byte{}
		p.hasClosedLedger = false
	}
	if x.HasPreviousLedger {
		p.previousLedger = x.PreviousLedger
		p.hasPreviousLedger = true
	} else {
		p.previousLedger = [32]byte{}
		p.hasPreviousLedger = false
	}
}

// applyStatusChange handles inbound mtSTATUS_CHANGE updates.
func (p *Peer) applyStatusChange(closed, previous []byte, lostSync bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if lostSync {
		p.hasClosedLedger = false
		p.hasPreviousLedger = false
		p.closedLedger = [32]byte{}
		p.previousLedger = [32]byte{}
		return
	}
	if len(closed) == 32 {
		copy(p.closedLedger[:], closed)
		p.hasClosedLedger = true
	} else {
		p.hasClosedLedger = false
		p.closedLedger = [32]byte{}
	}
	if len(previous) == 32 {
		copy(p.previousLedger[:], previous)
		p.hasPreviousLedger = true
	} else {
		p.hasPreviousLedger = false
		p.previousLedger = [32]byte{}
	}
}

func (p *Peer) ServerDomain() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.serverDomain
}

// ClosedLedger reports the peer's last closed-ledger hint, or ok=false.
func (p *Peer) ClosedLedger() ([32]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.closedLedger, p.hasClosedLedger
}

// PreviousLedger reports the peer's previous-ledger hint, or ok=false.
func (p *Peer) PreviousLedger() ([32]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.previousLedger, p.hasPreviousLedger
}

func (p *Peer) Connect(ctx context.Context, cfg PeerConfig) error {
	p.mu.Lock()
	if p.state != PeerStateDisconnected {
		p.mu.Unlock()
		return ErrAlreadyConnected
	}
	p.state = PeerStateConnecting
	p.mu.Unlock()

	if cfg.PeerTLSConfig == nil {
		p.setState(PeerStateDisconnected)
		return errors.New("peer.Connect: PeerTLSConfig required")
	}

	addr := p.endpoint.String()

	dialer := &net.Dialer{Timeout: DefaultConnectTimeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		p.setState(PeerStateDisconnected)
		return NewEndpointError(p.endpoint, "connect", err)
	}

	tlsConn, err := peertls.Client(tcpConn, cfg.PeerTLSConfig)
	if err != nil {
		tcpConn.Close()
		p.setState(PeerStateDisconnected)
		return NewHandshakeError(p.endpoint, "tls_setup", err)
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tlsConn.Close()
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

// AcceptConnection assigns conn to an inbound peer. Returns
// ErrAlreadyConnected if a Connect or earlier Accept is in flight.
func (p *Peer) AcceptConnection(conn net.Conn) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != PeerStateDisconnected {
		return ErrAlreadyConnected
	}
	p.conn = conn
	p.state = PeerStateConnecting
	return nil
}

func (p *Peer) performHandshake(ctx context.Context, tlsConn peertls.PeerConn) error {
	sharedValue, err := tlsConn.SharedValue()
	if err != nil {
		return NewHandshakeError(p.endpoint, "shared_value", err)
	}

	req, err := BuildHandshakeRequest(p.identity, sharedValue, p.handshakeCfg)
	if err != nil {
		return NewHandshakeError(p.endpoint, "build_request", err)
	}

	if peerIP := tcpRemoteIP(tlsConn); peerIP != nil {
		addAddressHeaders(req.Header, p.handshakeCfg, peerIP)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultHandshakeTimeout)
	}
	if err := tlsConn.SetDeadline(deadline); err != nil {
		return NewHandshakeError(p.endpoint, "set_deadline", err)
	}
	defer func() { _ = tlsConn.SetDeadline(time.Time{}) }()

	if err := WriteRawHandshakeRequest(tlsConn, req); err != nil {
		return NewHandshakeError(p.endpoint, "send_request", err)
	}

	p.mu.Lock()
	p.bufReader = bufio.NewReader(tlsConn)
	p.mu.Unlock()

	resp, err := http.ReadResponse(p.bufReader, req)
	if err != nil {
		return NewHandshakeError(p.endpoint, "read_response", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		return NewHandshakeError(p.endpoint, "verify",
			fmt.Errorf("%w: got status %d, headers: %v, body: %s",
				ErrInvalidHandshake, resp.StatusCode, resp.Header, string(body[:n])))
	}
	resp.Body.Close()

	// Server-Domain check runs first (rippled verify order).
	if _, err := ValidateServerDomain(resp.Header); err != nil {
		return NewHandshakeError(p.endpoint, "verify_extras", err)
	}

	peerPubKey, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		p.identity.EncodedPublicKey(),
		p.handshakeCfg,
	)
	if err != nil {
		return NewHandshakeError(p.endpoint, "verify", err)
	}
	p.mu.Lock()
	p.remotePubKey = peerPubKey
	p.mu.Unlock()

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(resp.Header)
	p.mu.Lock()
	p.capabilities = caps
	p.mu.Unlock()

	extras, err := ParseHandshakeExtras(
		resp.Header,
		p.handshakeCfg.PublicIP,
		tcpRemoteIP(tlsConn),
	)
	if err != nil {
		return NewHandshakeError(p.endpoint, "verify_extras", err)
	}
	p.applyHandshakeExtras(extras)

	return nil
}

func tcpRemoteIP(conn net.Conn) net.IP {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			return nil
		}
		return net.ParseIP(host)
	}
	return addr.IP
}

// Run starts read/write/ping loops; returns when any of them errors.
func (p *Peer) Run(ctx context.Context) error {
	p.mu.RLock()
	if p.state != PeerStateConnected {
		p.mu.RUnlock()
		return ErrConnectionClosed
	}
	p.mu.RUnlock()

	errCh := make(chan error, 3)

	go func() {
		errCh <- p.readLoop(ctx)
	}()

	go func() {
		errCh <- p.writeLoop(ctx)
	}()

	go func() {
		errCh <- p.pingLoop(ctx)
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
		reader := p.bufReader
		p.mu.RUnlock()

		if reader == nil {
			return ErrConnectionClosed
		}

		header, payload, err := ReadMessage(reader)
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

func (p *Peer) pingLoop(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closeCh:
			return nil
		case <-ticker.C:
			ping := &message.Ping{
				PType: message.PingTypePing,
				Seq:   uint32(time.Now().UnixMilli() & 0xFFFFFFFF),
			}
			encoded, err := message.Encode(ping)
			if err != nil {
				continue
			}
			wireMsg, err := message.BuildWireMessage(message.TypePing, encoded)
			if err != nil {
				continue
			}
			if err := p.Send(wireMsg); err != nil {
				return err
			}
		}
	}
}

// maxSquelchesPerPeer bounds memory under adversarial input. Existing
// entries can still be refreshed once the cap is hit.
const maxSquelchesPerPeer = 128

// AddSquelch records a squelch from this peer. Returns false (and
// removes any prior entry) on out-of-range duration or when the cap is
// hit by a NEW validator key. Both rejections charge bad-data fee.
func (p *Peer) AddSquelch(validator []byte, duration time.Duration) bool {
	if duration < MinUnsquelchExpire || duration > MaxUnsquelchExpirePeers {
		p.RemoveSquelch(validator)
		p.IncBadData("squelch-duration")
		return false
	}
	key := string(validator)
	p.squelchMu.Lock()
	_, exists := p.squelchMap[key]
	full := !exists && len(p.squelchMap) >= maxSquelchesPerPeer
	if !full {
		p.squelchMap[key] = time.Now().Add(duration)
	}
	p.squelchMu.Unlock()
	if full {
		p.IncBadData("squelch-map-full")
		return false
	}
	return true
}

// IncBadData adds BadDataWeight(reason) to the running balance and
// returns the new total (clamped to non-negative).
func (p *Peer) IncBadData(reason string) uint32 {
	w := BadDataWeight(reason)
	n := p.badDataBalance.Add(int64(w))
	slog.Debug("peer bad data",
		"t", "Peer", "peer", p.id, "reason", reason,
		"weight", w, "balance", n,
		"endpoint", p.endpoint.String(),
	)
	if n < 0 {
		return 0
	}
	return uint32(n)
}

// BadDataCount returns the running balance, clamped to non-negative.
func (p *Peer) BadDataCount() uint32 {
	n := p.badDataBalance.Load()
	if n < 0 {
		return 0
	}
	return uint32(n)
}

func (p *Peer) Load() int64 {
	return p.badDataBalance.Load()
}

// DecayBadData halves the balance. Called periodically by the overlay
// so transient errors don't accumulate to eviction.
func (p *Peer) DecayBadData() {
	for {
		cur := p.badDataBalance.Load()
		if cur <= 0 {
			return
		}
		next := cur / 2
		if p.badDataBalance.CompareAndSwap(cur, next) {
			return
		}
	}
}

func (p *Peer) RemoveSquelch(validator []byte) {
	p.squelchMu.Lock()
	delete(p.squelchMap, string(validator))
	p.squelchMu.Unlock()
}

// ExpireSquelch reports whether a message from validator may be relayed
// to this peer. Clears the entry if an existing squelch has expired.
func (p *Peer) ExpireSquelch(validator []byte) bool {
	key := string(validator)

	p.squelchMu.RLock()
	deadline, ok := p.squelchMap[key]
	p.squelchMu.RUnlock()

	if !ok {
		return true
	}
	if deadline.After(time.Now()) {
		return false
	}

	p.squelchMu.Lock()
	if d, stillThere := p.squelchMap[key]; stillThere && !d.After(time.Now()) {
		delete(p.squelchMap, key)
	}
	p.squelchMu.Unlock()
	return true
}

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

// PeerInfo is a read-only snapshot of peer state. ClosedLedger is
// upper-case hex (rippled convention) or "" when absent.
type PeerInfo struct {
	ID          PeerID
	Endpoint    Endpoint
	Inbound     bool
	State       PeerState
	PublicKey   string
	ConnectedAt time.Time
	MessagesIn  uint64
	MessagesOut uint64

	ServerDomain string
	ClosedLedger string
	Load         int64
}

func (p *Peer) Info() PeerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var pubKey string
	if p.remotePubKey != nil {
		pubKey = p.remotePubKey.Encode()
	}

	stats := p.traffic.GetTotalStats()

	var closedLedger string
	if p.hasClosedLedger {
		closedLedger = strings.ToUpper(hex.EncodeToString(p.closedLedger[:]))
	}

	return PeerInfo{
		ID:           p.id,
		Endpoint:     p.endpoint,
		Inbound:      p.inbound,
		State:        p.state,
		PublicKey:    pubKey,
		ConnectedAt:  p.createdAt,
		MessagesIn:   stats.MessagesIn,
		MessagesOut:  stats.MessagesOut,
		ServerDomain: p.serverDomain,
		ClosedLedger: closedLedger,
		Load:         p.Load(),
	}
}
