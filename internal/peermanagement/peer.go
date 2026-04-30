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

// PeerTracking mirrors rippled PeerImp::Tracking (PeerImp.h:58).
type PeerTracking int32

const (
	PeerTrackingUnknown PeerTracking = iota
	PeerTrackingConverged
	PeerTrackingDiverged
)

// rippled Tuning.h.
const (
	convergedLedgerLimit uint32 = 24
	divergedLedgerLimit  uint32 = 128
)

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
	metrics *peerMetrics

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

	tracking atomic.Int32

	serverDomain      string
	networkID         string
	userAgent         string
	closedLedger      [32]byte
	previousLedger    [32]byte
	hasClosedLedger   bool
	hasPreviousLedger bool

	// protocolVersion: negotiated peer-protocol token (e.g. "XRPL/2.2").
	// Mirrors rippled PeerImp::protocol_, surfaced via `protocol` in the
	// peers RPC (PeerImp.cpp:419). Rippled constructs PeerImp only after
	// successful negotiation, so its field is never empty; in goXRPL the
	// Peer struct outlives the handshake, and the field stays "" if no
	// supported XRPL/X.Y survived NegotiateProtocolVersion /
	// VerifyOutboundProtocolVersion. Production peers reach PeersJSON
	// only after addPeer (post-handshake), so the empty case is
	// test-only — but we still emit it unconditionally to match
	// rippled's wire shape.
	protocolVersion string

	firstLedgerSeq uint32
	lastLedgerSeq  uint32

	latencyMu     sync.RWMutex
	pingsInFlight map[uint32]time.Time
	latency       time.Duration
	hasLatency    bool
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
		id:            id,
		endpoint:      endpoint,
		inbound:       inbound,
		identity:      identity,
		state:         PeerStateDisconnected,
		send:          make(chan []byte, DefaultSendBufferSize),
		events:        events,
		score:         NewPeerScore(),
		traffic:       NewTrafficCounter(),
		metrics:       newPeerMetrics(nil),
		squelchMap:    make(map[string]time.Time),
		pingsInFlight: make(map[uint32]time.Time),
		createdAt:     time.Now(),
		closeCh:       make(chan struct{}),
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
	p.networkID = x.NetworkID
	// PeerImp::getVersion (PeerImp.cpp:381-386) picks by direction.
	if p.inbound {
		p.userAgent = x.UserAgentHeader
	} else {
		p.userAgent = x.ServerHeader
	}
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
// Mirrors rippled PeerImp.cpp:1812-1883: lostSync clears closed/previous
// ledger only; the (firstSeq, lastSeq) range is updated only when both
// fields are present, then clamped to (0,0) if either is zero or inverted.
func (p *Peer) applyStatusChange(closed, previous []byte, lostSync bool, firstSeq, lastSeq *uint32) {
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
	if firstSeq == nil || lastSeq == nil {
		return
	}
	if *firstSeq == 0 || *lastSeq == 0 || *lastSeq < *firstSeq {
		p.firstLedgerSeq = 0
		p.lastLedgerSeq = 0
	} else {
		p.firstLedgerSeq = *firstSeq
		p.lastLedgerSeq = *lastSeq
	}
}

func (p *Peer) Tracking() PeerTracking {
	return PeerTracking(p.tracking.Load())
}

func (p *Peer) setTracking(t PeerTracking) {
	p.tracking.Store(int32(t))
}

// CheckTracking mirrors rippled PeerImp::checkTracking (PeerImp.cpp:1986-2005).
// CAS on the diverged branch keeps a concurrent Converged write from
// being clobbered (rippled holds recentLock_; CAS is the lock-free equivalent).
func (p *Peer) CheckTracking(peerSeq, validSeq uint32) {
	if peerSeq == 0 || validSeq == 0 {
		return
	}
	var diff uint32
	if peerSeq > validSeq {
		diff = peerSeq - validSeq
	} else {
		diff = validSeq - peerSeq
	}
	if diff < convergedLedgerLimit {
		p.tracking.Store(int32(PeerTrackingConverged))
		return
	}
	if diff > divergedLedgerLimit {
		for {
			cur := p.tracking.Load()
			if PeerTracking(cur) == PeerTrackingDiverged {
				return
			}
			if p.tracking.CompareAndSwap(cur, int32(PeerTrackingDiverged)) {
				return
			}
		}
	}
}

func (p *Peer) ServerDomain() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.serverDomain
}

// NetworkID reports the peer's reported Network-ID handshake header,
// or "" if the peer omitted it. Mirrors rippled PeerImp's
// headers_["Network-ID"] passthrough (PeerImp.cpp:411-412).
func (p *Peer) NetworkID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.networkID
}

// ProtocolVersion returns the negotiated peer-protocol token (e.g.
// "XRPL/2.2") captured during the handshake, or "" if unknown.
func (p *Peer) ProtocolVersion() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.protocolVersion
}

// setProtocolVersion is for tests; production paths set protocolVersion
// inline under p.mu alongside capabilities.
func (p *Peer) setProtocolVersion(v string) {
	p.mu.Lock()
	p.protocolVersion = v
	p.mu.Unlock()
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

// LedgerRange returns the peer's advertised (min, max) ledger sequence,
// or (0, 0) when no range has been advertised.
func (p *Peer) LedgerRange() (uint32, uint32) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.firstLedgerSeq, p.lastLedgerSeq
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
	protocol := VerifyOutboundProtocolVersion(resp.Header.Get(HeaderUpgrade))
	if protocol == "" {
		return NewHandshakeError(p.endpoint, "verify",
			fmt.Errorf("%w: unable to negotiate protocol version (server replied %q)",
				ErrInvalidHandshake, resp.Header.Get(HeaderUpgrade)))
	}
	p.mu.Lock()
	p.capabilities = caps
	p.protocolVersion = protocol
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

		// Account wire bytes (header + on-the-wire payload, before
		// decompression) — matches rippled metrics_.recv.add_message at
		// PeerImp.cpp:911 which uses bytes_transferred from the socket.
		wireBytes := uint64(len(payload))
		if header.Compressed {
			wireBytes += HeaderSizeCompressed
		} else {
			wireBytes += HeaderSizeUncompressed
		}
		p.metrics.recv.addMessage(wireBytes)

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

			n, err := conn.Write(data)
			if err != nil {
				return err
			}
			// Mirrors rippled metrics_.sent.add_message at
			// PeerImp.cpp:970 — account whatever the socket reports as
			// transferred.
			p.metrics.sent.addMessage(uint64(n))
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
			now := time.Now()
			seq := uint32(now.UnixMilli())
			ping := &message.Ping{
				PType: message.PingTypePing,
				Seq:   seq,
			}
			encoded, err := message.Encode(ping)
			if err != nil {
				continue
			}
			wireMsg, err := message.BuildWireMessage(message.TypePing, encoded)
			if err != nil {
				continue
			}
			p.recordPingSent(seq, now)
			if err := p.Send(wireMsg); err != nil {
				return err
			}
		}
	}
}

const (
	pingInFlightTTL  = 30 * time.Second
	pingsInFlightCap = 16
)

func (p *Peer) recordPingSent(seq uint32, sentAt time.Time) {
	p.latencyMu.Lock()
	defer p.latencyMu.Unlock()
	cutoff := sentAt.Add(-pingInFlightTTL)
	for k, t := range p.pingsInFlight {
		if t.Before(cutoff) {
			delete(p.pingsInFlight, k)
		}
	}
	if len(p.pingsInFlight) >= pingsInFlightCap {
		var (
			oldestKey  uint32
			oldestTime time.Time
			haveOldest bool
		)
		for k, t := range p.pingsInFlight {
			if !haveOldest || t.Before(oldestTime) {
				oldestKey = k
				oldestTime = t
				haveOldest = true
			}
		}
		if haveOldest {
			delete(p.pingsInFlight, oldestKey)
		}
	}
	p.pingsInFlight[seq] = sentAt
}

// roundMillisHalfEven rounds d to the nearest millisecond, breaking
// ties toward the even multiple — mirrors C++ std::chrono::round.
func roundMillisHalfEven(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	const ms = int64(time.Millisecond)
	n := int64(d)
	q, r := n/ms, n%ms
	if r > ms/2 || (r == ms/2 && q%2 != 0) {
		q++
	}
	return time.Duration(q) * time.Millisecond
}

// OnPong correlates a Pong with the matching Ping by seq and updates
// the EWMA-smoothed latency. Mirrors PeerImp::onMessage(TMPing)
// (PeerImp.cpp:1099-1118): rtt is rounded to ms, then smoothed at ms
// granularity via latency = (latency*7 + rtt) / 8.
func (p *Peer) OnPong(seq uint32, receivedAt time.Time) {
	p.latencyMu.Lock()
	defer p.latencyMu.Unlock()
	sentAt, ok := p.pingsInFlight[seq]
	if !ok {
		return
	}
	delete(p.pingsInFlight, seq)
	rtt := roundMillisHalfEven(receivedAt.Sub(sentAt))
	if !p.hasLatency {
		p.latency = rtt
		p.hasLatency = true
		return
	}
	prev := int64(p.latency / time.Millisecond)
	sample := int64(rtt / time.Millisecond)
	p.latency = time.Duration((prev*7+sample)/8) * time.Millisecond
}

func (p *Peer) Latency() (time.Duration, bool) {
	p.latencyMu.RLock()
	defer p.latencyMu.RUnlock()
	return p.latency, p.hasLatency
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

// PeerInfo is a read-only snapshot of peer state.
type PeerInfo struct {
	ID             PeerID
	Endpoint       Endpoint
	Inbound        bool
	State          PeerState
	PublicKey      string
	PublicKeyBytes []byte
	ConnectedAt    time.Time
	MessagesIn     uint64
	MessagesOut    uint64

	ServerDomain    string
	NetworkID       string
	Version         string
	ClosedLedger    string
	CompleteLedgers string
	Tracking        PeerTracking
	Load            int64

	Latency    time.Duration
	HasLatency bool

	Protocol string

	// Per-peer wire byte counters and rolling-window throughput.
	// Mirrors rippled PeerImp::metrics_ (PeerImp.h:226-230). Emitted
	// under the `metrics` object in `peers` RPC.
	TotalBytesRecv uint64
	TotalBytesSent uint64
	AvgBpsRecv     uint64
	AvgBpsSent     uint64
}

func (p *Peer) Info() PeerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var (
		pubKey      string
		pubKeyBytes []byte
	)
	if p.remotePubKey != nil {
		pubKey = p.remotePubKey.Encode()
		pubKeyBytes = p.remotePubKey.Bytes()
	}

	stats := p.traffic.GetTotalStats()

	var closedLedger string
	if p.hasClosedLedger {
		closedLedger = strings.ToUpper(hex.EncodeToString(p.closedLedger[:]))
	}

	var completeLedgers string
	if p.firstLedgerSeq != 0 || p.lastLedgerSeq != 0 {
		completeLedgers = fmt.Sprintf("%d - %d", p.firstLedgerSeq, p.lastLedgerSeq)
	}

	latency, hasLatency := p.Latency()

	return PeerInfo{
		ID:              p.id,
		Endpoint:        p.endpoint,
		Inbound:         p.inbound,
		State:           p.state,
		PublicKey:       pubKey,
		PublicKeyBytes:  pubKeyBytes,
		ConnectedAt:     p.createdAt,
		MessagesIn:      stats.MessagesIn,
		MessagesOut:     stats.MessagesOut,
		ServerDomain:    p.serverDomain,
		NetworkID:       p.networkID,
		Version:         p.userAgent,
		ClosedLedger:    closedLedger,
		CompleteLedgers: completeLedgers,
		Tracking:        PeerTracking(p.tracking.Load()),
		Load:            p.Load(),
		Latency:         latency,
		HasLatency:      hasLatency,
		Protocol:        p.protocolVersion,
		TotalBytesRecv:  p.metrics.recv.totalBytesSnapshot(),
		TotalBytesSent:  p.metrics.sent.totalBytesSnapshot(),
		AvgBpsRecv:      p.metrics.recv.averageBytes(),
		AvgBpsSent:      p.metrics.sent.averageBytes(),
	}
}
