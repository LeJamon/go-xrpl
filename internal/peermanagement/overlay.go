package peermanagement

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

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

	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	o.addPeer(peer)

	// Run peer read/write loops
	go func() {
		peer.Run(ctx)
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

	// Read HTTP upgrade request
	buf := make([]byte, 4096)
	deadline := time.Now().Add(o.cfg.HandshakeTimeout)
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	n, err := tlsConn.Read(buf)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}

	// Verify the request
	if n < 12 {
		return NewHandshakeError(peer.Endpoint(), "verify", ErrInvalidHandshake)
	}

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
	o.discovery.MarkConnected(evt.Endpoint.String(), evt.PeerID)
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
	// Forward to external consumers
	select {
	case o.messages <- &InboundMessage{
		PeerID:  evt.PeerID,
		Type:    evt.MessageType,
		Payload: evt.Payload,
	}:
	default:
		// Drop if channel full
	}
}

func (o *Overlay) onEndpointsReceived(evt Event) {
	for _, ep := range evt.Endpoints {
		o.discovery.AddPeer(ep.String(), 1, evt.PeerID)
	}
}

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

// handleSquelch is called by the relay system when a peer should be squelched.
func (o *Overlay) handleSquelch(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	// Send squelch message to peer
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()

	if !exists {
		return
	}

	// TODO: Build and send squelch message
	_ = peer
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

	o.addPeer(peer)

	// Run peer read/write loops
	go func() {
		peer.Run(o.ctx)
		o.removePeer(peerID)
	}()

	return nil
}

// Broadcast sends a message to all connected peers.
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

// isConnectedTo checks if we're already connected to an endpoint.
func (o *Overlay) isConnectedTo(endpoint Endpoint) bool {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for _, peer := range o.peers {
		if peer.Endpoint().String() == endpoint.String() {
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
