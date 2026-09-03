package peermanagement

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"golang.org/x/sync/errgroup"
)

// serveWorkerCount bounds concurrent heavy serve operations (fetch-pack /
// get-objects / tx back-fill) handled off the event loop. The scheduler also
// applies a per-peer active and queued limit so one peer cannot monopolize
// workers or backlog.
const (
	serveWorkerCount        = 4
	serveQueueDepth         = 64
	servePerPeerQueue       = 8
	servePerPeerConcurrency = 2
)

// Overlay is the central orchestrator for XRPL peer-to-peer networking.
// It manages peer connections, discovery, message routing, and the reduce-relay system.
type Overlay struct {
	cfg      Config
	identity *Identity

	// cluster is the registry of nodes loaded from [cluster_nodes].
	// Always non-nil post-construction (an empty registry stands in
	// when no entries are configured) so call sites can dereference
	// without nil checks.
	cluster *cluster.Registry

	// instanceCookie: immutable post-New, lock-free.
	instanceCookie uint64

	// Higher-layer callbacks (avoid importing internal/ledger here).
	providersMu         sync.RWMutex
	ledgerHintProvider  func() (LedgerHints, bool)
	validLedgerProvider func() (seq uint32, age time.Duration, ok bool)
	// peerStatusPublisher: optional sink for pubPeerStatus updates.
	// Wired by the RPC layer to broadcast over the peer_status
	// WebSocket subscription. nil-safe — no-op when unset (tests,
	// embedded usage, or RPC disabled).
	peerStatusPublisher func(PeerStatusUpdate)

	// Components
	discovery  *Discovery
	relay      *Relay
	ledgerSync *LedgerSyncHandler

	// Peer management
	peers                          map[PeerID]*Peer
	peerKeys                       map[string]PeerID
	peerEndpoints                  map[string]PeerID
	inboundIPs                     map[string]int
	pendingInbound                 map[PeerID]struct{}
	peersMu                        sync.RWMutex
	nextID                         atomic.Uint64
	outboundBudget                 *outboundBudget
	outboundCriticalLocalFailures  atomic.Uint64
	outboundCriticalSharedFailures atomic.Uint64

	// peerWG joins every peer.Run goroutine launched by handleInbound
	// or Connect. Stop blocks on it for deterministic shutdown.
	peerWG sync.WaitGroup

	// inboundSem caps concurrent handleInbound goroutines.
	// Length = MaxInbound + inboundBacklogSlack.
	inboundSem chan struct{}

	// outboundSem caps concurrent autoconnect Connect goroutines so
	// a slow discovery tick cannot stack one goroutine per candidate.
	outboundSem      chan struct{}
	autoconnectWake  chan struct{}
	peerStartMu      sync.Mutex
	peerStartWG      sync.WaitGroup
	peerStartsClosed bool
	bootstrap        bootstrapGovernor
	bootstrapLeaseMu sync.Mutex
	bootstrapLeases  map[PeerID]*bootstrapLease
	bootstrapDiag    atomic.Uint64

	// relayedIndex maps a suppression hash to peers that delivered the
	// corresponding message to us. Outbound recipients are never recorded.
	relayedIndex   map[[32]byte]*relayedEntry
	relayedIndexMu sync.Mutex
	// clock is the single time source for Overlay state that needs
	// deterministic expiry or suppression windows.
	clock func() time.Time

	fanoutLogMu         sync.Mutex
	fanoutLogLast       time.Time
	fanoutLogSuppressed int

	// Coordination channels
	//
	// events is the best-effort hot path for ordinary EventMessageReceived
	// traffic and EventLedgerResponse. Consensus and acquisition traffic use
	// separate bounded paths so they apply transport backpressure instead of
	// being discarded.
	//
	// lifecycle is the dedicated NON-LOSSY path for peer lifecycle events
	// (Connecting/Connected/Disconnected/Failed). Sends BLOCK until the
	// event loop accepts them (see dispatchLifecycle): lifecycle volume is
	// tiny and bounded by peer count, and a dropped EventPeerDisconnected
	// would leak router/relay per-peer state until the idle sweep. Keeping
	// it off the message channel means a message burst can no longer crowd
	// out a disconnect. Both are drained by the single eventLoop goroutine.
	events                 chan Event
	consensusEvents        chan Event
	consensusControlEvents chan Event
	acquisitionEvents      chan Event
	lifecycle              chan Event

	// consensusMessages preserves ordering between trust updates and the
	// proposals and validations that depend on them. consensusControlMessages
	// isolates advisory status and transaction-set availability frames.
	// Both lanes use bounded backpressure. messages remains best-effort for
	// recoverable service traffic.
	consensusMessages        chan *InboundMessage
	consensusControlMessages chan *InboundMessage
	messages                 chan *InboundMessage
	txMessages               chan *InboundMessage
	// manifestMessages is fed directly by peer read loops with bounded
	// backpressure. Signature verification is intentionally isolated from both
	// the overlay event loop and the consensus router.
	manifestMessages  chan *InboundMessage
	inboundReadBudget *readBudget
	manifestSpoolDir  string

	// ledgerData carries acquisition replies (mtLEDGER_DATA, by-hash objects,
	// and replay/proof-path responses) on a bounded backpressure path. This
	// prevents unrelated traffic from discarding replies required for catch-up.
	ledgerData chan *InboundMessage

	// serveScheduler carries heavy inbound serve work off the event-loop
	// goroutine. It is created in Run; nil when the overlay was built without
	// Run (submitServeForPeerOwned then runs the job inline, preserving
	// synchronous unit test behaviour).
	serveScheduler *serveScheduler

	// Peer lifecycle callbacks wired by higher layers (e.g., consensus
	// router) that need to clean up per-peer state on disconnect. Fired
	// from the event-loop goroutine AFTER the peer has been removed from
	// the map, so callees can assume the peer is already gone. nil when
	// no subscriber is registered. Guarded by providersMu.
	onPeerDisconnect func(PeerID)

	// onPeerConnect fires once a peer has finished its handshake and
	// been added to the overlay's peer map. Higher layers use this to
	// trigger post-connect emissions like the local manifest broadcast
	// in the post-handshake window (#372). Same blocking
	// contract as onPeerDisconnect: runs on the event loop, must not
	// block. Guarded by providersMu.
	onPeerConnect func(PeerID)

	// txProvider is the legacy open-ledger-only lookup retained for embedded
	// callers. Production wiring uses txRecordProvider so queued transactions
	// are included in reduce-relay replies. Guarded by providersMu.
	txProvider func(hash [32]byte) ([]byte, bool)

	// txRecordProvider is the authoritative transaction-cache lookup used by
	// reduce-relay replies. It includes queued transactions, which are not part
	// of the open-ledger view exposed by txProvider.
	txRecordProvider func(hash [32]byte) (TxRecord, bool)

	// nodeObjectProvider returns the raw node-store blob for a content
	// hash. Wired by the server at startup so the generic TMGetObjectByHash serve
	// path (handleGetObjectsMessage → serveGetObjects) can answer a
	// peer's by-hash query without importing storage/nodestore lifecycle
	// into this package. nil-safe — the serve path drops without
	// charging when unwired (an overlay deployed without a backing
	// store, or tests). Guarded by providersMu.
	nodeObjectProvider func(hash [32]byte) ([]byte, bool)

	// openLedgerHashesProvider is retained for compatibility with older
	// embedded callers; per-peer deferred queues now drive
	// TMHaveTransactions emission. Guarded by providersMu.
	openLedgerHashesProvider func() [][32]byte

	// clusterFeeSink is invoked by handleClusterMessage after the
	// registry-update loop with the median LoadFee across members
	// reported within the last cluster-fee window. nil-safe — the
	// inbound handler skips the median computation when unwired.
	// Guarded by providersMu.
	clusterFeeSink func(fee uint32)

	// localLoadFeeProvider returns the local node's current load fee and
	// validated-ledger age. Wired into sendClusterUpdate so stale nodes
	// advertise zero load. nil-safe — sendClusterUpdate falls back to 0
	// when unwired. Guarded by providersMu.
	localLoadFeeProvider func() (uint32, time.Duration)

	// localNodeIdentity is the raw 33-byte compressed NodePublic of
	// THIS node. Used by the cluster timer to insert ourselves into
	// the gossip frame so peers can correlate validator load. Set in New
	// from o.identity; nil only when no identity could be loaded, in which
	// case the cluster timer leaves the self-entry out.
	localNodeIdentity []byte

	droppedMessages atomic.Uint64

	// droppedTransactions counts inbound TMTransaction frames shed
	// because the txMessages lane was at its MaxTransactions ceiling,
	// covering both wire frames and inner frames fanned out from a
	// TMTransactions batch. Surfaced via server_info as jq_trans_overflow.
	droppedTransactions atomic.Uint64

	// Transaction reduce-relay rolling-average metrics surfaced by the
	// tx_reduce_relay RPC. Inbound tx-relay-related messages are
	// counted by type at the ingress chokepoint (onMessageReceived),
	// gated on the negotiated feature.
	txm txMetrics

	// droppedServeJobs counts heavy serve jobs refused because the worker
	// pool queue was saturated. The requesting peer's query then goes
	// unanswered and it retries elsewhere — load-shedding that mirrors
	// rippled's jtPACK / send-queue busy guards.
	droppedServeJobs atomic.Uint64

	// droppedEvents counts non-blocking sends to the lossy events channel
	// (EventMessageReceived hot path) that fell through. Surfaces
	// back-pressure so a stalled handler shows up as a counter rather than
	// a deadlock against peer-side goroutines that contend for peersMu.
	// Lifecycle events use the separate blocking `lifecycle` channel and
	// are never dropped here.
	droppedEvents atomic.Uint64

	// pingTimeoutDisconnects counts peers torn down because the oldest
	// in-flight ping aged past pingTimeout. Distinct from
	// peerDisconnectsCharges (below) which only counts Resource-Manager
	// drops.
	pingTimeoutDisconnects atomic.Uint64

	// peerDisconnectsCharges counts peers torn down because a
	// resource.Consumer charge exceeded the drop threshold. Surfaced
	// via server_info.peer_disconnects_resources. Bumped from
	// Peer.Charge via the onDropDisconnect callback set in attachUsage.
	peerDisconnectsCharges atomic.Uint64

	// resourceManager owns the per-endpoint Consumer table. Lifetime
	// matches the overlay: Started at Run, Stopped at shutdown.
	resourceManager *resource.Manager

	// peerDisconnects counts every peer torn down for any reason.
	// Surfaced via server_info as peer_disconnects.
	peerDisconnects atomic.Uint64

	// Network
	// listenerMu guards listener publication. Run publishes only after the
	// complete TCP/TLS listener has been prepared; reads under RLock are used
	// by ListenAddr and Stop, while acceptLoop starts after publication.
	listenerMu sync.RWMutex
	listener   net.Listener
	listenFunc func(context.Context, string, string) (net.Listener, error)

	// listenerReady is closed once Run has finished its listener-bind
	// phase — after startListener publishes o.listener, or immediately
	// when no listener is configured.
	listenerReady     chan struct{}
	listenerReadyOnce sync.Once

	// Lifecycle
	// lifecycleMu guards the one-shot state transition, context ownership,
	// and shutdown channels. An Overlay admits exactly one Run lifetime.
	lifecycleMu    sync.Mutex
	lifecycleState overlayLifecycleState
	ctx            context.Context
	cancel         context.CancelFunc
	shutdownOnce   sync.Once
	shutdownDone   chan struct{}
	stopRequested  bool

	// stopCh is closed by Stop to release any lifecycle send blocked on an
	// event loop that has already exited during shutdown.
	stopCh      chan struct{}
	runDone     <-chan struct{}
	runComplete <-chan struct{}
}

func (o *Overlay) ResourceManager() *resource.Manager {
	if o == nil {
		return nil
	}
	return o.resourceManager
}

type overlayLifecycleState uint8

const (
	overlayLifecycleNew overlayLifecycleState = iota
	overlayLifecycleRunning
	overlayLifecycleStopping
	overlayLifecycleStopped
)

// TxRecord is the transaction-cache state needed to build a rippled-compatible
// TMTransactions reply. Status is normally TxStatusCurrent for an open-ledger
// transaction and TxStatusNew for a queued transaction; Deferred identifies a
// transaction held by the transaction queue.
type TxRecord struct {
	RawTransaction []byte
	Status         message.TransactionStatus
	Deferred       bool
}

// LedgerSync returns the overlay's ledger-sync handler so callers in a
// higher layer (e.g., consensus startup) can wire a LedgerProvider that
// imports internal/ledger packages — which this layer cannot.
func (o *Overlay) LedgerSync() *LedgerSyncHandler { return o.ledgerSync }

// PeersWithClosedLedger returns peers whose last-known Closed-Ledger
// hash equals target. The hash is seeded from the handshake hint and
// refreshed by inbound mtSTATUS_CHANGE messages. This is a primitive
// for callers that want a coarse "who advertised this LCL" filter; it
// is NOT full catchup peer selection, which would consult per-peer
// complete-ledger ranges — state go-xrpl does not yet track per peer.
func (o *Overlay) PeersWithClosedLedger(target [32]byte) []PeerID {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	var matches []PeerID
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		closed, ok := peer.ClosedLedger()
		if ok && closed == target {
			matches = append(matches, id)
		}
	}
	return matches
}

// PeerWithLedger picks a connected peer that can serve ledger (target,
// seq), excluding the peer with id exclude, and returns (id, true).
// Returns (0, false) when no peer qualifies. Mirrors rippled's
// getPeerWithLedger / PeerImp::hasLedger: a peer qualifies when seq falls
// within its advertised [first,last] range while it tracks the network
// (converged), OR it advertised target among its recent ledgers (the
// closed/previous hashes it announced via status changes). seq == 0
// disables the range arm; a zero target disables the hash arm.
//
// Among qualifiers the winner is chosen by peerRelayScore (rippled
// PeerImp::getScore): a latency-weighted random draw that spreads relay
// load instead of always hitting the same peer.
func (o *Overlay) PeerWithLedger(target [32]byte, seq uint32, exclude PeerID) (PeerID, bool) {
	return o.bestPeer(exclude, func(p *Peer) bool {
		return p.HasLedger(target, seq)
	})
}

// SelectLedgerPeers returns up to max connected peers for an inbound-ledger
// peer set. Peers known to have the ledger receive a scoring bonus, but peers
// without an advertisement remain eligible.
func (o *Overlay) SelectLedgerPeers(target [32]byte, seq uint32, excluded []PeerID, max int) []PeerID {
	if max <= 0 {
		return nil
	}
	skip := make(map[PeerID]struct{}, len(excluded))
	for _, id := range excluded {
		skip[id] = struct{}{}
	}

	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	type scoredPeer struct {
		id    PeerID
		score int
	}
	var cands []scoredPeer
	for id, peer := range o.peers {
		if _, ok := skip[id]; ok || peer.State() != PeerStateConnected {
			continue
		}
		cands = append(cands, scoredPeer{
			id:    id,
			score: peerSetScore(peer, peer.HasLedger(target, seq)),
		})
	}
	if len(cands) == 0 {
		return nil
	}
	slices.SortFunc(cands, func(a, b scoredPeer) int { return b.score - a.score })
	if len(cands) > max {
		cands = cands[:max]
	}
	out := make([]PeerID, len(cands))
	for i := range cands {
		out[i] = cands[i].id
	}
	return out
}

// PeerWithTxSet picks a connected peer that advertised tx-set root target
// (via mtHAVE_TRANSACTION_SET{tsHAVE}), excluding the peer with id
// exclude. Mirrors rippled's getPeerWithTree / PeerImp::hasTxSet. See
// PeerWithLedger for the selection-policy note.
func (o *Overlay) PeerWithTxSet(target [32]byte, exclude PeerID) (PeerID, bool) {
	return o.bestPeer(exclude, func(p *Peer) bool {
		return p.HasTxSet(target)
	})
}

// bestPeer returns the highest-scoring connected peer (other than exclude)
// for which want reports true, scoring each qualifier with peerRelayScore.
func (o *Overlay) bestPeer(exclude PeerID, want func(*Peer) bool) (PeerID, bool) {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	var best PeerID
	var bestScore int
	found := false
	for id, peer := range o.peers {
		if id == exclude || peer.State() != PeerStateConnected {
			continue
		}
		if !want(peer) {
			continue
		}
		score := peerRelayScore(peer)
		if !found || score > bestScore {
			best = id
			bestScore = score
			found = true
		}
	}
	return best, found
}

// peerRelayScore mirrors rippled PeerImp::getScore(true): a random tie-break
// plus a have-item bonus minus a latency penalty, picking a responsive peer
// while spreading relay load. bestPeer only scores peers that already hold
// the data, so the have-item term is constant across candidates and does not
// affect the argmax — it is kept for a faithful port of the constants.
func peerRelayScore(p *Peer) int {
	return peerSetScore(p, true)
}

func peerSetScore(p *Peer, haveItem bool) int {
	const (
		spRandomMax = 9999
		spHaveItem  = 10000
		spLatency   = 30
		spNoLatency = 8000
	)
	score := mrand.IntN(spRandomMax + 1) //nolint:gosec // G404: non-security peer timing/selection jitter
	if haveItem {
		score += spHaveItem
	}
	if lat, ok := p.Latency(); ok {
		score -= int(lat.Milliseconds()) * spLatency
	} else {
		score -= spNoLatency
	}
	return score
}

// NotePeerHasTxSet records that the peer with id peerID advertised tx-set
// root hash. No-op when the peer is unknown. Fed by the consensus router
// on inbound mtHAVE_TRANSACTION_SET{tsHAVE}.
func (o *Overlay) NotePeerHasTxSet(peerID PeerID, hash [32]byte) bool {
	if peer, ok := o.getPeer(peerID); ok {
		return peer.AddTxSet(hash)
	}
	return false
}

// SetLedgerHintProvider wires the hint source; nil suppresses headers.
// generateInstanceCookie draws a cookie uniform in [1, MaxUint64];
// only 0 is rejected.
func generateInstanceCookie() (uint64, error) {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if v := binary.BigEndian.Uint64(b[:]); v != 0 {
			return v, nil
		}
	}
}

// localValidatorPubKey returns the compressed secp256k1 public key of
// the local validator, or nil when this node is not acting as a
// validator. Used as a cheap passthrough by handleSquelchMessage so
// the self-target filter doesn't need to reach into cfg directly.
// Kept unexported — higher layers plumb the pubkey in via
// WithLocalValidatorPubKey at overlay construction.
func (o *Overlay) localValidatorPubKey() []byte {
	return o.cfg.LocalValidatorPubKey
}

// IncPeerBadData records an invalid-data event attributed to the peer
// with the given PeerID. Returns the new cumulative count, or 0 when
// the peer is unknown (gracefully no-ops). Exposed so higher layers
// that can't import *Peer directly — e.g., the consensus router, which
// only sees PeerID via InboundMessage — can still charge a peer for
// malformed/invalid payloads. `reason` is a short stable label for
// diagnostic logging; it's forwarded to Peer.IncBadData.
//
// Use this as the single surface for higher-layer charge-backs: the
// peermanagement package already increments inline for events it
// detects itself (e.g., AddSquelch) so callers outside this package
// only need to cover the cases they detect themselves.
func (o *Overlay) IncPeerBadData(peerID PeerID, reason string) uint32 {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return 0
	}
	return peer.IncBadData(reason)
}

func (o *Overlay) selectMessageCharge(evt *Event, fee resource.Charge, chargeContext string) {
	if evt != nil && evt.selectCharge(fee, chargeContext) {
		return
	}
	if evt == nil {
		return
	}
	if peer, ok := o.getPeer(evt.PeerID); ok {
		peer.Charge(fee, chargeContext)
	}
}

func (o *Overlay) selectMessageChargeReason(evt *Event, reason string) {
	o.selectMessageCharge(evt, chargeForReason(reason), reason)
}

// peerNegotiatedLedgerReplay reports whether the peer identified by
// peerID advertised the ledger-replay feature during handshake. Used
// to gate serving mtREPLAY_DELTA_REQ and mtPROOF_PATH_REQ: these
// frames from a peer that didn't negotiate the feature indicate a
// protocol violation.
func (o *Overlay) peerNegotiatedLedgerReplay(peerID PeerID) bool {
	return o.PeerSupports(peerID, FeatureLedgerReplay)
}

// PeerSupports reports whether the peer identified by peerID has
// advertised support for the given protocol feature via its handshake
// headers. Returns false when the peer is unknown, the handshake has
// not completed, or the feature was not negotiated. Used by higher
// layers (e.g., consensus catchup) to avoid issuing feature-gated
// requests to peers that would silently drop them.
func (o *Overlay) PeerSupports(peerID PeerID, f Feature) bool {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return false
	}
	caps := peer.Capabilities()
	if caps == nil {
		return false
	}
	return caps.HasFeature(f)
}

// PeerClosedLedger returns the closed-ledger hash advertised during the
// handshake or in the peer's latest status message.
func (o *Overlay) PeerClosedLedger(peerID PeerID) ([32]byte, bool) {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return [32]byte{}, false
	}
	return peer.ClosedLedger()
}

// AcknowledgePeerBootstrap releases cold-start admission after a peer's
// startup traffic has been parsed and accepted by its protocol handler.
func (o *Overlay) AcknowledgePeerBootstrap(peerID PeerID) {
	o.completePeerBootstrap(peerID)
}

// RejectPeerBootstrap closes a cold-start peer whose startup traffic could
// not be processed, allowing the automatic dialer to retry another endpoint.
func (o *Overlay) RejectPeerBootstrap(peerID PeerID) {
	released := o.releasePeerBootstrap(peerID)
	peer, ok := o.getPeer(peerID)
	if released && ok && peer.onBootstrapReady != nil {
		peer.Close()
	}
	if released {
		o.wakeAutoconnect()
	}
}

func (o *Overlay) acknowledgePeerBootstrapPing(peerID PeerID) {
	peer, ok := o.getPeer(peerID)
	if ok && !peer.bootstrapManifest.Load() {
		peer.acknowledgeBootstrap()
	}
}

// PeerRemoteAddr returns the peer's remote endpoint as "host:port", or
// "" if the peer is unknown. Used to populate the `uri` field on
// per-publisher state for peer-sourced lists.
func (o *Overlay) PeerRemoteAddr(peerID PeerID) string {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return ""
	}
	return peer.Endpoint().String()
}

// PeerProtocolAtLeast reports whether the peer's negotiated
// peer-protocol version is at least the given (major, minor). Used to
// gate version-implicit features such as ValidatorList2Propagation.
//
// Returns false when the peer is unknown or has not completed the
// handshake.
func (o *Overlay) PeerProtocolAtLeast(peerID PeerID, major, minor uint16) bool {
	peer, ok := o.getPeer(peerID)
	if !ok {
		return false
	}
	got := peer.ProtocolVersion()
	if got == "" {
		return false
	}
	pvs := parseProtocolVersions(got)
	if len(pvs) == 0 {
		return false
	}
	want := protocolVersion{major: major, minor: minor}
	for _, v := range pvs {
		if !v.less(want) {
			return true
		}
	}
	return false
}

// ListenAddr returns the resolved address the overlay is accepting
// connections on, or the empty string if no listener is bound. Useful
// when the overlay was configured with port 0 (ephemeral) and the
// caller needs the actual port to drive a peer connection — e.g.,
// integration tests that wire two overlays together on localhost.
func (o *Overlay) ListenAddr() string {
	o.listenerMu.RLock()
	l := o.listener
	o.listenerMu.RUnlock()
	if l == nil {
		return ""
	}
	return l.Addr().String()
}

// ListenerReady returns a channel closed once Run has finished binding
// the listener (or determined none is configured), after which
// ListenAddr reports the resolved ephemeral port.
func (o *Overlay) ListenerReady() <-chan struct{} {
	return o.listenerReady
}

// signalListenerReady closes listenerReady, guarding overlays built
// directly (outside New) whose channel is nil.
func (o *Overlay) signalListenerReady() {
	if o.listenerReady == nil {
		return
	}
	o.listenerReadyOnce.Do(func() { close(o.listenerReady) })
}

// messageBufferSize returns the inbound-message channel capacity,
// falling back to DefaultMessageBufferSize when the configured value
// is non-positive. A non-positive size would create an unbuffered
// channel, turning the non-blocking send in handlePeerMessage into a
// drop-every-message path under any load.
func messageBufferSize(configured int) int {
	if configured <= 0 {
		return DefaultMessageBufferSize
	}
	return configured
}

// txLaneBufferSize returns the inbound-transaction channel capacity. It
// equals the MaxTransactions ceiling so a full lane is exactly that
// ceiling and frames past it are shed, falling back to
// DefaultMaxTransactions when the configured value is non-positive.
// Keeping transactions on their own lane stops a tx flood from
// crowding consensus/acquisition frames out of the messages channel.
func txLaneBufferSize(configured int) int {
	if configured <= 0 {
		return DefaultMaxTransactions
	}
	return configured
}

// eventBufferSize returns the lossy events-channel capacity, falling back
// to DefaultEventBufferSize when the configured value is non-positive.
func eventBufferSize(configured int) int {
	if configured <= 0 {
		return DefaultEventBufferSize
	}
	return configured
}

// lifecycleBufferSize bounds the dedicated lifecycle channel. Lifecycle
// events are low-volume (bounded by peer churn) but blocking, so the
// buffer is sized to comfortably hold a full connect/disconnect cycle for
// every peer slot plus slack — the event loop drains it long before it
// fills under normal operation.
func lifecycleBufferSize(cfg *Config) int {
	return max(cfg.MaxInbound+cfg.MaxOutbound+64, 64)
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

	cookie, err := generateInstanceCookie()
	if err != nil {
		return nil, fmt.Errorf("instance cookie: %w", err)
	}

	clusterReg := cluster.New()
	if err := clusterReg.Load(cfg.ClusterNodes); err != nil {
		return nil, fmt.Errorf("invalid cluster_nodes: %w", err)
	}
	manifestSpoolDir, err := prepareManifestSpoolDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	events := make(chan Event, eventBufferSize(cfg.EventBufferSize))

	inboundCap := cfg.MaxInbound + inboundBacklogSlack
	if inboundCap <= 0 {
		inboundCap = inboundBacklogSlack
	}
	outboundCap := cfg.MaxOutbound
	if outboundCap <= 0 {
		outboundCap = 1
	}

	o := &Overlay{
		cfg:                      cfg,
		identity:                 identity,
		cluster:                  clusterReg,
		instanceCookie:           cookie,
		discovery:                NewDiscovery(&cfg, events),
		autoconnectWake:          make(chan struct{}, 1),
		ledgerSync:               NewLedgerSyncHandler(events),
		peers:                    make(map[PeerID]*Peer),
		peerKeys:                 make(map[string]PeerID),
		peerEndpoints:            make(map[string]PeerID),
		inboundIPs:               make(map[string]int),
		pendingInbound:           make(map[PeerID]struct{}),
		events:                   events,
		consensusEvents:          make(chan Event, eventBufferSize(cfg.EventBufferSize)),
		consensusControlEvents:   make(chan Event, eventBufferSize(cfg.EventBufferSize)),
		acquisitionEvents:        make(chan Event, acquisitionEventBufferSize),
		consensusMessages:        make(chan *InboundMessage, messageBufferSize(cfg.MessageBufferSize)),
		consensusControlMessages: make(chan *InboundMessage, messageBufferSize(cfg.MessageBufferSize)),
		messages:                 make(chan *InboundMessage, messageBufferSize(cfg.MessageBufferSize)),
		txMessages:               make(chan *InboundMessage, txLaneBufferSize(cfg.MaxTransactions)),
		manifestMessages:         make(chan *InboundMessage, manifestMessageBufferSize(cfg.MaxPeers)),
		inboundReadBudget:        newReadBudget(cfg.InboundRetainedBytes),
		manifestSpoolDir:         manifestSpoolDir,
		ledgerData:               make(chan *InboundMessage, DefaultLedgerDataBufferSize),
		lifecycle:                make(chan Event, lifecycleBufferSize(&cfg)),
		stopCh:                   make(chan struct{}),
		listenerReady:            make(chan struct{}),
		relayedIndex:             make(map[[32]byte]*relayedEntry),
		clock:                    cfg.Clock,
		inboundSem:               make(chan struct{}, inboundCap),
		outboundSem:              make(chan struct{}, outboundCap),
		resourceManager:          resource.NewManagerWithLimits(cfg.Clock, nil, cfg.ResourceLimits),
		outboundBudget:           newOutboundBudget(cfg.OutboundRetainedBytes, cfg.MaxPeers),
	}
	if identity != nil {
		o.localNodeIdentity = identity.PublicKey()
	}

	// The peer-selection averages are per-sample, not per-second.
	o.txm.selected.sampleAvg = true
	o.txm.suppressed.sampleAvg = true
	o.txm.notEnabled.sampleAvg = true

	// Wire reduce-relay callbacks. The squelch callback constructs and
	// dispatches TMSquelch frames to individual peers; the ignored-
	// squelch callback charges a peer's bad-data balance whenever it
	// keeps relaying a validator's messages after being squelched.
	// Both are set at construction — Relay never swaps them at runtime.
	o.relay = NewRelay(&cfg, o.handleSquelch, o.chargeIgnoredSquelch)

	o.ledgerSync.SetPeerLedgerHintLookup(o.PeersWithClosedLedger)
	o.ledgerSync.SetChargePeer(func(peerID PeerID, fee resource.Charge, reason string) {
		if peer, ok := o.getPeer(peerID); ok {
			peer.Charge(fee, reason)
		}
	})
	o.ledgerSync.SetPrioritySender(func(ctx context.Context, peerID PeerID, frame []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return o.SendPriority(peerID, frame)
	})

	return o, nil
}

func (o *Overlay) attachOutboundBudget(peer *Peer) {
	peer.outbound.setBudget(o.outboundBudget)
	peer.outbound.setFatalObserver(func(err *SendQueueError) {
		if err.Reason == SendQueueSharedByteLimit {
			o.outboundCriticalSharedFailures.Add(1)
			return
		}
		o.outboundCriticalLocalFailures.Add(1)
	})
}

func (o *Overlay) OutboundCriticalQueueFailures() (local, shared uint64) {
	return o.outboundCriticalLocalFailures.Load(), o.outboundCriticalSharedFailures.Load()
}

// chargeIgnoredSquelch is the Relay-layer callback fired when a peer
// keeps relaying a validator's messages despite being squelched. We
// charge the peer's bad-data balance under a stable reason label so
// operators watching bad-data metrics can attribute the increase to
// squelch-ignored behavior specifically. This is the only place we
// learn that a peer ignored our TMSquelch — there is no separate
// protocol signal.
//
// Non-blocking; safe to invoke from the hot receive path because
// IncPeerBadData is a single map lookup + atomic add.
func (o *Overlay) chargeIgnoredSquelch(peerID PeerID) {
	o.IncPeerBadData(peerID, "squelch-ignored")
}

// Run starts the overlay and blocks until the context is cancelled. An
// Overlay has one lifecycle; a second or post-shutdown Run is rejected before
// it can bind a listener or publish readiness.
func (o *Overlay) Run(ctx context.Context) error {
	o.lifecycleMu.Lock()
	if o.lifecycleState != overlayLifecycleNew {
		err := ErrShutdown
		if o.lifecycleState == overlayLifecycleRunning {
			err = ErrAlreadyRunning
		}
		o.lifecycleMu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(ctx) //nolint:gosec // G118: cancel is stored and called by requestStop
	runComplete := make(chan struct{})
	o.ctx = runCtx
	o.cancel = cancel
	o.runComplete = runComplete
	o.shutdownDone = make(chan struct{})
	o.stopRequested = false
	o.lifecycleState = overlayLifecycleRunning
	o.lifecycleMu.Unlock()

	defer func() {
		o.requestStop()
		// runComplete means that the errgroup/event loops are joined. The
		// shutdown barrier waits peer producers after this closes before it
		// performs Discovery.Stop's one-shot final save.
		close(runComplete)
		o.shutdown()
	}()
	go func() {
		select {
		case <-runCtx.Done():
			o.requestStop()
		case <-runComplete:
		}
	}()

	// Load persistent state and start discovery before publishing readiness.
	// This keeps corrupt state from being reported as a ready listener and
	// gives startup callers a deterministic Run error.
	if o.discovery != nil {
		if err := o.discovery.Start(runCtx); err != nil {
			return fmt.Errorf("discovery error: %w", err)
		}
	}
	if err := o.requireRunning(); err != nil {
		return err
	}

	// Bind under lifecycleMu so Stop cannot transition the state between the
	// running check and listener publication.
	if o.cfg.ListenAddr != "" {
		listener, err := o.startListener(runCtx)
		if err != nil {
			return fmt.Errorf("listener error: %w", err)
		}
		o.lifecycleMu.Lock()
		if o.lifecycleState != overlayLifecycleRunning {
			o.lifecycleMu.Unlock()
			_ = listener.Close()
			return ErrShutdown
		}
		o.listenerMu.Lock()
		o.listener = listener
		o.listenerMu.Unlock()
		o.lifecycleMu.Unlock()
	}

	// Start resource manager (per-endpoint consumer table). The
	// periodic-activity goroutine ages out inactive entries; the
	// charge-time decay runs inline.
	if o.resourceManager != nil {
		o.lifecycleMu.Lock()
		if o.lifecycleState != overlayLifecycleRunning {
			o.lifecycleMu.Unlock()
			return ErrShutdown
		}
		o.resourceManager.Start()
		o.lifecycleMu.Unlock()
	}

	g, gCtx := errgroup.WithContext(runCtx)
	o.lifecycleMu.Lock()
	o.runDone = gCtx.Done()
	o.lifecycleMu.Unlock()

	// Closing the listener is the cancellation bridge for Accept: net.Listener
	// does not observe context cancellation on its own. This watcher belongs to
	// the same errgroup as every event/serve loop, so g.Wait joins it too.
	g.Go(func() error {
		<-gCtx.Done()
		_ = o.closeListener()
		return nil
	})

	// Start the fair, bounded serve scheduler before the event loop so heavy
	// inbound serve work (handleGetObjectsMessage) runs off the loop. The
	// scheduler context is canceled with the overlay and cancels queued or
	// running work during shutdown.
	scheduler := newServeScheduler(gCtx, serveWorkerCount, serveQueueDepth, servePerPeerQueue, servePerPeerConcurrency)
	scheduler.onTaskPanic = o.closePeerAfterServePanic
	o.lifecycleMu.Lock()
	if o.lifecycleState != overlayLifecycleRunning {
		o.lifecycleMu.Unlock()
		scheduler.close()
		if err := runCtx.Err(); err != nil {
			return err
		}
		return ErrShutdown
	}
	if err := runCtx.Err(); err != nil {
		o.lifecycleMu.Unlock()
		scheduler.close()
		return err
	}
	o.serveScheduler = scheduler
	o.signalListenerReady()
	o.lifecycleMu.Unlock()
	g.Go(func() error { return scheduler.Run(gCtx) })

	o.listenerMu.RLock()
	hasListener := o.listener != nil
	o.listenerMu.RUnlock()
	if hasListener {
		g.Go(func() error { return o.acceptLoop(gCtx) })
	}

	g.Go(func() error { return o.eventLoop(gCtx) })
	g.Go(func() error { return o.consensusEventLoop(gCtx) })
	g.Go(func() error { return o.consensusControlEventLoop(gCtx) })
	g.Go(func() error { return o.acquisitionEventLoop(gCtx) })

	if o.discovery != nil {
		g.Go(func() error { return o.discoveryLoop(gCtx) })
	}

	g.Go(func() error { return o.maintenanceLoop(gCtx) })

	err := g.Wait()
	o.releaseQueuedInbound()
	return err
}

// Stop gracefully shuts down the overlay. It is safe before Run, while Run is
// starting, and after Run returns. All callers share one shutdown barrier so a
// context-only Run cancellation performs the same final Discovery save as an
// external Stop.
func (o *Overlay) Stop() error {
	o.requestStop()
	o.shutdown()
	return nil
}

func (o *Overlay) requireRunning() error {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if o.lifecycleState == overlayLifecycleRunning {
		return nil
	}
	return ErrShutdown
}

func (o *Overlay) requestStop() {
	o.peerStartMu.Lock()
	o.peerStartsClosed = true
	o.peerStartMu.Unlock()

	o.lifecycleMu.Lock()
	if o.lifecycleState == overlayLifecycleNew || o.lifecycleState == overlayLifecycleRunning {
		o.lifecycleState = overlayLifecycleStopping
	}
	if o.shutdownDone == nil {
		o.shutdownDone = make(chan struct{})
	}
	cancel := o.cancel
	scheduler := o.serveScheduler
	if !o.stopRequested {
		o.stopRequested = true
		if o.stopCh != nil {
			close(o.stopCh)
		}
	}
	o.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}

	o.listenerMu.RLock()
	l := o.listener
	o.listenerMu.RUnlock()
	if l != nil {
		_ = l.Close()
	}

	o.peersMu.Lock()
	for _, p := range o.peers {
		p.Close()
	}
	o.peersMu.Unlock()
	if scheduler != nil {
		scheduler.close()
	}
}

func (o *Overlay) closeListener() error {
	o.listenerMu.RLock()
	l := o.listener
	o.listenerMu.RUnlock()
	if l == nil {
		return nil
	}
	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (o *Overlay) shutdown() {
	o.lifecycleMu.Lock()
	if o.shutdownDone == nil {
		o.shutdownDone = make(chan struct{})
	}
	done := o.shutdownDone
	o.lifecycleMu.Unlock()

	o.shutdownOnce.Do(func() {
		o.peerStartWG.Wait()
		o.peerWG.Wait()

		o.lifecycleMu.Lock()
		runComplete := o.runComplete
		o.lifecycleMu.Unlock()
		if runComplete != nil {
			<-runComplete
		}
		o.releaseQueuedInbound()

		if o.discovery != nil {
			o.discovery.Stop()
		}
		if o.resourceManager != nil {
			o.resourceManager.Stop()
		}

		o.lifecycleMu.Lock()
		o.lifecycleState = overlayLifecycleStopped
		o.lifecycleMu.Unlock()
		close(done)
	})
	<-done
}

func (o *Overlay) releaseQueuedInbound() {
	drainEvents := func(events <-chan Event) {
		for {
			select {
			case evt := <-events:
				evt.release()
			default:
				return
			}
		}
	}
	drainMessages := func(messages <-chan *InboundMessage) {
		for {
			select {
			case msg := <-messages:
				_ = msg.Close()
			default:
				return
			}
		}
	}

	drainEvents(o.events)
	drainEvents(o.consensusEvents)
	drainEvents(o.consensusControlEvents)
	drainEvents(o.acquisitionEvents)
	drainMessages(o.consensusMessages)
	drainMessages(o.consensusControlMessages)
	drainMessages(o.messages)
	drainMessages(o.txMessages)
	drainMessages(o.manifestMessages)
	drainMessages(o.ledgerData)
}

// eventLoop processes internal events. It drains both the dedicated
// lifecycle channel and the best-effort message channel; the single goroutine
// keeps handleEvent's per-peer state mutations serialized.
func (o *Overlay) eventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-o.lifecycle:
			o.handleEvent(evt)
		case evt := <-o.events:
			o.handleEvent(evt)
		}
	}
}

func (o *Overlay) acquisitionEventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-o.acquisitionEvents:
			o.onMessageReceived(evt)
		}
	}
}

func (o *Overlay) consensusEventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-o.consensusEvents:
			o.onMessageReceived(evt)
		}
	}
}

func (o *Overlay) consensusControlEventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-o.consensusControlEvents:
			o.onMessageReceived(evt)
		}
	}
}

func (o *Overlay) submitServeForPeerOwned(
	peerID PeerID,
	admission resource.Charge,
	job func(context.Context),
	discard func(),
) bool {
	if job == nil {
		return false
	}
	peer, exists := o.getPeer(peerID)
	if peerID != 0 && !exists {
		return false
	}
	if exists && peer.closed.Load() {
		return false
	}
	if exists && admission.Cost() > 0 {
		peer.Charge(admission, "serve request admission")
	}
	o.lifecycleMu.Lock()
	scheduler := o.serveScheduler
	serveCtx := o.ctx
	lifecycleState := o.lifecycleState
	o.lifecycleMu.Unlock()
	if lifecycleState == overlayLifecycleStopping || lifecycleState == overlayLifecycleStopped {
		o.droppedServeJobs.Add(1)
		if exists {
			peer.Charge(resource.FeeRequestNoReply(), "serve scheduler stopped")
		}
		return false
	}
	if scheduler == nil {
		if exists {
			peer.sendMu.RLock()
			closed := peer.closed.Load() || peer.gracefulClosing
			peer.sendMu.RUnlock()
			if closed {
				return false
			}
		}
		job(context.Background())
		return true
	}
	if exists {
		peer.sendMu.RLock()
		if peer.closed.Load() || peer.gracefulClosing {
			peer.sendMu.RUnlock()
			return false
		}
		accepted := scheduler.SubmitOwned(serveCtx, peerID, job, discard)
		peer.sendMu.RUnlock()
		if accepted {
			return true
		}
	} else if scheduler.SubmitOwned(serveCtx, peerID, job, discard) {
		return true
	}
	o.droppedServeJobs.Add(1)
	if exists {
		peer.Charge(resource.FeeRequestNoReply(), "serve scheduler saturated")
	}
	slog.Debug("serve job dropped: scheduler saturated", "t", "Overlay", "peer", peerID)
	return false
}

// cancelServePeer disposes queued work and propagates cancellation to any
// active provider calls when a peer disconnects.
func (o *Overlay) cancelServePeer(peerID PeerID) {
	o.lifecycleMu.Lock()
	scheduler := o.serveScheduler
	o.lifecycleMu.Unlock()
	if scheduler != nil {
		scheduler.CancelPeer(peerID)
	}
}

func (o *Overlay) closePeerAfterServePanic(peerID PeerID) {
	if peer, ok := o.getPeer(peerID); ok {
		_ = peer.Close()
	}
}

// DroppedServeJobs returns the cumulative count of heavy serve jobs shed
// because the scheduler was saturated or stopping.
func (o *Overlay) DroppedServeJobs() uint64 {
	return o.droppedServeJobs.Load()
}
