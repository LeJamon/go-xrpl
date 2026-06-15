package peermanagement

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
	"golang.org/x/sync/errgroup"
)

// serveWorkerCount bounds concurrent heavy serve operations (fetch-pack /
// get-objects / tx back-fill) handled off the event loop, and
// serveQueueDepth bounds the pending backlog before submitServe sheds
// load. Mirrors rippled bounding these behind its job queue rather than
// the read strand.
const (
	serveWorkerCount = 4
	serveQueueDepth  = 64
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
	peers   map[PeerID]*Peer
	peersMu sync.RWMutex
	nextID  atomic.Uint64

	// peerWG joins every peer.Run goroutine launched by handleInbound
	// or Connect. Stop blocks on it for deterministic shutdown.
	peerWG sync.WaitGroup

	// inboundSem caps concurrent handleInbound goroutines.
	// Length = MaxInbound + inboundBacklogSlack.
	inboundSem chan struct{}

	// outboundSem caps concurrent autoconnect Connect goroutines so
	// a slow discovery tick cannot stack one goroutine per candidate.
	outboundSem chan struct{}

	// relayedIndex maps suppression-hash → set of peers known to have
	// that message. Populated as we forward a validator message (each
	// recipient joins the set) and queried by the consensus router on
	// duplicate arrivals so ALL known-havers feed the reduce-relay
	// slot — not just the peer that delivered the current duplicate.
	relayedIndex   map[[32]byte]*relayedEntry
	relayedIndexMu sync.Mutex
	clockForIndex  func() time.Time

	// Coordination channels
	//
	// events is the LOSSY hot path: EventMessageReceived (from peers) and
	// EventLedgerResponse (server-side ledger-sync replies). A non-blocking
	// send drops to droppedEvents under back-pressure so the read hot path
	// can never deadlock against an event loop holding peersMu.
	//
	// lifecycle is the dedicated NON-LOSSY path for peer lifecycle events
	// (Connecting/Connected/Disconnected/Failed). Sends BLOCK until the
	// event loop accepts them (see dispatchLifecycle): lifecycle volume is
	// tiny and bounded by peer count, and a dropped EventPeerDisconnected
	// would leak router/relay per-peer state until the idle sweep. Keeping
	// it off the message channel means a message burst can no longer crowd
	// out a disconnect. Both are drained by the single eventLoop goroutine.
	events    chan Event
	lifecycle chan Event
	messages  chan *InboundMessage

	// serveJobs carries heavy inbound serve work (fetch-pack, generic
	// get-objects, tx back-fill) off the event-loop goroutine onto a
	// bounded worker pool, so a single expensive serve can't stall ping
	// replies and lifecycle handling. Created in Run; nil when the overlay
	// was built without Run (submitServe then runs the job inline, which
	// preserves the synchronous behaviour unit tests rely on). Mirrors
	// rippled offloading these to its job queue (jtPACK et al.) rather than
	// running them on the peer read strand.
	serveJobs chan func()

	// maxTransactions caches cfg.MaxTransactions, the per-type
	// in-flight TMTransaction ceiling consulted at the overlay → router
	// boundary. Non-positive disables the gate.
	maxTransactions int

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

	// txProvider returns the raw tx blob for hash if it is in the
	// open-ledger view. Wired by the consensus adaptor at startup so
	// the tx-reduce-relay reply path (handleGetObjectsMessage,
	// otTRANSACTIONS branch) can answer a peer's TMGetObjectByHash
	// query without importing internal/ledger/service into this
	// package. nil-safe — the reply path drops without charging when
	// the provider isn't wired (tests, or operators running the
	// overlay without a ledger backend). Guarded by providersMu.
	txProvider func(hash [32]byte) ([]byte, bool)

	// nodeObjectProvider returns the raw node-store blob for a content
	// hash. Wired by the server at startup so the generic TMGetObjectByHash serve
	// path (handleGetObjectsMessage → serveGetObjects) can answer a
	// peer's by-hash query without importing storage/nodestore lifecycle
	// into this package. nil-safe — the serve path drops without
	// charging when unwired (an overlay deployed without a backing
	// store, or tests). Guarded by providersMu.
	nodeObjectProvider func(hash [32]byte) ([]byte, bool)

	// openLedgerHashesProvider returns the set of tx hashes currently
	// in the open-ledger view. Drives the periodic tx-reduce-relay
	// TMHaveTransactions emission in sendTxQueueAnnounce. nil-safe —
	// the emitter skips when unwired. Guarded by providersMu.
	openLedgerHashesProvider func() [][32]byte

	// clusterFeeSink is invoked by handleClusterMessage after the
	// registry-update loop with the median LoadFee across members
	// reported within the last cluster-fee window. nil-safe — the
	// inbound handler skips the median computation when unwired.
	// Guarded by providersMu.
	clusterFeeSink func(fee uint32)

	// localLoadFeeProvider returns the local node's current load fee
	// factor (LoadFeeTrack.getLocalFee). Wired into sendClusterUpdate
	// so the self-entry in each outbound TMCluster gossip advertises
	// real load instead of 0. nil-safe — sendClusterUpdate falls back
	// to 0 when unwired. Guarded by providersMu.
	localLoadFeeProvider func() uint32

	// localNodeIdentity is the raw 33-byte compressed NodePublic of
	// THIS node. Used by the cluster timer to insert ourselves into
	// the gossip frame so peers can correlate validator load. Set in New
	// from o.identity; nil only when no identity could be loaded, in which
	// case the cluster timer leaves the self-entry out.
	localNodeIdentity []byte

	// droppedMessages counts how many times the non-blocking send to
	// the messages channel hit its default branch (downstream consumer
	// slow). Exposed via DroppedMessages() so server_info / telemetry
	// can surface back-pressure to operators. Without this counter a
	// slow consumer silently loses events with only a debug-level log.
	droppedMessages atomic.Uint64

	// droppedTransactions counts inbound TMTransaction frames refused
	// by the MaxTransactions gate in onMessageReceived; the
	// channel-saturation drop is the backstop when the gate is
	// disabled. Surfaced via server_info as jq_trans_overflow.
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
	// listenerMu guards listener: written once by startListener (called
	// from Run before any concurrent reader exists). Read under RLock
	// from ListenAddr and Stop (other goroutines). The reads in Run and
	// acceptLoop are unlocked: Run's read at "if o.listener != nil" is
	// in the same goroutine as the write, and acceptLoop is spawned via
	// g.Go after the write returns, so happens-before applies.
	listenerMu sync.RWMutex
	listener   net.Listener

	// Lifecycle
	// lifecycleMu guards ctx/cancel against the Run-write vs Stop-read
	// race: Run is typically launched in its own goroutine and lazily
	// initialises cancel, while a concurrent Stop (e.g. error-path
	// teardown) reads it. Other ctx reads live in goroutines spawned by
	// Run after the write, so happens-before covers them.
	lifecycleMu sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	stopOnce    sync.Once

	// stopCh is closed by Stop to release any lifecycle send blocked on an
	// event loop that has already exited during shutdown.
	stopCh chan struct{}
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

// SetLedgerHintProvider wires the hint source; nil suppresses headers.
func (o *Overlay) SetLedgerHintProvider(fn func() (LedgerHints, bool)) {
	o.providersMu.Lock()
	o.ledgerHintProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) ledgerHintProviderSnapshot() func() (LedgerHints, bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.ledgerHintProvider
}

// SetValidLedgerProvider wires the validated-ledger source used by
// handleStatusChange. ok=false suppresses tracking updates.
func (o *Overlay) SetValidLedgerProvider(fn func() (seq uint32, age time.Duration, ok bool)) {
	o.providersMu.Lock()
	o.validLedgerProvider = fn
	o.providersMu.Unlock()
}

func (o *Overlay) validLedgerProviderSnapshot() func() (seq uint32, age time.Duration, ok bool) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.validLedgerProvider
}

// PeerStatusUpdate captures the post-decode TMStatusChange fields the
// RPC layer needs to materialize a peer_status WebSocket event. Pointer
// fields preserve protobuf has-presence; nil means the wire field was
// absent and the RPC layer omits the JSON field.
type PeerStatusUpdate struct {
	// Status is the UPPERCASE status name. Carries the
	// post-inheritance value returned by applyStatusChange, so a
	// status-less wire message still emits the prior enum once.
	Status string
	// Action is CLOSING_LEDGER, ACCEPTED_LEDGER or SWITCHED_LEDGER.
	// LOST_SYNC is unreachable because handleStatusChange returns
	// before the publish.
	Action string
	// LedgerHash is sourced from the peer's post-apply closed-ledger
	// state rather than echoing the raw wire bytes. When the wire
	// bytes were malformed that state is cleared and the 64-char zero
	// hex string is emitted — so callers must ALWAYS emit a value
	// when the wire carried the field, falling back to "00…00".
	LedgerHash string
	// LedgerIndex: nil = field absent; non-nil = emit (even when
	// value is 0 — a peer can legitimately advertise the genesis seq).
	LedgerIndex *uint32
	// Date is auto-stamped with the local clock when the wire didn't
	// carry a networktime, so it is always non-nil here.
	Date *uint32
	// LedgerIndexMin / LedgerIndexMax are nil unless both wire fields
	// were present.
	LedgerIndexMin *uint32
	LedgerIndexMax *uint32
}

// SetPeerStatusPublisher wires a sink for peer_status events. The
// overlay invokes this callback for every non-lostSync TMStatusChange
// after state has been recorded. Passing nil disconnects the sink.
func (o *Overlay) SetPeerStatusPublisher(fn func(PeerStatusUpdate)) {
	o.providersMu.Lock()
	o.peerStatusPublisher = fn
	o.providersMu.Unlock()
}

func (o *Overlay) peerStatusPublisherSnapshot() func(PeerStatusUpdate) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.peerStatusPublisher
}

// peerStatusUpperName returns the UPPERCASE status name
// (CONNECTING/...) emitted by peer_status events, distinct from the
// lowercase strings used by the `peers` RPC. Returns "" for nsUNKNOWN
// or any unknown enum.
func peerStatusUpperName(s message.NodeStatus) string {
	switch s {
	case message.NodeStatusConnecting:
		return "CONNECTING"
	case message.NodeStatusConnected:
		return "CONNECTED"
	case message.NodeStatusMonitoring:
		return "MONITORING"
	case message.NodeStatusValidating:
		return "VALIDATING"
	case message.NodeStatusShutting:
		return "SHUTTING"
	default:
		return ""
	}
}

// peerStatusActionName maps a NodeEvent to its peer_status action
// name. handleStatusChange returns before the publish for neLOST_SYNC,
// so the LOST_SYNC arm is unreachable from this call site and
// intentionally omitted. Unknown enums fall through silently.
func peerStatusActionName(e message.NodeEvent) string {
	switch e {
	case message.NodeEventClosingLedger:
		return "CLOSING_LEDGER"
	case message.NodeEventAcceptedLedger:
		return "ACCEPTED_LEDGER"
	case message.NodeEventSwitchedLedger:
		return "SWITCHED_LEDGER"
	default:
		return ""
	}
}

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
		cfg:             cfg,
		identity:        identity,
		cluster:         clusterReg,
		instanceCookie:  cookie,
		discovery:       NewDiscovery(&cfg, events),
		ledgerSync:      NewLedgerSyncHandler(events),
		peers:           make(map[PeerID]*Peer),
		events:          events,
		messages:        make(chan *InboundMessage, messageBufferSize(cfg.MessageBufferSize)),
		lifecycle:       make(chan Event, lifecycleBufferSize(&cfg)),
		stopCh:          make(chan struct{}),
		maxTransactions: cfg.MaxTransactions,
		relayedIndex:    make(map[[32]byte]*relayedEntry),
		clockForIndex:   time.Now,
		inboundSem:      make(chan struct{}, inboundCap),
		outboundSem:     make(chan struct{}, outboundCap),
		resourceManager: resource.NewManager(nil, nil),
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

	return o, nil
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
	o.lifecycleMu.Lock()
	o.ctx, o.cancel = context.WithCancel(ctx)
	cancel := o.cancel
	o.lifecycleMu.Unlock()
	defer cancel()

	// Start listener if configured
	if o.cfg.ListenAddr != "" {
		if err := o.startListener(); err != nil {
			return fmt.Errorf("listener error: %w", err)
		}
	}

	// Start resource manager (per-endpoint consumer table). The
	// periodic-activity goroutine ages out inactive entries; the
	// charge-time decay runs inline.
	if o.resourceManager != nil {
		o.resourceManager.Start()
	}

	// Start discovery
	if err := o.discovery.Start(o.ctx); err != nil {
		return fmt.Errorf("discovery error: %w", err)
	}

	g, gCtx := errgroup.WithContext(o.ctx)

	// Start the bounded serve-worker pool before the event loop so heavy
	// inbound serve work (handleGetObjectsMessage) runs off the loop. The
	// channel is assigned before eventLoop is launched (happens-before the
	// only reader, submitServe, which runs on the loop).
	o.serveJobs = make(chan func(), serveQueueDepth)
	for range serveWorkerCount {
		g.Go(func() error { return o.serveWorker(gCtx) })
	}

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

// Stop gracefully shuts down the overlay. Blocks on peerWG so callers
// observe a fully-quiesced overlay rather than racing against
// peer.Run goroutines still draining after Close. Idempotent: repeated
// calls (defensive cleanup, error-path + deferred stop) are no-ops.
func (o *Overlay) Stop() error {
	o.stopOnce.Do(func() {
		// Release any lifecycle send blocked on an event loop that is
		// about to exit, so run-watcher goroutines drain cleanly under
		// peerWG.Wait below. Guarded for overlays built outside New (some
		// tests / embedders construct the struct directly).
		if o.stopCh != nil {
			close(o.stopCh)
		}

		o.lifecycleMu.Lock()
		cancel := o.cancel
		o.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}

		// Close listener
		o.listenerMu.RLock()
		l := o.listener
		o.listenerMu.RUnlock()
		if l != nil {
			l.Close()
		}

		// Stop discovery
		o.discovery.Stop()

		// Close all peers
		o.peersMu.Lock()
		for _, p := range o.peers {
			p.Close()
		}
		o.peersMu.Unlock()

		o.peerWG.Wait()

		if o.resourceManager != nil {
			o.resourceManager.Stop()
		}
	})

	return nil
}

// eventLoop processes internal events. It drains both the dedicated
// lifecycle channel and the lossy message channel; the single goroutine
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

// serveWorker drains the bounded serve-job queue. Multiple workers run
// concurrently; the serve paths (fetch-pack / get-objects / tx back-fill)
// are read-only against the ledger/node store and peer-safe, so parallel
// execution is sound.
func (o *Overlay) serveWorker(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job := <-o.serveJobs:
			job()
		}
	}
}

// submitServe hands a heavy serve job to the worker pool. When the overlay
// was built without Run (no pool — most unit tests), it runs the job
// inline to preserve synchronous behaviour. On a saturated queue it sheds
// the job and bumps droppedServeJobs: the requesting peer's query goes
// unanswered and it retries elsewhere.
func (o *Overlay) submitServe(job func()) {
	if o.serveJobs == nil {
		job()
		return
	}
	select {
	case o.serveJobs <- job:
	default:
		o.droppedServeJobs.Add(1)
		slog.Debug("serve job dropped: worker pool saturated", "t", "Overlay")
	}
}

// DroppedServeJobs returns the cumulative count of heavy serve jobs shed
// because the worker pool was saturated.
func (o *Overlay) DroppedServeJobs() uint64 {
	return o.droppedServeJobs.Load()
}

// handleEvent dispatches events to appropriate handlers.
func (o *Overlay) handleEvent(evt Event) {
	switch evt.Type {
	case EventPeerConnected:
		o.onPeerConnected(evt)
	case EventPeerDisconnected:
		o.onPeerDisconnected(evt)
	case EventPeerFailed:
		o.onPeerFailed(evt)
	case EventMessageReceived:
		o.onMessageReceived(evt)
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
	// Notify higher layers AFTER discovery state is updated so any work
	// they do (e.g. sending us-originated frames to the peer) sees a
	// fully-bookkept overlay. Mirrors the disconnect callback ordering.
	if cb := o.onPeerConnectSnapshot(); cb != nil {
		cb(evt.PeerID)
	}
}

func (o *Overlay) onPeerDisconnected(evt Event) {
	o.peerDisconnects.Add(1)
	o.discovery.MarkDisconnected(evt.PeerID)
	o.relay.RemovePeer(evt.PeerID)
	// Fire the higher-layer disconnect callback so per-peer state in
	// consumers (router peerStates, adaptor peerLCLs) gets cleaned.
	// Without this the peer's last-reported ledger stays in the
	// engine's getNetworkLedger vote set indefinitely, biasing
	// consensus toward the view of a peer that's no longer here.
	if cb := o.onPeerDisconnectSnapshot(); cb != nil {
		cb(evt.PeerID)
	}
}

// SetPeerDisconnectCallback registers a callback fired after a peer is
// removed from the overlay. The callback runs on the event-loop
// goroutine so implementations MUST NOT block — push to a channel if
// meaningful work is needed. Passing nil clears the callback.
//
// This is the channel by which higher layers (e.g. the consensus
// router) are notified of disconnects so they can clean their own
// per-peer state. Prefer this over polling Peers().
func (o *Overlay) SetPeerDisconnectCallback(cb func(PeerID)) {
	o.providersMu.Lock()
	o.onPeerDisconnect = cb
	o.providersMu.Unlock()
}

func (o *Overlay) onPeerDisconnectSnapshot() func(PeerID) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.onPeerDisconnect
}

// SetPeerConnectCallback registers a callback fired after a peer's
// handshake has completed and the peer is in the overlay's peer map.
// Same blocking contract as SetPeerDisconnectCallback: runs on the
// event loop and MUST NOT block. Passing nil clears the callback.
//
// Used by the consensus router to send our local validator manifest
// to a freshly-connected peer (#372), so peers configured under
// validator-list publishing can resolve our signing key back to the
// trusted master.
func (o *Overlay) SetPeerConnectCallback(cb func(PeerID)) {
	o.providersMu.Lock()
	o.onPeerConnect = cb
	o.providersMu.Unlock()
}

func (o *Overlay) onPeerConnectSnapshot() func(PeerID) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.onPeerConnect
}

func (o *Overlay) onPeerFailed(evt Event) {
	if o.discovery.bootCache != nil {
		o.discovery.bootCache.MarkFailed(evt.Endpoint.String())
	}
}

func (o *Overlay) onMessageReceived(evt Event) {
	msgType := message.MessageType(evt.MessageType)

	// Record reduce-relay traffic metrics before dispatch: counted on
	// the inbound path, by message type and on-wire payload size, gated
	// on the negotiated tx-reduce-relay feature or the metrics-only
	// override.
	if o.cfg.EnableTxReduceRelayMetrics ||
		(o.cfg.EnableTxReduceRelay && o.PeerSupports(evt.PeerID, FeatureTxReduceRelay)) {
		o.recordInboundTxMetric(msgType, evt.Payload, evt.WireSize)
	}

	// Handle PING at transport level — respond with PONG immediately
	if msgType == message.TypePing {
		o.handlePing(evt)
		return
	}

	// Inbound TMSquelch is accepted UNCONDITIONALLY, matching rippled —
	// there is no per-peer gating on vpReduceRelay for incoming
	// squelches. Feature negotiation governs what WE SEND (we only
	// emit TMSquelch to peers who advertised reduce-relay), not what
	// we accept: a squelch directive is harmless when applied — it
	// only suppresses what we send next — and rejecting it creates an
	// attack surface where a hostile peer could advertise one
	// capability set to us and another to a neighbor to desync
	// squelch state.
	if msgType == message.TypeSquelch {
		if !o.PeerSupports(evt.PeerID, FeatureVpReduceRelay) {
			slog.Debug("TMSquelch from peer without vprr feature; accepting (matches rippled)",
				"t", "Overlay", "peer", evt.PeerID)
		}
		o.handleSquelchMessage(evt)
		return
	}

	// mtSTATUS_CHANGE refreshes Closed-/Previous-Ledger hints and is
	// then forwarded to the consensus
	// router. The overlay handler updates per-peer state +
	// peer_status WS publishing; the consensus router needs the same
	// frame to update its peer-LCL view (Adaptor.UpdatePeerLCL feeds
	// getNetworkLedger) and to drive initial-sync ledger acquisition
	// (startLedgerAcquisition / checkBehind). Splitting at the
	// overlay and dropping here would leave the router blind to peer
	// status — a fresh node would never leave OpModeDisconnected and
	// the engine's timerEntry would never advance (issue #381).
	if msgType == message.TypeStatusChange {
		o.handleStatusChange(evt)
		// fall through to the o.messages forward
	}

	// Serve mtREPLAY_DELTA_REQ from the local ledger sync handler.
	// Before dispatching we verify the peer actually negotiated
	// ledger-replay in its handshake; a peer sending these without the
	// feature is silently dropped and charged bad data.
	if msgType == message.TypeReplayDeltaReq {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("ReplayDeltaRequest from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "replay-delta-req-unnegotiated")
			return
		}
		o.dispatchReplayDeltaRequest(evt)
		return
	}

	// Serve mtPROOF_PATH_REQ from the local ledger sync handler. Same
	// handshake-negotiation gate as mtREPLAY_DELTA_REQ above — the
	// proof-path protocol is part of the ledger-replay feature bundle.
	if msgType == message.TypeProofPathReq {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("ProofPathRequest from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "proof-path-req-unnegotiated")
			return
		}
		o.dispatchProofPathRequest(evt)
		return
	}

	// Response-path feature gate. A peer that didn't negotiate
	// ledgerreplay in handshake shouldn't be sending us
	// TMReplayDeltaResponse or TMProofPathResponse unsolicited. Gate
	// BEFORE forwarding to the router so a non-negotiated peer can't
	// wedge the inbound acquisition state with bogus responses.
	if msgType == message.TypeReplayDeltaResponse {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("TMReplayDeltaResponse from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "replay-delta-resp-unnegotiated")
			return
		}
	}
	if msgType == message.TypeProofPathResponse {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("TMProofPathResponse from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "proof-path-resp-unnegotiated")
			return
		}
	}

	// mtREPLAY_DELTA_RESPONSE / mtPROOF_PATH_RESPONSE that pass the
	// feature gate above reach the consensus router via the overlay's
	// Messages() channel — like every other peer-originated reply
	// (mtLEDGER_DATA, mtTRANSACTION, mtVALIDATION). The router owns
	// the verification + adoption state and is the only place that
	// can drive it.

	// Transport-level messages with no consensus-router impact are
	// handled inline here and NOT forwarded to o.messages.
	switch msgType {
	case message.TypeCluster:
		o.handleClusterMessage(evt)
		return
	case message.TypeGetObjects:
		o.handleGetObjectsMessage(evt)
		return
	case message.TypeHaveTransactions:
		o.handleHaveTransactionsMessage(evt)
		return
	case message.TypeTransactions:
		o.handleTransactionsBatchMessage(evt)
		return
	case message.TypeEndpoints:
		o.handleEndpointsMessage(evt)
		return
	}

	slog.Debug("Message received", "t", "Overlay", "type", msgType.String(), "peer", evt.PeerID, "size", len(evt.Payload))

	// Per-type ingress gate: refuse before the channel send so non-tx
	// traffic keeps flowing when the dispatch pipeline is
	// tx-saturated. Channel-saturation branch below is the backstop
	// when the gate is disabled.
	if msgType == message.TypeTransaction && o.maxTransactions > 0 && len(o.messages) >= o.maxTransactions {
		o.droppedTransactions.Add(1)
		slog.Info("Transaction queue is full", "t", "Overlay",
			"pending", len(o.messages), "max", o.maxTransactions, "peer", evt.PeerID)
		return
	}

	// Forward to external consumers. On back-pressure (channel full),
	// increment a visible counter rather than silently dropping — the
	// warn log alone is easy to miss at production log levels.
	select {
	case o.messages <- &InboundMessage{
		PeerID:  evt.PeerID,
		Type:    evt.MessageType,
		Payload: evt.Payload,
	}:
	default:
		o.droppedMessages.Add(1)
		if msgType == message.TypeTransaction {
			o.droppedTransactions.Add(1)
		}
		slog.Warn("Message dropped: channel full", "t", "Overlay", "type", msgType.String())
	}
}

// DroppedTransactions returns the cumulative count of TMTransaction
// frames refused at the overlay → router boundary. Surfaced via
// server_info as jq_trans_overflow.
func (o *Overlay) DroppedTransactions() uint64 {
	return o.droppedTransactions.Load()
}

// DroppedMessages returns the cumulative count of inbound messages the
// overlay had to drop because the downstream consumer channel was
// full. Surfaced via server_info/server_state for operators to detect
// consumer back-pressure — a nonzero and growing value indicates the
// router/engine can't keep up with network ingress.
func (o *Overlay) DroppedMessages() uint64 {
	return o.droppedMessages.Load()
}

// PingTimeoutDisconnects returns the cumulative count of peers torn
// down because they failed to answer pings within pingTimeout. A
// nonzero, growing value flags either a flaky network or peers that
// have stopped servicing the overlay protocol.
func (o *Overlay) PingTimeoutDisconnects() uint64 {
	return o.pingTimeoutDisconnects.Load()
}

// PeerDisconnects returns the cumulative count of peers torn down for
// any reason. Surfaced via server_info.peer_disconnects.
func (o *Overlay) PeerDisconnects() uint64 {
	return o.peerDisconnects.Load()
}

// PeerDisconnectsResources returns the count of peers torn down by a
// resource.Consumer charge exceeding the drop threshold. Surfaced via
// server_info.peer_disconnects_resources.
func (o *Overlay) PeerDisconnectsResources() uint64 {
	return o.peerDisconnectsCharges.Load()
}

func (o *Overlay) ResourceManager() *resource.Manager {
	return o.resourceManager
}

// DroppedEvents returns the cumulative count of events dropped
// because the event loop fell behind. Non-zero growth means handlers
// are slow enough that blocking sends would have deadlocked the read
// hot path — investigate handler latency before raising the buffer.
func (o *Overlay) DroppedEvents() uint64 {
	return o.droppedEvents.Load()
}

// dispatchLifecycle delivers a peer lifecycle event to the event loop.
// Unlike the lossy EventMessageReceived path, lifecycle events must not be
// dropped: a lost EventPeerDisconnected leaks router/relay per-peer state
// until the idle sweep. The send blocks until the event loop accepts it
// (lifecycle volume is tiny and bounded by peer count), bailing only when
// the overlay is shutting down so a stopped event loop can't wedge the
// caller. Every caller is a handshake / run-watcher / autoconnect
// goroutine — never the event loop itself — so a blocking send cannot
// self-deadlock.
func (o *Overlay) dispatchLifecycle(evt Event) {
	select {
	case o.lifecycle <- evt:
	case <-o.stopCh:
	}
}

func (o *Overlay) notePeerRunEnded(err error) {
	if errors.Is(err, ErrPingTimeout) {
		o.pingTimeoutDisconnects.Add(1)
	}
}

// DroppedLedgerResponses returns the cumulative count of ledger-sync
// responses dropped due to a full events channel (see
// LedgerSyncHandler.sendReplayDeltaResponse / sendProofPathResponse).
// Same shape as DroppedMessages but for the server-side response path;
// delegates to the handler's own counter.
func (o *Overlay) DroppedLedgerResponses() uint64 {
	if o.ledgerSync != nil {
		return o.ledgerSync.DroppedResponses()
	}
	return 0
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
		o.IncPeerBadData(evt.PeerID, "replay-delta-req-decode")
		return
	}
	req, ok := decoded.(*message.ReplayDeltaRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ReplayDeltaRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
		if errors.Is(err, ErrPeerBadRequest) {
			o.IncPeerBadData(evt.PeerID, "replay-delta-req-bad")
		}
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
		o.IncPeerBadData(evt.PeerID, "proof-path-req-decode")
		return
	}
	req, ok := decoded.(*message.ProofPathRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ProofPathRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
		if errors.Is(err, ErrPeerBadRequest) {
			o.IncPeerBadData(evt.PeerID, "proof-path-req-bad")
		}
	}
}

func (o *Overlay) handleStatusChange(evt Event) {
	decoded, err := message.Decode(message.TypeStatusChange, evt.Payload)
	if err != nil {
		slog.Debug("StatusChange decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}
	peer, exists := o.getPeer(evt.PeerID)
	if !exists {
		return
	}
	// Stamp the wire's networktime with the local clock when the peer
	// didn't include it, so the peer_status emit always carries a
	// `date`. Mutate sc so the auto-filled value is observable to
	// subscribers.
	if sc.NetworkTime == 0 {
		sc.NetworkTime = uint64(time.Now().Unix() - protocol.RippleEpochUnix)
	}

	effectiveStatus := peer.applyStatusChange(sc)

	// lostSync returns before either the tracking check or the
	// publish runs, so a lostSync update never surfaces as a
	// peer_status WebSocket event.
	if sc.NewEvent == message.NodeEventLostSync {
		return
	}

	// The tracking check is gated on a fresh (<2 min) validated
	// ledger. The gate must NOT short-circuit the publish below,
	// which runs unconditionally for non-lostSync messages.
	if sc.LedgerSeq != 0 {
		if provider := o.validLedgerProviderSnapshot(); provider != nil {
			if validSeq, age, ok := provider(); ok && validSeq != 0 && age < 2*time.Minute {
				peer.CheckTracking(sc.LedgerSeq, validSeq)
			}
		}
	}

	// Publish to peer_status subscribers.
	if pub := o.peerStatusPublisherSnapshot(); pub != nil {
		// Emit ledger_hash whenever the wire carried the field,
		// sourcing the value from the peer's post-apply closed-ledger
		// state. When the wire bytes were malformed, applyStatusChange
		// clears that storage and the all-zeros 64-char hex string is
		// emitted.
		var ledgerHash string
		if len(sc.LedgerHash) > 0 {
			if h, ok := peer.ClosedLedger(); ok {
				ledgerHash = strings.ToUpper(hex.EncodeToString(h[:]))
			} else {
				ledgerHash = strings.Repeat("0", 64)
			}
		}
		// Emit min/max only when both wire fields were present.
		// nil-on-absence keeps that paired gate without conflating
		// value 0 with "absent".
		var minSeq, maxSeq *uint32
		if sc.FirstSeq != nil && sc.LastSeq != nil {
			f, l := *sc.FirstSeq, *sc.LastSeq
			minSeq, maxSeq = &f, &l
		}
		// The decoder loses proto-presence for ledger_seq (see
		// internal/peermanagement/proto/ripple.pb.go), so use 0 as
		// the absence proxy — XRPL ledger sequences start at the
		// genesis ledger 1, no real peer broadcasts has_ledgerseq=0.
		var ledgerIndex *uint32
		if sc.LedgerSeq != 0 {
			ls := sc.LedgerSeq
			ledgerIndex = &ls
		}
		// Date is always set thanks to the auto-fill above. Truncate
		// uint64 → uint32 to match the uint32 date rippled emits.
		dateVal := uint32(sc.NetworkTime)
		pub(PeerStatusUpdate{
			Status:         peerStatusUpperName(effectiveStatus),
			Action:         peerStatusActionName(sc.NewEvent),
			LedgerIndex:    ledgerIndex,
			LedgerHash:     ledgerHash,
			Date:           &dateVal,
			LedgerIndexMin: minSeq,
			LedgerIndexMax: maxSeq,
		})
	}
}

// handleSquelchMessage processes an inbound TMSquelch from a peer and
// updates the per-peer validator squelch table.
func (o *Overlay) handleSquelchMessage(evt Event) {
	decoded, err := message.Decode(message.TypeSquelch, evt.Payload)
	if err != nil {
		slog.Debug("Squelch decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
		return
	}
	sq, ok := decoded.(*message.Squelch)
	if !ok {
		return
	}
	// Validator pubkey must be a 33-byte compressed secp256k1 point.
	// Silently dropping would let a peer spam bogus TMSquelch frames
	// without penalty, so charge bad data.
	if len(sq.ValidatorPubKey) != 33 {
		slog.Debug("Squelch malformed pubkey",
			"t", "Overlay", "peer", evt.PeerID, "len", len(sq.ValidatorPubKey))
		o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
		return
	}

	// Drop any inbound squelch whose target pubkey is our own
	// validator — otherwise a peer could silence our own traffic on
	// the RelayFromValidator path (self-silencing DoS). go-xrpl
	// additionally charges the sending peer a bad-data event so
	// repeated attempts feed the eviction threshold; rippled just
	// logs-and-returns there.
	if ownPubKey := o.localValidatorPubKey(); len(ownPubKey) == 33 && bytes.Equal(sq.ValidatorPubKey, ownPubKey) {
		slog.Debug("Squelch dropped: targets local validator",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "squelch-targets-self")
		return
	}

	peer, exists := o.getPeer(evt.PeerID)
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

	switch ping.PType {
	case message.PingTypePing:
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
	case message.PingTypePong:
		if peer, exists := o.getPeer(evt.PeerID); exists {
			peer.OnPong(ping.Seq, time.Now())
		}
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
	// Reconcile discovery's Connected view with the live overlay peer
	// set first. Without this, an event-bus race on disconnect can
	// leave fixed peers marked Connected=true in d.peers even after
	// their TCP connection ended — and SelectPeersToConnect filters
	// them out forever. Observed in iter23/24 soak: a single dropped
	// rippled connection on goxrpl-1 stranded the network sub-quorum
	// because Autoconnect reported `candidates=0 needed=N` indefinitely.
	o.reconcileDiscoveryConnected()

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
		case o.outboundSem <- struct{}{}:
		}
		go func(a string) {
			defer func() { <-o.outboundSem }()
			if err := o.Connect(a); err != nil {
				slog.Info("Peer connection failed", "t", "Overlay", "addr", a, "err", err)
			} else {
				slog.Info("Peer connected", "t", "Overlay", "addr", a)
			}
		}(addr)
	}
}

// maintenanceLoop performs periodic maintenance tasks.
func (o *Overlay) maintenanceLoop(ctx context.Context) error {
	// idleSweepTicker drives the reduce-relay idle-peer sweep (G2).
	// Cadence is Idled/2 (4s) so no relay peer stays referenced more
	// than ~1.5x the idle threshold before being evicted. Without
	// this sweep, r.slots only shrinks on explicit RemovePeer and
	// accumulates stale entries for validators we no longer see.
	idleSweepTicker := time.NewTicker(Idled / 2)
	defer idleSweepTicker.Stop()

	// endpointsTicker drives the periodic TMEndpoints emission. The
	// helper itself decides per-peer whether to actually emit.
	endpointsTicker := time.NewTicker(endpointsBroadcastInterval)
	defer endpointsTicker.Stop()

	// clusterTicker drives the periodic TMCluster gossip.
	// sendClusterUpdate early-returns when cluster is empty, so this
	// is essentially free for non-cluster deployments.
	clusterTicker := time.NewTicker(clusterBroadcastInterval)
	defer clusterTicker.Stop()

	// txQueueTicker drives the periodic TMHaveTransactions emission
	// for tx-reduce-relay. sendTxQueueAnnounce early-returns when
	// EnableTxReduceRelay is off, so this is free for the default
	// configuration.
	txQueueTicker := time.NewTicker(txQueueBroadcastInterval)
	defer txQueueTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-idleSweepTicker.C:
			if o.relay != nil {
				o.relay.deleteIdlePeers(now)
			}
		case <-endpointsTicker.C:
			o.sendEndpoints()
		case <-clusterTicker.C:
			o.sendClusterUpdate()
		case <-txQueueTicker.C:
			o.sendTxQueueAnnounce()
		}
	}
}

// handleSquelch is called by the relay system when a peer should be squelched
// or unsquelched for a given validator. It constructs a TMSquelch message and
// delivers it to the specific peer (unicast).
func (o *Overlay) handleSquelch(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	peer, exists := o.getPeer(peerID)
	if !exists {
		return
	}

	msg := &message.Squelch{
		Squelch:         squelch,
		ValidatorPubKey: validator,
	}
	if squelch {
		// The wire carries the duration as seconds. Only set on
		// squelch=true — on un-squelch the peer ignores this field
		// per the XRPL reduce-relay protocol.
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
	if o.outboundCount() >= o.cfg.MaxOutbound {
		return ErrMaxPeersReached
	}

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, false, o.identity, o.events)
	peer.SetDroppedEventsCounter(&o.droppedEvents)
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

	// Re-check after handshake: another goroutine may have connected
	// to the same host (inbound or outbound) while we were handshaking.
	if o.isConnectedTo(endpoint) {
		peer.Close()
		return ErrAlreadyConnected
	}

	o.addPeer(peer)

	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		err := peer.Run(baseCtx)
		if err != nil {
			slog.Info("Peer run ended", "t", "Overlay", "addr", addr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()

	return nil
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

// Send sends a message to a specific peer.
func (o *Overlay) Send(peerID PeerID, msg []byte) error {
	peer, ok := o.getPeer(peerID)
	if !ok {
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
func (o *Overlay) addPeer(peer *Peer) {
	o.peersMu.Lock()
	o.peers[peer.ID()] = peer
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

	o.dispatchLifecycle(Event{
		Type:     EventPeerConnected,
		PeerID:   peer.ID(),
		Endpoint: peer.Endpoint(),
		Inbound:  peer.Inbound(),
	})
}

// removePeer removes a peer from the overlay and releases its
// resource.Consumer back to the manager. The manager keeps the entry
// in its inactive list for SecondsUntilExpiration so a reconnect
// inherits the prior balance.
func (o *Overlay) removePeer(peerID PeerID) {
	o.peersMu.Lock()
	peer, exists := o.peers[peerID]
	delete(o.peers, peerID)
	o.peersMu.Unlock()

	if exists {
		peer.releaseUsage()
		o.dispatchLifecycle(Event{
			Type:     EventPeerDisconnected,
			PeerID:   peerID,
			Endpoint: peer.Endpoint(),
			Inbound:  peer.Inbound(),
		})
	}
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
