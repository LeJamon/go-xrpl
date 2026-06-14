package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
	"golang.org/x/sync/errgroup"
)

// inboundBacklogSlack caps the accept-side goroutine count to
// MaxInbound + slack so a burst of accepts cannot fan out unbounded;
// canAcceptInbound is the authoritative slot gate.
const inboundBacklogSlack = 8

// acceptBackoff throttles the retry rate when listener.Accept returns
// a non-fatal error (typically EMFILE-class) so the loop does not
// spin at CPU speed under FD pressure.
const acceptBackoff = 100 * time.Millisecond

// serveWorkerCount bounds concurrent heavy serve operations (fetch-pack /
// get-objects / tx back-fill) handled off the event loop, and
// serveQueueDepth bounds the pending backlog before submitServe sheds
// load. Mirrors rippled bounding these behind its job queue rather than
// the read strand.
const (
	serveWorkerCount = 4
	serveQueueDepth  = 64
)

// RelayedIndexTTL bounds how long a suppression-key → peers entry is
// kept in the reverse index. Must match the consensus router's
// messageDedupTTL so that a hash remains queryable for as long as the
// router may observe duplicates for it. If the index expired before
// the dedup window, a duplicate hitting router.handleProposal could
// find no "peers that have the message" entry and under-feed the
// slot — the exact bug B3 was filed to fix.
const RelayedIndexTTL = 30 * time.Second

// RelayedIndexMaxEntries caps memory for the reverse index under
// adversarial traffic. Sized to match the adaptor's dedup cap so both
// age out together under sustained churn.
const RelayedIndexMaxEntries = 4096

// relayedEntry is one bucket in the reverse index — the set of peers
// we know "have" a given suppression-key, plus the last-update time
// for TTL reaping.
type relayedEntry struct {
	peers  map[PeerID]struct{}
	seenAt time.Time
}

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
	// the gossip frame so peers can correlate validator load. Filled
	// at Start from o.identity; nil before Start, in which case the
	// cluster timer leaves the self-entry out.
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

	// droppedLedgerResponses counts the same shape for the ledger-sync
	// response send path (EventLedgerResponse). Separate from
	// droppedMessages so the two traffic classes can be distinguished.
	droppedLedgerResponses atomic.Uint64

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

// chargeInboundHandshake charges the inbound endpoint's resource Consumer
// for a malformed or abusive handshake. During the handshake the peer is
// not yet in o.peers (addPeer runs only after a successful handshake), so
// routing the charge through IncPeerBadData / the peer map would silently
// no-op. We charge the endpoint Consumer directly by address, mirroring
// rippled which charges the inbound endpoint's Resource::Consumer for
// handshake abuse. The Consumer's balance persists in the manager keyed by
// address, so a host spamming malformed handshakes accrues balance across
// attempts and is eventually throttled at admission.
func (o *Overlay) chargeInboundHandshake(addr, reason string) {
	if o.resourceManager == nil {
		return
	}
	c := o.resourceManager.NewInboundEndpoint(addr)
	c.Charge(chargeForReason(reason), reason)
	c.Release()
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

// startListener creates and starts the TCP/TLS listener.
func (o *Overlay) startListener() error {
	tcpListener, err := net.Listen("tcp", o.cfg.ListenAddr)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		tcpListener.Close()
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}

	l := peertls.NewListener(tcpListener, &peertls.Config{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	})
	o.listenerMu.Lock()
	o.listener = l
	o.listenerMu.Unlock()
	return nil
}

// acceptLoop accepts incoming connections. acceptBackoff throttles
// retries under EMFILE-class errors; inboundSem caps the handler
// goroutine fan-out.
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
			// A closed listener is terminal — exit instead of
			// spinning the backoff. Also the !cgo peertls stub path,
			// which closes the inner listener at NewListener.
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(acceptBackoff):
			}
			continue
		}

		select {
		case o.inboundSem <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return ctx.Err()
		}

		o.peerWG.Add(1)
		go func(c net.Conn) {
			defer o.peerWG.Done()
			defer func() { <-o.inboundSem }()
			o.handleInbound(ctx, c)
		}(conn)
	}
}

func (o *Overlay) handleInbound(ctx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in inbound handler", "t", "Overlay", "panic", r)
			conn.Close()
		}
	}()

	// The inbound slot limit is enforced after the handshake (see
	// hasInboundSlot below) because reserved/cluster peers are admitted beyond
	// the cap and their node key is unknown until the handshake completes.
	// Concurrent handshakes stay bounded by inboundSem regardless.
	remoteAddr := conn.RemoteAddr().String()
	endpoint, _ := ParseEndpoint(remoteAddr)

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, true, o.identity, o.events)
	peer.SetDroppedEventsCounter(&o.droppedEvents)
	if err := peer.AcceptConnection(conn); err != nil {
		slog.Warn("Inbound rejected: peer not in disconnected state",
			"t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		return
	}

	tlsConn, ok := conn.(peertls.PeerConn)
	if !ok {
		slog.Error("Inbound connection is not peertls", "t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}

	if err := o.performInboundHandshake(ctx, peer, tlsConn); err != nil {
		slog.Info("Inbound handshake failed", "t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		o.dispatchLifecycle(Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  true,
			Error:    err,
		})
		return
	}

	if o.isConnectedTo(endpoint) {
		conn.Close()
		return
	}

	if !o.hasInboundSlot(peer) {
		slog.Info("Inbound rejected: no slots", "t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}

	peer.setState(PeerStateConnected)
	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	o.addPeer(peer)

	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		err := peer.Run(ctx)
		if err != nil {
			slog.Info("Inbound peer run ended", "t", "Overlay", "remote", remoteAddr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()
}

func (o *Overlay) performInboundHandshake(ctx context.Context, peer *Peer, tlsConn peertls.PeerConn) error {
	// The peer is not in o.peers during the handshake, so bad-data
	// charges route through the endpoint Consumer keyed by this address.
	addr := peer.Endpoint().String()

	// Accept() does not drive the handshake; complete it before reading
	// the Finished bytes for SharedValue.
	handshakeCtx, cancel := context.WithTimeout(ctx, o.cfg.HandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return NewHandshakeError(peer.Endpoint(), "tls", err)
	}

	sharedValue, err := tlsConn.SharedValue()
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "shared_value", err)
	}

	deadline := time.Now().Add(o.cfg.HandshakeTimeout)
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	bufReader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}
	req.Body.Close()

	// Server-Domain runs first in the verify chain.
	if _, err := ValidateServerDomain(req.Header); err != nil {
		o.chargeInboundHandshake(addr, "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", err)
	}

	// Build the handshake config once and share it with both
	// VerifyPeerHandshake and BuildHandshakeResponse so the inbound
	// and outbound paths cannot diverge.
	hsCfg := o.handshakeConfigFor()

	// Full session-signature verification — the whole point of #269.
	peerPubKey, verifyErr := VerifyPeerHandshake(
		req.Header,
		sharedValue,
		o.identity.EncodedPublicKey(),
		hsCfg,
	)
	if verifyErr != nil {
		if !errors.Is(verifyErr, ErrSelfConnection) && !errors.Is(verifyErr, ErrNetworkMismatch) {
			o.chargeInboundHandshake(addr, "handshake-verify")
		}
		return NewHandshakeError(peer.Endpoint(), "verify", verifyErr)
	}
	peer.mu.Lock()
	peer.remotePubKey = peerPubKey
	peer.mu.Unlock()

	peerRemote := tcpRemoteIP(tlsConn)
	extras, extraErr := ParseHandshakeExtras(
		req.Header,
		o.cfg.PublicIP,
		peerRemote,
	)
	if extraErr != nil {
		o.chargeInboundHandshake(addr, "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", extraErr)
	}
	peer.applyHandshakeExtras(extras)

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(req.Header)
	protocol := NegotiateProtocolVersion(req.Header.Get(HeaderUpgrade))
	if protocol == "" {
		o.chargeInboundHandshake(addr, "handshake-protocol-negotiation")
		// Write a 400 Bad Request back so a misconfigured peer sees
		// the rejection reason instead of a TCP RST. Best-effort: a
		// write error here is shadowed by the negotiation failure we
		// are already returning.
		var remoteAddr string
		if peerRemote != nil {
			remoteAddr = peerRemote.String()
		}
		errResp := BuildHandshakeErrorResponse(
			hsCfg.UserAgent,
			remoteAddr,
			"Unable to agree on a protocol version",
		)
		_ = errResp.Write(tlsConn)
		return NewHandshakeError(peer.Endpoint(), "verify",
			fmt.Errorf("%w: unable to agree on a protocol version (peer offered %q)",
				ErrInvalidHandshake, req.Header.Get(HeaderUpgrade)))
	}

	peer.mu.Lock()
	peer.bufReader = bufReader
	peer.capabilities = caps
	peer.protocolVersion = protocol
	peer.mu.Unlock()

	resp := BuildHandshakeResponse(o.identity, sharedValue, hsCfg, protocol)
	addAddressHeaders(resp.Header, hsCfg, peerRemote)
	if err := resp.Write(tlsConn); err != nil {
		return NewHandshakeError(peer.Endpoint(), "send_response", err)
	}

	return nil
}

// handshakeConfigFor builds the per-handshake config used by both
// inbound and outbound paths so they cannot drift.
func (o *Overlay) handshakeConfigFor() HandshakeConfig {
	return HandshakeConfig{
		UserAgent:           o.cfg.UserAgent,
		NetworkID:           o.cfg.NetworkID,
		CrawlPublic:         false,
		EnableLedgerReplay:  o.cfg.EnableLedgerReplay,
		EnableCompression:   o.cfg.EnableCompression,
		EnableVPReduceRelay: o.cfg.EnableVPReduceRelay,
		EnableTxReduceRelay: o.cfg.EnableTxReduceRelay,
		InstanceCookie:      o.instanceCookie,
		ServerDomain:        o.cfg.ServerDomain,
		PublicIP:            o.cfg.PublicIP,
		LedgerHintProvider:  o.ledgerHintProviderSnapshot(),
	}
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
	case EventPeerHandshakeComplete:
		o.onPeerHandshakeComplete(evt)
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

func (o *Overlay) onPeerHandshakeComplete(evt Event) {
	// Mark slot as active in discovery
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
// LedgerSyncHandler.sendReplayDeltaResponse /
// sendProofPathResponse). Same shape as DroppedMessages but for the
// server-side response path. Delegates to the handler's own counter
// so the two drop sites (handler-side events-channel drop and any
// future overlay-side drop tracked in droppedLedgerResponses) can
// both contribute.
func (o *Overlay) DroppedLedgerResponses() uint64 {
	var handler uint64
	if o.ledgerSync != nil {
		handler = o.ledgerSync.DroppedResponses()
	}
	return o.droppedLedgerResponses.Load() + handler
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
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

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
		case <-ticker.C:
			o.performMaintenance()
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

func (o *Overlay) performMaintenance() {
	o.ledgerSync.CleanupExpiredRequests()
	// resource.Manager runs its own periodic activity; charge-driven
	// eviction is handled inline by Peer.Charge.
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
	peer.SetDroppedEventsCounter(&o.droppedEvents)
	peer.handshakeCfg = o.handshakeConfigFor()

	o.dispatchLifecycle(Event{
		Type:     EventPeerConnecting,
		PeerID:   peerID,
		Endpoint: endpoint,
		Inbound:  false,
	})

	ctx, cancel := context.WithTimeout(o.ctx, o.cfg.ConnectTimeout)
	defer cancel()

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}
	cfg := PeerConfig{
		SendBufferSize: DefaultSendBufferSize,
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
		err := peer.Run(o.ctx)
		if err != nil {
			slog.Info("Peer run ended", "t", "Overlay", "addr", addr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()

	return nil
}

// Broadcast sends a message to all connected peers, unfiltered. Used
// for SELF-originated validator traffic (our own proposals and
// validations) and for non-validator messages (statusChange, etc.).
// The squelch filter is deliberately skipped for self-originated
// broadcasts; otherwise a peer that squelches our own pubkey would
// silence us to them.
//
// For peer-originated validator messages that need to be gossip-
// forwarded, use RelayFromValidator which applies the squelch filter
// and excludes the originating peer.
func (o *Overlay) Broadcast(msg []byte) error {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if err := peer.Send(msg); err != nil {
			// Buffer-full at Warn — silent drops masked TMTransaction
			// relay loss in #401; other failures Info.
			level := slog.LevelInfo
			if errors.Is(err, ErrSendBufferFull) {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "broadcast send failed",
				"t", "Overlay",
				"peer", id,
				"frame_size", len(msg),
				"err", err.Error(),
			)
		}
	}
	return nil
}

// BroadcastExcept sends a message to every connected peer except the
// one identified by exceptPeer. Used for gossip of peer-originated
// messages that are NOT per-validator (manifests) — the per-validator
// squelch filter in RelayFromValidator doesn't apply. Pass 0 for
// exceptPeer to fall through to a plain Broadcast.
func (o *Overlay) BroadcastExcept(exceptPeer PeerID, msg []byte) error {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	for id, peer := range o.peers {
		if id == exceptPeer {
			continue
		}
		if peer.State() != PeerStateConnected {
			continue
		}
		if err := peer.Send(msg); err != nil {
			level := slog.LevelInfo
			if errors.Is(err, ErrSendBufferFull) {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "broadcast-except send failed",
				"t", "Overlay",
				"peer", id,
				"frame_size", len(msg),
				"err", err.Error(),
			)
		}
	}
	return nil
}

// BroadcastExceptSet sends a message to every connected peer whose
// ID is not present in excluded. Used by tx-set acquire to skip peers
// that have repeatedly returned non-progressing TMLedgerData responses.
// This is a go-xrpl-specific outbound filter; rippled does NOT remove
// such peers from its peer set — it charges them and lets the global
// resource manager throttle them, so the peer stays eligible for the
// next broadcast. go-xrpl has no equivalent per-message resource accounting
// today, hence the explicit per-acquire exclusion. A nil or empty
// excluded map falls through to a plain Broadcast. Issue #420.
//
// Issue #724: the exclusion must never starve the broadcast. If every
// connected peer is excluded, the message would reach no one and the
// caller (tx-set missing-node acquisition) wedges in wrongLedger until
// the TTL sweep — the recurring under-load validation stall. When that
// happens, fall back to broadcasting to all connected peers, restoring
// rippled's "peer stays eligible for the next request" semantics rather
// than dropping the request on the floor.
func (o *Overlay) BroadcastExceptSet(excluded map[PeerID]bool, msg []byte) error {
	if len(excluded) == 0 {
		return o.Broadcast(msg)
	}
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

	connected, eligible := 0, 0
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		connected++
		if !excluded[id] {
			eligible++
		}
	}
	ignoreExclusion := eligible == 0 && connected > 0

	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if !ignoreExclusion && excluded[id] {
			continue
		}
		if err := peer.Send(msg); err != nil {
			level := slog.LevelInfo
			if errors.Is(err, ErrSendBufferFull) {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "broadcast-except-set send failed",
				"t", "Overlay",
				"peer", id,
				"frame_size", len(msg),
				"err", err.Error(),
			)
		}
	}
	return nil
}

// RelayFromValidator forwards a peer-originated validator message
// (proposal or validation) to other connected peers, applying the
// per-peer squelch filter on the ORIGINATING validator's pubkey AND
// excluding the originating peer (exceptPeer). Pass 0 for exceptPeer
// when no peer should be excluded (e.g. tests that synthesize a relay).
//
// suppressionHash is the consensus-router suppression key for this
// message (same [32]byte used by the dedup cache). Every peer we
// actually send to is recorded in the reverse index so a later
// duplicate arrival from ANOTHER peer can query
// Overlay.PeersThatHave(suppressionHash) and feed the reduce-relay
// slot with the full set of known-havers.
//
// The squelch is consulted before each outbound send and expired
// squelches auto-clear via Peer.ExpireSquelch. Self-origin is handled
// by a separate code path (see Broadcast) that skips the filter
// entirely.
func (o *Overlay) RelayFromValidator(validator []byte, suppressionHash [32]byte, exceptPeer PeerID, msg []byte) error {
	// Collect the set of peers we actually forwarded to, under the
	// peer-map RLock. Record into the reverse index AFTER releasing
	// that lock so we never nest index-mutex inside peers-mutex.
	var forwarded []PeerID

	o.peersMu.RLock()
	for id, peer := range o.peers {
		if id == exceptPeer {
			continue
		}
		if peer.State() != PeerStateConnected {
			continue
		}
		if !peer.ExpireSquelch(validator) {
			continue
		}
		if err := peer.Send(msg); err != nil {
			level := slog.LevelInfo
			if errors.Is(err, ErrSendBufferFull) {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "relay-from-validator send failed",
				"t", "Overlay",
				"peer", id,
				"frame_size", len(msg),
				"err", err.Error(),
			)
			// Still record in reverse index — best-effort.
		}
		forwarded = append(forwarded, id)
	}
	o.peersMu.RUnlock()

	if len(forwarded) > 0 {
		o.recordRelayedPeers(suppressionHash, forwarded)
	}
	return nil
}

// recordRelayedPeers adds peerIDs to the reverse-index bucket for
// suppressionHash, trimming expired buckets if we hit the size cap.
// Safe for concurrent callers.
func (o *Overlay) recordRelayedPeers(suppressionHash [32]byte, peerIDs []PeerID) {
	if o.relayedIndex == nil {
		return
	}
	clock := o.clockForIndex
	if clock == nil {
		clock = time.Now
	}
	now := clock()

	o.relayedIndexMu.Lock()
	defer o.relayedIndexMu.Unlock()

	// Trim if we're at capacity. A cheap TTL sweep rather than a
	// formal LRU — the index is a cache, not a hot path.
	if len(o.relayedIndex) >= RelayedIndexMaxEntries {
		cutoff := now.Add(-RelayedIndexTTL)
		for h, e := range o.relayedIndex {
			if e.seenAt.Before(cutoff) {
				delete(o.relayedIndex, h)
			}
		}
		// If that didn't free enough space (adversarial churn), drop
		// half the map — bounded worst case, same shape as the
		// messageSuppression eviction in the consensus router.
		if len(o.relayedIndex) >= RelayedIndexMaxEntries {
			i := 0
			for h := range o.relayedIndex {
				if i >= RelayedIndexMaxEntries/2 {
					break
				}
				delete(o.relayedIndex, h)
				i++
			}
		}
	}

	entry, ok := o.relayedIndex[suppressionHash]
	if !ok {
		entry = &relayedEntry{peers: make(map[PeerID]struct{})}
		o.relayedIndex[suppressionHash] = entry
	}
	for _, id := range peerIDs {
		entry.peers[id] = struct{}{}
	}
	entry.seenAt = now
}

// PeersThatHave returns the set of peer IDs known to have the message
// whose suppression-hash is `suppressionHash`. Entries are populated
// when we relay a validator message outward (RelayFromValidator) and
// expire after RelayedIndexTTL.
//
// Returns nil when the hash is unknown or the bucket has aged out —
// callers treat both equivalently (nothing to feed the slot with
// beyond the current originPeer).
//
// Thread-safe. The returned slice is a private copy the caller may
// mutate freely.
func (o *Overlay) PeersThatHave(suppressionHash [32]byte) []PeerID {
	if o.relayedIndex == nil {
		return nil
	}
	clock := o.clockForIndex
	if clock == nil {
		clock = time.Now
	}

	o.relayedIndexMu.Lock()
	defer o.relayedIndexMu.Unlock()

	entry, ok := o.relayedIndex[suppressionHash]
	if !ok {
		return nil
	}
	// Lazy-expire: if the bucket is older than TTL, drop it and report
	// "unknown". Keeps queries from returning stale peers after the
	// dedup window has elapsed (which would feed the slot with
	// counters the rest of the network would have dropped long ago).
	if clock().Sub(entry.seenAt) >= RelayedIndexTTL {
		delete(o.relayedIndex, suppressionHash)
		return nil
	}

	out := make([]PeerID, 0, len(entry.peers))
	for id := range entry.peers {
		out = append(out, id)
	}
	return out
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

// PeersJSON implements types.PeerSource for the `peers` RPC method,
// emitting the subset of rippled's per-peer RPC fields for which
// go-xrpl has data.
func (o *Overlay) PeersJSON() []map[string]any {
	list := o.Peers()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		entry := map[string]any{
			"address":    p.Endpoint.String(),
			"public_key": p.PublicKey,
			"uptime":     int64(time.Since(p.ConnectedAt).Seconds()),
			"load":       p.Load,
		}
		if p.Inbound {
			entry["inbound"] = true
		}
		if p.ServerDomain != "" {
			entry["server_domain"] = p.ServerDomain
		}
		// Emit only when the peer set a Network-ID.
		if p.NetworkID != "" {
			entry["network_id"] = p.NetworkID
		}
		if p.ClosedLedger != "" {
			entry["ledger"] = p.ClosedLedger
		}
		if p.CompleteLedgers != "" {
			entry["complete_ledgers"] = p.CompleteLedgers
		}
		if len(p.PublicKeyBytes) > 0 {
			if member, ok := o.cluster.Member(p.PublicKeyBytes); ok {
				entry["cluster"] = true
				if member.Name != "" {
					entry["name"] = member.Name
				}
			}
		}
		// Omit when converged.
		switch p.Tracking {
		case PeerTrackingDiverged:
			entry["track"] = "diverged"
		case PeerTrackingUnknown:
			entry["track"] = "unknown"
		}
		if p.HasLatency {
			entry["latency"] = uint32(p.Latency / time.Millisecond)
		}
		// Version sourced from User-Agent (inbound) or Server
		// (outbound) header.
		if p.Version != "" {
			entry["version"] = p.Version
		}
		// Emit unconditionally — a negotiated value always exists once
		// the handshake has completed.
		entry["protocol"] = p.Protocol
		// Emit only when the peer has reported a status.
		if s, known := nodeStatusRPCName(p.Status); s != "" {
			entry["status"] = s
		} else if !known && p.Status != 0 {
			// Log a warning when the status falls outside the known
			// enum, then drop the field so out-of-range values aren't
			// silent.
			slog.Warn("Unknown peer status",
				"t", "Overlay", "peer", p.ID, "status", int32(p.Status))
		}
		// Emit the metrics object; values are decimal strings to match
		// rippled's formatting.
		entry["metrics"] = map[string]any{
			"total_bytes_recv": strconv.FormatUint(p.TotalBytesRecv, 10),
			"total_bytes_sent": strconv.FormatUint(p.TotalBytesSent, 10),
			"avg_bps_recv":     strconv.FormatUint(p.AvgBpsRecv, 10),
			"avg_bps_sent":     strconv.FormatUint(p.AvgBpsSent, 10),
			"send_drops":       strconv.FormatUint(p.SendDrops, 10),
		}
		out = append(out, entry)
	}
	return out
}

// nodeStatusRPCName returns the rippled spelling for each known
// NodeStatus and a `known` flag distinguishing "no status reported"
// (nsUNKNOWN, known=true) from "unrecognized enum value" (known=false).
// The caller omits the `status` field for either case but logs only
// the unknown-enum case.
func nodeStatusRPCName(s message.NodeStatus) (string, bool) {
	switch s {
	case 0:
		return "", true
	case message.NodeStatusConnecting:
		return "connecting", true
	case message.NodeStatusConnected:
		return "connected", true
	case message.NodeStatusMonitoring:
		return "monitoring", true
	case message.NodeStatusValidating:
		return "validating", true
	case message.NodeStatusShutting:
		return "shutting", true
	default:
		return "", false
	}
}

// clusterFeeRef is the load-fee reference baseline. Replace with a
// live reference once go-xrpl grows a load-fee tracker.
const clusterFeeRef uint32 = 256

// ClusterJSON returns the top-level cluster object for the `peers`
// RPC response.
func (o *Overlay) ClusterJSON() map[string]any {
	out := map[string]any{}
	if o == nil || o.cluster == nil {
		return out
	}

	var selfKey []byte
	if o.identity != nil {
		selfKey = o.identity.PublicKey()
	}

	now := o.cfg.Clock()

	o.cluster.ForEach(func(m cluster.Member) {
		if len(selfKey) > 0 && bytes.Equal(selfKey, m.Identity) {
			return
		}
		encoded, err := addresscodec.EncodeNodePublicKey(m.Identity)
		if err != nil || encoded == "" {
			return
		}
		entry := map[string]any{}
		if m.Name != "" {
			entry["tag"] = m.Name
		}
		if m.LoadFee != clusterFeeRef && m.LoadFee != 0 {
			entry["fee"] = float64(m.LoadFee) / float64(clusterFeeRef)
		}
		if !m.ReportTime.IsZero() {
			age := max(int64(now.Sub(m.ReportTime).Seconds()), 0)
			entry["age"] = age
		}
		out[encoded] = entry
	})
	return out
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
