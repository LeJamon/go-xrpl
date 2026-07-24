package peermanagement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// Connect initiates an outbound connection to the specified address. It
// must be called after Run has set the overlay context (the autoconnect
// loop, its only production caller, is itself launched by Run); a guard
// rejects an out-of-lifecycle call rather than nil-panic on o.ctx.
//
// The outbound-cap check below is best-effort: it is not atomic with
// addPeer, so concurrent external Connect calls can briefly exceed
// MaxOutbound. Autoconnect-originated dials stay bounded by outboundSem;
// this matters only for direct external callers driving many parallel
// Connects.
func (o *Overlay) Connect(addr string) error {
	done, ok := o.beginPeerStart()
	if !ok {
		return ErrConnectionClosed
	}
	defer done()
	return o.connectReserved(addr, nil)
}

func (o *Overlay) beginPeerStart() (func(), bool) {
	o.peerStartMu.Lock()
	defer o.peerStartMu.Unlock()
	if o.peerStartsClosed {
		return nil, false
	}
	o.peerStartWG.Add(1)
	return o.peerStartWG.Done, true
}

func (o *Overlay) connectReserved(addr string, bootstrapLease *bootstrapLease) error {
	endpoint, err := ParseEndpoint(addr)
	if err != nil {
		return err
	}

	o.lifecycleMu.Lock()
	baseCtx := o.ctx
	o.lifecycleMu.Unlock()
	if baseCtx == nil {
		return errors.New("overlay: Connect called before Run")
	}

	// Check if already connected
	if o.isConnectedTo(endpoint) {
		return ErrAlreadyConnected
	}

	// Check if we can make more outbound connections
	if !o.discovery.IsFixed(addr) && o.ordinaryOutboundCount() >= o.cfg.MaxOutbound {
		return ErrMaxPeersReached
	}

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, false, o.identity, o.events)
	peer.SetDroppedEventsCounter(&o.droppedEvents)
	peer.SetAcquisitionEvents(o.acquisitionEvents)
	peer.SetManifestMessages(o.manifestMessages)
	peer.SetManifestReadBudget(o.manifestReadBudget)
	peer.SetManifestSpoolDir(o.manifestSpoolDir)
	peer.handshakeCfg = o.handshakeConfigFor()
	peer.onRedirect = func(peerIPs []string) {
		o.ingestRedirectEndpoints(peerIPs, peerID)
	}

	ctx, cancel := context.WithTimeout(baseCtx, o.cfg.ConnectTimeout)
	defer cancel()

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}
	cfg := PeerConfig{
		PeerTLSConfig: &peertls.Config{
			CertPEM: certPEM,
			KeyPEM:  keyPEM,
		},
	}

	if err := peer.Connect(ctx, cfg); err != nil {
		o.dispatchLifecycle(Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  false,
			Error:    err,
		})
		return err
	}
	compression := peer.compressionNegotiated()
	o.discovery.markNegotiatedCompression(addr, compression)
	ledgerReplay := peer.Capabilities().HasFeature(FeatureLedgerReplay)
	slog.Info("Peer handshake complete", "t", "Overlay", "addr", addr,
		"protocol", peer.ProtocolVersion(), "compression", compression,
		"ledger_replay_local", o.cfg.EnableLedgerReplay,
		"ledger_replay_negotiated", ledgerReplay)
	if bootstrapLease != nil {
		peer.onBootstrapReady = func() { o.completePeerBootstrap(peerID) }
		peer.onBootstrapProgress = func(progress bootstrapFrameProgress) {
			observation := bootstrapLease.observeProgress(progress)
			if observation.sampled {
				o.discovery.observeBootstrapSource(addr, observation.projected)
			}
			if !observation.hedge {
				return
			}
			slog.Info("Bootstrap hedge enabled", "t", "Overlay", "addr", addr,
				"wire_size", progress.wireSize, "compressed", progress.compressed,
				"bytes_read", progress.bytesRead, "elapsed", progress.elapsed,
				"rate", frameReadRate(progress.bytesRead, progress.elapsed),
				"projected", observation.projected)
			o.wakeAutoconnect()
		}
	}

	// Re-check after handshake: another goroutine may have connected
	// to the resolved endpoint while we were handshaking.
	if o.isConnectedTo(postHandshakeEndpoint(peer, endpoint)) {
		peer.Close()
		return ErrAlreadyConnected
	}

	if err := o.addPeer(peer); err != nil {
		peer.Close()
		return err
	}
	if bootstrapLease != nil {
		o.trackPeerBootstrap(peerID, bootstrapLease)
	}

	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		err := peer.Run(baseCtx)
		if bootstrapLease != nil && baseCtx.Err() == nil {
			retry := recentConnectAttempt
			var frameErr *FrameReadError
			if errors.As(err, &frameErr) && frameErr.MessageType == TypeManifests && frameErr.BytesRead > 0 {
				retry = bootstrapPartialRetry
				slog.Info("Bootstrap source quarantined", "t", "Overlay", "addr", addr,
					"retry_after", retry, "wire_size", frameErr.WireSize,
					"compressed", frameErr.Compressed, "bytes_read", frameErr.BytesRead,
					"elapsed", frameErr.Elapsed, "rate", frameReadRate(frameErr.BytesRead, frameErr.Elapsed),
					"projected", projectedFrameDuration(frameErr.WireSize, frameErr.BytesRead, frameErr.Elapsed))
			}
			o.discovery.delayBootstrapRetry(addr, retry)
		}
		o.onBootstrapTransportEnd(peerID, bootstrapLease != nil && peer.bootstrapManifestPending.Load(), baseCtx.Err() != nil)
		if err != nil {
			slog.Info("Peer run ended", "t", "Overlay", "addr", addr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()

	return nil
}

func (o *Overlay) trackPeerBootstrap(peerID PeerID, lease *bootstrapLease) {
	if lease == nil {
		return
	}
	o.bootstrapLeaseMu.Lock()
	if o.bootstrapLeases == nil {
		o.bootstrapLeases = make(map[PeerID]*bootstrapLease)
	}
	o.bootstrapLeases[peerID] = lease
	o.bootstrapLeaseMu.Unlock()
}

func (o *Overlay) takePeerBootstrap(peerID PeerID) *bootstrapLease {
	o.bootstrapLeaseMu.Lock()
	lease := o.bootstrapLeases[peerID]
	delete(o.bootstrapLeases, peerID)
	o.bootstrapLeaseMu.Unlock()
	return lease
}

func (o *Overlay) completePeerBootstrap(peerID PeerID) bool {
	lease := o.takePeerBootstrap(peerID)
	if lease == nil {
		return false
	}
	lease.markReady()
	o.wakeAutoconnect()
	return true
}

func (o *Overlay) releasePeerBootstrap(peerID PeerID) bool {
	lease := o.takePeerBootstrap(peerID)
	if lease == nil {
		return false
	}
	lease.release()
	return true
}

func (o *Overlay) onBootstrapTransportEnd(peerID PeerID, manifestPending, stopping bool) {
	if manifestPending && !stopping {
		return
	}
	if o.releasePeerBootstrap(peerID) && !stopping {
		o.wakeAutoconnect()
	}
}

func postHandshakeEndpoint(peer *Peer, configured Endpoint) Endpoint {
	if remote, ok := peer.remoteEndpoint(); ok {
		return remote
	}
	return configured
}

// OnValidatorMessage is called by the consensus router on every inbound
// trusted proposal/validation so the reduce-relay state machine can
// select peers to squelch.
//
// Without this wiring the Relay.OnMessage loop never sees inbound
// activity and mtSQUELCH is never emitted — which was the pre-fix
// behavior the PR review caught.
func (o *Overlay) OnValidatorMessage(validatorKey []byte, peerID PeerID) {
	if o.relay == nil {
		return
	}
	o.relay.OnMessage(validatorKey, peerID)
}

// getPeer looks up a peer by ID under the peers read-lock.
func (o *Overlay) getPeer(peerID PeerID) (*Peer, bool) {
	o.peersMu.RLock()
	peer, ok := o.peers[peerID]
	o.peersMu.RUnlock()
	return peer, ok
}

// IsPeerConnected reports whether peerID still identifies a live session.
func (o *Overlay) IsPeerConnected(peerID PeerID) bool {
	peer, ok := o.getPeer(peerID)
	return ok && peer.State() == PeerStateConnected
}

// PeerLatency returns the overlay's smoothed round-trip estimate for peerID.
func (o *Overlay) PeerLatency(peerID PeerID) (time.Duration, bool) {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return 0, false
	}
	return peer.Latency()
}

// Send sends a message to a specific peer.
func (o *Overlay) Send(peerID PeerID, msg []byte) error {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return ErrPeerNotFound
	}
	return peer.Send(msg)
}

// SendManifestFrames schedules a complete, chunked manifest snapshot for a
// peer without consuming one ordinary send-queue slot per chunk.
func (o *Overlay) SendManifestFrames(peerID PeerID, frames [][]byte) error {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return ErrPeerNotFound
	}
	return peer.SendManifestFrames(frames)
}

// SendPriority admits acquisition and control traffic to the peer's independent
// priority queue.
func (o *Overlay) SendPriority(peerID PeerID, msg []byte) error {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return ErrPeerNotFound
	}
	return peer.SendPriority(msg)
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

// CheckTracking compares every connected peer's advertised ledger range to a
// quorum-validated sequence during initial synchronization.
func (o *Overlay) CheckTracking(validSeq uint32) {
	o.peersMu.RLock()
	peers := make([]*Peer, 0, len(o.peers))
	for _, peer := range o.peers {
		peers = append(peers, peer)
	}
	o.peersMu.RUnlock()

	for _, peer := range peers {
		peer.CheckTrackingAgainst(validSeq)
	}
}

// Cluster returns the registry of cluster-trusted node identities
// loaded from [cluster_nodes]. Always non-nil post-construction.
func (o *Overlay) Cluster() *cluster.Registry { return o.cluster }

// SetTxProvider installs the tx-blob lookup used by the tx-reduce-relay
// reply path (handleGetObjectsMessage, otTRANSACTIONS). The provider
// receives the requested 32-byte tx hash and returns (blob, true) when
// the tx is in the open-ledger view. Wiring is optional — when nil the
// reply path drops without charging, matching the pre-existing
// "feature gated off" behaviour.
func (o *Overlay) SetTxProvider(fn func(hash [32]byte) ([]byte, bool)) {
	o.providersMu.Lock()
	o.txProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) txProviderSnapshot() func(hash [32]byte) ([]byte, bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.txProvider
}

// SetNodeObjectProvider installs the node-store lookup used by the
// generic TMGetObjectByHash serve path (handleGetObjectsMessage →
// serveGetObjects). The provider receives a requested 32-byte content
// hash and returns (blob, true) when the object is present in the local
// node store. Wiring is optional — when nil the serve path drops
// without charging, matching an overlay deployed without a backing
// store.
func (o *Overlay) SetNodeObjectProvider(fn func(hash [32]byte) ([]byte, bool)) {
	o.providersMu.Lock()
	o.nodeObjectProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) nodeObjectProviderSnapshot() func(hash [32]byte) ([]byte, bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.nodeObjectProvider
}

// SetOpenLedgerHashesProvider installs the tx-hash snapshot reader
// used by the periodic TMHaveTransactions emission. The provider
// returns a (possibly empty) slice of 32-byte tx hashes currently in
// the open-ledger view. The emitter only fires when EnableTxReduceRelay
// is true AND this provider is wired; nil leaves the gossip dark.
func (o *Overlay) SetOpenLedgerHashesProvider(fn func() [][32]byte) {
	o.providersMu.Lock()
	o.openLedgerHashesProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) openLedgerHashesProviderSnapshot() func() [][32]byte {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.openLedgerHashesProvider
}

// SetClusterFeeSink installs the callback invoked from handleClusterMessage
// with the median cluster LoadFee whenever a TMCluster frame refreshes
// the registry. Wiring is optional — when nil the inbound handler
// skips the median computation. Guarded by providersMu like the other
// provider setters: the server wires this after Overlay.Run has already
// launched, so a TMCluster frame arriving during startup reads it
// concurrently on the event loop.
func (o *Overlay) SetClusterFeeSink(fn func(fee uint32)) {
	o.providersMu.Lock()
	o.clusterFeeSink = fn
	o.providersMu.Unlock()
}

func (o *Overlay) clusterFeeSinkSnapshot() func(fee uint32) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.clusterFeeSink
}

// SetLocalLoadFeeProvider installs the reader that supplies our own
// LoadFee for the outbound TMCluster gossip self-entry. nil-safe —
// sendClusterUpdate falls back to 0 when unwired. Guarded by providersMu:
// read concurrently by the maintenance loop's sendClusterUpdate while the
// server wires it after Run has launched.
func (o *Overlay) SetLocalLoadFeeProvider(fn func() uint32) {
	o.providersMu.Lock()
	o.localLoadFeeProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) localLoadFeeProviderSnapshot() func() uint32 {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.localLoadFeeProvider
}

// clusterFeeWindow is the freshness threshold for cluster-fee median
// inclusion — entries reporting older than this are dropped before the
// median is taken.
const clusterFeeWindow = 90 * time.Second

// PeerCount returns the number of connected peers.
func (o *Overlay) PeerCount() int {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()
	return len(o.peers)
}

// Messages returns the channel of inbound consensus- and
// ledger-acquisition-relevant frames (everything except transactions).
func (o *Overlay) Messages() <-chan *InboundMessage {
	return o.messages
}

// TxMessages returns the channel of inbound TMTransaction frames. It is
// drained separately from Messages so a transaction flood can't starve
// consensus/acquisition traffic (issue #1103).
func (o *Overlay) TxMessages() <-chan *InboundMessage {
	return o.txMessages
}

// ManifestMessages returns the lossless, bounded manifest-verification lane.
func (o *Overlay) ManifestMessages() <-chan *InboundMessage {
	return o.manifestMessages
}

// LedgerDataMessages returns the dedicated acquisition-reply lane
// (mtLEDGER_DATA and the replay-delta / proof-path responses). Its own lane
// keeps a reply this node requested from being shed behind a serve/propose
// flood on the shared Messages channel.
func (o *Overlay) LedgerDataMessages() <-chan *InboundMessage {
	return o.ledgerData
}

// Identity returns the node's identity.
func (o *Overlay) Identity() *Identity {
	return o.identity
}

// IssueSquelch hand-rolls a TMSquelch frame to the given peer, marking
// the given validator's messages as to-be-squelched (or cleared when
// squelch=false). This is the same path the reduce-relay system takes
// when it autonomously squelches a peer, exposed as a deliberate API so
// callers (including integration tests) can drive squelch state changes
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
	peer, exists := o.getPeer(peerID)
	if !exists {
		return false
	}
	return !peer.ExpireSquelch(validator)
}

// addPeer adds a peer to the overlay and binds a resource.Consumer to
// it. The Consumer's key (IP for inbound, host:port for outbound)
// persists in the manager after disconnect, so a misbehaving peer that
// reconnects from the same address inherits its prior balance — this
// is what enables charge-based blacklisting.
func (o *Overlay) addPeer(peer *Peer) error {
	key, hasKey := remotePeerKey(peer)
	endpoint := endpointKey(postHandshakeEndpoint(peer, peer.Endpoint()))
	o.peersMu.Lock()
	if _, exists := o.peers[peer.ID()]; exists {
		o.peersMu.Unlock()
		return ErrAlreadyConnected
	}
	if owner, exists := o.peerEndpoints[endpoint]; exists && owner != peer.ID() {
		o.peersMu.Unlock()
		return ErrAlreadyConnected
	}
	if hasKey {
		if owner, exists := o.peerKeys[key]; exists && owner != peer.ID() {
			o.peersMu.Unlock()
			return ErrAlreadyConnected
		}
		o.peerKeys[key] = peer.ID()
	}
	o.peerEndpoints[endpoint] = peer.ID()
	o.peers[peer.ID()] = peer
	delete(o.pendingInbound, peer.ID())
	o.peersMu.Unlock()

	if o.resourceManager != nil {
		addr := peer.Endpoint().String()
		var c *resource.Consumer
		if o.isClusterPeer(peer) {
			c = o.resourceManager.NewUnlimitedEndpoint(addr)
		} else if peer.Inbound() {
			c = o.resourceManager.NewInboundEndpoint(addr)
		} else {
			c = o.resourceManager.NewOutboundEndpoint(addr)
		}
		peer.attachUsage(c, o.bumpPeerDisconnectCharges)
	}
	if !peer.Inbound() {
		o.discovery.MarkConnected(peer.Endpoint().String(), peer.ID())
	}

	o.dispatchLifecycle(Event{
		Type:     EventPeerConnected,
		PeerID:   peer.ID(),
		Endpoint: peer.Endpoint(),
		Inbound:  peer.Inbound(),
	})
	return nil
}

// removePeer removes a peer from the overlay and releases its
// resource.Consumer back to the manager. The manager keeps the entry
// in its inactive list for SecondsUntilExpiration so a reconnect
// inherits the prior balance.
func (o *Overlay) removePeer(peerID PeerID) {
	o.peersMu.Lock()
	peer, exists := o.peers[peerID]
	delete(o.peers, peerID)
	for key, owner := range o.peerKeys {
		if owner == peerID {
			delete(o.peerKeys, key)
		}
	}
	for endpoint, owner := range o.peerEndpoints {
		if owner == peerID {
			delete(o.peerEndpoints, endpoint)
		}
	}
	o.peersMu.Unlock()

	if exists {
		if peer.inbound {
			o.releaseInboundIP(peer.Endpoint().Host)
		} else {
			o.discovery.MarkDisconnected(peerID)
		}
		peer.releaseUsage()
		o.dispatchLifecycle(Event{
			Type:     EventPeerDisconnected,
			PeerID:   peerID,
			Endpoint: peer.Endpoint(),
			Inbound:  peer.Inbound(),
		})
	}
}

func remotePeerKey(peer *Peer) (string, bool) {
	key := peer.RemotePublicKey()
	if key == nil {
		return "", false
	}
	return string(key.Bytes()), true
}

func (o *Overlay) reservePeerKey(peer *Peer, bypassInboundLimit bool) error {
	key, ok := remotePeerKey(peer)
	if !ok {
		return ErrInvalidPublicKey
	}
	o.peersMu.Lock()
	defer o.peersMu.Unlock()
	if owner, exists := o.peerKeys[key]; exists && owner != peer.ID() {
		return ErrAlreadyConnected
	}
	if peer.Inbound() && !bypassInboundLimit {
		active := 0
		for _, current := range o.peers {
			if current.inbound {
				active++
			}
		}
		if active+len(o.pendingInbound) >= o.cfg.MaxInbound {
			return ErrMaxPeersReached
		}
		o.pendingInbound[peer.ID()] = struct{}{}
	}
	o.peerKeys[key] = peer.ID()
	return nil
}

func (o *Overlay) releasePeerKey(peer *Peer) {
	key, ok := remotePeerKey(peer)
	if !ok {
		return
	}
	o.peersMu.Lock()
	if owner, exists := o.peerKeys[key]; exists && owner == peer.ID() {
		delete(o.peerKeys, key)
	}
	delete(o.pendingInbound, peer.ID())
	o.peersMu.Unlock()
}

func (o *Overlay) reserveInboundIP(host string) bool {
	if !isPublicPeerIP(host) {
		return true
	}
	o.peersMu.Lock()
	defer o.peersMu.Unlock()
	if o.inboundIPs[host] >= o.cfg.IPLimit {
		return false
	}
	o.inboundIPs[host]++
	return true
}

func (o *Overlay) releaseInboundIP(host string) {
	o.peersMu.Lock()
	o.releaseInboundIPLocked(host)
	o.peersMu.Unlock()
}

func (o *Overlay) releaseInboundIPLocked(host string) {
	if !isPublicPeerIP(host) {
		return
	}
	if o.inboundIPs[host] <= 1 {
		delete(o.inboundIPs, host)
		return
	}
	o.inboundIPs[host]--
}

var nonPublicPeerPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicPeerIP(host string) bool {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.WithZone("")
	addr = addr.Unmap()
	for _, prefix := range nonPublicPeerPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// bumpPeerDisconnectCharges is the callback Peer.Charge invokes when a
// resource.Consumer charge crosses the drop threshold.
func (o *Overlay) bumpPeerDisconnectCharges() {
	o.peerDisconnectsCharges.Add(1)
}

// ShouldShedLedgerRequest reports whether a ledger-BODY request from
// peerID should be dropped under load. Two gates:
//   - the peer's send queue is at/over the drop threshold (applies to
//     every peer, cluster included); or
//   - the local node is fee-loaded AND the peer is not a cluster member.
//
// loadedLocal is supplied by the caller (LoadFeeTrack.IsLoadedLocal())
// to keep the overlay free of a fee-track dependency. tx-set candidate
// (liTS_CANDIDATE) requests must never be passed here — consensus
// liveness depends on them never being shed.
func (o *Overlay) ShouldShedLedgerRequest(peerID PeerID, loadedLocal bool) bool {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return false
	}
	if peer.SendQueueLen() >= peerSendQueueDropThreshold {
		return true
	}
	return loadedLocal && !o.isClusterPeer(peer)
}

// isClusterPeer reports whether peer's node public key matches a
// cluster registry entry. Cluster members are bound to an unlimited
// Consumer so charges are no-ops.
func (o *Overlay) isClusterPeer(peer *Peer) bool {
	if o.cluster == nil {
		return false
	}
	pk := peer.RemotePublicKey()
	if pk == nil {
		return false
	}
	_, ok := o.cluster.Member(pk.Bytes())
	return ok
}

func (o *Overlay) isConnectedTo(endpoint Endpoint) bool {
	o.peersMu.RLock()
	peers := make([]*Peer, 0, len(o.peers))
	for _, peer := range o.peers {
		peers = append(peers, peer)
	}
	o.peersMu.RUnlock()

	target := endpointKey(endpoint)
	for _, peer := range peers {
		if remote, ok := peer.remoteEndpoint(); ok && endpointKey(remote) == target {
			return true
		}
		if endpointKey(peer.Endpoint()) == target {
			return true
		}
	}
	return false
}

func endpointKey(endpoint Endpoint) string {
	return (Endpoint{Host: connectAttemptHost(endpoint.String()), Port: endpoint.Port}).String()
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

// hasInboundSlot reports whether the just-handshaked inbound peer may be
// admitted: either a normal slot is free, or the peer is a cluster member or
// has an operator reservation and is therefore admitted beyond the inbound
// cap.
func (o *Overlay) hasInboundSlot(peer *Peer) bool {
	if o.canAcceptInbound() {
		return true
	}
	return o.isClusterPeer(peer) || o.isReservedPeer(peer)
}

func (o *Overlay) ordinaryOutboundCount() int {
	o.peersMu.RLock()
	peers := make([]*Peer, 0, len(o.peers))
	for _, peer := range o.peers {
		if !peer.Inbound() {
			peers = append(peers, peer)
		}
	}
	o.peersMu.RUnlock()

	count := 0
	for _, peer := range peers {
		if !o.discovery.IsFixed(peer.Endpoint().String()) {
			count++
		}
	}
	return count
}

// reconcileDiscoveryConnected pushes the live peer address+host set
// into Discovery so its `Connected` flags reflect the actual TCP
// state. Called from autoconnect before SelectPeersToConnect so any
// peer whose connection ended without a corresponding MarkDisconnected
// gets re-considered, AND any peer we already have inbound from is
// recognized as covered (so we don't re-dial it and trigger the
// post-handshake isConnectedTo rejection in Connect / accept).
//
// goxrpl splits the overlay (Overlay.peers) and the connect scheduler
// (Discovery.peers) across an event bus, so the two sets can drift;
// this reconcile bridges them once per autoconnect tick.
//
// Two pieces of state are reconciled:
//  1. exactAddrs: full "host:port" strings of OUTBOUND peers. These
//     were originally tracked by MarkConnected.
//  2. hosts: the unique HOST set across all current peers (inbound
//     AND outbound). Used so a fixed-peer entry like
//     "goxrpl-0:51235" gets flagged as covered when there's an
//     inbound peer whose RemoteIP matches goxrpl-0, even though the
//     inbound's ephemeral source port doesn't match :51235.
//
// Without (2), goxrpl-1 (with an inbound from goxrpl-0) would
// repeatedly outbound-dial goxrpl-0:51235 and have every attempt
// post-handshake-rejected by goxrpl-0's isConnectedTo (it already
// has the inbound bidirectionally bookkept). Empirically the cause
// of the iter25 stall on goxrpl-1.
func (o *Overlay) reconcileDiscoveryConnected() {
	o.peersMu.RLock()
	exactAddrs := make(map[string]struct{}, len(o.peers))
	hosts := make(map[string]struct{}, len(o.peers))
	for _, peer := range o.peers {
		if !peer.Inbound() {
			exactAddrs[peer.Endpoint().String()] = struct{}{}
		}
		if h := peer.RemoteIP(); h != "" {
			hosts[h] = struct{}{}
		}
		if h := peer.Endpoint().Host; h != "" {
			hosts[h] = struct{}{}
		}
	}
	o.peersMu.RUnlock()
	o.discovery.SyncConnectedState(exactAddrs)
	o.discovery.SyncConnectedHosts(hosts)
}
