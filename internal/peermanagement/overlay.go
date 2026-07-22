package peermanagement

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
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
	peers          map[PeerID]*Peer
	peerKeys       map[string]PeerID
	peerEndpoints  map[string]PeerID
	inboundIPs     map[string]int
	pendingInbound map[PeerID]struct{}
	peersMu        sync.RWMutex
	nextID         atomic.Uint64

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
	// events is the best-effort hot path for ordinary EventMessageReceived
	// traffic and EventLedgerResponse. Acquisition traffic uses the separate
	// bounded acquisitionEvents path so requested replies apply transport
	// backpressure instead of being discarded.
	//
	// lifecycle is the dedicated NON-LOSSY path for peer lifecycle events
	// (Connecting/Connected/Disconnected/Failed). Sends BLOCK until the
	// event loop accepts them (see dispatchLifecycle): lifecycle volume is
	// tiny and bounded by peer count, and a dropped EventPeerDisconnected
	// would leak router/relay per-peer state until the idle sweep. Keeping
	// it off the message channel means a message burst can no longer crowd
	// out a disconnect. Both are drained by the single eventLoop goroutine.
	events            chan Event
	acquisitionEvents chan Event
	lifecycle         chan Event

	// messages carries ordinary consensus peer frames (proposals,
	// validations, status changes, and validator lists) to the
	// router. txMessages carries inbound TMTransaction frames on a
	// separate lane. Splitting the two means a transaction flood that
	// saturates txMessages can no longer crowd consensus/acquisition
	// traffic out of a single shared buffer and get the node
	// resource-disconnected for dropping mtLEDGER_DATA/mtPROPOSE/
	// mtVALIDATION (issue #1103).
	messages   chan *InboundMessage
	txMessages chan *InboundMessage
	// manifestMessages is fed directly by peer read loops with bounded
	// backpressure. Signature verification is intentionally isolated from both
	// the overlay event loop and the consensus router.
	manifestMessages chan *InboundMessage

	// ledgerData carries acquisition replies (mtLEDGER_DATA, by-hash objects,
	// and replay/proof-path responses) on a bounded backpressure path. This
	// prevents unrelated traffic from discarding replies required for catch-up.
	ledgerData chan *InboundMessage

	// serveJobs carries heavy inbound serve work (fetch-pack, generic
	// get-objects, tx back-fill) off the event-loop goroutine onto a
	// bounded worker pool, so a single expensive serve can't stall ping
	// replies and lifecycle handling. Created in Run; nil when the overlay
	// was built without Run (submitServe then runs the job inline, which
	// preserves the synchronous behaviour unit tests rely on). Mirrors
	// rippled offloading these to its job queue (jtPACK et al.) rather than
	// running them on the peer read strand.
	serveJobs chan func()

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
	// the consensus messages channel hit its default branch (downstream
	// consumer slow). Exposed via DroppedMessages() so server_info /
	// telemetry can surface back-pressure to operators. Without this
	// counter a slow consumer silently loses events with only a
	// debug-level log.
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
	// listenerMu guards listener: written once by startListener (called
	// from Run before any concurrent reader exists). Read under RLock
	// from ListenAddr and Stop (other goroutines). The reads in Run and
	// acceptLoop are unlocked: Run's read at "if o.listener != nil" is
	// in the same goroutine as the write, and acceptLoop is spawned via
	// g.Go after the write returns, so happens-before applies.
	listenerMu sync.RWMutex
	listener   net.Listener

	// listenerReady is closed once Run has finished its listener-bind
	// phase — after startListener publishes o.listener, or immediately
	// when no listener is configured.
	listenerReady     chan struct{}
	listenerReadyOnce sync.Once

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
	stopCh  chan struct{}
	runDone <-chan struct{}
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
func (o *Overlay) NotePeerHasTxSet(peerID PeerID, hash [32]byte) {
	if peer, ok := o.getPeer(peerID); ok {
		peer.AddTxSet(hash)
	}
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
		cfg:               cfg,
		identity:          identity,
		cluster:           clusterReg,
		instanceCookie:    cookie,
		discovery:         NewDiscovery(&cfg, events),
		autoconnectWake:   make(chan struct{}, 1),
		ledgerSync:        NewLedgerSyncHandler(events),
		peers:             make(map[PeerID]*Peer),
		peerKeys:          make(map[string]PeerID),
		peerEndpoints:     make(map[string]PeerID),
		inboundIPs:        make(map[string]int),
		pendingInbound:    make(map[PeerID]struct{}),
		events:            events,
		acquisitionEvents: make(chan Event, acquisitionEventBufferSize),
		messages:          make(chan *InboundMessage, messageBufferSize(cfg.MessageBufferSize)),
		txMessages:        make(chan *InboundMessage, txLaneBufferSize(cfg.MaxTransactions)),
		manifestMessages:  make(chan *InboundMessage, manifestMessageBufferSize),
		ledgerData:        make(chan *InboundMessage, DefaultLedgerDataBufferSize),
		lifecycle:         make(chan Event, lifecycleBufferSize(&cfg)),
		stopCh:            make(chan struct{}),
		listenerReady:     make(chan struct{}),
		relayedIndex:      make(map[[32]byte]*relayedEntry),
		clockForIndex:     time.Now,
		inboundSem:        make(chan struct{}, inboundCap),
		outboundSem:       make(chan struct{}, outboundCap),
		resourceManager:   resource.NewManager(nil, nil),
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
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, ErrInvalidPrivateKey) {
		return nil, err
	}

	// Generate new identity
	id, err = GenerateIdentity()
	if err != nil {
		return nil, err
	}

	if err := id.Save(dataDir); err != nil {
		return nil, fmt.Errorf("save identity: %w", err)
	}

	return id, nil
}

// Run starts the overlay and blocks until the context is cancelled.
func (o *Overlay) Run(ctx context.Context) error {
	o.lifecycleMu.Lock()
	o.ctx, o.cancel = context.WithCancel(ctx) //nolint:gosec // G118: cancel stored in struct field and deferred, called on shutdown
	cancel := o.cancel
	o.lifecycleMu.Unlock()
	defer cancel()

	// Start listener if configured
	if o.cfg.ListenAddr != "" {
		if err := o.startListener(); err != nil {
			return fmt.Errorf("listener error: %w", err)
		}
	}
	o.signalListenerReady()

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
	o.runDone = gCtx.Done()

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

	// Event processing loops
	g.Go(func() error { return o.eventLoop(gCtx) })
	g.Go(func() error { return o.acquisitionEventLoop(gCtx) })

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
		o.peerStartMu.Lock()
		o.peerStartsClosed = true
		o.peerStartMu.Unlock()

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

		o.peerStartWG.Wait()
		o.peerWG.Wait()

		if o.resourceManager != nil {
			o.resourceManager.Stop()
		}
	})

	return nil
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
