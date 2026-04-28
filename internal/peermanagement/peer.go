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

	// squelchMap tracks per-validator squelch expiry deadlines. Outgoing
	// validation/proposal messages originating from a squelched validator
	// must not be relayed to this peer until the deadline passes.
	// Mirrors rippled's `Squelch` (see overlay/Squelch.h). The key is the
	// validator's public key bytes as a string for use as a map key.
	squelchMu  sync.RWMutex
	squelchMap map[string]time.Time

	createdAt time.Time
	closeCh   chan struct{}
	closed    atomic.Bool

	// badDataBalance tracks the weighted invalid-data "fee" this peer
	// owes, modeled on rippled's Resource::Consumer balance. Each bad-
	// data event adds a weight from BadDataWeight(reason); a background
	// decay in the overlay halves the balance every
	// badDataDecayInterval so a chatty-but-honest peer can recover while
	// a persistent offender eventually evicts. Stored as int64 to
	// accommodate potential negative values from decay overshoot.
	badDataBalance atomic.Int64

	// closedLedger / previousLedger are also refreshed by inbound
	// mtSTATUS_CHANGE (PeerImp.cpp:1812-1862).
	instanceCookie     uint64
	serverDomain       string
	closedLedger       [32]byte
	previousLedger     [32]byte
	hasClosedLedger    bool
	hasPreviousLedger  bool
	remoteIPSelfReport string
	localIPSelfReport  string
}

// PeerConfig holds peer connection configuration.
type PeerConfig struct {
	SendBufferSize int
	PeerTLSConfig  *peertls.Config
}

// DefaultPeerConfig returns the default peer configuration.
// Callers must populate PeerTLSConfig with cert + key from the local
// Identity before invoking Connect.
func DefaultPeerConfig() PeerConfig {
	return PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
	}
}

// NewPeer creates a new peer.
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

// RemoteIP returns the resolved remote IP from the actual TCP connection.
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

func (p *Peer) applyHandshakeExtras(x HandshakeExtras) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.instanceCookie = x.InstanceCookie
	p.serverDomain = x.ServerDomain
	p.closedLedger = x.ClosedLedger
	p.previousLedger = x.PreviousLedger
	p.hasClosedLedger = x.HasClosedLedger
	p.hasPreviousLedger = x.HasPreviousLedger
	p.remoteIPSelfReport = x.RemoteIPSelf
	p.localIPSelfReport = x.LocalIPSelf
}

// applyStatusChange mirrors rippled PeerImp.cpp:1812-1862.
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

func (p *Peer) InstanceCookie() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.instanceCookie
}

func (p *Peer) ServerDomain() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.serverDomain
}

// ClosedLedger returns (hash, ok); ok is false when no valid hint.
func (p *Peer) ClosedLedger() ([32]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.closedLedger, p.hasClosedLedger
}

// PreviousLedger returns (hash, ok); ok is false when no valid hint.
func (p *Peer) PreviousLedger() ([32]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.previousLedger, p.hasPreviousLedger
}

func (p *Peer) RemoteIPSelfReport() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.remoteIPSelfReport
}

func (p *Peer) LocalIPSelfReport() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.localIPSelfReport
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

// AcceptConnection sets the connection for an inbound peer. Returns
// ErrAlreadyConnected if the peer is not in the Disconnected state —
// without this check a concurrent AcceptConnection / Connect could
// clobber an in-flight handshake.
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

// performHandshake performs the XRPL HTTP upgrade handshake.
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

	// Server-Domain runs first to match rippled's verifyHandshake
	// (Handshake.cpp:235-239 — the very first throw, before signature
	// verification). A malformed Server-Domain from a peer with a
	// valid signature must still be rejected before any further work.
	if _, err := ValidateServerDomain(resp.Header); err != nil {
		return NewHandshakeError(p.endpoint, "verify_extras", err)
	}

	// Full session-signature verification — the whole point of #269.
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

// Run starts the peer's read/write/ping loops.
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

// maxSquelchesPerPeer caps the per-peer squelch map to bound memory
// under adversarial input. A peer cannot squelch more than this many
// distinct validators at a time; once the cap is reached further
// AddSquelch calls for NEW validators are refused (existing entries
// may still be refreshed). Rippled doesn't document a fixed cap but
// its per-peer Slot has the same intent; 128 comfortably covers a
// UNL of realistic size while preventing unbounded growth.
const maxSquelchesPerPeer = 128

// AddSquelch records a squelch instruction received from this peer for the
// given validator. Mirrors rippled's `Squelch::addSquelch`: returns false
// (and removes any prior squelch) when duration is outside the allowed
// [MinUnsquelchExpire, MaxUnsquelchExpirePeers] range.
//
// An out-of-range duration is treated as a bad-data event attributed to
// the peer — rippled's equivalent path charges feeInvalidData. We keep
// the increment here (the only place the duration is checked) so callers
// in the overlay message layer don't need a separate "did we reject it"
// branch and so the counter can never miss a rejection.
//
// Returns false when the per-peer squelch map is full AND the validator
// is not already present — prevents a peer from spamming squelches for
// random validator keys to grow our memory footprint. An existing entry
// is still refreshable regardless of cap.
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
		// Cap hit with a fresh key — refuse the insert and charge
		// the peer for overflowing our buffer. IncBadData only
		// touches an atomic counter so it's safe outside squelchMu.
		p.IncBadData("squelch-map-full")
		return false
	}
	return true
}

// IncBadData records an invalid-data event attributed to this peer and
// returns the new cumulative balance. `reason` is a short stable label
// used both for diagnostic logging AND to look up a per-reason weight
// via BadDataWeight — mirrors rippled's Resource::Consumer which
// charges different fee amounts (feeInvalidData=400,
// feeMalformedRequest=200, feeRequestNoReply=10) depending on the
// severity of the offense.
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

// BadDataCount returns the cumulative invalid-data balance for this
// peer (clamped to non-negative). Thread-safe. The return type stays
// uint32 for compatibility with callers that treat this as a count;
// the underlying storage is int64 to handle decay overshoot.
func (p *Peer) BadDataCount() uint32 {
	n := p.badDataBalance.Load()
	if n < 0 {
		return 0
	}
	return uint32(n)
}

// DecayBadData halves the bad-data balance. Called by the overlay's
// maintenance loop on a fixed cadence so transient protocol hiccups
// (a flaky peer throwing a few malformed compression frames) don't
// accumulate to eviction over a long session. Rippled's Resource::
// Fees.cpp:26-43 uses a similar exponential decay. The floor at 0
// prevents the balance from going meaningfully negative.
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

// RemoveSquelch deletes any squelch entry for the given validator.
// Mirrors rippled's `Squelch::removeSquelch`.
func (p *Peer) RemoveSquelch(validator []byte) {
	p.squelchMu.Lock()
	delete(p.squelchMap, string(validator))
	p.squelchMu.Unlock()
}

// ExpireSquelch reports whether a message originating from `validator`
// may be relayed to this peer. Returns true when there is no squelch or
// the existing squelch has expired (and clears the expired entry); false
// when an active squelch is in effect. Mirrors rippled's
// `Squelch::expireSquelch`.
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

	// Squelch expired — remove and allow.
	p.squelchMu.Lock()
	if d, stillThere := p.squelchMap[key]; stillThere && !d.After(time.Now()) {
		delete(p.squelchMap, key)
	}
	p.squelchMu.Unlock()
	return true
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

	// Issue-#270 handshake fields. ClosedLedger / PreviousLedger are
	// upper-case hex (rippled `peers` convention), "" when absent.
	InstanceCookie uint64
	ServerDomain   string
	ClosedLedger   string
	PreviousLedger string
	RemoteIP       string
	LocalIP        string
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

	var closedLedger, previousLedger string
	if p.hasClosedLedger {
		closedLedger = strings.ToUpper(hex.EncodeToString(p.closedLedger[:]))
	}
	if p.hasPreviousLedger {
		previousLedger = strings.ToUpper(hex.EncodeToString(p.previousLedger[:]))
	}

	return PeerInfo{
		ID:             p.id,
		Endpoint:       p.endpoint,
		Inbound:        p.inbound,
		State:          p.state,
		PublicKey:      pubKey,
		ConnectedAt:    p.createdAt,
		MessagesIn:     stats.MessagesIn,
		MessagesOut:    stats.MessagesOut,
		InstanceCookie: p.instanceCookie,
		ServerDomain:   p.serverDomain,
		ClosedLedger:   closedLedger,
		PreviousLedger: previousLedger,
		RemoteIP:       p.remoteIPSelfReport,
		LocalIP:        p.localIPSelfReport,
	}
}
