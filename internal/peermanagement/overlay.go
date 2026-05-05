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

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/cluster"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/peertls"
	"github.com/LeJamon/goXRPLd/protocol"
	"golang.org/x/sync/errgroup"
)

// EvictBadDataThreshold is the bad-data BALANCE at which the overlay
// disconnects a peer. IncBadData adds a per-reason weight (see
// BadDataWeight) and a background decay halves the balance every
// badDataDecayInterval — together this approximates rippled's
// Resource::Consumer model: persistent offenders accumulate faster
// than decay and evict; chatty-but-honest peers decay below threshold.
// 25000 matches rippled's dropThreshold constant in
// src/xrpld/overlay/Resource/impl/Tuning.h:30-40 (NOT its much lower
// minGossipBalance — those are distinct gates). Calibration: a peer
// sending one genuinely-corrupt message per decay window
// (weightInvalidData=400) asymptotically approaches 800 (below the
// 25000 threshold), so sporadic offenders survive; sustained
// multi-hundred-per-window abuse crosses the threshold within seconds.
const EvictBadDataThreshold = 25000

// badDataDecayInterval is the cadence at which Peer.DecayBadData
// halves the balance. Go's step-halving every 10s decays about twice
// as fast as rippled's continuous ~32s exponential window; this is an
// intentional difference — goXRPL recovers faster from transient bad
// behavior while still evicting sustained abuse within a few
// intervals. Paired with the 25000 threshold, a rapid burst of
// malformed data (say, 70 × 400 = 28000 within one interval) evicts
// immediately; a steady stream of one such message per interval
// asymptotes at 800 and never crosses the threshold.
const badDataDecayInterval = 10 * time.Second

// Bad-data weights mirror rippled's Resource::Fees.cpp:26-30. They
// scale the per-reason severity of an IncBadData call:
//   - feeInvalidSignature (2000) — bad signature / wrong pubkey format
//   - feeInvalidData      (400)  — genuinely corrupt data
//   - feeMalformedRequest (200)  — syntactically bad request / bad hash
//   - feeRequestNoReply   (10)   — peer didn't answer a request
//
// Keeping the numbers at rippled's values means an operator familiar
// with rippled's tuning can port reasoning across implementations.
// Signature offenses are the heaviest — a peer forging or mangling
// signatures is either broken or hostile and a small number of such
// events should approach the eviction threshold.
const (
	weightInvalidSignature = 2000
	weightInvalidData      = 400
	weightMalformedReq     = 200
	weightRequestNoReply   = 10
	weightDefaultBadData   = 100 // fallback for unrecognized reasons
)

// BadDataWeight returns the weight to charge IncBadData for `reason`.
// Maps reason labels to rippled's fee tiers. Unrecognized reasons fall
// back to weightDefaultBadData so a new bad-data source doesn't
// accidentally ship with zero weight.
//
// Classification rationale per rippled PeerImp.cpp:
//   - sig-size / pubkey-size → feeInvalidSignature (PeerImp.cpp:1683-1686)
//   - ledger-hash / txset / prev-ledger-size / node-id-zero → feeMalformedRequest
//     (PeerImp.cpp:1693: "bad hashes")
//   - verify / decode / wire-corruption → feeInvalidData
//   - no-reply → feeRequestNoReply
func BadDataWeight(reason string) int {
	switch reason {
	// Bad signatures and malformed pubkeys — heaviest charge.
	// Rippled: feeInvalidSignature at PeerImp.cpp:1683-1686 for
	// ProposeSet, equivalent path for Validation.
	case "proposal-malformed-sig-size",
		"proposal-malformed-pubkey-size",
		"validation-malformed-sig-size":
		return weightInvalidSignature
	// Genuine data corruption / protocol violation — next-heaviest.
	case "replay-delta-verify",
		"ledger-data-base",
		"ledger-data-state",
		"squelch-duration",
		"squelch-map-full",
		"squelch-malformed-pubkey":
		return weightInvalidData
	// Malformed requests: decode failures, bad hashes, wrong-field
	// requests. Rippled: feeMalformedRequest at PeerImp.cpp:1693 for
	// "bad hashes" and at PeerImp.cpp:1476 for bad requests.
	//
	// "squelch-ignored" lives here (G4): a peer that keeps relaying a
	// validator's messages after being squelched is mis-behaving at
	// the protocol level, but a single event is not catastrophic —
	// sustained ignore (many per decay window) accumulates toward
	// eviction while a one-off (packet already in flight when the
	// squelch landed) decays below threshold. Rippled's upstream only
	// increments a traffic counter here (OverlayImpl.cpp:1435-1437);
	// goXRPL additionally charges reputation so repeat offenders are
	// actually evicted rather than just counted.
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
		return weightMalformedReq
	// A peer that didn't respond or returned benign "no data" — lowest.
	case "no-reply":
		return weightRequestNoReply
	default:
		return weightDefaultBadData
	}
}

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
	// without nil checks. Mirrors rippled's app_.cluster() which is
	// always present even when the section is empty.
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

	// relayedIndex maps suppression-hash → set of peers known to have
	// that message. Populated as we forward a validator message (each
	// recipient joins the set) and queried by the consensus router on
	// duplicate arrivals so ALL known-havers feed the reduce-relay
	// slot — not just the peer that delivered the current duplicate.
	// Matches rippled's overlay().relay returning the haveMessage set
	// that is then passed to updateSlotAndSquelch
	// (PeerImp.cpp:3010-3017 for proposals, 3044-3054 for validations).
	relayedIndex   map[[32]byte]*relayedEntry
	relayedIndexMu sync.Mutex
	clockForIndex  func() time.Time

	// Coordination channels
	events   chan Event
	messages chan *InboundMessage

	// Peer lifecycle callbacks wired by higher layers (e.g., consensus
	// router) that need to clean up per-peer state on disconnect. Fired
	// from the event-loop goroutine AFTER the peer has been removed from
	// the map, so callees can assume the peer is already gone. nil when
	// no subscriber is registered.
	onPeerDisconnect func(PeerID)

	// onPeerConnect fires once a peer has finished its handshake and
	// been added to the overlay's peer map. Higher layers use this to
	// trigger post-connect emissions like the local manifest broadcast
	// (#372 / rippled OverlayImpl::sendEndpoints which emits manifests
	// alongside endpoints in the post-handshake window). Same blocking
	// contract as onPeerDisconnect: runs on the event loop, must not
	// block.
	onPeerConnect func(PeerID)

	// droppedMessages counts how many times the non-blocking send to
	// the messages channel hit its default branch (downstream consumer
	// slow). Exposed via DroppedMessages() so server_info / telemetry
	// can surface back-pressure to operators. Without this counter a
	// slow consumer silently loses events with only a debug-level log.
	droppedMessages atomic.Uint64

	// droppedLedgerResponses counts the same shape for the ledger-sync
	// response send path (EventLedgerResponse). Separate from
	// droppedMessages so the two traffic classes can be distinguished.
	droppedLedgerResponses atomic.Uint64

	// pingTimeoutDisconnects counts peers torn down because the oldest
	// in-flight ping aged past pingTimeout. Mirrors rippled's
	// fail("Ping Timeout") at PeerImp.cpp:731-736.
	pingTimeoutDisconnects atomic.Uint64

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

// PeersWithClosedLedger returns peers whose last-known Closed-Ledger
// hash equals target. The hash is seeded from the handshake hint and
// refreshed by inbound mtSTATUS_CHANGE messages, mirroring the
// PeerImp::closedLedgerHash_ field in rippled. This is a primitive for
// callers that want a coarse "who advertised this LCL" filter; it is
// NOT an analogue of rippled's catchup peer selection, which goes
// through PeerImp::hasLedger(hash, seq) over [minLedger_, maxLedger_]
// and the recentLedgers_ ring — state goXRPL does not yet track per
// peer.
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
// handleStatusChange (PeerImp.cpp:1885-1890). ok=false suppresses
// tracking updates.
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
// fields preserve protobuf has-presence; nil means "wire field absent,
// rippled's `if (m->has_xxx)` gate is false" and the RPC layer omits the
// JSON field. Mirrors PeerImp.cpp:1892-1963.
type PeerStatusUpdate struct {
	// Status mirrors PeerImp.cpp:1895-1915. UPPERCASE spelling.
	// Carries the post-inheritance value returned by applyStatusChange,
	// so a status-less wire message still emits the prior enum once
	// (rippled's `m->set_newstatus(status)` mutation at PeerImp.cpp:1808).
	Status string
	// Action mirrors PeerImp.cpp:1917-1934 — CLOSING_LEDGER,
	// ACCEPTED_LEDGER, SWITCHED_LEDGER. LOST_SYNC is unreachable
	// because handleStatusChange returns at PeerImp.cpp:1830 before
	// the publish.
	Action string
	// LedgerHash mirrors PeerImp.cpp:1941-1949: rippled re-reads the
	// peer's closedLedgerHash_ under recentLock_ rather than echoing
	// the raw wire bytes. When wire bytes were malformed, that stored
	// hash is zeroed at PeerImp.cpp:1850 and rippled emits the
	// 64-char zero hex string — so callers must ALWAYS emit a value
	// when has_ledgerhash, falling back to "00…00" if the peer's
	// post-apply state was cleared.
	LedgerHash string
	// LedgerIndex mirrors PeerImp.cpp:1936-1939 (`has_ledgerseq`).
	// nil = field absent; non-nil = emit (even when value is 0 — a
	// peer can legitimately advertise the genesis seq).
	LedgerIndex *uint32
	// Date mirrors PeerImp.cpp:1951-1954 (`has_networktime`). rippled
	// auto-stamps this at PeerImp.cpp:1796-1797 when the wire didn't
	// carry it, so handleStatusChange does the same and Date is
	// always non-nil here.
	Date *uint32
	// LedgerIndexMin / LedgerIndexMax mirror PeerImp.cpp:1956-1960
	// (`has_firstseq && has_lastseq`). Both are nil unless both wire
	// fields were present.
	LedgerIndexMin *uint32
	LedgerIndexMax *uint32
}

// SetPeerStatusPublisher wires a sink for pubPeerStatus events.
// Mirrors rippled's NetworkOPs::pubPeerStatus (NetworkOPs.cpp:2514) —
// the overlay invokes this callback for every non-lostSync TMStatusChange
// after state has been recorded, mirroring PeerImp.cpp:1892-1963.
// Passing nil disconnects the sink.
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

// peerStatusUpperName mirrors PeerImp.cpp:1899-1913: the pubPeerStatus
// callback emits status names in UPPERCASE (CONNECTING/...), distinct
// from the lowercase strings used by the `peers` RPC at
// PeerImp.cpp:467-485. Returns "" for nsUNKNOWN or any unknown enum
// (rippled's switch falls through silently for the latter).
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

// peerStatusActionName mirrors PeerImp.cpp:1921-1932. handleStatusChange
// returns at PeerImp.cpp:1830 before pubPeerStatus runs for neLOST_SYNC,
// so the LOST_SYNC arm is unreachable from this call site and intentionally
// omitted. Unknown enums fall through silently.
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

// generateInstanceCookie matches rippled Application.cpp:
//
//	1 + rand_int(crypto_prng(), MAX_UINT64 - 1)
//
// where rand_int returns a closed interval, so the result is uniform
// in [1, MAX_UINT64]. Only 0 is rejected.
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
	o.peersMu.RLock()
	peer, ok := o.peers[peerID]
	o.peersMu.RUnlock()
	if !ok {
		return 0
	}
	return peer.IncBadData(reason)
}

// peerNegotiatedLedgerReplay reports whether the peer identified by
// peerID advertised the ledger-replay feature during handshake. Used
// to gate serving mtREPLAY_DELTA_REQ and mtPROOF_PATH_REQ: rippled
// drops these from peers that didn't negotiate the feature
// (PeerImp.cpp:1473-1478) because they indicate protocol-violation.
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
	o.peersMu.RLock()
	peer, ok := o.peers[peerID]
	o.peersMu.RUnlock()
	if !ok {
		return false
	}
	caps := peer.Capabilities()
	if caps == nil {
		return false
	}
	return caps.HasFeature(f)
}

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

	cookie, err := generateInstanceCookie()
	if err != nil {
		return nil, fmt.Errorf("instance cookie: %w", err)
	}

	clusterReg := cluster.New()
	if err := clusterReg.Load(cfg.ClusterNodes); err != nil {
		return nil, fmt.Errorf("invalid cluster_nodes: %w", err)
	}

	events := make(chan Event, 256)

	o := &Overlay{
		cfg:            cfg,
		identity:       identity,
		cluster:        clusterReg,
		instanceCookie: cookie,
		discovery:      NewDiscovery(&cfg, events),
		ledgerSync:     NewLedgerSyncHandler(events),
		peers:          make(map[PeerID]*Peer),
		events:         events,
		messages:       make(chan *InboundMessage, 256),
		relayedIndex:   make(map[[32]byte]*relayedEntry),
		clockForIndex:  time.Now,
	}

	// Wire reduce-relay callbacks. The squelch callback constructs and
	// dispatches TMSquelch frames to individual peers; the ignored-
	// squelch callback charges a peer's bad-data balance whenever it
	// keeps relaying a validator's messages after being squelched.
	// Both are set at construction — Relay never swaps them at runtime.
	// Mirrors rippled's OverlayImpl wiring where SquelchHandler +
	// ignored_squelch_callback are passed into Slot at construction
	// (Slot.h:121-132 + Slot.h:112-113).
	o.relay = NewRelayWithIgnoredCallback(&cfg, o.handleSquelch, o.chargeIgnoredSquelch)

	o.ledgerSync.SetPeerLedgerHintLookup(o.PeersWithClosedLedger)

	return o, nil
}

// chargeIgnoredSquelch is the Relay-layer callback fired when a peer
// keeps relaying a validator's messages despite being squelched. We
// charge the peer's bad-data balance under a stable reason label so
// operators watching bad-data metrics can attribute the increase to
// squelch-ignored behavior specifically. Per rippled Slot.h:329-331,
// this is the only place we learn that a peer ignored our TMSquelch
// — there is no separate protocol signal.
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

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		tcpListener.Close()
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}

	o.listener = peertls.NewListener(tcpListener, &peertls.Config{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	})
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

func (o *Overlay) handleInbound(ctx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in inbound handler", "t", "Overlay", "panic", r)
			conn.Close()
		}
	}()

	if !o.canAcceptInbound() {
		slog.Info("Inbound rejected: no slots", "t", "Overlay", "remote", conn.RemoteAddr())
		conn.Close()
		return
	}

	remoteAddr := conn.RemoteAddr().String()
	endpoint, _ := ParseEndpoint(remoteAddr)

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, true, o.identity, o.events)
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
		o.events <- Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  true,
			Error:    err,
		}
		return
	}

	if o.isConnectedTo(endpoint) {
		conn.Close()
		return
	}

	peer.setState(PeerStateConnected)
	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	o.addPeer(peer)

	go func() {
		err := peer.Run(ctx)
		if err != nil {
			slog.Info("Inbound peer run ended", "t", "Overlay", "remote", remoteAddr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()
}

func (o *Overlay) performInboundHandshake(ctx context.Context, peer *Peer, tlsConn peertls.PeerConn) error {
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

	// Server-Domain runs first (rippled Handshake.cpp:235-239 — the
	// first throw in verifyHandshake).
	if _, err := ValidateServerDomain(req.Header); err != nil {
		o.IncPeerBadData(peer.ID(), "handshake-malformed-extras")
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
			o.IncPeerBadData(peer.ID(), "handshake-verify")
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
		o.IncPeerBadData(peer.ID(), "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", extraErr)
	}
	peer.applyHandshakeExtras(extras)

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(req.Header)
	protocol := NegotiateProtocolVersion(req.Header.Get(HeaderUpgrade))
	if protocol == "" {
		o.IncPeerBadData(peer.ID(), "handshake-protocol-negotiation")
		// Mirror rippled OverlayImpl.cpp:227 — write a 400 Bad Request
		// back so a misconfigured peer sees the rejection reason
		// instead of a TCP RST. Best-effort: a write error here is
		// shadowed by the negotiation failure we are already returning.
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
	// Notify higher layers AFTER discovery state is updated so any work
	// they do (e.g. sending us-originated frames to the peer) sees a
	// fully-bookkept overlay. Mirrors the disconnect callback ordering.
	if cb := o.onPeerConnect; cb != nil {
		cb(evt.PeerID)
	}
}

func (o *Overlay) onPeerHandshakeComplete(evt Event) {
	// Mark slot as active in discovery
}

func (o *Overlay) onPeerDisconnected(evt Event) {
	o.discovery.MarkDisconnected(evt.PeerID)
	o.relay.RemovePeer(evt.PeerID)
	// Fire the higher-layer disconnect callback so per-peer state in
	// consumers (router peerStates, adaptor peerLCLs) gets cleaned.
	// Without this the peer's last-reported ledger stays in the
	// engine's getNetworkLedger vote set indefinitely, biasing
	// consensus toward the view of a peer that's no longer here.
	if cb := o.onPeerDisconnect; cb != nil {
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
	o.onPeerDisconnect = cb
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
	o.onPeerConnect = cb
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

	// Inbound TMSquelch is accepted UNCONDITIONALLY. Rippled
	// PeerImp::onMessage(TMSquelch) at PeerImp.cpp:2684-2721 does the
	// same — there is no per-peer gating on vpReduceRelay for incoming
	// squelches. Feature negotiation governs what WE SEND (we only
	// emit TMSquelch to peers who advertised reduce-relay), not what
	// we accept: a squelch directive is harmless when applied — it
	// only suppresses what we send next — and rejecting it creates a
	// not-actually-rippled attack surface where a hostile peer could
	// advertise one capability set to us and another to a neighbor
	// to desync squelch state.
	//
	// C3 note: an earlier round-2 commit message claimed this path
	// gated on vpReduceRelay and charged-then-dropped peers that
	// hadn't negotiated it. The code has always accepted
	// unconditionally, matching rippled — the commit message was
	// inaccurate and this comment is the canonical statement of
	// intended behavior.
	if msgType == message.TypeSquelch {
		if !o.PeerSupports(evt.PeerID, FeatureVpReduceRelay) {
			slog.Debug("TMSquelch from peer without vprr feature; accepting (matches rippled)",
				"t", "Overlay", "peer", evt.PeerID)
		}
		o.handleSquelchMessage(evt)
		return
	}

	// mtSTATUS_CHANGE refreshes Closed-/Previous-Ledger hints
	// (PeerImp.cpp:1812-1862).
	if msgType == message.TypeStatusChange {
		o.handleStatusChange(evt)
		return
	}

	// Serve mtREPLAY_DELTA_REQ from the local ledger sync handler. Mirrors
	// rippled's PeerImp::onMessage(TMReplayDeltaRequest) which delegates to
	// LedgerReplayMsgHandler::processReplayDeltaRequest. Before dispatching
	// we verify the peer actually negotiated ledger-replay in its handshake
	// — rippled charges feeMalformedRequest and drops when a peer sends
	// these without the feature (PeerImp.cpp:1473-1478). Silently dropping
	// + charging badData preserves that guarantee while keeping our
	// response path honest.
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

	// Serve mtPROOF_PATH_REQ from the local ledger sync handler. Mirrors
	// rippled's PeerImp::onMessage(TMProofPathRequest) which delegates to
	// LedgerReplayMsgHandler::processProofPathRequest. Same handshake-
	// negotiation gate as mtREPLAY_DELTA_REQ above — the proof-path
	// protocol is part of the ledger-replay feature bundle in rippled.
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
	// TMReplayDeltaResponse or TMProofPathResponse unsolicited —
	// rippled PeerImp.cpp:1511-1515 charges feeMalformedRequest and
	// drops. Gate BEFORE forwarding to the router so a non-negotiated
	// peer can't wedge the inbound acquisition state with bogus
	// responses.
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
	// can drive it. Mirrors rippled's PeerImp dispatching all
	// consensus traffic through the same inbound path.

	slog.Debug("Message received", "t", "Overlay", "type", msgType.String(), "peer", evt.PeerID, "size", len(evt.Payload))

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
		slog.Warn("Message dropped: channel full", "t", "Overlay", "type", msgType.String())
	}
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
	o.peersMu.RLock()
	peer, exists := o.peers[evt.PeerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}
	// PeerImp.cpp:1796-1797 — rippled stamps the wire's networktime
	// with the local clock when the peer didn't include it, so the
	// pubPeerStatus emit at PeerImp.cpp:1951-1954 always carries a
	// `date`. Mirror that here, mutating sc so the auto-filled value
	// is observable to subscribers.
	if sc.NetworkTime == 0 {
		sc.NetworkTime = uint64(time.Now().Unix() - protocol.RippleEpochUnix)
	}

	effectiveStatus := peer.applyStatusChange(sc)

	// PeerImp.cpp:1812-1830 — rippled's lostSync handling returns
	// before either checkTracking or pubPeerStatus runs. Match that
	// flow so a lostSync update never surfaces as a peer_status
	// WebSocket event.
	if sc.NewEvent == message.NodeEventLostSync {
		return
	}

	// PeerImp.cpp:1885-1890: tracking check is gated on a fresh
	// (<2 min) validated ledger. The gate must NOT short-circuit
	// pubPeerStatus, which rippled invokes unconditionally for
	// non-lostSync messages at PeerImp.cpp:1892-1963.
	if sc.LedgerSeq != 0 {
		if provider := o.validLedgerProviderSnapshot(); provider != nil {
			if validSeq, age, ok := provider(); ok && validSeq != 0 && age < 2*time.Minute {
				peer.CheckTracking(sc.LedgerSeq, validSeq)
			}
		}
	}

	// PeerImp.cpp:1892-1963 — publish to peer_status subscribers.
	if pub := o.peerStatusPublisherSnapshot(); pub != nil {
		// PeerImp.cpp:1941-1948 emits ledger_hash whenever the wire
		// carried the field, sourcing the value from the peer's
		// post-apply closedLedgerHash_. When the wire bytes were
		// malformed, applyStatusChange clears that storage and
		// rippled emits the all-zeros 64-char hex string. Match both
		// branches.
		var ledgerHash string
		if len(sc.LedgerHash) > 0 {
			if h, ok := peer.ClosedLedger(); ok {
				ledgerHash = strings.ToUpper(hex.EncodeToString(h[:]))
			} else {
				ledgerHash = strings.Repeat("0", 64)
			}
		}
		// PeerImp.cpp:1956-1960 — emit min/max only when both wire
		// fields were present. nil-on-absence keeps that paired gate
		// without conflating value 0 with "absent".
		var minSeq, maxSeq *uint32
		if sc.FirstSeq != nil && sc.LastSeq != nil {
			f, l := *sc.FirstSeq, *sc.LastSeq
			minSeq, maxSeq = &f, &l
		}
		// PeerImp.cpp:1936-1939 (`has_ledgerseq`). The decoder loses
		// proto-presence for ledger_seq (see
		// internal/peermanagement/proto/ripple.pb.go), so use 0 as
		// the absence proxy — XRPL ledger sequences start at the
		// genesis ledger 1, no real peer broadcasts has_ledgerseq=0.
		var ledgerIndex *uint32
		if sc.LedgerSeq != 0 {
			ls := sc.LedgerSeq
			ledgerIndex = &ls
		}
		// PeerImp.cpp:1951-1954 — Date is always set thanks to the
		// auto-fill above. Truncate uint64 → uint32 to match
		// rippled's `Json::UInt(...)` cast (Json::UInt is uint32_t).
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
	// Rippled charges feeInvalidData on empty/wrong-length at
	// PeerImp.cpp:2701-2712. Silently dropping here let a peer spam
	// bogus TMSquelch frames without penalty.
	if len(sq.ValidatorPubKey) != 33 {
		slog.Debug("Squelch malformed pubkey",
			"t", "Overlay", "peer", evt.PeerID, "len", len(sq.ValidatorPubKey))
		o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
		return
	}

	// Rippled PeerImp.cpp:2715-2721 drops any inbound squelch whose
	// target pubkey is our own validator — otherwise a peer could
	// silence our own traffic on the RelayFromValidator path
	// (self-silencing DoS). goXRPL additionally charges the sending
	// peer a bad-data event so repeated attempts feed the eviction
	// threshold; rippled just logs-and-returns there.
	if ownPubKey := o.localValidatorPubKey(); len(ownPubKey) == 33 && bytes.Equal(sq.ValidatorPubKey, ownPubKey) {
		slog.Debug("Squelch dropped: targets local validator",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "squelch-targets-self")
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
		o.peersMu.RLock()
		peer, exists := o.peers[evt.PeerID]
		o.peersMu.RUnlock()
		if exists {
			peer.OnPong(ping.Seq, time.Now())
		}
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

	// decayTicker drives bad-data balance decay on its own cadence so
	// the eviction scoring matches rippled's time-windowed Consumer
	// model rather than a flat counter.
	decayTicker := time.NewTicker(badDataDecayInterval)
	defer decayTicker.Stop()

	// idleSweepTicker drives the reduce-relay idle-peer sweep (G2).
	// Cadence is Idled/2 (4s) so no relay peer stays referenced more
	// than ~1.5x the idle threshold before being evicted — matches
	// rippled's OverlayImpl.cpp:107-111 which triggers
	// Slots::deleteIdlePeers every 4 timer ticks of its 1s timer
	// (Tuning::checkIdlePeers=4). Without this sweep, r.slots only
	// shrinks on explicit RemovePeer and accumulates stale entries
	// for validators we no longer see.
	idleSweepTicker := time.NewTicker(Idled / 2)
	defer idleSweepTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.performMaintenance()
		case <-decayTicker.C:
			o.decayBadData()
		case now := <-idleSweepTicker.C:
			if o.relay != nil {
				o.relay.deleteIdlePeers(now)
			}
		}
	}
}

func (o *Overlay) performMaintenance() {
	// Cleanup expired ledger requests
	o.ledgerSync.CleanupExpiredRequests()
	// Evict peers that have accumulated enough bad-data events to cross
	// the threshold. Runs here (not inline in IncBadData) so the
	// disconnect happens off any hot receive path and so a single tick
	// can evict multiple offenders found since the last pass.
	o.evictBadDataPeers()
}

// decayBadData halves every connected peer's bad-data balance.
// Mirrors rippled's Resource::Fees.cpp:26-43 exponential decay so a
// chatty-but-honest peer's transient protocol hiccups don't
// accumulate to eviction over a long session.
func (o *Overlay) decayBadData() {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()
	for _, peer := range o.peers {
		peer.DecayBadData()
	}
}

// evictBadDataPeers disconnects peers whose cumulative invalid-data
// count has reached EvictBadDataThreshold. Mirrors rippled's behavior
// of dropping peers that have been charged enough feeInvalidData to
// exhaust their balance. Must be safe to call with the peer map locked
// for read only — we collect offenders under RLock, then disconnect
// them after releasing the lock to avoid holding it across Close().
func (o *Overlay) evictBadDataPeers() {
	type offender struct {
		id    PeerID
		peer  *Peer
		count uint32
	}
	var toEvict []offender

	o.peersMu.RLock()
	for id, peer := range o.peers {
		if n := peer.BadDataCount(); n >= EvictBadDataThreshold {
			toEvict = append(toEvict, offender{id: id, peer: peer, count: n})
		}
	}
	o.peersMu.RUnlock()

	for _, off := range toEvict {
		slog.Info("Evicting peer for bad data",
			"t", "Overlay",
			"peer", off.id,
			"count", off.count,
			"threshold", EvictBadDataThreshold,
			"endpoint", off.peer.Endpoint().String(),
		)
		// Close first so the peer's run goroutine exits and its
		// removePeer callback fires; defensively remove here too so a
		// caller-driven test path (no running goroutine) still sees the
		// peer gone after this function returns.
		off.peer.Close()
		o.removePeer(off.id)
	}
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
		// rippled stores the duration as seconds in TMSquelch. Only set
		// on squelch=true — on un-squelch the peer ignores this field
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
	peer.handshakeCfg = o.handshakeConfigFor()

	o.events <- Event{
		Type:     EventPeerConnecting,
		PeerID:   peerID,
		Endpoint: endpoint,
		Inbound:  false,
	}

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

	go func() {
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
// Rippled deliberately skips the squelch filter for self-originated
// broadcasts in OverlayImpl.cpp:1133-1137; otherwise a peer that
// squelches our own pubkey would silence us to them.
//
// For peer-originated validator messages that need to be gossip-
// forwarded, use RelayFromValidator which applies the squelch filter
// and excludes the originating peer.
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

// BroadcastExcept sends a message to every connected peer except the
// one identified by exceptPeer. Used for gossip of peer-originated
// messages that are NOT per-validator (manifests) — the per-validator
// squelch filter in RelayFromValidator doesn't apply. Pass 0 for
// exceptPeer to fall through to a plain Broadcast. Mirrors rippled's
// OverlayImpl::foreach used to relay TMManifests at
// OverlayImpl.cpp:633-686.
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
		peer.Send(msg)
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
// slot with the full set of known-havers — matching rippled's
// haveMessage return from overlay_.relay at PeerImp.cpp:3010-3017 /
// 3044-3054.
//
// Mirrors rippled's gossip-forward path in OverlayImpl::relay: the
// squelch is consulted before each outbound send (PeerImp.cpp:240-256)
// and expired squelches auto-clear via Peer.ExpireSquelch. Self-origin
// is handled by a separate code path (see Broadcast) that skips the
// filter entirely.
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
		peer.Send(msg)
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
// select peers to squelch. Mirrors rippled's
// PeerImp::updateSlotAndSquelch (PeerImp.cpp:1737,2385,3013,3049).
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

// Cluster returns the registry of cluster-trusted node identities
// loaded from [cluster_nodes]. Always non-nil post-construction.
func (o *Overlay) Cluster() *cluster.Registry { return o.cluster }

// PeersJSON implements types.PeerSource for the `peers` RPC method,
// emitting the subset of rippled PeerImp::json (PeerImp.cpp:388-503)
// fields for which goXRPL has data.
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
		// PeerImp.cpp:411-412: emit only when the peer set a Network-ID.
		if p.NetworkID != "" {
			entry["network_id"] = p.NetworkID
		}
		if p.ClosedLedger != "" {
			entry["ledger"] = p.ClosedLedger
		}
		if p.CompleteLedgers != "" {
			entry["complete_ledgers"] = p.CompleteLedgers
		}
		// cluster + name: PeerImp.cpp:399-406.
		if len(p.PublicKeyBytes) > 0 {
			if member, ok := o.cluster.Member(p.PublicKeyBytes); ok {
				entry["cluster"] = true
				if member.Name != "" {
					entry["name"] = member.Name
				}
			}
		}
		// PeerImp.cpp:437-450: omit when converged.
		switch p.Tracking {
		case PeerTrackingDiverged:
			entry["track"] = "diverged"
		case PeerTrackingUnknown:
			entry["track"] = "unknown"
		}
		if p.HasLatency {
			entry["latency"] = uint32(p.Latency / time.Millisecond)
		}
		// PeerImp.cpp:416-417: version sourced from User-Agent (inbound)
		// or Server (outbound) header.
		if p.Version != "" {
			entry["version"] = p.Version
		}
		// PeerImp.cpp:419 — emit unconditionally (rippled always has a
		// negotiated value once the handshake has completed).
		entry["protocol"] = p.Protocol
		// PeerImp.cpp:463-491 — emit only when last_status_.has_newstatus().
		if s, known := nodeStatusRPCName(p.Status); s != "" {
			entry["status"] = s
		} else if !known && p.Status != 0 {
			// PeerImp.cpp:487-490 — rippled logs a journal warning
			// when last_status_.newstatus() falls outside the known
			// enum, then drops the field. Mirror the diagnostic so
			// out-of-range values aren't silent.
			slog.Warn("Unknown peer status",
				"t", "Overlay", "peer", p.ID, "status", int32(p.Status))
		}
		// PeerImp.cpp:493-501: emit the metrics object — rippled formats
		// each value with std::to_string, so they're decimal strings.
		entry["metrics"] = map[string]any{
			"total_bytes_recv": strconv.FormatUint(p.TotalBytesRecv, 10),
			"total_bytes_sent": strconv.FormatUint(p.TotalBytesSent, 10),
			"avg_bps_recv":     strconv.FormatUint(p.AvgBpsRecv, 10),
			"avg_bps_sent":     strconv.FormatUint(p.AvgBpsSent, 10),
		}
		out = append(out, entry)
	}
	return out
}

// nodeStatusRPCName mirrors PeerImp.cpp:463-491. Returns the rippled
// spelling for each known NodeStatus and a `known` flag distinguishing
// "no status reported" (nsUNKNOWN, known=true) from "unrecognized
// enum value" (known=false). The caller omits the `status` field for
// either case but logs only the unknown-enum case, matching rippled.
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

// clusterFeeRef mirrors rippled's LoadFeeTrack::getLoadBase() default.
// Replace with a live reference once goXRPL grows a load-fee tracker.
const clusterFeeRef uint32 = 256

// ClusterJSON returns the top-level cluster object for the `peers`
// RPC response, mirroring rippled doPeers (Peers.cpp:59-80).
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
			age := int64(now.Sub(m.ReportTime).Seconds())
			if age < 0 {
				age = 0
			}
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
