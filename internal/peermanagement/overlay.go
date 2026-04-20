package peermanagement

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"golang.org/x/sync/errgroup"
)

// Overlay is the central orchestrator for XRPL peer-to-peer networking.
// It manages peer connections, discovery, message routing, and the reduce-relay system.
type Overlay struct {
	cfg      Config
	identity *Identity

	// Components
	discovery  *Discovery
	relay      *Relay
	ledgerSync *LedgerSyncHandler

	// Peer management
	peers   map[PeerID]*Peer
	peersMu sync.RWMutex
	nextID  atomic.Uint64

	// Coordination channels
	events   chan Event
	messages chan *InboundMessage

	// Network
	listener net.Listener

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

// LedgerSync returns the overlay's ledger-sync handler so callers in a
// higher layer (e.g., consensus startup) can wire a LedgerProvider that
// imports internal/ledger packages — which this layer cannot.
func (o *Overlay) LedgerSync() *LedgerSyncHandler { return o.ledgerSync }

// ListenAddr returns the resolved address the overlay is accepting
// connections on, or the empty string if no listener is bound. Useful
// when the overlay was configured with port 0 (ephemeral) and the
// caller needs the actual port to drive a peer connection — e.g.,
// integration tests that wire two overlays together on localhost.
func (o *Overlay) ListenAddr() string {
	if o.listener == nil {
		return ""
	}
	return o.listener.Addr().String()
}

// New creates a new Overlay with the provided options.
func New(opts ...Option) (*Overlay, error) {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Load or create identity
	identity, err := loadOrCreateIdentity(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("identity error: %w", err)
	}

	events := make(chan Event, 256)

	o := &Overlay{
		cfg:        cfg,
		identity:   identity,
		discovery:  NewDiscovery(&cfg, events),
		relay:      NewRelay(&cfg, nil), // squelch callback set below
		ledgerSync: NewLedgerSyncHandler(events),
		peers:      make(map[PeerID]*Peer),
		events:     events,
		messages:   make(chan *InboundMessage, 256),
	}

	// Set squelch callback for reduce-relay
	o.relay.onSquelch = o.handleSquelch

	return o, nil
}

// loadOrCreateIdentity loads existing identity or creates a new one.
func loadOrCreateIdentity(dataDir string) (*Identity, error) {
	if dataDir == "" {
		return GenerateIdentity()
	}

	// Try to load existing identity
	id, err := LoadIdentity(dataDir)
	if err == nil {
		return id, nil
	}

	// Generate new identity
	id, err = GenerateIdentity()
	if err != nil {
		return nil, err
	}

	// Try to save it (ignore errors if dataDir doesn't exist)
	_ = id.Save(dataDir)

	return id, nil
}

// Run starts the overlay and blocks until the context is cancelled.
func (o *Overlay) Run(ctx context.Context) error {
	o.ctx, o.cancel = context.WithCancel(ctx)
	defer o.cancel()

	// Start listener if configured
	if o.cfg.ListenAddr != "" {
		if err := o.startListener(); err != nil {
			return fmt.Errorf("listener error: %w", err)
		}
	}

	// Start discovery
	if err := o.discovery.Start(o.ctx); err != nil {
		return fmt.Errorf("discovery error: %w", err)
	}

	g, gCtx := errgroup.WithContext(o.ctx)

	// Accept incoming connections
	if o.listener != nil {
		g.Go(func() error { return o.acceptLoop(gCtx) })
	}

	// Event processing loop
	g.Go(func() error { return o.eventLoop(gCtx) })

	// Discovery/autoconnect loop
	g.Go(func() error { return o.discoveryLoop(gCtx) })

	// Maintenance loop (cleanup, ping, etc.)
	g.Go(func() error { return o.maintenanceLoop(gCtx) })

	return g.Wait()
}

// Stop gracefully shuts down the overlay.
func (o *Overlay) Stop() error {
	if o.cancel != nil {
		o.cancel()
	}

	// Close listener
	if o.listener != nil {
		o.listener.Close()
	}

	// Stop discovery
	o.discovery.Stop()

	// Close all peers
	o.peersMu.Lock()
	for _, p := range o.peers {
		p.Close()
	}
	o.peersMu.Unlock()

	return nil
}

// startListener creates and starts the TCP/TLS listener.
func (o *Overlay) startListener() error {
	tcpListener, err := net.Listen("tcp", o.cfg.ListenAddr)
	if err != nil {
		return err
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{o.identity.TLSCertificate()},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
		ClientAuth:         tls.RequestClientCert,
	}

	o.listener = tls.NewListener(tcpListener, tlsConfig)
	return nil
}

// acceptLoop accepts incoming connections.
func (o *Overlay) acceptLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := o.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		go o.handleInbound(ctx, conn)
	}
}

// handleInbound handles an incoming peer connection.
func (o *Overlay) handleInbound(ctx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in inbound handler", "t", "Overlay", "panic", r)
			conn.Close()
		}
	}()

	// Check if we can accept more inbound connections
	if !o.canAcceptInbound() {
		slog.Info("Inbound rejected: no slots", "t", "Overlay", "remote", conn.RemoteAddr())
		conn.Close()
		return
	}

	remoteAddr := conn.RemoteAddr().String()
	endpoint, _ := ParseEndpoint(remoteAddr)

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, true, o.identity, o.events)
	peer.AcceptConnection(conn)

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		slog.Error("Inbound connection is not TLS", "t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}

	// Perform handshake
	if err := o.performInboundHandshake(ctx, peer, tlsConn); err != nil {
		slog.Info("Inbound handshake failed", "t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		o.events <- Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  true,
			Error:    err,
		}
		return
	}

	// Reject duplicate: if we already have a connection to this IP.
	if o.isConnectedTo(endpoint) {
		conn.Close()
		return
	}

	peer.setState(PeerStateConnected)
	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	o.addPeer(peer)

	// Run peer read/write loops
	go func() {
		err := peer.Run(ctx)
		if err != nil {
			slog.Info("Inbound peer run ended", "t", "Overlay", "remote", remoteAddr, "err", err)
		}
		o.removePeer(peerID)
	}()
}

// performInboundHandshake handles the inbound handshake.
func (o *Overlay) performInboundHandshake(ctx context.Context, peer *Peer, tlsConn *tls.Conn) error {
	// The TLS handshake is lazy after Accept(); we must complete it
	// before accessing the finished messages via reflection.
	handshakeCtx, cancel := context.WithTimeout(ctx, o.cfg.HandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return NewHandshakeError(peer.Endpoint(), "tls", err)
	}

	sharedValue, err := MakeSharedValue(tlsConn)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "shared_value", err)
	}

	// Use a buffered reader to parse the HTTP request precisely
	// without consuming binary protocol data that follows.
	deadline := time.Now().Add(o.cfg.HandshakeTimeout)
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	bufReader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}
	req.Body.Close()

	// Store the buffered reader on the peer for the readLoop
	peer.mu.Lock()
	peer.bufReader = bufReader
	peer.mu.Unlock()

	// Build and send response
	cfg := HandshakeConfig{
		UserAgent:   o.cfg.UserAgent,
		NetworkID:   o.cfg.NetworkID,
		CrawlPublic: false,
	}

	resp := BuildHandshakeResponse(o.identity, sharedValue, cfg)
	if err := resp.Write(tlsConn); err != nil {
		return NewHandshakeError(peer.Endpoint(), "send_response", err)
	}

	return nil
}

// eventLoop processes internal events.
func (o *Overlay) eventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-o.events:
			o.handleEvent(evt)
		}
	}
}

// handleEvent dispatches events to appropriate handlers.
func (o *Overlay) handleEvent(evt Event) {
	switch evt.Type {
	case EventPeerConnected:
		o.onPeerConnected(evt)
	case EventPeerHandshakeComplete:
		o.onPeerHandshakeComplete(evt)
	case EventPeerDisconnected:
		o.onPeerDisconnected(evt)
	case EventPeerFailed:
		o.onPeerFailed(evt)
	case EventMessageReceived:
		o.onMessageReceived(evt)
	case EventEndpointsReceived:
		o.onEndpointsReceived(evt)
	case EventLedgerResponse:
		o.onLedgerResponse(evt)
	}
}

func (o *Overlay) onPeerConnected(evt Event) {
	// Only track outbound connections in discovery — inbound endpoints
	// use ephemeral source ports that aren't connectable.
	if !evt.Inbound {
		o.discovery.MarkConnected(evt.Endpoint.String(), evt.PeerID)
	}
}

func (o *Overlay) onPeerHandshakeComplete(evt Event) {
	// Mark slot as active in discovery
}

func (o *Overlay) onPeerDisconnected(evt Event) {
	o.discovery.MarkDisconnected(evt.PeerID)
	o.relay.RemovePeer(evt.PeerID)
}

func (o *Overlay) onPeerFailed(evt Event) {
	if o.discovery.bootCache != nil {
		o.discovery.bootCache.MarkFailed(evt.Endpoint.String())
	}
}

func (o *Overlay) onMessageReceived(evt Event) {
	msgType := message.MessageType(evt.MessageType)

	// Handle PING at transport level — respond with PONG immediately
	if msgType == message.TypePing {
		o.handlePing(evt)
		return
	}

	// Handle TMSquelch at the transport level — update per-peer squelch
	// state and do not forward to external consumers. Mirrors rippled's
	// PeerImp::onMessage(TMSquelch).
	if msgType == message.TypeSquelch {
		o.handleSquelchMessage(evt)
		return
	}

	// Serve mtREPLAY_DELTA_REQ from the local ledger sync handler. Mirrors
	// rippled's PeerImp::onMessage(TMReplayDeltaRequest) which delegates to
	// LedgerReplayMsgHandler::processReplayDeltaRequest. The request is
	// addressed at us — responses (if any) are pushed back via the events
	// channel as EventLedgerResponse. We do not forward inbound requests to
	// external consumers; only the internal handler answers them.
	if msgType == message.TypeReplayDeltaReq {
		o.dispatchReplayDeltaRequest(evt)
		return
	}

	// Serve mtPROOF_PATH_REQ from the local ledger sync handler. Mirrors
	// rippled's PeerImp::onMessage(TMProofPathRequest) which delegates to
	// LedgerReplayMsgHandler::processProofPathRequest. The request is
	// addressed at us — responses are pushed back via the events channel
	// as EventLedgerResponse. We do not forward inbound requests to
	// external consumers; only the internal handler answers them.
	if msgType == message.TypeProofPathReq {
		o.dispatchProofPathRequest(evt)
		return
	}

	// mtREPLAY_DELTA_RESPONSE is NOT intercepted here. Like every other
	// peer-originated reply (mtLEDGER_DATA, mtTRANSACTION, mtVALIDATION),
	// it must reach the consensus router via the overlay's Messages()
	// channel. The router maintains the matching InboundReplayDelta state
	// and is the only place that can verify the response and adopt the
	// resulting ledger. Mirrors rippled's PeerImp dispatching the message
	// through the same path it dispatches all consensus traffic.

	slog.Debug("Message received", "t", "Overlay", "type", msgType.String(), "peer", evt.PeerID, "size", len(evt.Payload))

	// Forward to external consumers
	select {
	case o.messages <- &InboundMessage{
		PeerID:  evt.PeerID,
		Type:    evt.MessageType,
		Payload: evt.Payload,
	}:
	default:
		slog.Warn("Message dropped: channel full", "t", "Overlay", "type", msgType.String())
	}
}

// dispatchReplayDeltaRequest decodes an inbound mtREPLAY_DELTA_REQ frame and
// routes it to the local LedgerSyncHandler. Decode failures are logged and
// dropped silently — a malformed request from a peer should not crash the
// dispatch loop. The handler answers via the configured LedgerProvider, which
// is wired at startup by the consensus adaptor (see
// internal/consensus/adaptor.NewLedgerProvider) — that layer can import
// internal/ledger, which this package cannot.
func (o *Overlay) dispatchReplayDeltaRequest(evt Event) {
	decoded, err := message.Decode(message.TypeReplayDeltaReq, evt.Payload)
	if err != nil {
		slog.Debug("ReplayDeltaRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		return
	}
	req, ok := decoded.(*message.ReplayDeltaRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ReplayDeltaRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
	}
}

// dispatchProofPathRequest decodes an inbound mtPROOF_PATH_REQ frame and
// routes it to the local LedgerSyncHandler. Decode failures are logged
// and dropped silently — a malformed request from a peer should not
// crash the dispatch loop. The handler answers via the configured
// LedgerProvider, which is wired at startup by the consensus adaptor
// (see internal/consensus/adaptor.NewLedgerProvider) — that layer can
// import internal/ledger, which this package cannot.
func (o *Overlay) dispatchProofPathRequest(evt Event) {
	decoded, err := message.Decode(message.TypeProofPathReq, evt.Payload)
	if err != nil {
		slog.Debug("ProofPathRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		return
	}
	req, ok := decoded.(*message.ProofPathRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ProofPathRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
	}
}

// handleSquelchMessage processes an inbound TMSquelch from a peer and
// updates the per-peer validator squelch table.
func (o *Overlay) handleSquelchMessage(evt Event) {
	decoded, err := message.Decode(message.TypeSquelch, evt.Payload)
	if err != nil {
		slog.Debug("Squelch decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		return
	}
	sq, ok := decoded.(*message.Squelch)
	if !ok || len(sq.ValidatorPubKey) == 0 {
		return
	}

	o.peersMu.RLock()
	peer, exists := o.peers[evt.PeerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}

	if !sq.Squelch {
		peer.RemoveSquelch(sq.ValidatorPubKey)
		return
	}
	duration := time.Duration(sq.SquelchDuration) * time.Second
	if !peer.AddSquelch(sq.ValidatorPubKey, duration) {
		slog.Debug("Squelch ignored: invalid duration", "t", "Overlay", "peer", evt.PeerID, "duration", sq.SquelchDuration)
	}
}

func (o *Overlay) handlePing(evt Event) {
	decoded, err := message.Decode(message.TypePing, evt.Payload)
	if err != nil {
		return
	}
	ping, ok := decoded.(*message.Ping)
	if !ok {
		return
	}

	if ping.PType == message.PingTypePing {
		pong := &message.Ping{
			PType:    message.PingTypePong,
			Seq:      ping.Seq,
			PingTime: ping.PingTime,
		}
		encoded, err := message.Encode(pong)
		if err != nil {
			return
		}
		wireMsg, err := message.BuildWireMessage(message.TypePing, encoded)
		if err != nil {
			return
		}
		o.Send(evt.PeerID, wireMsg)
	}
}

func (o *Overlay) onEndpointsReceived(evt Event) {
	for _, ep := range evt.Endpoints {
		o.discovery.AddPeer(ep.String(), 1, evt.PeerID)
	}
}

// onLedgerResponse ships an already-wire-framed ledger-sync response
// (produced by LedgerSyncHandler.send*Response) to the requesting peer.
// The payload MUST be a full wire frame (6-byte header + protobuf body)
// — see sendReplayDeltaResponse for the contract. Shipping a bare
// protobuf here caused B to parse the first 6 body bytes as a garbage
// wire header and stall for the phantom payload, which was the
// post-handshake I/O regression fixed alongside this comment.
func (o *Overlay) onLedgerResponse(evt Event) {
	o.Send(evt.PeerID, evt.Payload)
}

// discoveryLoop periodically attempts to connect to new peers.
func (o *Overlay) discoveryLoop(ctx context.Context) error {
	// Immediate first attempt on startup
	o.autoconnect(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.autoconnect(ctx)
		}
	}
}

// autoconnect attempts to connect to peers if we need more.
func (o *Overlay) autoconnect(ctx context.Context) {
	if !o.discovery.NeedsMorePeers() {
		return
	}

	count := o.cfg.MaxOutbound - o.outboundCount()
	if count <= 0 {
		return
	}

	addrs := o.discovery.SelectPeersToConnect(count)
	slog.Info("Autoconnect", "t", "Overlay", "candidates", len(addrs), "needed", count)
	for _, addr := range addrs {
		select {
		case <-ctx.Done():
			return
		default:
			go func(a string) {
				if err := o.Connect(a); err != nil {
					slog.Info("Peer connection failed", "t", "Overlay", "addr", a, "err", err)
				} else {
					slog.Info("Peer connected", "t", "Overlay", "addr", a)
				}
			}(addr)
		}
	}
}

// maintenanceLoop performs periodic maintenance tasks.
func (o *Overlay) maintenanceLoop(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.performMaintenance()
		}
	}
}

func (o *Overlay) performMaintenance() {
	// Cleanup expired ledger requests
	o.ledgerSync.CleanupExpiredRequests()
}

// handleSquelch is called by the relay system when a peer should be squelched
// or unsquelched for a given validator. It constructs a TMSquelch message and
// delivers it to the specific peer (unicast — see rippled's
// OverlayImpl::squelch in src/xrpld/overlay/detail/OverlayImpl.cpp).
func (o *Overlay) handleSquelch(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()

	if !exists {
		return
	}

	msg := &message.Squelch{
		Squelch:         squelch,
		ValidatorPubKey: validator,
	}
	if squelch {
		// rippled stores the duration as seconds in TMSquelch.
		msg.SquelchDuration = uint32(duration / time.Second)
	}

	encoded, err := message.Encode(msg)
	if err != nil {
		slog.Warn("Squelch encode failed", "t", "Overlay", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeSquelch, encoded)
	if err != nil {
		slog.Warn("Squelch frame build failed", "t", "Overlay", "peer", peerID, "err", err)
		return
	}

	if err := peer.Send(frame); err != nil {
		slog.Info("Squelch send failed", "t", "Overlay", "peer", peerID, "err", err)
	}
}

// Connect initiates an outbound connection to the specified address.
func (o *Overlay) Connect(addr string) error {
	endpoint, err := ParseEndpoint(addr)
	if err != nil {
		return err
	}

	// Check if already connected
	if o.isConnectedTo(endpoint) {
		return ErrAlreadyConnected
	}

	// Check if we can make more outbound connections
	if o.outboundCount() >= o.cfg.MaxOutbound {
		return ErrMaxPeersReached
	}

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, false, o.identity, o.events)
	peer.handshakeCfg = HandshakeConfig{
		UserAgent:   o.cfg.UserAgent,
		NetworkID:   o.cfg.NetworkID,
		CrawlPublic: false,
	}

	o.events <- Event{
		Type:     EventPeerConnecting,
		PeerID:   peerID,
		Endpoint: endpoint,
		Inbound:  false,
	}

	ctx, cancel := context.WithTimeout(o.ctx, o.cfg.ConnectTimeout)
	defer cancel()

	cfg := PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
		TLSConfig: &tls.Config{
			Certificates:       []tls.Certificate{o.identity.TLSCertificate()},
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
			MaxVersion:         tls.VersionTLS12,
		},
	}

	if err := peer.Connect(ctx, cfg); err != nil {
		o.events <- Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  false,
			Error:    err,
		}
		return err
	}

	// Re-check after handshake: another goroutine may have connected
	// to the same host (inbound or outbound) while we were handshaking.
	if o.isConnectedTo(endpoint) {
		peer.Close()
		return ErrAlreadyConnected
	}

	o.addPeer(peer)

	// Run peer read/write loops
	go func() {
		err := peer.Run(o.ctx)
		if err != nil {
			slog.Info("Peer run ended", "t", "Overlay", "addr", addr, "err", err)
		}
		o.removePeer(peerID)
	}()

	return nil
}

// Broadcast sends a message to all connected peers.
//
// Use BroadcastFromValidator for messages originating from a specific
// validator (mtVALIDATION, mtPROPOSE_LEDGER) so the reduce-relay squelch
// filter is honored.
func (o *Overlay) Broadcast(msg []byte) error {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for _, peer := range o.peers {
		if peer.State() == PeerStateConnected {
			peer.Send(msg)
		}
	}
	return nil
}

// BroadcastFromValidator sends a validator-originated message (proposal or
// validation) to all connected peers, skipping peers that have squelched the
// originating validator. Mirrors rippled's per-peer squelch filter at
// PeerImp.cpp:240-256: the squelch is consulted before each outbound send,
// and expired squelches auto-clear via Peer.ExpireSquelch.
func (o *Overlay) BroadcastFromValidator(validator []byte, msg []byte) error {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for _, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if !peer.ExpireSquelch(validator) {
			continue
		}
		peer.Send(msg)
	}
	return nil
}

// Send sends a message to a specific peer.
func (o *Overlay) Send(peerID PeerID, msg []byte) error {
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()

	if !exists {
		return ErrPeerNotFound
	}

	return peer.Send(msg)
}

// Peers returns information about all connected peers.
func (o *Overlay) Peers() []PeerInfo {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	result := make([]PeerInfo, 0, len(o.peers))
	for _, peer := range o.peers {
		result = append(result, peer.Info())
	}
	return result
}

// PeerCount returns the number of connected peers.
func (o *Overlay) PeerCount() int {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()
	return len(o.peers)
}

// Messages returns a channel for receiving inbound messages.
func (o *Overlay) Messages() <-chan *InboundMessage {
	return o.messages
}

// Identity returns the node's identity.
func (o *Overlay) Identity() *Identity {
	return o.identity
}

// IssueSquelch hand-rolls a TMSquelch frame to the given peer, marking
// the given validator's messages as to-be-squelched (or cleared when
// squelch=false). This is the same path the reduce-relay system takes
// when it autonomously squelches a peer — mirroring rippled's
// OverlayImpl::squelch — but is exposed as a deliberate API so callers
// (including integration tests) can drive squelch state changes
// without having to reach a natural squelch threshold.
func (o *Overlay) IssueSquelch(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	o.handleSquelch(validator, peerID, squelch, duration)
}

// IsValidatorSquelchedOnPeer reports whether the local peer with the
// given PeerID currently has an active squelch for `validator`. It is
// the programmatic counterpart of peer.ExpireSquelch, which returns
// true when there is NO active squelch — this wrapper inverts so the
// name matches the usual intuition (true = this peer has been told to
// squelch this validator). Useful for end-to-end tests that verify
// TMSquelch was parsed and recorded by the receiver.
func (o *Overlay) IsValidatorSquelchedOnPeer(peerID PeerID, validator []byte) bool {
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()
	if !exists {
		return false
	}
	return !peer.ExpireSquelch(validator)
}

// addPeer adds a peer to the overlay.
func (o *Overlay) addPeer(peer *Peer) {
	o.peersMu.Lock()
	o.peers[peer.ID()] = peer
	o.peersMu.Unlock()

	o.events <- Event{
		Type:     EventPeerConnected,
		PeerID:   peer.ID(),
		Endpoint: peer.Endpoint(),
		Inbound:  peer.Inbound(),
	}
}

// removePeer removes a peer from the overlay.
func (o *Overlay) removePeer(peerID PeerID) {
	o.peersMu.Lock()
	peer, exists := o.peers[peerID]
	delete(o.peers, peerID)
	o.peersMu.Unlock()

	if exists {
		o.events <- Event{
			Type:     EventPeerDisconnected,
			PeerID:   peerID,
			Endpoint: peer.Endpoint(),
			Inbound:  peer.Inbound(),
		}
	}
}

// isConnectedTo checks if we're already connected to a host.
// Compares by resolved remote IP to handle DNS names vs raw IPs.
func (o *Overlay) isConnectedTo(endpoint Endpoint) bool {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for _, peer := range o.peers {
		if peer.RemoteIP() == endpoint.Host {
			return true
		}
		if peer.Endpoint().Host == endpoint.Host {
			return true
		}
	}
	return false
}

// canAcceptInbound checks if we can accept another inbound connection.
func (o *Overlay) canAcceptInbound() bool {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	count := 0
	for _, peer := range o.peers {
		if peer.Inbound() {
			count++
		}
	}
	return count < o.cfg.MaxInbound
}

// outboundCount returns the number of outbound connections.
func (o *Overlay) outboundCount() int {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	count := 0
	for _, peer := range o.peers {
		if !peer.Inbound() {
			count++
		}
	}
	return count
}
