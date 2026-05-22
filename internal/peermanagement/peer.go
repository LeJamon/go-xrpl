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
	"github.com/LeJamon/goXRPLd/internal/peermanagement/resource"
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

	// droppedEvents counts non-blocking event sends that fell through
	// because the overlay event loop was wedged. nil until wired by
	// Overlay; back-pressure must surface as a counter rather than
	// deadlocking the read hot path.
	droppedEvents *atomic.Uint64

	// runWG joins read/write/ping goroutines so callers of Run observe
	// a fully-quiesced peer rather than racing against goroutines
	// parked on a slow OS-level close.
	runWG sync.WaitGroup

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

	// usage points at this peer's entry in the overlay's
	// resource.Manager — the source of truth for per-endpoint cost,
	// decay, and reconnect blacklist. onDropDisconnect mirrors
	// rippled's overlay_.incPeerDisconnectCharges hook
	// (PeerImp.cpp:358) and is wired by Overlay.attachUsage.
	usage            *resource.Consumer
	usageMu          sync.RWMutex
	onDropDisconnect func()
	// chargeDropFired CAS-gates the once-per-peer onDropDisconnect
	// callback and disconnect log line. Rippled serialises this via
	// the peer strand (PeerImp.cpp:355); goxrpl has no strand, so
	// concurrent Drop observers each see Disconnect()==true but only
	// one wins the CAS and fires the metric/log.
	chargeDropFired atomic.Bool

	tracking atomic.Int32

	// Consecutive ErrSendBufferFull count; close at sendqIntervals.
	// PeerImp.cpp:705-708 "Large send queue".
	largeSendQ atomic.Uint32

	// consecutiveDecompressFailures: back-to-back LZ4 errors; closed
	// at maxConsecutiveDecompressFailures.
	consecutiveDecompressFailures atomic.Uint32

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

	lastStatus message.NodeStatus

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

// SetDroppedEventsCounter wires the counter dispatchEvent bumps on
// non-blocking sends that fall through. Safe to call once before Run.
func (p *Peer) SetDroppedEventsCounter(c *atomic.Uint64) {
	p.droppedEvents = c
}

// dispatchEvent attempts a non-blocking send to the events channel.
// The read hot path and Close path must never block on the event
// loop, which itself takes overlay-level locks.
func (p *Peer) dispatchEvent(evt Event) {
	if p.events == nil {
		return
	}
	select {
	case p.events <- evt:
	default:
		if p.droppedEvents != nil {
			p.droppedEvents.Add(1)
		}
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
//
// newStatus mirrors rippled PeerImp.cpp:1799-1810. Read carefully: rippled's
// branches both end with `last_status_ = *m;`, which copy-assigns the
// inbound proto verbatim — so the stored last_status_.newstatus() is
// dropped whenever the wire message has no newstatus. The else-branch
// additionally mutates the local `m` to carry the prior enum, so the
// pubPeerStatus callback (which reads `m`, not last_status_) still sees
// the inherited value once. We model the same split:
//   - lastStatus is overwritten verbatim with the wire's NewStatus (zero
//     argument drops the prior value, matching rippled's `peers` RPC).
//   - The returned effective status is the wire value when set, or the
//     prior lastStatus otherwise — consumed by handleStatusChange's
//     pubPeerStatus emit so subscribers receive the inherited enum.
//
// The lostSync early-return runs after the lastStatus write, so a
// lostSync update carrying a NewStatus still records it — but
// handleStatusChange returns before any publish (PeerImp.cpp:1830).
func (p *Peer) applyStatusChange(sc *message.StatusChange) message.NodeStatus {
	if sc == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	effective := sc.NewStatus
	if sc.NewStatus == 0 {
		effective = p.lastStatus
	}
	p.lastStatus = sc.NewStatus
	if sc.NewEvent == message.NodeEventLostSync {
		p.hasClosedLedger = false
		p.hasPreviousLedger = false
		p.closedLedger = [32]byte{}
		p.previousLedger = [32]byte{}
		return effective
	}
	if len(sc.LedgerHash) == 32 {
		copy(p.closedLedger[:], sc.LedgerHash)
		p.hasClosedLedger = true
	} else {
		p.hasClosedLedger = false
		p.closedLedger = [32]byte{}
	}
	if len(sc.LedgerHashPrevious) == 32 {
		copy(p.previousLedger[:], sc.LedgerHashPrevious)
		p.hasPreviousLedger = true
	} else {
		p.hasPreviousLedger = false
		p.previousLedger = [32]byte{}
	}
	if sc.FirstSeq == nil || sc.LastSeq == nil {
		return effective
	}
	if *sc.FirstSeq == 0 || *sc.LastSeq == 0 || *sc.LastSeq < *sc.FirstSeq {
		p.firstLedgerSeq = 0
		p.lastLedgerSeq = 0
	} else {
		p.firstLedgerSeq = *sc.FirstSeq
		p.lastLedgerSeq = *sc.LastSeq
	}
	return effective
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

// LastStatus returns the peer's most recently advertised NodeStatus
// (rippled's last_status_.newstatus()). Returns 0 (nsUNKNOWN) when the
// peer has never sent a TMStatusChange with new_status, OR when the
// most recent TMStatusChange omitted new_status — both cases drop the
// stored value, mirroring rippled's `last_status_ = *m;` overwrite at
// PeerImp.cpp:1802 / 1807. This is what the `peers` RPC's
// `if (last_status.has_newstatus())` gate (PeerImp.cpp:463) reads;
// the per-event pubPeerStatus inheritance is a separate path that
// flows through applyStatusChange's return value.
func (p *Peer) LastStatus() message.NodeStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastStatus
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

// Run starts read/write/ping loops and blocks until all three have
// returned. The first loop to error (or ctx cancellation) triggers
// Close, which fans the shutdown out to the others via the closed
// conn and closeCh; runWG.Wait ensures a fully-quiesced peer before
// return so a slow OS-level close cannot leak goroutines past the
// caller's cleanup.
func (p *Peer) Run(ctx context.Context) error {
	p.mu.RLock()
	if p.state != PeerStateConnected {
		p.mu.RUnlock()
		return ErrConnectionClosed
	}
	p.mu.RUnlock()

	errCh := make(chan error, 3)

	p.runWG.Add(3)
	go func() {
		defer p.runWG.Done()
		errCh <- p.readLoop(ctx)
	}()

	go func() {
		defer p.runWG.Done()
		errCh <- p.writeLoop(ctx)
	}()

	go func() {
		defer p.runWG.Done()
		errCh <- p.pingLoop(ctx)
	}()

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case err := <-errCh:
		runErr = err
	}
	p.Close()
	p.runWG.Wait()
	return runErr
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
		conn := p.conn
		p.mu.RUnlock()

		if reader == nil {
			return ErrConnectionClosed
		}

		// Refresh the read deadline each iteration so a peer that goes
		// silent post-handshake cannot park us in io.ReadFull forever.
		if conn != nil {
			_ = conn.SetReadDeadline(time.Now().Add(readIdleDeadline))
		}

		header, payload, err := ReadMessage(reader)
		if err != nil {
			if p.closed.Load() {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return ErrReadIdle
			}
			// Over-budget size claim is protocol abuse — charge so the
			// reputation system evicts. Other framing errors are
			// unrecoverable and tear the peer down directly.
			if errors.Is(err, message.ErrMessageTooLarge) {
				p.IncBadData("message-too-large")
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
				p.IncBadData("decompress-lz4-failed")
				if p.consecutiveDecompressFailures.Add(1) >= maxConsecutiveDecompressFailures {
					return fmt.Errorf("decompress-lz4 failed %d times in a row: %w",
						maxConsecutiveDecompressFailures, err)
				}
				continue
			}
			p.consecutiveDecompressFailures.Store(0)
		}

		p.traffic.AddCount(CategorizeMessage(uint16(header.MessageType)), true, len(payload))

		p.dispatchEvent(Event{
			Type:        EventMessageReceived,
			PeerID:      p.id,
			MessageType: uint16(header.MessageType),
			Payload:     payload,
		})
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

			// Cap each Write so a half-open TCP peer with a
			// never-draining kernel send buffer cannot pin the writer.
			_ = conn.SetWriteDeadline(time.Now().Add(writeIdleDeadline))
			n, err := conn.Write(data)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return ErrWriteIdle
				}
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
	// First probe at t≈pingTimeout matches rippled's setTimer+onTimer
	// envelope (PeerImp.cpp:61,690-748): disconnect-on-silence at
	// t≈2*pingTimeout.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closeCh:
		return nil
	case <-time.After(pingTimeout):
	}

	if err := p.runPingTick(time.Now()); err != nil {
		return err
	}

	ticker := time.NewTicker(pingProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closeCh:
			return nil
		case <-ticker.C:
			if err := p.runPingTick(time.Now()); err != nil {
				return err
			}
		}
	}
}

func (p *Peer) runPingTick(now time.Time) error {
	if seq, age, ok := p.staleInFlightPing(now, pingTimeout); ok {
		slog.Warn("peer ping timeout",
			"t", "Peer", "peer", p.id,
			"endpoint", p.endpoint.String(),
			"seq", seq, "age", age,
		)
		return ErrPingTimeout
	}
	// PeerImp.cpp:705-708 "Large send queue" disconnect.
	if p.largeSendQ.Load() >= sendqIntervals {
		slog.Warn("peer large send queue",
			"t", "Peer", "peer", p.id,
			"endpoint", p.endpoint.String(),
			"intervals", p.largeSendQ.Load(),
		)
		return ErrLargeSendQueue
	}
	seq := uint32(now.UnixMilli())
	ping := &message.Ping{
		PType: message.PingTypePing,
		Seq:   seq,
	}
	encoded, err := message.Encode(ping)
	if err != nil {
		return nil
	}
	wireMsg, err := message.BuildWireMessage(message.TypePing, encoded)
	if err != nil {
		return nil
	}
	p.recordPingSent(seq, now)
	if err := p.Send(wireMsg); err != nil {
		return err
	}
	return nil
}

const (
	// pingTimeout: disconnect when the oldest unanswered ping reaches
	// this age, and the cold-start delay before pingLoop's first
	// probe. Mirrors rippled's peerTimerInterval (PeerImp.cpp:61) and
	// the fail("Ping Timeout") branch at PeerImp.cpp:731-736.
	pingTimeout = 60 * time.Second
	// pingProbeInterval: cadence after the first probe. Finer than
	// rippled's 60s peerTimerInterval; OnPong's sweep coalesces
	// concurrent in-flight pings so the disconnect criterion stays
	// "no pong for ≥pingTimeout".
	pingProbeInterval = 15 * time.Second
	// pingInFlightTTL bounds map growth between successful pongs. Set
	// equal to pingTimeout so recordPingSent's GC sweep cannot evict
	// an entry that staleInFlightPing would still treat as live: any
	// entry with age < pingTimeout has age < pingInFlightTTL and is
	// retained, while stale entries (age >= pingTimeout) trigger the
	// disconnect on this tick before a future tick's GC could mask
	// them. Any TTL smaller than pingTimeout would silently shrink
	// the disconnect window.
	pingInFlightTTL  = pingTimeout
	pingsInFlightCap = 16
	// Tuning::sendqIntervals (PeerImp.cpp:705 + Tuning.h).
	sendqIntervals = 4
	// maxConsecutiveDecompressFailures: close after this many
	// back-to-back LZ4 errors. A single bad frame still charges
	// bad-data but resets on the next successful decompress.
	maxConsecutiveDecompressFailures = 4
	// readIdleDeadline: belt-and-braces backstop to ErrPingTimeout for
	// a peer that completes the handshake then goes silent. Set above
	// pingTimeout so the existing disconnect path stays primary.
	readIdleDeadline = pingTimeout + 5*time.Second
	// writeIdleDeadline bounds a single conn.Write so a half-open TCP
	// peer with a never-draining kernel send buffer cannot pin
	// writeLoop forever.
	writeIdleDeadline = 10 * time.Second
)

func (p *Peer) staleInFlightPing(now time.Time, threshold time.Duration) (seq uint32, age time.Duration, ok bool) {
	p.latencyMu.RLock()
	defer p.latencyMu.RUnlock()
	var (
		oldestSeq  uint32
		oldestSent time.Time
		have       bool
	)
	for s, t := range p.pingsInFlight {
		if !have || t.Before(oldestSent) {
			oldestSeq = s
			oldestSent = t
			have = true
		}
	}
	if !have {
		return 0, 0, false
	}
	age = now.Sub(oldestSent)
	if age < threshold {
		return 0, 0, false
	}
	return oldestSeq, age, true
}

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
//
// A matching pong also evicts every pending entry sent at-or-before
// the matched send-time. Rippled keeps a single in-flight ping
// (PeerImp.h:115 `std::optional<uint32_t> lastPingSeq_`), so its 60s
// ping-timeout fires only when the most-recent cycle goes
// unanswered. goXRPL ticks at 15s and may queue several pings while
// a pong is in transit; without this sweep, a peer that drops one
// ping per 60s window but otherwise responds promptly would be
// evicted by staleInFlightPing's oldest-wins check, while rippled
// would keep it. Clearing older entries makes the disconnect
// criterion "no pong for ≥pingTimeout" instead of "any single ping
// ≥pingTimeout old", matching rippled's single-cycle semantics.
func (p *Peer) OnPong(seq uint32, receivedAt time.Time) {
	p.latencyMu.Lock()
	defer p.latencyMu.Unlock()
	sentAt, ok := p.pingsInFlight[seq]
	if !ok {
		return
	}
	delete(p.pingsInFlight, seq)
	for s, t := range p.pingsInFlight {
		if !t.After(sentAt) {
			delete(p.pingsInFlight, s)
		}
	}
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

// attachUsage wires this peer's resource.Consumer and the
// onDropDisconnect hook. Re-attaching releases the prior Consumer so
// the Manager's refcount stays balanced.
func (p *Peer) attachUsage(c *resource.Consumer, onDrop func()) {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	if p.usage != nil {
		p.usage.Release()
	}
	p.usage = c
	p.onDropDisconnect = onDrop
}

func (p *Peer) releaseUsage() {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	if p.usage != nil {
		p.usage.Release()
		p.usage = nil
	}
}

func (p *Peer) usageHandle() *resource.Consumer {
	p.usageMu.RLock()
	c := p.usage
	p.usageMu.RUnlock()
	if c != nil {
		return c
	}
	// Tests and embedded usages that construct a Peer outside the
	// addPeer path still need IncBadData / Charge to work. Lazily
	// create a private Manager-backed Consumer so the bad-data path
	// stays self-contained instead of silently no-op'ing. Production
	// addPeer attaches a real Consumer before this fallback can fire.
	//
	// The lazily-created Manager is NOT Started — its PeriodicActivity
	// goroutine never runs, so inactive entries are not aged out. That
	// is fine for the test-only callers this branch serves (the
	// Manager is GCed when the Peer goes out of scope), but means the
	// fallback must not be relied on for production paths.
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	if p.usage != nil {
		return p.usage
	}
	rm := resource.NewManager(nil, nil)
	p.usage = rm.NewInboundEndpoint(p.endpoint.String())
	if p.onDropDisconnect == nil {
		p.onDropDisconnect = func() {}
	}
	return p.usage
}

// Charge applies fee to this peer's Consumer and tears the peer down
// on Drop. Mirrors rippled PeerImp::charge at PeerImp.cpp:351-361.
func (p *Peer) Charge(fee resource.Charge, context string) resource.Disposition {
	c := p.usageHandle()
	if c == nil {
		return resource.Ok
	}
	d := c.Charge(fee, context)
	if d == resource.Drop && c.Disconnect() {
		// CAS-gate the metric + log so concurrent Charge callers that
		// each observe Drop only count one disconnect per peer.
		if p.chargeDropFired.CompareAndSwap(false, true) {
			slog.Warn("peer disconnect by resource charge",
				"t", "Peer", "peer", p.id,
				"endpoint", p.endpoint.String(), "fee", fee.String(), "context", context)
			p.usageMu.RLock()
			hook := p.onDropDisconnect
			p.usageMu.RUnlock()
			if hook != nil {
				hook()
			}
		}
		// Fire-and-forget; Close is safe to call concurrently.
		p.Close()
	}
	return d
}

// chargeForReason maps a goxrpl reason label to a resource.Charge
// tiered against rippled's Resource::Fees. Unknown labels fall
// through to FeeInvalidData.
func chargeForReason(reason string) resource.Charge {
	switch reason {
	case "proposal-malformed-sig-size",
		"proposal-malformed-pubkey-size",
		"validation-malformed-sig-size":
		return resource.FeeInvalidSignature
	case "replay-delta-verify",
		"ledger-data-base",
		"ledger-data-state",
		"squelch-duration",
		"squelch-map-full",
		"squelch-malformed-pubkey",
		"decompress-lz4-failed",
		"message-too-large":
		return resource.FeeInvalidData
	case "proposal-malformed-prev-ledger-size",
		"proposal-malformed-txset-size",
		"validation-malformed-ledger-hash-zero",
		"validation-malformed-node-id-zero",
		"handshake-malformed-networkid",
		"handshake-malformed-networktime",
		"handshake-malformed-extras",
		"replay-delta-resp-decode",
		"replay-delta-req-decode",
		"replay-delta-req-bad",
		"proof-path-req-decode",
		"proof-path-req-bad",
		"proof-path-req-unnegotiated",
		"replay-delta-req-unnegotiated",
		"replay-delta-resp-unnegotiated",
		"proof-path-resp-unnegotiated",
		"proposal-decode",
		"validation-decode",
		"validation-parse",
		"ledger-data-decode",
		"squelch-ignored":
		return resource.FeeMalformedRequest
	case "no-reply":
		return resource.FeeRequestNoReply
	}
	switch {
	case reason == "vl-coll-no-blobs",
		strings.HasSuffix(reason, "-heavy-no-blobs"):
		return resource.FeeHeavyBurdenPeer
	case strings.Contains(reason, "-badsig-"):
		return resource.FeeInvalidSignature
	case strings.Contains(reason, "-baddata-"),
		strings.HasSuffix(reason, "-wrong-version"),
		strings.HasSuffix(reason, "-decode"):
		return resource.FeeInvalidData
	case strings.Contains(reason, "-useless-"),
		strings.HasSuffix(reason, "-duplicate"),
		strings.HasSuffix(reason, "-unsupported-peer"):
		return resource.FeeUselessData
	}
	return resource.FeeInvalidData
}

// IncBadData routes a reason-keyed charge through the resource
// manager and returns the post-charge normalized balance.
func (p *Peer) IncBadData(reason string) uint32 {
	c := p.usageHandle()
	if c == nil {
		return 0
	}
	p.Charge(chargeForReason(reason), reason)
	bal := c.Balance()
	if bal < 0 {
		return 0
	}
	return uint32(bal)
}

// BadDataCount returns the consumer's current normalized balance,
// clamped to non-negative.
func (p *Peer) BadDataCount() uint32 {
	c := p.usageHandle()
	if c == nil {
		return 0
	}
	bal := c.Balance()
	if bal < 0 {
		return 0
	}
	return uint32(bal)
}

// Load returns the consumer's normalized balance as int64 — signed so
// callers can observe transient negative values during decay.
func (p *Peer) Load() int64 {
	c := p.usageHandle()
	if c == nil {
		return 0
	}
	return int64(c.Balance())
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
		// PeerImp.cpp:270-276: reset below targetSendQueue.
		p.largeSendQ.Store(0)
		return nil
	default:
		// Sustained backpressure → close via runPingTick.
		p.largeSendQ.Add(1)
		return ErrSendBufferFull
	}
}

// SendQueueLen returns the number of frames currently buffered for
// transmission to this peer. Used by handlers that should refuse new
// outbound work when the pipe is already saturated — mirrors the
// rippled gate at PeerImp.cpp:2452 (`send_queue_.size() >=
// Tuning::dropSendQueue`).
func (p *Peer) SendQueueLen() int {
	return len(p.send)
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

	p.dispatchEvent(Event{
		Type:     EventPeerDisconnected,
		PeerID:   p.id,
		Endpoint: p.endpoint,
	})

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

	Status message.NodeStatus

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
		Status:          p.lastStatus,
		TotalBytesRecv:  p.metrics.recv.totalBytesSnapshot(),
		TotalBytesSent:  p.metrics.sent.totalBytesSnapshot(),
		AvgBpsRecv:      p.metrics.recv.averageBytes(),
		AvgBpsSent:      p.metrics.sent.averageBytes(),
	}
}
