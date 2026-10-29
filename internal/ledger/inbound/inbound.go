// Package inbound provides lightweight ledger acquisition from peers.
// It fetches the full ledger header, account-state tree, and transaction tree
// via the TMGetLedger/TMLedgerData peer protocol, matching rippled's
// InboundLedger behavior.
package inbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Acquisition retry-loop tuning, ported from rippled's InboundLedger
// (InboundLedger.cpp:46-74). The loop fires on a timer rather than only on
// peer replies, so a silent or dropped-reply peer cannot stall it: every
// acquireTimerInterval the acquisition checks whether it made forward progress
// since the last fire and, if not, counts a timeout and escalates.
const (
	// acquireTimerInterval is how often OnTimer evaluates progress
	// (rippled ledgerAcquireTimeout).
	acquireTimerInterval = 3 * time.Second
	// ledgerTimeoutRetriesMax bounds consecutive no-progress timer fires before
	// the acquisition fails cleanly (rippled ledgerTimeoutRetriesMax).
	ledgerTimeoutRetriesMax = 6
	// ledgerBecomeAggressiveThreshold is the no-progress timeout count past
	// which the acquisition abandons path-based requests and asks every peer
	// for the missing nodes by content hash (rippled
	// ledgerBecomeAggressiveThreshold).
	ledgerBecomeAggressiveThreshold = 4
	// missingRequestReadyReserve retains a small amount of discovered work when
	// all peer request slots are occupied. It is deliberately independent from
	// peer credit: a reply can satisfy only the nodes it actually contains.
	missingRequestReadyReserve = reqNodesReply
	missingRequestWindow       = requestPeerLimit * reqNodesReply
	missingRequestLeaseTTL     = 2 * acquireTimerInterval
	txOnlyTailHedgeDelay       = time.Second
	txOnlyTailNodeLimit        = 8
	txOnlyTailPeerLimit        = 1
)

// hardMaxReplyNodes is rippled's per-message cap on the nodes a peer may pack
// into a single TMLedgerData reply (Tuning::hardMaxReplyNodes, Tuning.h:42).
const hardMaxReplyNodes = 12288

const persistenceCheckpointNodes = 32 * 1024

// ValidateReplyNodeCount enforces the bounds rippled places on a single
// TMLedgerData reply — at least one node, at most hardMaxReplyNodes — so the
// router can charge an offending peer badData. Mirrors the ingress guard in
// rippled's PeerImp::onMessage(TMLedgerData) (PeerImp.cpp:1628), which rejects
// both nodes_size() <= 0 and nodes_size() > Tuning::hardMaxReplyNodes.
func ValidateReplyNodeCount(nodes []message.LedgerNode) error {
	switch n := len(nodes); {
	case n <= 0:
		return fmt.Errorf("ledger data reply has no nodes")
	case n > hardMaxReplyNodes:
		return fmt.Errorf("ledger data exceeds hardMaxReplyNodes: %d > %d", n, hardMaxReplyNodes)
	}
	return nil
}

// Reason records why an acquisition was started, mirroring rippled's
// InboundLedger::Reason. It governs completion handling: a consensus-driven
// acquisition adopts the ledger into the active chain, while a generic
// (RPC-driven, e.g. ledger_request) acquisition only persists it so it can be
// queried without disturbing consensus state.
type Reason int

const (
	// ReasonConsensus is catch-up / consensus-driven acquisition. Zero value
	// so existing callers keep their behavior.
	ReasonConsensus Reason = iota
	// ReasonGeneric is an RPC-driven acquisition (rippled Reason::GENERIC).
	ReasonGeneric
	// ReasonHistory is a background backfill of a ledger a jump-adopt skipped
	// (rippled Reason::HISTORY): store-only ingest, off the catch-up path.
	ReasonHistory
)

// State tracks the acquisition progress.
type State int

const (
	StateWantBase    State = iota // Waiting for header + root nodes
	StateWantState                // Have header, fetching state tree nodes
	StateComplete                 // Fully acquired
	StateFailed                   // Unrecoverable error
	StateReplayReady              // Replay response verified; apply pending
)

// TimerAction tells the router what to do after an OnTimer evaluation,
// mirroring the dispatch rippled's InboundLedger::onTimer performs inline.
type TimerAction int

const (
	// TimerNone: the timer was not due yet.
	TimerNone TimerAction = iota
	// TimerRefresh: the acquisition made progress this interval. The caller may
	// refill idle peer request slots without counting a timeout.
	TimerRefresh
	// TimerEscalate: a no-progress interval elapsed and the retry budget is
	// not yet exhausted — broaden peers and re-request the missing nodes
	// (and, once aggressive, escalate to a by-hash fetch).
	TimerEscalate
	// TimerFailed: the retry budget is exhausted — the acquisition is now
	// StateFailed and the caller must reap it.
	TimerFailed
)

// Ledger manages the acquisition of a single ledger from a peer.
// It progresses through: WantBase → WantState → Complete. Like rippled's
// InboundLedger, it prioritizes the account-state tree while allowing spare
// reply peers to work on transactions; acquisition is Complete only when both
// trees are available.
//
// Field lock guarantees:
//   - hash, reason, logger, family, and headerAdmission are set at construction
//     and never mutated thereafter.
//   - seq, peers, header, stateMap, txMap, haveState, haveTx, state, err, the
//     retry-loop fields, and fetchPackRequested are written under mu and must
//     be read through accessors that take mu (State, PeerID, OnTimer, GotBase,
//     etc.).
type Ledger struct {
	hash [32]byte
	seq  uint32
	// sequenceInitiallyUnknown records hash-only acquisitions. The verified
	// header later fills seq, but the router still needs to know how this
	// acquisition began so it can promote a consensus request into the frozen
	// fast-load recovery session instead of treating it as an ordinary fetch.
	sequenceInitiallyUnknown bool
	header                   *header.LedgerHeader
	stateMap                 *shamap.SHAMap
	txMap                    *shamap.SHAMap // nil when the transaction tree is empty (TxHash zero)
	haveState                bool
	haveTx                   bool
	peers                    []uint64 // source peers, broadened on no-progress; peers[0] is the original
	reason                   Reason
	state                    State
	err                      error
	mu                       sync.Mutex
	workMu                   sync.Mutex
	logger                   *slog.Logger
	snapshot                 atomic.Pointer[Snapshot]

	// family backs the acquisition SHAMaps with the persistent node store when
	// set, so getMissingNodes only reports nodes not already held locally. nil
	// leaves the maps unbacked. Set once at construction, never mutated after.
	family            shamap.Family
	memory            *shamap.AcquisitionBudget
	headerAdmission   func(uint32) error
	verifiedStateBase [32]byte

	// Retry-loop bookkeeping ported from rippled's TimeoutCounter. lastTimer
	// is when OnTimer last evaluated; progress records a fresh node attach
	// since then; timeouts is the cumulative no-progress count used for
	// diagnostics and escalation; consecutiveTimeouts bounds the current
	// no-progress streak for terminal failure, diagnostics, and victim ranking.
	// byHash latches eligibility for a by-hash escalation on the next aggressive
	// request. All guarded by mu.
	lastTimer           time.Time
	lastProgressReport  time.Time
	progress            bool
	timeouts            int
	consecutiveTimeouts int
	byHash              bool
	// byHashMode keeps reply-driven content-addressed acquisition active after
	// escalation. It uses the same bounded peer leases as path-based requests.
	byHashMode         bool
	lastUsefulProgress time.Time

	// recentNodes keeps request frontiers disjoint within the bounded request
	// window. A value of zero identifies a direct path request; non-zero values
	// identify a peer lease. requestPeers caps the acquisition at one active
	// request per peer and six active peer streams.
	recentNodes          map[[32]byte]uint64
	requestPeers         map[uint64]struct{}
	requestLeases        map[[32]byte]missingRequestLease
	requestByNode        map[requestNodeKey][32]byte
	readyRequests        []readyMissingNode
	readySet             map[[32]byte]struct{}
	requestMetadataBytes uint64
	nextRequestTx        bool
	lastScanStatus       shamap.MissingNodesStatus
	// missingState and missingTx retain the last verified path frontier. Timeout
	// retries can resend it without walking the durable SHAMap again; useful
	// replies still perform a fresh walk and replace the frontier. neededState
	// and neededTx are the bounded diagnostic views exposed in Snapshot.
	missingState      []shamap.MissingNode
	missingTx         []shamap.MissingNode
	neededState       [][32]byte
	neededTx          [][32]byte
	stateRecv         uint64
	stateUseful       uint64
	txRecv            uint64
	txUseful          uint64
	stateCacheUseful  uint64
	txCacheUseful     uint64
	checkpointUseful  uint64
	memoryWaiting     bool
	memoryCheckpoints uint64
	retired           atomic.Bool

	// Rejection diagnostics, surfaced on the no-progress tick so a stuck
	// acquisition names which node it cannot place and why (the signal the
	// swallowed Debug logs hid). Guarded by mu.
	rejectCount   int
	lastRejectErr string

	// fetchPackRequested records that the router escalated this stalled
	// acquisition to a fetch-pack (at most once). Guarded by mu.
	fetchPackRequested bool

	// transactionOnly is used by forward catch-up when the parent ledger is
	// already held locally but the peer does not support the optional
	// ledger-replay extension. Standard mtGET_LEDGER can still provide the
	// target header and transaction SHAMap; the router replays those
	// transactions against the held parent and verifies AccountHash before
	// adoption. In this mode the peer's account-state SHAMap is deliberately
	// ignored, avoiding a full-state download for every child ledger.
	transactionOnly   bool
	fullStateRequired atomic.Bool
}

// Option configures an acquisition at construction.
type Option func(*Ledger)

// ErrHeaderRejected identifies a locally valid ledger header that acquisition
// policy declined. The source peer did not send malformed data, but the
// acquisition must terminate without accepting or persisting its roots.
var ErrHeaderRejected = errors.New("inbound ledger header rejected")

// ErrInvalidPeerNode identifies a ledger node that failed wire, reference or hash
// validation. The acquisition remains active so a later reply can recover,
// while the router can charge the peer that supplied the malformed node.
var ErrInvalidPeerNode = errors.New("invalid peer ledger node")

// ErrPersistence identifies a verified-node write that did not reach the
// acquisition's backing store. Callers must treat this as a failed durability
// barrier and must not acknowledge the acquisition as complete.
var ErrPersistence = errors.New("inbound persistence failed")

// ErrAcquisitionRetired identifies a persistence scope whose ownership ended.
// It is cancellation only when the acquisition itself has also retired.
var ErrAcquisitionRetired = errors.New("acquisition persistence scope retired")

var errAcquisitionCanceled = fmt.Errorf("%w: %w", context.Canceled, ErrAcquisitionRetired)

// WithFamily backs the acquisition's state and transaction SHAMaps with the
// node-store family, so nodes already present locally (the shared majority of
// the tree after a fork or during forward catch-up) are satisfied from the
// store and only the genuinely-missing ones are fetched from peers. A nil
// family is ignored, leaving the maps unbacked.
func WithFamily(family shamap.Family) Option {
	return func(l *Ledger) {
		if family != nil {
			l.family = family
		}
	}
}

// WithAcquisitionBudget shares acquisition memory limits across ledger maps.
func WithAcquisitionBudget(memory *shamap.AcquisitionBudget) Option {
	return func(l *Ledger) {
		l.memory = memory
	}
}

// WithHeaderAdmission installs a policy check that runs after the header hash
// and any requested sequence have been verified, but before the acquisition
// adopts a hash-only header's sequence or constructs its SHAMaps.
func WithHeaderAdmission(admit func(uint32) error) Option {
	return func(l *Ledger) {
		l.headerAdmission = admit
	}
}

// WithTransactionOnly configures a standard-protocol acquisition to fetch the
// target header and transaction SHAMap without walking its account-state
// SHAMap. The result must be replayed against a locally-held parent and its
// derived AccountHash verified before it is trusted.
func WithTransactionOnly() Option {
	return func(l *Ledger) {
		l.transactionOnly = true
	}
}

// New creates a new InboundLedger acquisition for the given ledger hash.
// The acquisition reason defaults to ReasonConsensus.
func New(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger, opts ...Option) *Ledger {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		"ledger_seq", seq,
		"ledger_hash", fmt.Sprintf("%x", hash[:8]),
	)
	l := &Ledger{
		hash:                     hash,
		seq:                      seq,
		sequenceInitiallyUnknown: seq == 0,
		state:                    StateWantBase,
		lastTimer:                SystemClock.Now(),
		logger:                   logger,
		recentNodes:              make(map[[32]byte]uint64),
		requestPeers:             make(map[uint64]struct{}),
		requestLeases:            make(map[[32]byte]missingRequestLease),
		requestByNode:            make(map[requestNodeKey][32]byte),
		readySet:                 make(map[[32]byte]struct{}),
	}
	if peerID != 0 {
		l.peers = []uint64{peerID}
	}
	for _, opt := range opts {
		opt(l)
	}
	l.publishSnapshotLocked()
	return l
}

// NewGeneric creates an RPC-driven (ReasonGeneric) acquisition: on completion
// the ledger is persisted for querying but not adopted into the active chain.
func NewGeneric(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger, opts ...Option) *Ledger {
	l := New(hash, seq, peerID, logger, opts...)
	if peerID == 0 {
		l.peers = nil
	}
	l.reason = ReasonGeneric
	return l
}

// NewHistory creates a background history-backfill (ReasonHistory) acquisition:
// on completion the ledger is store-ingested below the closed tip, never
// advancing consensus state.
func NewHistory(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger, opts ...Option) *Ledger {
	l := New(hash, seq, peerID, logger, opts...)
	l.reason = ReasonHistory
	return l
}

// newSyncMap builds an acquisition SHAMap, backed by the node-store family when
// one is wired (see WithFamily) and unbacked otherwise.
func (l *Ledger) newSyncMap(ctx context.Context, t shamap.Type) (*shamap.SHAMap, error) {
	if l.family != nil {
		m, err := shamap.NewBacked(t, l.family)
		if err != nil {
			return nil, err
		}
		if l.memory != nil {
			m.SetAcquisitionBudget(l.memory)
		}
		if t == shamap.TypeState && l.verifiedStateBase != ([32]byte{}) {
			if err := m.SetVerifiedBaseContext(ctx, l.verifiedStateBase); err != nil {
				l.logger.Warn("inbound ledger: verified state base unavailable; using strict discovery", "error", err)
			}
		}
		return m, nil
	}
	m := shamap.New(t)
	if l.memory != nil {
		m.SetAcquisitionBudget(l.memory)
	}
	return m, nil
}

// SetVerifiedStateBaseContext installs a base before or during account-state
// acquisition. An existing discovery cursor is reset by the SHAMap.
func (l *Ledger) SetVerifiedStateBaseContext(ctx context.Context, rootHash [32]byte) error {
	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return err
	}
	l.mu.Lock()
	l.verifiedStateBase = rootHash
	stateMap := l.stateMap
	l.mu.Unlock()
	if stateMap == nil {
		return nil
	}
	if err := stateMap.SetVerifiedBaseContext(ctx, rootHash); err != nil {
		l.mu.Lock()
		if l.verifiedStateBase == rootHash {
			l.verifiedStateBase = [32]byte{}
		}
		l.mu.Unlock()
		return err
	}
	return nil
}

// WaitForWork waits for the ledger's current discovery or attachment operation
// to leave its critical section. Callers must first prevent new work from being
// submitted for the ledger.
func (l *Ledger) WaitForWork() {
	l.workMu.Lock()
	l.workMu.Unlock()
}

// Reason returns why this acquisition was started.
func (l *Ledger) Reason() Reason {
	return l.reason
}

// TransactionOnly reports whether this acquisition contains only the target
// header and transaction SHAMap for local replay.
func (l *Ledger) TransactionOnly() bool {
	return l.transactionOnly
}

// RequireFullState retains a replay fallback requirement for this acquisition.
func (l *Ledger) RequireFullState() {
	l.fullStateRequired.Store(true)
}

// FullStateRequired reports whether replay fallback must preserve this acquisition.
func (l *Ledger) FullStateRequired() bool {
	return l.fullStateRequired.Load()
}

// State returns the current acquisition state.
func (l *Ledger) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// PeerID returns the primary source peer (the one the acquisition started on).
func (l *Ledger) PeerID() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.peers) == 0 {
		return 0
	}
	return l.peers[0]
}

// Peers returns a snapshot of the acquisition's current source-peer set.
func (l *Ledger) Peers() []uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]uint64(nil), l.peers...)
}

// AddPeer adds peerID to the source set. The connected overlay bounds the set;
// each no-progress interval may broaden it further. Returns true if newly added.
func (l *Ledger) AddPeer(peerID uint64) bool {
	return l.AddPeerBounded(peerID, 0)
}

// AddPeerBounded adds peerID unless it is already present or max peers are
// already assigned. A non-positive max leaves the set unbounded.
func (l *Ledger) AddPeerBounded(peerID uint64, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if slices.Contains(l.peers, peerID) {
		return false
	}
	if max > 0 && len(l.peers) >= max {
		return false
	}
	l.peers = append(l.peers, peerID)
	return true
}

// RemovePeer removes peerID from the acquisition's source set.
func (l *Ledger) RemovePeer(peerID uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clearHedgePeerLocked(peerID)
	l.releaseAllPeerLeasesLocked(peerID, time.Now(), true)
	for i, id := range l.peers {
		if id == peerID {
			l.peers = slices.Delete(l.peers, i, i+1)
			return true
		}
	}
	return false
}

// MissingRequest assigns one disjoint missing-node frontier to a peer.
type MissingRequest struct {
	PeerID      uint64
	NodeIDs     [][]byte
	NodeHashes  [][32]byte
	Transaction bool
	Blind       bool
	ByHash      bool
	Hedge       bool
}

// MissingRequestOutcome tells the acquisition owner which condition should
// drive the next worker event after planning requests.
type MissingRequestOutcome uint8

const (
	MissingRequestContinue MissingRequestOutcome = iota
	MissingRequestWaitReply
	MissingRequestWaitMemory
	MissingRequestWaitPersistence
	MissingRequestComplete
)

// MissingRequestPlan is the typed result of bounded request planning. A plan
// may contain requests and still require Continue: partial output never
// implies that discovery is exhausted.
type MissingRequestPlan struct {
	Requests []MissingRequest
	Outcome  MissingRequestOutcome
}

// MissingReplyResult reports how much of a peer reply was safely consumed.
// Consumed excludes the current node when acquisition memory pressure stops
// processing. The caller can retry the unconsumed suffix after a checkpoint.
type MissingReplyResult struct {
	Useful      int
	Received    int
	Fulfilled   int
	Unfulfilled int
	Consumed    int
	Complete    bool
}

type missingRequestLease struct {
	peerID          uint64
	previousPeerID  uint64
	previousExpires time.Time
	// wireShadow retains in-flight peer credits after a hedge fulfilled this
	// node. It is not a missing logical lease and must not be claimed or
	// requeued.
	wireShadow  bool
	transaction bool
	nodeID      shamap.NodeID
	started     time.Time
	expires     time.Time
	retries     uint32
	hedges      uint32
}

type readyMissingNode struct {
	node            shamap.MissingNode
	transaction     bool
	previousPeerID  uint64
	previousExpires time.Time
	started         time.Time
	retries         uint32
	hedges          uint32
}

type requestNodeKey struct {
	transaction bool
	nodeID      shamap.NodeID
}

// RequestPeerSnapshot describes the bounded request ownership held by one
// peer at a point in time. OldestRequest is the first assignment time of the
// oldest outstanding node; retries and hedges are per-node counts retained by
// the acquisition until its lease is fulfilled or retired. OutstandingNodes
// includes wire-only shadow slots retained after logical fulfillment.
type RequestPeerSnapshot struct {
	PeerID           uint64
	OutstandingNodes int
	OldestRequest    time.Time
	Retries          uint32
	Hedges           uint32
}

// Timeouts returns the number of no-progress timer intervals counted so far,
// mirroring rippled's timeouts_. The router gates qtINDIRECT relaying on
// timeouts > 0, matching InboundLedger::trigger.
func (l *Ledger) Timeouts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.timeouts
}

// ConsecutiveTimeouts returns the current no-progress streak. Unlike Timeouts,
// useful peer or local data resets this value.
func (l *Ledger) ConsecutiveTimeouts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.consecutiveTimeouts
}

// TimerDue reports whether the acquisition retry interval has elapsed.
func (l *Ledger) TimerDue(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state != StateComplete && l.state != StateFailed && now.Sub(l.lastTimer) >= acquireTimerInterval
}

// OnTimer escalates due no-progress intervals and fails after the consecutive
// timeout budget is exhausted. Useful progress resets the failure streak.
func (l *Ledger) OnTimer(now time.Time) TimerAction {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state == StateComplete || l.state == StateFailed {
		return TimerNone
	}
	if now.Sub(l.lastTimer) < acquireTimerInterval {
		return TimerNone
	}

	requestPeerCount := len(l.requestPeers)

	// Legacy path requests refresh each interval. The reply-driven by-hash
	// window retains live leases until their retry TTL; a progress tick must
	// not duplicate requests that are still in flight.
	l.expireMissingLeasesLocked(now, !l.byHashMode)

	if l.progress || (l.memoryWaiting && requestPeerCount == 0) {
		l.lastTimer = now
		l.progress = false
		return TimerRefresh
	}

	l.lastTimer = now
	l.timeouts++
	l.consecutiveTimeouts++
	if l.consecutiveTimeouts > ledgerTimeoutRetriesMax {
		l.state = StateFailed
		l.err = fmt.Errorf("inbound ledger %d: acquisition failed after %d consecutive timeouts (%d total; have_state=%t have_tx=%t last_reject=%q)",
			l.seq, l.consecutiveTimeouts, l.timeouts, l.haveState, l.haveTx, l.lastRejectErr)
		l.logger.Warn("inbound ledger: acquisition failed, retry budget exhausted",
			"seq", l.seq,
			"hash", fmt.Sprintf("%x", l.hash[:8]),
			"timeouts", l.timeouts,
			"consecutive_timeouts", l.consecutiveTimeouts,
			"phase", l.snapshotLocked().Phase(),
			"have_state", l.haveState,
			"have_tx", l.haveTx,
			"peers", len(l.peers),
			"request_peers", requestPeerCount,
			"needed_state", len(l.neededState),
			"needed_tx", len(l.neededTx),
			"state_received_total", l.stateRecv,
			"state_useful_total", l.stateUseful,
			"tx_received_total", l.txRecv,
			"tx_useful_total", l.txUseful,
			"reject_count", l.rejectCount,
			"last_reject", l.lastRejectErr,
		)
		return TimerFailed
	}

	// No progress, budget remains: arm a by-hash escalation and surface the
	// diagnostic that the swallowed Debug-level rejections used to hide.
	l.byHash = true
	if l.state == StateWantState && l.timeouts > ledgerBecomeAggressiveThreshold {
		l.byHashMode = true
	}
	l.logger.Warn("inbound ledger: no progress in current interval",
		"seq", l.seq,
		"hash", fmt.Sprintf("%x", l.hash[:8]),
		"timeouts", l.timeouts,
		"phase", l.snapshotLocked().Phase(),
		"have_state", l.haveState,
		"have_tx", l.haveTx,
		"peers", len(l.peers),
		"request_peers", requestPeerCount,
		"needed_state", len(l.neededState),
		"needed_tx", len(l.neededTx),
		"state_received_total", l.stateRecv,
		"state_useful_total", l.stateUseful,
		"tx_received_total", l.txRecv,
		"tx_useful_total", l.txUseful,
		"reject_count", l.rejectCount,
		"last_reject", l.lastRejectErr,
	)
	return TimerEscalate
}

// RearmTimer starts the next acquisition interval after a timeout escalation
// has finished. The escalation may perform an expensive local SHAMap walk, so
// rearming before it runs would make the next interval expire during that work.
func (l *Ledger) RearmTimer(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == StateComplete || l.state == StateFailed {
		return
	}
	l.lastTimer = now
}

// SetMemoryWaiting records a local resource wait without suppressing retries
// for unanswered peer requests.
func (l *Ledger) SetMemoryWaiting(waiting bool) {
	l.mu.Lock()
	l.memoryWaiting = waiting
	l.publishSnapshotLocked()
	l.mu.Unlock()
}

// AcquisitionMemoryWaiting reports a retryable wait caused by the shared
// acquisition byte budget. Persistence waits use a separate terminal path.
func (l *Ledger) AcquisitionMemoryWaiting() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memoryWaiting && l.lastScanStatus == shamap.MissingNodesMemory
}

// markProgressLocked records that a fresh node was attached this interval, so
// the next OnTimer fire treats the acquisition as progressing rather than
// timing out (rippled sets progress_ on a useful received node). The
// cumulative timeout budget remains unchanged; only the current streak used
// for diagnostics and victim ranking is reset. Caller holds mu.
func (l *Ledger) markProgressLocked() {
	l.progress = true
	l.consecutiveTimeouts = 0
	l.lastUsefulProgress = SystemClock.Now()
}

// ByHashMode reports whether content-addressed requests replaced path-based
// acquisition after repeated no-progress intervals. Useful replies keep this
// mode active: an aged-out pivot does not regain full-ledger availability.
func (l *Ledger) ByHashMode() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byHashMode
}

// TxOnlyTailHedgeDue reports whether a small transaction-only frontier can be
// hedged. The caller must restrict this check to the actionable replay head.
func (l *Ledger) TxOnlyTailHedgeDue(now time.Time) bool {
	if !l.mu.TryLock() {
		return false
	}
	defer l.mu.Unlock()
	return l.txOnlyTailHedgeDueLocked(now)
}

func (l *Ledger) txOnlyTailHedgeDueLocked(now time.Time) bool {
	if l.retired.Load() || !l.transactionOnly || l.state != StateWantState || !l.haveState || l.haveTx {
		return false
	}
	frontier := 0
	active := 0
	for _, lease := range l.requestLeases {
		if !lease.transaction {
			continue
		}
		if lease.wireShadow {
			continue
		}
		frontier++
		if lease.peerID == 0 || lease.hedges != 0 || lease.started.IsZero() {
			continue
		}
		if !now.Before(lease.expires) {
			continue
		}
		if now.Sub(lease.started) >= txOnlyTailHedgeDelay {
			active++
		}
	}
	for _, ready := range l.readyRequests {
		if ready.transaction {
			frontier++
		}
	}
	if len(l.missingTx) > txOnlyTailNodeLimit || len(l.neededTx) > txOnlyTailNodeLimit {
		return false
	}
	return frontier > 0 && frontier <= txOnlyTailNodeLimit && active > 0
}

// PlanTransactionOnlyTailRequestsContext assigns aged transaction nodes to one
// alternate peer without duplicating logical node reservations. peerIDs must be
// ordered by preference. Verified replies from the original peer remain useful.
func (l *Ledger) PlanTransactionOnlyTailRequestsContext(ctx context.Context, peerIDs []uint64, now time.Time) (MissingRequestPlan, error) {
	plan := MissingRequestPlan{Outcome: MissingRequestWaitReply}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if err := l.retirementError(); err != nil {
		return plan, err
	}

	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return plan, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if l.state == StateComplete {
		plan.Outcome = MissingRequestComplete
		return plan, nil
	}
	if !l.txOnlyTailHedgeDueLocked(now) {
		return plan, nil
	}
	l.refreshRequestPeersAtLocked(now)

	type tailLease struct {
		hash  [32]byte
		lease missingRequestLease
	}
	tail := make([]tailLease, 0, txOnlyTailNodeLimit)
	for hash, lease := range l.requestLeases {
		if lease.wireShadow || !lease.transaction || lease.peerID == 0 || lease.hedges != 0 || lease.started.IsZero() ||
			!now.Before(lease.expires) || now.Sub(lease.started) < txOnlyTailHedgeDelay {
			continue
		}
		tail = append(tail, tailLease{hash: hash, lease: lease})
	}
	if len(tail) == 0 || len(tail) > txOnlyTailNodeLimit {
		return plan, nil
	}
	slices.SortFunc(tail, func(a, b tailLease) int {
		if a.lease.started.Before(b.lease.started) {
			return -1
		}
		if a.lease.started.After(b.lease.started) {
			return 1
		}
		return bytes.Compare(a.hash[:], b.hash[:])
	})

	// A candidate must not already own another request. The old owners remain
	// occupied until their reply boundary, preserving the one-request-per-peer
	// invariant while the hedge is in flight.
	availablePeers := requestPeerLimit - len(l.requestPeers)
	if availablePeers < txOnlyTailPeerLimit {
		return plan, nil
	}
	var candidate uint64
	for _, peerID := range peerIDs {
		if peerID == 0 {
			continue
		}
		if slices.ContainsFunc(tail, func(item tailLease) bool { return item.lease.peerID == peerID }) {
			continue
		}
		if _, busy := l.requestPeers[peerID]; busy {
			continue
		}
		candidate = peerID
		break
	}
	if candidate == 0 {
		return plan, nil
	}

	request := MissingRequest{
		PeerID:      candidate,
		Transaction: true,
		ByHash:      l.byHashMode,
		Hedge:       true,
		NodeIDs:     make([][]byte, 0, len(tail)),
		NodeHashes:  make([][32]byte, 0, len(tail)),
	}
	for _, item := range tail {
		lease := item.lease
		lease.previousPeerID = lease.peerID
		lease.previousExpires = lease.expires
		lease.peerID = candidate
		lease.expires = now.Add(missingRequestLeaseTTL)
		if lease.hedges != ^uint32(0) {
			lease.hedges++
		}
		l.requestLeases[item.hash] = lease
		l.recentNodes[item.hash] = lease.peerID
		request.NodeIDs = append(request.NodeIDs, lease.nodeID.Bytes())
		request.NodeHashes = append(request.NodeHashes, item.hash)
	}
	l.refreshRequestPeersAtLocked(now)
	if len(request.NodeHashes) > 0 {
		plan.Requests = append(plan.Requests, request)
	}
	return plan, nil
}

// TakeByHashRequest returns the content hashes of up to max still-missing nodes
// per outstanding tree once the acquisition has gone aggressive (more
// no-progress timeouts than ledgerBecomeAggressiveThreshold), consuming the
// by-hash latch. Mirrors rippled InboundLedger::trigger's getNeededHashes
// branch, which past the aggressive threshold abandons path-based requests and
// asks every peer for the missing nodes by content hash — unambiguous
// placement for a node on a divergent path that path-based requests cannot
// resolve. Returns nil sets when not yet aggressive.
func (l *Ledger) TakeByHashRequest(max int) (state, tx [][32]byte) {
	state, tx, _ = l.TakeByHashRequestContext(context.Background(), max)
	return state, tx
}

// TakeByHashRequestContext is TakeByHashRequest with the expensive tree walks
// outside the acquisition mutex and cancellation propagated to the node store.
func (l *Ledger) TakeByHashRequestContext(ctx context.Context, max int) (state, tx [][32]byte, err error) {
	l.mu.Lock()
	if !l.byHash || l.timeouts <= ledgerBecomeAggressiveThreshold || l.state != StateWantState {
		l.mu.Unlock()
		return nil, nil, nil
	}
	l.byHash = false // consumed; re-armed by the next no-progress OnTimer
	if !l.haveState {
		state = missingNodeHashes(l.missingState, max)
	}
	if !l.haveTx {
		tx = missingNodeHashes(l.missingTx, max)
	}
	// A cached frontier is already sufficient to make network progress. Do not
	// follow it with another durable traversal merely to fill the other tree's
	// aggressive request; a later reply or timeout will advance that tree.
	if len(state) > 0 || len(tx) > 0 {
		l.mu.Unlock()
		return state, tx, nil
	}
	var stateMap, txMap *shamap.SHAMap
	if !l.haveState && l.stateMap != nil {
		stateMap = l.stateMap
	}
	if !l.haveTx && l.txMap != nil {
		txMap = l.txMap
	}
	l.mu.Unlock()

	if stateMap != nil {
		state, err = neededHashesContext(ctx, stateMap, max)
		if err != nil {
			l.restoreByHashAfterInterruptedWalk()
			if missingWalkYield(err) {
				return state, nil, err
			}
			return nil, nil, err
		}
	}
	if txMap != nil {
		tx, err = neededHashesContext(ctx, txMap, max)
		if err != nil {
			l.restoreByHashAfterInterruptedWalk()
			if missingWalkYield(err) {
				return state, tx, err
			}
			return nil, nil, err
		}
	}
	return state, tx, nil
}

func missingWalkYield(err error) bool {
	return errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget)
}

func neededHashesContext(ctx context.Context, m *shamap.SHAMap, max int) ([][32]byte, error) {
	missing, err := m.GetMissingNodesContext(ctx, max, nil)
	if err != nil && !missingWalkYield(err) {
		return nil, err
	}
	if len(missing) == 0 {
		return nil, err
	}
	out := make([][32]byte, 0, len(missing))
	for i := range missing {
		out = append(out, missing[i].Hash)
	}
	return out, err
}

func (l *Ledger) restoreByHashAfterInterruptedWalk() {
	l.mu.Lock()
	if l.state == StateWantState {
		l.byHash = true
	}
	l.mu.Unlock()
}

// Seq returns the ledger sequence being acquired.
func (l *Ledger) Seq() uint32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// updateSequence fills a sequence learned after a hash-only acquisition was
// registered. The acquisition's origin remains hash-only so recovery can
// distinguish it from an index-addressed request.
func (l *Ledger) updateSequence(seq uint32) {
	if l == nil || seq == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seq != 0 {
		return
	}
	l.seq = seq
	l.publishSnapshotLocked()
}

// SequenceInitiallyUnknown reports whether the acquisition was created by
// hash before its ledger sequence was available. It is immutable after New.
func (l *Ledger) SequenceInitiallyUnknown() bool {
	return l.sequenceInitiallyUnknown
}

// Hash returns the ledger hash being acquired.
func (l *Ledger) Hash() [32]byte {
	return l.hash
}

// GotBase processes the LedgerInfoBase response containing the header and root
// nodes. Rippled sends node[0]=header, node[1]=state root, and node[2]=tx root —
// but the tx root is present only when the transaction tree is non-empty
// (PeerImp.cpp:3139-3148). An empty tree (zero TxHash) is complete on arrival.
func (l *Ledger) GotBase(nodes []message.LedgerNode) error {
	return l.GotBaseContext(context.Background(), nodes)
}

func (l *Ledger) GotBaseContext(ctx context.Context, nodes []message.LedgerNode) error {
	_, err := l.GotBaseUsefulContext(ctx, nodes)
	return err
}

// GotBaseUsefulContext processes a base reply and reports how many header or
// root nodes were newly accepted. Duplicate partial replies return zero even
// while the acquisition remains in StateWantBase.
func (l *Ledger) GotBaseUsefulContext(ctx context.Context, nodes []message.LedgerNode) (useful int, err error) {
	var stored []shamap.FlushEntry
	var stateMap, txMap *shamap.SHAMap

	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return 0, err
	}

	l.mu.Lock()
	if l.state != StateWantBase {
		l.mu.Unlock()
		return 0, nil
	}
	defer func() {
		if useful > 0 {
			l.markProgressLocked()
		}
		l.publishSnapshotLocked()
		l.mu.Unlock()

		if persistErr := l.persistReceived(ctx, stored, "roots"); persistErr != nil {
			if err == nil {
				err = persistErr
			} else {
				err = errors.Join(err, persistErr)
			}
			l.mu.Lock()
			l.publishSnapshotLocked()
			l.mu.Unlock()
			return
		}
		var stateErr, txErr error
		if stateMap != nil {
			stateErr = stateMap.FinishSyncContext(ctx)
		}
		if txMap != nil {
			txErr = txMap.FinishSyncContext(ctx)
		}
		stateComplete := stateMap != nil && stateErr == nil
		txComplete := txMap != nil && txErr == nil
		if err == nil {
			if errors.Is(stateErr, shamap.ErrAcquisitionMemory) {
				err = stateErr
			} else if errors.Is(txErr, shamap.ErrAcquisitionMemory) {
				err = txErr
			}
		}

		l.mu.Lock()
		if l.state == StateWantBase || l.state == StateWantState {
			if stateComplete && l.stateMap == stateMap {
				l.haveState = true
			}
			if txComplete && l.txMap == txMap {
				l.haveTx = true
			}
			if l.state == StateWantState {
				l.recomputeComplete()
			} else {
				l.publishSnapshotLocked()
			}
		}
		l.mu.Unlock()
	}()

	if len(nodes) > hardMaxReplyNodes {
		return 0, fmt.Errorf("ledger data exceeds hardMaxReplyNodes: %d > %d", len(nodes), hardMaxReplyNodes)
	}

	if len(nodes) == 0 || len(nodes[0].NodeData) == 0 {
		return 0, fmt.Errorf("ledger base missing header")
	}

	// Parse header from node[0].
	// Rippled's sendLedgerBase() serializes with addRaw(info, s) — no prefix, no hash.
	// The data is exactly 118 bytes (SizeBase).
	h, err := header.DeserializeHeader(nodes[0].NodeData, false)
	if err != nil {
		// Try with prefix (some sources add a 4-byte prefix)
		h, err = header.DeserializePrefixedHeader(nodes[0].NodeData, false)
		if err != nil {
			return 0, fmt.Errorf("deserialize header: %w (data_len=%d)", err, len(nodes[0].NodeData))
		}
	}
	// The wire format doesn't include the hash, so recompute it and reject a
	// peer that supplied a header whose true hash (or seq, when known) doesn't
	// match what we asked for. Mirrors rippled's takeHeader (InboundLedger.cpp:830).
	//
	computed := header.CalculateHash(*h)
	if computed != l.hash || (l.seq != 0 && l.seq != h.LedgerIndex) {
		return 0, fmt.Errorf("acquire hash mismatch: computed %x != requested %x (seq %d, requested %d)",
			computed[:8], l.hash[:8], h.LedgerIndex, l.seq)
	}
	// No ledger has an empty state map, so a header with a zero account hash
	// cannot name one. The peer sent the header asked for, so it is not to
	// blame; the acquisition fails at once instead of waiting for a state root.
	if h.AccountHash == ([32]byte{}) {
		l.state = StateFailed
		l.err = fmt.Errorf("%w: ledger %d has a zero account hash", ErrHeaderRejected, h.LedgerIndex)
		return 0, l.err
	}
	if l.headerAdmission != nil {
		if err := l.headerAdmission(h.LedgerIndex); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrHeaderRejected, err)
		}
	}
	h.Hash = computed
	// When acquiring by hash alone (seq unknown), adopt the verified header's
	// seq, mirroring rippled's takeHeader (InboundLedger.cpp:839-840).
	if l.seq == 0 {
		l.seq = h.LedgerIndex
	}
	if l.header == nil {
		l.header = h
		useful++
		l.logger.Info("inbound ledger: got header",
			"seq", h.LedgerIndex,
			"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
		)
	}

	if l.transactionOnly {
		// The parent state is already local. Do not attach or persist the target
		// state root: the replay engine derives the target state and verifies it
		// against h.AccountHash before the router adopts the ledger.
		l.haveState = true
	} else if l.stateMap == nil && len(nodes) >= 2 && len(nodes[1].NodeData) > 0 {
		sm, createErr := l.newSyncMap(ctx, shamap.TypeState)
		if createErr != nil {
			return useful, fmt.Errorf("create state map: %w", createErr)
		}
		sm.SetLedgerSeq(h.LedgerIndex)
		stateRoot, rootErr := sm.AddRootNodeWithEntry(h.AccountHash, nodes[1].NodeData)
		if rootErr != nil {
			return useful, fmt.Errorf("add state root node: %w", rootErr)
		}
		l.stateMap = sm
		stateMap = sm
		l.publishSnapshotLocked()
		stored = append(stored, stateRoot)
		useful++
	}

	if h.TxHash == ([32]byte{}) {
		l.haveTx = true
	} else if l.txMap == nil && len(nodes) >= 3 && len(nodes[2].NodeData) > 0 {
		tm, terr := l.newSyncMap(ctx, shamap.TypeTransaction)
		if terr != nil {
			return useful, fmt.Errorf("create tx map: %w", terr)
		}
		tm.SetLedgerSeq(h.LedgerIndex)
		txRoot, rootErr := tm.AddRootNodeWithEntry(h.TxHash, nodes[2].NodeData)
		if rootErr != nil {
			return useful, fmt.Errorf("add tx root node: %w", rootErr)
		}
		l.txMap = tm
		txMap = tm
		l.publishSnapshotLocked()
		stored = append(stored, txRoot)
		useful++
	}

	if (!l.transactionOnly && l.stateMap == nil) || (h.TxHash != ([32]byte{}) && l.txMap == nil) {
		return useful, nil
	}
	l.state = StateWantState
	l.recomputeComplete()

	l.logger.Info("inbound ledger: roots added, fetching missing nodes",
		"seq", h.LedgerIndex,
		"have_state", l.haveState,
		"have_tx", l.haveTx,
	)

	return useful, nil
}

// GotStateNodes processes state tree nodes received from the peer.
func (l *Ledger) GotStateNodes(nodes []message.LedgerNode) error {
	_, err := l.GotStateNodesUseful(nodes)
	return err
}

// GotStateNodesUseful processes state nodes and reports how many extended the
// acquisition, so the router can stop retriggering unproductive peers.
// Completeness is established by the next missing-frontier collection.
func (l *Ledger) GotStateNodesUseful(nodes []message.LedgerNode) (int, error) {
	return l.GotStateNodesUsefulContext(context.Background(), nodes)
}

func (l *Ledger) GotStateNodesUsefulContext(ctx context.Context, nodes []message.LedgerNode) (added int, err error) {
	if err := ValidateReplyNodeCount(nodes); err != nil {
		return 0, err
	}

	var stored []shamap.FlushEntry
	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer func() {
		l.mu.Unlock()
		persistErr := l.persistReceived(ctx, stored, "state nodes")
		if persistErr != nil {
			if err == nil {
				err = persistErr
			} else {
				err = errors.Join(err, persistErr)
			}
		}
	}()

	if l.state == StateComplete || l.haveState {
		return 0, nil // State tree already acquired
	}
	if l.state != StateWantState {
		return 0, fmt.Errorf("unexpected state %d for GotStateNodes", l.state)
	}

	// Mirrors the tx-set sync fix in router.handleTxSetData (issue #413):
	// drive placement by the peer-supplied NodeID via AddKnownNodeByID
	// rather than the hash-search AddKnownNodeUnchecked, which silently
	// drops nodes whose direct parent isn't loaded yet.
	added, stored, applyErr := l.applyKnownNodes(ctx, l.stateMap, nodes, "state")
	l.stateRecv += uint64(len(nodes))
	l.stateUseful += uint64(added)
	if applyErr != nil && !errors.Is(applyErr, ErrInvalidPeerNode) && !errors.Is(applyErr, shamap.ErrAcquisitionMemory) {
		l.state = StateFailed
		l.err = applyErr
		return added, applyErr
	}

	if added > 0 {
		l.markProgressLocked()
	}
	l.refreshRequestPeersLocked()
	l.publishSnapshotLocked()
	l.logger.Debug("inbound ledger: added state nodes",
		"added", added,
		"total_received", len(nodes),
		"useful_total", l.stateUseful,
		"received_total", l.stateRecv,
	)

	return added, applyErr
}

// GotTransactionNodes processes transaction tree nodes received from the peer.
// It mirrors GotStateNodes by driving placement from the peer-supplied NodeID.
func (l *Ledger) GotTransactionNodes(nodes []message.LedgerNode) error {
	_, err := l.GotTransactionNodesUseful(nodes)
	return err
}

// GotTransactionNodesUseful is GotTransactionNodes with useful-node
// accounting for reply-peer selection. Completeness is established by the
// next missing-frontier collection.
func (l *Ledger) GotTransactionNodesUseful(nodes []message.LedgerNode) (int, error) {
	return l.GotTransactionNodesUsefulContext(context.Background(), nodes)
}

func (l *Ledger) GotTransactionNodesUsefulContext(ctx context.Context, nodes []message.LedgerNode) (added int, err error) {
	if err := ValidateReplyNodeCount(nodes); err != nil {
		return 0, err
	}

	var stored []shamap.FlushEntry
	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer func() {
		l.mu.Unlock()
		persistErr := l.persistReceived(ctx, stored, "transaction nodes")
		if persistErr != nil {
			if err == nil {
				err = persistErr
			} else {
				err = errors.Join(err, persistErr)
			}
		}
	}()

	if l.state == StateComplete || l.haveTx {
		return 0, nil // Transaction tree already acquired (or empty)
	}
	if l.state != StateWantState || l.txMap == nil {
		return 0, fmt.Errorf("unexpected state %d for GotTransactionNodes", l.state)
	}

	added, stored, applyErr := l.applyKnownNodes(ctx, l.txMap, nodes, "tx")
	l.txRecv += uint64(len(nodes))
	l.txUseful += uint64(added)
	if applyErr != nil && !errors.Is(applyErr, ErrInvalidPeerNode) && !errors.Is(applyErr, shamap.ErrAcquisitionMemory) {
		l.state = StateFailed
		l.err = applyErr
		return added, applyErr
	}

	if added > 0 {
		l.markProgressLocked()
	}
	l.refreshRequestPeersLocked()
	l.publishSnapshotLocked()
	l.logger.Debug("inbound ledger: added tx nodes",
		"added", added,
		"total_received", len(nodes),
		"useful_total", l.txUseful,
		"received_total", l.txRecv,
	)

	return added, applyErr
}

// applyKnownNodes places peer-supplied tree nodes by NodeID, returning the
// number freshly attached. A node whose ancestor is still a hash-only stub
// (NodeReRequest) is dropped without counting as a reject — the next
// getMissingNodes walk re-requests the correct frontier and it returns on a
// later reply. Invalid nodes return ErrInvalidPeerNode so the caller can
// charge their source while keeping the acquisition recoverable.
// The first genuinely invalid node stops harvesting the rest of the reply.
// Caller holds l.mu.
func (l *Ledger) applyKnownNodes(ctx context.Context, m *shamap.SHAMap, nodes []message.LedgerNode, label string) (int, []shamap.FlushEntry, error) {
	added := 0
	stored := make([]shamap.FlushEntry, 0, len(nodes))
	for _, node := range nodes {
		parsedID, err := node.SHAMapNodeID()
		if err != nil {
			l.rejectCount++
			l.lastRejectErr = err.Error()
			return added, stored, fmt.Errorf("%w: %s node reference: %v", ErrInvalidPeerNode, label, err)
		}
		if parsedID.IsRoot() {
			continue
		}
		// A reply for a cached path is no longer an outstanding frontier item,
		// whether it proves useful, is already local, or contains bad data. A
		// failed/invalid attachment therefore forces discovery of a fresh path
		// instead of blindly retrying the same cached NodeID forever.
		l.removeMissingNodeLocked(m == l.txMap, parsedID)
		res, entry, err := m.AddKnownNodeByIDWithEntryContext(ctx, parsedID, node.NodeData)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return added, stored, err
		}
		if errors.Is(err, shamap.ErrAcquisitionMemory) {
			return added, stored, err
		}
		switch res {
		case shamap.NodeUseful:
			added++
			if err != nil {
				return added, stored, fmt.Errorf("prepare verified %s node for persistence: %w", label, err)
			}
			stored = append(stored, entry)
			if hash, ok := l.requestByNode[requestNodeKey{transaction: m == l.txMap, nodeID: parsedID}]; ok {
				l.fulfillLeaseLocked(hash, 0)
			}
		case shamap.NodeDuplicate:
			// Already present, or ahead of its frontier: neither progress nor
			// a reject. A duplicate still validates the requested frontier.
			if hash, ok := l.requestByNode[requestNodeKey{transaction: m == l.txMap, nodeID: parsedID}]; ok {
				l.fulfillLeaseLocked(hash, 0)
			}
		case shamap.NodeReRequest:
			// The requested frontier remains unresolved and is retried by the
			// next missing-node walk.
		default: // NodeInvalid, or any unrecognized result — reject conservatively.
			l.rejectCount++
			if err != nil {
				l.lastRejectErr = err.Error()
			}
			l.logger.Debug("inbound ledger: "+label+" node rejected",
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err)
			return added, stored, fmt.Errorf("%w: %s node rejected", ErrInvalidPeerNode, label)
		}
	}
	return added, stored, nil
}

func (l *Ledger) retirementError() error {
	if l.retired.Load() {
		return errAcquisitionCanceled
	}
	return nil
}

func (l *Ledger) persistenceResult(ctx context.Context, err error) error {
	if err == nil {
		return l.retirementError()
	}
	if errors.Is(err, ErrAcquisitionRetired) && l.retired.Load() {
		return errAcquisitionCanceled
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrPersistence, err)
}

func (l *Ledger) persistReceived(ctx context.Context, entries []shamap.FlushEntry, label string) error {
	if err := l.retirementError(); err != nil {
		return err
	}
	if l.family == nil || len(entries) == 0 {
		return nil
	}
	if err := l.persistenceResult(ctx, l.family.StoreBatch(ctx, entries)); err != nil {
		if errors.Is(err, ErrPersistence) {
			l.logger.Warn("inbound ledger: failed to persist verified "+label, "error", err)
		} else {
			l.logger.Debug("inbound ledger: persistence canceled", "nodes", label, "error", err)
		}
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// FlushPersistence waits for every verified node queued by this acquisition.
func (l *Ledger) FlushPersistence(ctx context.Context) error {
	if err := l.retirementError(); err != nil {
		return err
	}
	flusher, ok := l.family.(interface {
		Flush(context.Context) error
	})
	if !ok {
		return nil
	}
	return l.persistenceResult(ctx, flusher.Flush(ctx))
}

// CheckpointPersistence bounds the resident acquisition tree after enough
// newly useful nodes have been queued for durable storage.
func (l *Ledger) CheckpointPersistence(ctx context.Context, useful int) error {
	return l.checkpointPersistence(ctx, useful, false)
}

// CheckpointMemory flushes and acknowledges received nodes before acquisition
// resumes from a memory-pressure wait.
func (l *Ledger) CheckpointMemory(ctx context.Context) error {
	return l.checkpointPersistence(ctx, 0, true)
}

func (l *Ledger) checkpointPersistence(ctx context.Context, useful int, force bool) error {
	if l.family == nil || (useful <= 0 && !force) {
		return nil
	}
	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return err
	}

	l.mu.Lock()
	if useful > 0 {
		l.checkpointUseful += uint64(useful)
	}
	if !force && l.checkpointUseful < persistenceCheckpointNodes {
		l.mu.Unlock()
		return nil
	}
	checkpointed := l.checkpointUseful
	stateMap, txMap := l.stateMap, l.txMap
	l.mu.Unlock()

	if err := l.FlushPersistence(ctx); err != nil {
		return err
	}
	for _, m := range []*shamap.SHAMap{stateMap, txMap} {
		if m == nil {
			continue
		}
		var err error
		if force {
			err = m.AcknowledgePersistedAndReleaseContext(ctx)
		} else {
			err = m.AcknowledgePersistedContext(ctx)
		}
		if err := l.persistenceResult(ctx, err); err != nil {
			return err
		}
	}

	l.mu.Lock()
	if err := l.retirementError(); err != nil {
		l.mu.Unlock()
		return err
	}
	if force {
		l.memoryCheckpoints++
	}
	if l.checkpointUseful >= checkpointed {
		l.checkpointUseful -= checkpointed
	} else {
		l.checkpointUseful = 0
	}
	l.mu.Unlock()
	return nil
}

// PromotePersistence acknowledges flushed acquisition writes and binds the
// maps to the underlying Family for use by adopted ledgers.
func (l *Ledger) PromotePersistence(ctx context.Context) error {
	if err := l.retirementError(); err != nil {
		return err
	}
	promoter, ok := l.family.(interface {
		Promote(context.Context) (shamap.Family, error)
	})
	if !ok {
		return nil
	}

	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return err
	}
	l.mu.Lock()
	stateMap, txMap := l.stateMap, l.txMap
	l.mu.Unlock()
	family, err := promoter.Promote(ctx)
	if err := l.persistenceResult(ctx, err); err != nil {
		return err
	}
	if stateMap != nil {
		if err := l.persistenceResult(ctx, stateMap.AcknowledgePersistedContext(ctx)); err != nil {
			return err
		}
	}
	if txMap != nil {
		if err := l.persistenceResult(ctx, txMap.AcknowledgePersistedContext(ctx)); err != nil {
			return err
		}
	}
	for _, m := range []*shamap.SHAMap{stateMap, txMap} {
		if m != nil {
			m.SetFamily(family)
		}
	}
	return l.retirementError()
}

// RetirePersistence releases persistence state owned by an acquisition that
// will perform no more writes. It is idempotent and does not surface an
// abandoned acquisition's write failure as a failure of another acquisition.
func (l *Ledger) RetirePersistence(ctx context.Context) error {
	l.ReleaseAcquisitionMemory()
	retirer, ok := l.family.(interface {
		Retire(context.Context) error
	})
	if !ok {
		return nil
	}
	return retirer.Retire(ctx)
}

// ReleaseAcquisitionMemory ends acquisition ownership after publication. It
// leaves persistence available to adopted ledger snapshots.
// RetirePersistence is for abandoned scopes, not successful handoffs.
func (l *Ledger) ReleaseAcquisitionMemory() {
	l.retired.Store(true)
	l.releaseRetiredMemory()
}

func (l *Ledger) finishWork() {
	l.workMu.Unlock()
	l.releaseRetiredMemory()
}

func (l *Ledger) releaseRetiredMemory() {
	if !l.retired.Load() || !l.workMu.TryLock() {
		return
	}
	defer l.workMu.Unlock()
	l.mu.Lock()
	stateMap, txMap := l.stateMap, l.txMap
	requestMetadata := l.releaseAllRequestMetadataLocked()
	l.mu.Unlock()
	if requestMetadata != 0 && l.memory != nil {
		l.memory.Release(requestMetadata)
	}
	for _, m := range []*shamap.SHAMap{stateMap, txMap} {
		if m != nil {
			m.ReleaseAcquisitionBudget()
		}
	}
}

// recomputeComplete promotes the acquisition to StateComplete once both the
// account-state and transaction trees are in hand, mirroring rippled's
// complete_ = mHaveHeader && mHaveState && mHaveTransactions
// (InboundLedger.cpp:734,946). Caller must hold l.mu.
func (l *Ledger) recomputeComplete() {
	if l.haveState && l.haveTx && l.state != StateFailed && !l.retired.Load() {
		l.state = StateComplete
		l.logger.Info("inbound ledger: acquisition complete", "seq", l.header.LedgerIndex)
	}
	l.publishSnapshotLocked()
}

func (l *Ledger) finishReceivedTree(ctx context.Context, m *shamap.SHAMap, transaction bool) {
	if m == nil {
		return
	}
	if err := m.FinishSyncContext(ctx); err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retired.Load() || l.state != StateWantState && l.state != StateComplete {
		return
	}
	if transaction {
		if l.txMap != m || l.haveTx {
			return
		}
		l.haveTx = true
		l.missingTx = nil
		l.neededTx = nil
	} else {
		if l.stateMap != m || l.haveState {
			return
		}
		l.haveState = true
		l.missingState = nil
		l.neededState = nil
	}
	l.clearTreeReservationsLocked(transaction, SystemClock.Now())
	l.recomputeComplete()
}

// missingNodeBatch caps NodeIDs per TMGetLedger request on the inspection
// queries. Sits between rippled's blind-request cap (reqNodes=12) and reply
// cap (reqNodesReply=128, InboundLedger.cpp).
const missingNodeBatch = 16

// Request-path widths, matching rippled InboundLedger.cpp: collect up to
// missingNodesFind before the recentNodes de-dup, then cap the request at
// reqNodesReply on a reply and reqNodes on a timeout fan-out. The wide
// pre-dedup collect keeps per-reply frontier coverage at ~128 nodes/RTT.
const (
	missingNodesFind = 256
	reqNodesReply    = 128
	reqNodes         = 12
	requestPeerLimit = 6
)

type missingNodeExclusionFilter map[[32]byte]struct{}

func (f missingNodeExclusionFilter) ShouldFetch(hash [32]byte) bool {
	_, excluded := f[hash]
	return !excluded
}

// NeedsMissingNodeIDs returns up to missingNodeBatch wire-encoded
// path-based NodeIDs of missing SHAMap inner nodes, ordered by depth.
// Returns nil if the state map is complete or not yet ready (issue #395).
func (l *Ledger) NeedsMissingNodeIDs() [][]byte {
	l.workMu.Lock()
	defer l.finishWork()
	l.mu.Lock()
	if l.stateMap == nil || l.haveState || l.state != StateWantState {
		l.mu.Unlock()
		return nil
	}
	stateMap := l.stateMap
	l.mu.Unlock()

	missing := stateMap.GetMissingNodes(missingNodeBatch, nil)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stateMap != stateMap || l.haveState || l.state != StateWantState {
		return nil
	}
	l.cacheMissingLocked(false, missing)
	return missingNodeIDs(missing)
}

// NeedsMissingTxNodeIDs returns up to missingNodeBatch wire-encoded NodeIDs of
// missing transaction-tree inner nodes, mirroring NeedsMissingNodeIDs for the
// tx map. Returns nil once the tx tree is complete (or empty).
func (l *Ledger) NeedsMissingTxNodeIDs() [][]byte {
	l.workMu.Lock()
	defer l.finishWork()
	l.mu.Lock()
	if l.txMap == nil || l.haveTx || l.state != StateWantState {
		l.mu.Unlock()
		return nil
	}
	txMap := l.txMap
	l.mu.Unlock()

	missing := txMap.GetMissingNodes(missingNodeBatch, nil)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.txMap != txMap || l.haveTx || l.state != StateWantState {
		return nil
	}
	l.cacheMissingLocked(true, missing)
	return missingNodeIDs(missing)
}

// missingNodeIDs returns up to missingNodeBatch wire-encoded path-based NodeIDs
// from a missing-node result, or nil when the map is complete.
func missingNodeIDs(missing []shamap.MissingNode) [][]byte {
	if len(missing) == 0 {
		return nil
	}
	nodeIDs := make([][]byte, 0, len(missing))
	for i := range missing {
		nodeIDs = append(nodeIDs, missing[i].NodeID.Bytes())
	}
	return nodeIDs
}

// CollectMissingRequest returns wire-encoded NodeIDs for the first incomplete
// tree (state before transactions), de-duplicated against nodes already asked
// for this timer interval. It is the request-path counterpart to the pure
// NeedsMissing* inspection queries and the choke point for the re-request
// throttle. isReply distinguishes the two trigger paths (rippled
// InboundLedger::filterNodes(reason)): a reply drops already-requested nodes and
// returns nil when the whole missing set is duplicates (taming the per-reply
// spin); a timeout bypasses that short-circuit so the fan-out still queries every
// peer.
func (l *Ledger) CollectMissingRequest(isReply bool) (state, txn [][]byte) {
	state, txn, _, _ = l.CollectMissingRequestContext(context.Background(), isReply)
	return state, txn
}

// CollectMissingRequestContext performs the backing-store traversal without
// holding the acquisition mutex. The active tree is checked again before
// recent-node filtering or completion is committed.
func (l *Ledger) CollectMissingRequestContext(ctx context.Context, isReply bool) (state, txn [][]byte, complete bool, err error) {
	l.workMu.Lock()
	defer l.finishWork()
	l.mu.Lock()
	if l.state != StateWantState || l.retired.Load() {
		complete = l.state == StateComplete
		l.mu.Unlock()
		return nil, nil, complete, nil
	}
	var m *shamap.SHAMap
	stateTree := false
	if l.stateMap != nil && !l.haveState {
		m = l.stateMap
		stateTree = true
	} else if l.txMap != nil && !l.haveTx {
		m = l.txMap
	}
	if m != nil && !l.reserveRequestMetadataLocked() {
		l.lastScanStatus = shamap.MissingNodesMemory
		l.mu.Unlock()
		return nil, nil, false, shamap.ErrAcquisitionMemory
	}
	l.mu.Unlock()
	if m == nil {
		return nil, nil, false, nil
	}
	if !isReply {
		l.mu.Lock()
		var cached []shamap.MissingNode
		if stateTree && l.stateMap == m && !l.haveState {
			cached = l.missingState
		} else if !stateTree && l.txMap == m && !l.haveTx {
			cached = l.missingTx
		}
		if len(cached) > 0 {
			nodeIDs := l.filterMissingResultLocked(cached, false, !stateTree)
			l.mu.Unlock()
			if stateTree {
				return nodeIDs, nil, false, nil
			}
			return nil, nodeIDs, false, nil
		}
		l.mu.Unlock()
	}

	started := time.Now()
	missing, scanStatus, scanErr := scanMissingNodesContext(ctx, m, missingNodesFind, nil)
	if scanErr == nil && scanStatus == shamap.MissingNodesWaiting && len(missing) == 0 {
		// A backed scanner pass can finish with no IDs while it is still
		// materializing a blocked lane. The legacy timer contract must still
		// expose the bounded frontier, so use the existing bounded walk as a
		// compatibility fallback for this transient status.
		fallback, fallbackErr := m.GetMissingNodesContext(ctx, missingNodesFind, nil)
		missing = fallback
		scanErr = fallbackErr
		if fallbackErr == nil && len(fallback) >= missingNodesFind {
			scanStatus = shamap.MissingNodesMore
		} else if fallbackErr == nil && len(fallback) == 0 {
			scanStatus = shamap.MissingNodesComplete
		}
	}
	err = scanErr
	if err == nil {
		switch scanStatus {
		case shamap.MissingNodesMore:
			if len(missing) < missingNodesFind {
				err = shamap.ErrTraversalBudget
			}
		case shamap.MissingNodesMemory, shamap.MissingNodesPersistence:
			err = shamap.ErrAcquisitionMemory
		}
	}
	l.mu.Lock()
	l.lastScanStatus = scanStatus
	l.mu.Unlock()
	if err != nil && (!missingWalkYield(err) || len(missing) == 0) {
		return nil, nil, false, err
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		l.logger.Info("inbound ledger: missing-node walk delayed", "elapsed", elapsed, "missing", len(missing), "reply", isReply)
	}

	var finishErr error
	if len(missing) == 0 && scanStatus == shamap.MissingNodesComplete {
		finishErr = m.FinishSyncContext(ctx)
		if missingWalkYield(finishErr) {
			return nil, nil, false, finishErr
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateWantState {
		return nil, nil, l.state == StateComplete, nil
	}
	if stateTree {
		if l.stateMap != m || l.haveState {
			return nil, nil, l.state == StateComplete, nil
		}
	} else if l.txMap != m || l.haveTx || !l.haveState {
		return nil, nil, l.state == StateComplete, nil
	}

	if len(missing) == 0 {
		if scanStatus == shamap.MissingNodesComplete && finishErr == nil {
			if stateTree {
				l.haveState = true
				l.missingState = nil
				l.neededState = nil
				if l.txMap != nil && !l.haveTx && l.txRecv > 0 && len(l.missingTx) == 0 {
					txMap := l.txMap
					l.mu.Unlock()
					txFinishErr := txMap.FinishSyncContext(ctx)
					l.mu.Lock()
					if txFinishErr == nil && l.txMap == txMap && !l.haveTx {
						l.haveTx = true
						l.neededTx = nil
					}
				}
			} else {
				l.haveTx = true
				l.missingTx = nil
				l.neededTx = nil
			}
			l.recomputeComplete()
			return nil, nil, l.state == StateComplete, nil
		}
		return nil, nil, false, err
	}

	nodeIDs := l.filterMissingResultLocked(missing, isReply, !stateTree)
	if stateTree {
		l.cacheMissingLocked(false, missing)
		return nodeIDs, nil, false, err
	}
	l.cacheMissingLocked(true, missing)
	return nil, nodeIDs, false, err
}

// filterMissingResultLocked applies the request throttle to a frontier already
// collected outside the acquisition lock. Caller holds l.mu.
func (l *Ledger) filterMissingResultLocked(missing []shamap.MissingNode, isReply, transaction bool) [][]byte {
	fresh := make([]shamap.MissingNode, 0, len(missing))
	for i := range missing {
		if _, dup := l.requestLeases[missing[i].Hash]; !dup {
			if _, queued := l.readySet[missing[i].Hash]; queued {
				continue
			}
			fresh = append(fresh, missing[i])
		}
	}
	use := fresh
	if len(fresh) == 0 {
		// Every outstanding node was already requested this interval. On a reply
		// send nothing (stops the spin); on a timeout re-query everyone.
		if isReply {
			return nil
		}
		use = missing
	}
	limit := reqNodes
	if isReply {
		limit = reqNodesReply
	}
	if len(use) > limit {
		use = use[:limit]
	}
	nodeIDs := make([][]byte, 0, len(use))
	for i := range use {
		if lease, reserved := l.requestLeases[use[i].Hash]; reserved {
			if lease.wireShadow {
				continue
			}
			if isReply {
				continue
			}
			nodeIDs = append(nodeIDs, use[i].NodeID.Bytes())
			continue
		}
		if l.requestAvailableLocked() == 0 || !l.reserveMissingNodeLocked(use[i], transaction, 0, time.Now()) {
			break
		}
		nodeIDs = append(nodeIDs, use[i].NodeID.Bytes())
	}
	return nodeIDs
}

// CollectMissingReplyRequests reserves disjoint reply-driven frontiers for the
// supplied peers. State nodes are preferred, while peers left over after the
// available state frontier is reserved may work on the transaction tree.
func (l *Ledger) CollectMissingReplyRequests(peerIDs []uint64) ([]MissingRequest, bool) {
	requests, complete, _ := l.CollectMissingReplyRequestsContext(context.Background(), peerIDs)
	return requests, complete
}

// CollectMissingReplyRequestsContext is CollectMissingReplyRequests with
// cancellation propagated to backing-store traversal.
func (l *Ledger) CollectMissingReplyRequestsContext(ctx context.Context, peerIDs []uint64) ([]MissingRequest, bool, error) {
	return l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodesReply, false, false)
}

// CollectMissingAddedRequestsContext reserves small blind frontiers for peers
// newly admitted to an active acquisition.
func (l *Ledger) CollectMissingAddedRequestsContext(ctx context.Context, peerIDs []uint64) ([]MissingRequest, bool, error) {
	return l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodes, true, false)
}

// CollectMissingCachedAddedRequests assigns newly-added peers work from the
// last verified frontier without touching the backing store. It is used only
// by the timeout path, after CollectMissingRequestContext has guaranteed that
// a frontier exists (or established that discovery itself must continue).
func (l *Ledger) CollectMissingCachedAddedRequests(peerIDs []uint64) []MissingRequest {
	l.workMu.Lock()
	defer l.finishWork()
	l.mu.Lock()
	defer l.mu.Unlock()

	available := requestPeerLimit - len(l.requestPeers)
	peers := make([]uint64, 0, min(len(peerIDs), available))
	for _, peerID := range peerIDs {
		if available == 0 {
			break
		}
		if peerID == 0 || slices.Contains(peers, peerID) {
			continue
		}
		if _, busy := l.requestPeers[peerID]; busy {
			continue
		}
		peers = append(peers, peerID)
		available--
	}
	if l.state != StateWantState || len(peers) == 0 {
		return nil
	}

	requests := make([]MissingRequest, 0, len(peers))
	for _, transaction := range []bool{false, true} {
		var frontier []shamap.MissingNode
		if transaction {
			if !l.haveTx {
				frontier = l.missingTx
			}
		} else if !l.haveState {
			frontier = l.missingState
		}
		fresh := make([]shamap.MissingNode, 0, len(frontier))
		for i := range frontier {
			if _, reserved := l.requestLeases[frontier[i].Hash]; !reserved {
				fresh = append(fresh, frontier[i])
			}
		}
		for len(peers) > 0 && len(fresh) > 0 {
			peerID := peers[0]
			peers = peers[1:]
			n := min(reqNodes, len(fresh))
			batch := fresh[:n]
			fresh = fresh[n:]
			request := MissingRequest{
				PeerID:      peerID,
				NodeIDs:     make([][]byte, 0, n),
				NodeHashes:  make([][32]byte, 0, n),
				Transaction: transaction,
				Blind:       true,
			}
			for i := range batch {
				request.NodeIDs = append(request.NodeIDs, batch[i].NodeID.Bytes())
				request.NodeHashes = append(request.NodeHashes, batch[i].Hash)
				if !l.reserveMissingNodeLocked(batch[i], transaction, peerID, time.Now()) {
					request.NodeIDs = request.NodeIDs[:len(request.NodeIDs)-1]
					request.NodeHashes = request.NodeHashes[:len(request.NodeHashes)-1]
					break
				}
			}
			if len(request.NodeHashes) == 0 {
				continue
			}
			l.requestPeers[peerID] = struct{}{}
			requests = append(requests, request)
		}
		if len(peers) == 0 {
			break
		}
	}
	return requests
}

func (l *Ledger) collectMissingPeerRequestsContext(ctx context.Context, peerIDs []uint64, batchLimit int, blind, useScanner bool) ([]MissingRequest, bool, error) {
	l.workMu.Lock()
	defer l.finishWork()
	if l.retired.Load() {
		return nil, l.IsComplete(), nil
	}
	if len(peerIDs) == 0 {
		return nil, l.IsComplete(), nil
	}
	l.mu.Lock()
	l.expireMissingLeasesLocked(time.Now(), false)
	available := requestPeerLimit - len(l.requestPeers)
	peers := make([]uint64, 0, min(len(peerIDs), available))
	for _, peerID := range peerIDs {
		if available == 0 {
			break
		}
		if peerID == 0 || slices.Contains(peers, peerID) {
			continue
		}
		if _, busy := l.requestPeers[peerID]; busy {
			continue
		}
		peers = append(peers, peerID)
		available--
	}
	if len(peers) > 0 && l.state == StateWantState && !l.reserveRequestMetadataLocked() {
		l.lastScanStatus = shamap.MissingNodesMemory
		l.mu.Unlock()
		return nil, false, shamap.ErrAcquisitionMemory
	}
	complete := l.state == StateComplete
	firstTransaction := l.nextRequestTx
	l.nextRequestTx = !l.nextRequestTx
	statePending := l.stateMap != nil && !l.haveState
	txPending := l.txMap != nil && !l.haveTx
	l.mu.Unlock()
	if len(peers) == 0 {
		l.mu.Lock()
		l.lastScanStatus = shamap.MissingNodesWaiting
		l.mu.Unlock()
		return nil, complete, nil
	}
	requests := make([]MissingRequest, 0, len(peers))
	var yieldErr error
	transactionOrder := []bool{false, true}
	if firstTransaction {
		transactionOrder = []bool{true, false}
	}
	for orderIndex, transaction := range transactionOrder {
		if len(peers) == 0 {
			break
		}
		treeQuota := len(peers)
		if statePending && txPending && orderIndex == 0 {
			treeQuota = (len(peers) + 1) / 2
		}
		treeAssigned := 0
		l.mu.Lock()
		if l.state != StateWantState {
			complete := l.state == StateComplete
			l.mu.Unlock()
			return requests, complete, nil
		}
		var m *shamap.SHAMap
		if transaction {
			if l.txMap != nil && !l.haveTx {
				m = l.txMap
			}
		} else if l.stateMap != nil && !l.haveState {
			m = l.stateMap
		}
		if m == nil {
			l.mu.Unlock()
			continue
		}
		l.mu.Unlock()

		for len(peers) > 0 && treeAssigned < treeQuota {
			l.mu.Lock()
			peerID := peers[0]
			ready := make([]shamap.MissingNode, 0, batchLimit)
			for len(ready) < batchLimit {
				node, ok := l.claimReadyNodeLocked(transaction, peerID, time.Now(), useScanner)
				if !ok {
					break
				}
				ready = append(ready, node)
			}
			if len(ready) == 0 {
				l.mu.Unlock()
				break
			}
			peers = peers[1:]
			l.requestPeers[peerID] = struct{}{}
			nodeIDs := make([][]byte, 0, len(ready))
			nodeHashes := make([][32]byte, 0, len(ready))
			for _, node := range ready {
				nodeIDs = append(nodeIDs, node.NodeID.Bytes())
				nodeHashes = append(nodeHashes, node.Hash)
			}
			requests = append(requests, MissingRequest{
				PeerID:      peerID,
				NodeIDs:     nodeIDs,
				NodeHashes:  nodeHashes,
				Transaction: transaction,
				Blind:       blind,
			})
			treeAssigned++
			l.mu.Unlock()
		}

		var filter shamap.SyncFilter
		restarted := false
		pending := make([]shamap.MissingNode, 0, min(treeQuota*batchLimit, l.requestReservationLimit()))
		pendingSet := make(map[[32]byte]struct{}, cap(pending))
		for len(peers) > 0 && treeAssigned < treeQuota {
			var missing []shamap.MissingNode
			var scanStatus shamap.MissingNodesStatus
			var err error
			if useScanner {
				missing, scanStatus, err = scanMissingNodesContext(ctx, m, missingNodesFind, filter)
			} else {
				missing, err = m.GetMissingNodesContext(ctx, missingNodesFind, filter)
				scanStatus = shamap.MissingNodesWaiting
				if err == nil && len(missing) >= missingNodesFind {
					scanStatus = shamap.MissingNodesMore
				} else if err == nil && len(missing) == 0 {
					scanStatus = shamap.MissingNodesComplete
				}
			}
			l.mu.Lock()
			l.lastScanStatus = scanStatus
			restart := scanStatus == shamap.MissingNodesWaiting && len(missing) == 0 &&
				len(pending) == 0 && len(l.requestLeases) == 0 && len(l.readyRequests) == 0 && !restarted
			l.mu.Unlock()
			if restart && err == nil {
				restarted = true
				continue
			}
			if err != nil && !missingWalkYield(err) {
				l.releaseMissingRequests(requests)
				return nil, false, err
			}
			if err != nil && (yieldErr == nil || errors.Is(err, shamap.ErrAcquisitionMemory)) {
				yieldErr = err
			}
			if err != nil && len(missing) == 0 && len(pending) == 0 {
				return requests, false, yieldErr
			}
			traversalExhausted := scanStatus != shamap.MissingNodesMore ||
				(scanStatus == shamap.MissingNodesMore && len(missing) == 0 && len(pending) > 0)
			if len(missing) == 0 && len(pending) == 0 && scanStatus != shamap.MissingNodesWaiting {
				if filter != nil || scanStatus == shamap.MissingNodesWaiting {
					break
				}
				finishErr := m.FinishSyncContext(ctx)
				if missingWalkYield(finishErr) {
					return requests, false, finishErr
				}
				l.mu.Lock()
				if l.state != StateWantState || (!transaction && (l.stateMap != m || l.haveState)) ||
					(transaction && (l.txMap != m || l.haveTx)) {
					complete := l.state == StateComplete
					l.mu.Unlock()
					return requests, complete, nil
				}
				if finishErr != nil {
					l.mu.Unlock()
					return requests, false, yieldErr
				}
				if transaction {
					l.haveTx = true
					l.missingTx = nil
					l.neededTx = nil
				} else {
					l.haveState = true
					l.missingState = nil
					l.neededState = nil
					l.clearTreeReservationsLocked(false, time.Now())
				}
				l.recomputeComplete()
				complete := l.state == StateComplete
				l.mu.Unlock()
				if complete {
					return requests, true, nil
				}
				break
			}

			l.mu.Lock()
			if l.state != StateWantState || (!transaction && (l.stateMap != m || l.haveState)) ||
				(transaction && (l.txMap != m || l.haveTx)) {
				complete := l.state == StateComplete
				l.mu.Unlock()
				return requests, complete, nil
			}
			// Filtered walks expose extra work for additional peers, but the
			// unfiltered result remains the authoritative timeout retry frontier.
			if filter == nil && ((!transaction && len(l.missingState) == 0) || (transaction && len(l.missingTx) == 0)) {
				l.cacheMissingLocked(transaction, missing)
			}
			if len(missing) == 0 && scanStatus == shamap.MissingNodesWaiting && len(pending) == 0 {
				cached := l.missingState
				if transaction {
					cached = l.missingTx
				}
				for _, node := range cached {
					if _, exists := l.requestLeases[node.Hash]; exists {
						continue
					}
					if _, queued := l.readySet[node.Hash]; queued {
						continue
					}
					if _, queued := pendingSet[node.Hash]; queued {
						continue
					}
					pending = append(pending, node)
					pendingSet[node.Hash] = struct{}{}
				}
			}
			for i := range missing {
				if _, exists := l.requestLeases[missing[i].Hash]; exists {
					continue
				}
				if _, queued := l.readySet[missing[i].Hash]; queued {
					continue
				}
				if _, queued := pendingSet[missing[i].Hash]; queued {
					continue
				}
				pending = append(pending, missing[i])
				pendingSet[missing[i].Hash] = struct{}{}
			}
			for len(peers) > 0 && len(pending) > 0 {
				if !traversalExhausted && len(pending) < batchLimit && len(peers) > 1 {
					break
				}
				peerID := peers[0]
				peers = peers[1:]
				if peerID == 0 {
					continue
				}
				if _, busy := l.requestPeers[peerID]; busy {
					continue
				}
				if len(l.requestPeers) >= requestPeerLimit {
					l.mu.Unlock()
					return requests, false, yieldErr
				}
				n := min(batchLimit, len(pending))
				if scanStatus == shamap.MissingNodesWaiting && len(peers) > 0 {
					n = min(n, max(1, (len(pending)+len(peers))/(len(peers)+1)))
				}
				if n == 0 {
					break
				}
				batch := pending[:n]
				request := MissingRequest{
					PeerID:      peerID,
					NodeIDs:     make([][]byte, 0, n),
					NodeHashes:  make([][32]byte, 0, n),
					Transaction: transaction,
					Blind:       blind,
				}
				assigned := 0
				for i := range batch {
					request.NodeIDs = append(request.NodeIDs, batch[i].NodeID.Bytes())
					request.NodeHashes = append(request.NodeHashes, batch[i].Hash)
					if !l.reserveMissingNodeLocked(batch[i], transaction, request.PeerID, time.Now()) {
						request.NodeIDs = request.NodeIDs[:len(request.NodeIDs)-1]
						request.NodeHashes = request.NodeHashes[:len(request.NodeHashes)-1]
						break
					}
					assigned++
				}
				if len(request.NodeHashes) == 0 {
					break
				}
				l.requestPeers[request.PeerID] = struct{}{}
				requests = append(requests, request)
				treeAssigned++
				for i := 0; i < assigned; i++ {
					delete(pendingSet, pending[i].Hash)
				}
				pending = pending[assigned:]
				if !useScanner && err != nil {
					break
				}
			}
			if len(peers) == 0 || (!useScanner && err != nil) ||
				(useScanner && (scanStatus == shamap.MissingNodesMemory || scanStatus == shamap.MissingNodesPersistence)) {
				l.mu.Unlock()
				return requests, false, yieldErr
			}
			if traversalExhausted {
				l.mu.Unlock()
				break
			}
			if len(pending) > 0 && len(pending) < batchLimit && len(peers) > 1 {
				// A resumable walk may expose the tail of one slice. Keep that
				// tail local until another filtered slice can fill a full wire
				// batch, reserving underfilled requests for the final peer only.
				excluded := make(missingNodeExclusionFilter, len(l.recentNodes)+len(pending))
				for hash := range l.recentNodes {
					excluded[hash] = struct{}{}
				}
				for hash := range pendingSet {
					excluded[hash] = struct{}{}
				}
				filter = excluded
				l.mu.Unlock()
				continue
			}
			excluded := make(missingNodeExclusionFilter, len(l.recentNodes)+len(pending))
			for hash := range l.recentNodes {
				excluded[hash] = struct{}{}
			}
			for hash := range pendingSet {
				excluded[hash] = struct{}{}
			}
			filter = excluded
			l.mu.Unlock()
		}
	}
	return requests, false, yieldErr
}

func (l *Ledger) releaseMissingRequests(requests []MissingRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := SystemClock.Now()
	for _, request := range requests {
		for _, hash := range request.NodeHashes {
			if lease, ok := l.requestLeases[hash]; ok && lease.peerID == request.PeerID {
				if lease.previousPeerID != 0 {
					// A failed hedge assignment must not discard the original
					// wire request. Requeue the logical lease while retaining its
					// previous owner; the next claim can use the ready reserve
					// without opening a seventh wire slot.
					l.requeueLeaseLocked(hash, now)
					continue
				}
				l.removeLeaseLocked(hash)
			}
		}
	}
	l.refreshRequestPeersLocked()
}

// ReleaseMissingRequest makes a failed peer assignment immediately eligible
// for another reply-driven request.
func (l *Ledger) ReleaseMissingRequest(peerID uint64, hashes [][32]byte) {
	l.releaseMissingRequests([]MissingRequest{{PeerID: peerID, NodeHashes: hashes}})
}

// ReleaseUnreservedMissingNodes makes a failed timeout fan-out eligible for a
// later peer without disturbing peer-owned reservations.
func (l *Ledger) ReleaseUnreservedMissingNodes() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for hash, lease := range l.requestLeases {
		if lease.wireShadow {
			continue
		}
		if lease.peerID == 0 && len(l.readyRequests) < missingRequestReadyReserve {
			if released, ok := l.removeLeaseLocked(hash); ok {
				l.enqueueReadyLeaseLocked(readyMissingNode{
					node:            shamap.MissingNode{Hash: hash, NodeID: released.nodeID},
					transaction:     released.transaction,
					previousPeerID:  released.previousPeerID,
					previousExpires: released.previousExpires,
					started:         released.started,
					retries:         released.retries,
					hedges:          released.hedges,
				})
			}
		}
	}
}

// ReleaseMissingPeer marks a peer's previous request as answered. Its node
// reservations remain until they arrive or the acquisition timer advances.
func (l *Ledger) ReleaseMissingPeer(peerID uint64) {
	l.mu.Lock()
	l.clearHedgePeerLocked(peerID)
	l.releaseAllPeerLeasesLocked(peerID, time.Now(), false)
	l.mu.Unlock()
}

// Snapshot is a point-in-time view of an acquisition's progress, used by the
// fetch_info RPC (mirrors the per-ledger fields rippled emits from
// InboundLedger::getJson). Timeouts is the live no-progress retry count, and
// Peers is the current broadened source-peer set size.
type Snapshot struct {
	Hash                       [32]byte
	Seq                        uint32
	HaveHeader                 bool
	HaveState                  bool
	HaveTransactions           bool
	Complete                   bool
	Failed                     bool
	Timeouts                   int
	ConsecutiveTimeouts        int
	ByHashMode                 bool
	OutstandingNodes           int // includes wire-only shadow slots
	Hedges                     uint32
	OldestRequest              time.Time
	RequestPeerSnapshots       []RequestPeerSnapshot
	LastUsefulProgress         time.Time
	Peers                      int
	RequestPeers               int
	StateReceived              uint64
	StateUseful                uint64
	TxReceived                 uint64
	TxUseful                   uint64
	StateCacheUseful           uint64
	TxCacheUseful              uint64
	StateEqualSubtreesSkipped  uint64
	StateNodesDescended        uint64
	StateDurableReads          uint64
	StateNodesLoaded           uint64
	StateMissingDiscovered     uint64
	StateVerifiedBaseFallbacks uint64
	TxNodesDescended           uint64
	TxDurableReads             uint64
	TxNodesLoaded              uint64
	AcquisitionMemory          shamap.AcquisitionBudgetStats
	StateWalk                  shamap.BackedWalkStats
	TxWalk                     shamap.BackedWalkStats
	MemoryWaiting              bool
	MemoryCheckpoints          uint64
	NeededState                [][32]byte // hashes of up to missingNodeBatch missing state nodes
	NeededTx                   [][32]byte // hashes of up to missingNodeBatch missing tx nodes
}

func (s Snapshot) Phase() string {
	switch {
	case s.Failed:
		return "failed"
	case s.Complete:
		return "complete"
	case !s.HaveHeader:
		return "base"
	case !s.HaveState:
		return "state"
	case !s.HaveTransactions:
		return "transactions"
	default:
		return "complete"
	}
}

// Snapshot returns the acquisition fields and the most recently scanned
// missing-node frontier without performing node-store I/O.
func (l *Ledger) Snapshot() Snapshot {
	if l.mu.TryLock() {
		s := l.snapshotLocked()
		l.snapshot.Store(snapshotCopy(s))
		l.mu.Unlock()
		return s
	}
	if cached := l.snapshot.Load(); cached != nil {
		return *snapshotCopy(*cached)
	}
	return Snapshot{}
}

func (l *Ledger) snapshotLocked() Snapshot {
	if l.transactionOnly {
		l.refreshRequestPeersLocked()
	}
	s := Snapshot{
		Hash:                l.hash,
		Seq:                 l.seq,
		HaveHeader:          l.header != nil,
		HaveState:           l.haveState,
		HaveTransactions:    l.haveTx,
		Complete:            l.state == StateComplete,
		Failed:              l.state == StateFailed,
		Timeouts:            l.timeouts,
		ConsecutiveTimeouts: l.consecutiveTimeouts,
		ByHashMode:          l.byHashMode,
		OutstandingNodes:    len(l.requestLeases),
		LastUsefulProgress:  l.lastUsefulProgress,
		Peers:               len(l.peers),
		RequestPeers:        len(l.requestPeers),
		StateReceived:       l.stateRecv,
		StateUseful:         l.stateUseful,
		TxReceived:          l.txRecv,
		TxUseful:            l.txUseful,
		StateCacheUseful:    l.stateCacheUseful,
		TxCacheUseful:       l.txCacheUseful,
		MemoryWaiting:       l.memoryWaiting,
		MemoryCheckpoints:   l.memoryCheckpoints,
	}
	if l.stateMap != nil {
		s.StateNodesLoaded = l.stateMap.FamilyLoadCount()
		stats := l.stateMap.BackedWalkStats()
		s.StateWalk = stats
		s.StateEqualSubtreesSkipped = stats.EqualSubtreesSkipped
		s.StateNodesDescended = stats.NodesDescended
		s.StateDurableReads = stats.DurableReads
		s.StateMissingDiscovered = stats.MissingNodes
		s.StateVerifiedBaseFallbacks = stats.VerifiedBaseFallbacks
	}
	if l.txMap != nil {
		s.TxNodesLoaded = l.txMap.FamilyLoadCount()
		stats := l.txMap.BackedWalkStats()
		s.TxWalk = stats
		s.TxNodesDescended = stats.NodesDescended
		s.TxDurableReads = stats.DurableReads
	}
	if l.memory != nil {
		s.AcquisitionMemory = l.memory.Snapshot()
	}
	if !l.haveState {
		s.NeededState = cloneHashes(l.neededState)
	}
	if !l.haveTx {
		s.NeededTx = cloneHashes(l.neededTx)
	}
	if l.transactionOnly {
		s.OldestRequest = l.oldestRequestLocked()
		for _, lease := range l.requestLeases {
			s.Hedges += lease.hedges
		}
		for _, ready := range l.readyRequests {
			s.Hedges += ready.hedges
		}
		s.RequestPeerSnapshots = l.requestPeerSnapshotsLocked()
	}
	return s
}

func (l *Ledger) oldestRequestLocked() time.Time {
	var oldest time.Time
	for _, lease := range l.requestLeases {
		if lease.peerID == 0 && lease.previousPeerID == 0 || lease.started.IsZero() {
			continue
		}
		if oldest.IsZero() || lease.started.Before(oldest) {
			oldest = lease.started
		}
	}
	for _, ready := range l.readyRequests {
		if ready.previousPeerID == 0 || ready.started.IsZero() {
			continue
		}
		if oldest.IsZero() || ready.started.Before(oldest) {
			oldest = ready.started
		}
	}
	return oldest
}

func (l *Ledger) requestPeerSnapshotsLocked() []RequestPeerSnapshot {
	if len(l.requestPeers) == 0 {
		return nil
	}
	peerIDs := make([]uint64, 0, min(len(l.requestPeers), requestPeerLimit))
	for peerID := range l.requestPeers {
		peerIDs = append(peerIDs, peerID)
	}
	slices.Sort(peerIDs)
	if len(peerIDs) > requestPeerLimit {
		peerIDs = peerIDs[:requestPeerLimit]
	}
	now := SystemClock.Now()
	snapshots := make([]RequestPeerSnapshot, 0, len(peerIDs))
	for _, peerID := range peerIDs {
		peerSnapshot := RequestPeerSnapshot{PeerID: peerID}
		for _, lease := range l.requestLeases {
			current := lease.peerID == peerID
			previous := lease.previousPeerID == peerID && lease.previousExpires.After(now)
			if !current && !previous {
				continue
			}
			peerSnapshot.OutstandingNodes++
			if current {
				peerSnapshot.Retries += lease.retries
				peerSnapshot.Hedges += lease.hedges
			}
			if !lease.started.IsZero() && (peerSnapshot.OldestRequest.IsZero() || lease.started.Before(peerSnapshot.OldestRequest)) {
				peerSnapshot.OldestRequest = lease.started
			}
		}
		for _, ready := range l.readyRequests {
			if ready.previousPeerID != peerID || !ready.previousExpires.After(now) {
				continue
			}
			peerSnapshot.OutstandingNodes++
			if !ready.started.IsZero() && (peerSnapshot.OldestRequest.IsZero() || ready.started.Before(peerSnapshot.OldestRequest)) {
				peerSnapshot.OldestRequest = ready.started
			}
		}
		snapshots = append(snapshots, peerSnapshot)
	}
	return snapshots
}

func (l *Ledger) cacheMissingLocked(transaction bool, missing []shamap.MissingNode) {
	hashes := missingHashes(missing)
	if transaction {
		l.missingTx = slices.Clone(missing)
		l.neededTx = hashes
	} else {
		l.missingState = slices.Clone(missing)
		l.neededState = hashes
	}
	l.publishSnapshotLocked()
}

// removeMissingNodeLocked invalidates a path satisfied (or explicitly
// answered) by a peer. Caller holds l.mu; the next useful-reply walk replaces
// the remainder with the newly exposed frontier.
func (l *Ledger) removeMissingNodeLocked(transaction bool, nodeID shamap.NodeID) {
	frontier := &l.missingState
	needed := &l.neededState
	if transaction {
		frontier = &l.missingTx
		needed = &l.neededTx
	}
	for i := range *frontier {
		if (*frontier)[i].NodeID == nodeID {
			*frontier = slices.Delete(*frontier, i, i+1)
			*needed = missingHashes(*frontier)
			return
		}
	}
}

func (l *Ledger) publishSnapshotLocked() {
	l.snapshot.Store(snapshotCopy(l.snapshotLocked()))
}

func snapshotCopy(s Snapshot) *Snapshot {
	s.NeededState = cloneHashes(s.NeededState)
	s.NeededTx = cloneHashes(s.NeededTx)
	s.RequestPeerSnapshots = slices.Clone(s.RequestPeerSnapshots)
	return &s
}

func cloneHashes(src [][32]byte) [][32]byte {
	if src == nil {
		return nil
	}
	dst := make([][32]byte, len(src))
	copy(dst, src)
	return dst
}

func missingHashes(missing []shamap.MissingNode) [][32]byte {
	if len(missing) > missingNodeBatch {
		missing = missing[:missingNodeBatch]
	}
	hashes := make([][32]byte, len(missing))
	for i := range missing {
		hashes[i] = missing[i].Hash
	}
	return hashes
}

func missingNodeHashes(missing []shamap.MissingNode, max int) [][32]byte {
	if max <= 0 || len(missing) == 0 {
		return nil
	}
	if len(missing) > max {
		missing = missing[:max]
	}
	hashes := make([][32]byte, len(missing))
	for i := range missing {
		hashes[i] = missing[i].Hash
	}
	return hashes
}

// IsRetired reports whether acquisition work has relinquished ownership.
func (l *Ledger) IsRetired() bool {
	return l.retired.Load()
}

// IsComplete returns true if the ledger has been fully acquired.
func (l *Ledger) IsComplete() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == StateComplete
}

// Result returns the acquired header, state map, and transaction map.
// The tx map is nil when the ledger has no transactions (empty tx tree).
// Only valid after IsComplete() returns true.
func (l *Ledger) Result() (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateComplete {
		return nil, nil, nil, fmt.Errorf("acquisition not complete (state=%d)", l.state)
	}

	return l.header, l.stateMap, l.txMap, nil
}

// Err returns the error if the acquisition failed.
func (l *Ledger) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// localFillBatch caps how many missing node hashes CheckLocal pulls per
// SHAMap descent pass. Larger than missingNodeBatch because the source is a
// local cache, not a network round-trip, so a wider frontier per pass means
// fewer descents to drain the tree.
const localFillBatch = 256

// FetchPackRequested reports whether a fetch-pack has already been requested
// for this acquisition, so the router escalates at most once.
func (l *Ledger) FetchPackRequested() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fetchPackRequested
}

// MarkFetchPackRequested records that a fetch-pack was requested for this
// acquisition, so the router escalates at most once. The acquisition stays in
// flight under its OnTimer retry budget while the reply arrives and completes
// it locally via CheckLocal. Mirrors rippled arming an aggressive fetch-pack
// fallback (LedgerMaster::getFetchPack) without abandoning the InboundLedger.
func (l *Ledger) MarkFetchPackRequested() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fetchPackRequested = true
}

// CheckLocal attempts to complete the still-outstanding trees from a local
// node source instead of the network, mirroring rippled's
// InboundLedger::tryDB / checkLocal which drains missing SHAMap nodes from the
// node store after a fetch-pack populates it (InboundLedger.cpp:162-178,
// 284-296). For each outstanding tree it repeatedly asks the SHAMap for its
// missing node hashes and feeds back any the supplied fetch func can satisfy,
// until the source is exhausted or the tree is complete.
//
// fetch returns the prefix-format (serializeWithPrefix) bytes for a SHAMap node
// hash and whether it was found. CheckLocal returns true if it placed at least
// one node, so the caller can re-check completion (IsComplete) and finalize.
func (l *Ledger) CheckLocal(fetch func(hash [32]byte) ([]byte, bool)) bool {
	progressed, _, _ := l.CheckLocalContext(context.Background(), fetch)
	return progressed
}

// CheckLocalContext is CheckLocal with traversal outside the acquisition mutex
// and cancellation propagated between local-store passes.
func (l *Ledger) CheckLocalContext(ctx context.Context, fetch func(hash [32]byte) ([]byte, bool)) (progressed, complete bool, err error) {
	if fetch == nil {
		return false, false, nil
	}
	l.workMu.Lock()
	defer l.finishWork()
	if err := l.retirementError(); err != nil {
		return false, false, err
	}

	l.mu.Lock()
	if l.state != StateWantState {
		complete = l.state == StateComplete
		l.mu.Unlock()
		return false, complete, nil
	}
	var stateMap, txMap *shamap.SHAMap
	if !l.haveState {
		stateMap = l.stateMap
	}
	if !l.haveTx {
		txMap = l.txMap
	}
	l.mu.Unlock()

	stateProgress, stateComplete := false, false
	var stateStored []shamap.FlushEntry
	var stateMissing []shamap.MissingNode
	if stateMap != nil {
		discoveriesBefore := stateMap.BackedWalkStats().TraversalDiscoveries
		stateProgress, stateStored, stateMissing, err = l.fillFromCacheContext(ctx, stateMap, false, fetch)
		l.recordCachePlacements(false, stateStored)
		stateProgress = stateProgress || stateMap.BackedWalkStats().TraversalDiscoveries > discoveriesBefore
		if persistErr := l.persistLocalNodes(ctx, stateMap, stateStored, "locally recovered state nodes"); persistErr != nil {
			l.recordLocalProgress(stateMap, txMap, stateProgress)
			return stateProgress, false, persistErr
		}
		if err != nil {
			l.recordLocalProgress(stateMap, txMap, stateProgress)
			return stateProgress, false, err
		}
		finishErr := stateMap.FinishSyncContext(ctx)
		if errors.Is(finishErr, shamap.ErrAcquisitionMemory) {
			l.recordLocalProgress(stateMap, txMap, stateProgress)
			return stateProgress, false, finishErr
		}
		stateComplete = finishErr == nil
	}
	txProgress, txComplete := false, false
	var txStored []shamap.FlushEntry
	var txMissing []shamap.MissingNode
	if txMap != nil {
		discoveriesBefore := txMap.BackedWalkStats().TraversalDiscoveries
		txProgress, txStored, txMissing, err = l.fillFromCacheContext(ctx, txMap, true, fetch)
		l.recordCachePlacements(true, txStored)
		txProgress = txProgress || txMap.BackedWalkStats().TraversalDiscoveries > discoveriesBefore
		if persistErr := l.persistLocalNodes(ctx, txMap, txStored, "locally recovered transaction nodes"); persistErr != nil {
			progressed = stateProgress || txProgress
			l.recordLocalProgress(stateMap, txMap, progressed)
			return progressed, false, persistErr
		}
		if err != nil {
			progressed = stateProgress || txProgress
			l.recordLocalProgress(stateMap, txMap, progressed)
			return progressed, false, err
		}
		finishErr := txMap.FinishSyncContext(ctx)
		if errors.Is(finishErr, shamap.ErrAcquisitionMemory) {
			progressed = stateProgress || txProgress
			l.recordLocalProgress(stateMap, txMap, progressed)
			return progressed, false, finishErr
		}
		txComplete = finishErr == nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateWantState {
		return false, l.state == StateComplete, nil
	}
	if stateMap != nil && l.stateMap == stateMap {
		if stateComplete {
			l.haveState = true
			l.missingState = nil
			l.neededState = nil
			l.clearTreeReservationsLocked(false, SystemClock.Now())
		} else if len(stateMissing) > 0 {
			l.cacheMissingLocked(false, stateMissing)
		}
	}
	if txMap != nil && l.txMap == txMap {
		if txComplete {
			l.haveTx = true
			l.missingTx = nil
			l.neededTx = nil
			l.clearTreeReservationsLocked(true, SystemClock.Now())
		} else if len(txMissing) > 0 {
			l.cacheMissingLocked(true, txMissing)
		}
	}
	progressed = stateProgress || txProgress
	if progressed {
		l.markProgressLocked()
	}
	l.recomputeComplete()
	return progressed, l.state == StateComplete, nil
}

func (l *Ledger) recordLocalProgress(stateMap, txMap *shamap.SHAMap, progressed bool) {
	if !progressed {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateWantState {
		return
	}
	if stateMap != nil && l.stateMap != stateMap {
		return
	}
	if txMap != nil && l.txMap != txMap {
		return
	}
	l.markProgressLocked()
}

func (l *Ledger) persistLocalNodes(ctx context.Context, m *shamap.SHAMap, entries []shamap.FlushEntry, label string) error {
	if err := l.persistReceived(ctx, entries, label); err != nil {
		return err
	}
	if l.family == nil || len(entries) == 0 {
		return nil
	}
	if err := l.FlushPersistence(ctx); err != nil {
		return fmt.Errorf("flush %s: %w", label, err)
	}
	if err := l.persistenceResult(ctx, m.AcknowledgePersistedContext(ctx)); err != nil {
		return fmt.Errorf("acknowledge %s: %w", label, err)
	}
	return nil
}

func fillFromLocalContext(
	ctx context.Context,
	m *shamap.SHAMap,
	fetch func(hash [32]byte) ([]byte, bool),
) (bool, []shamap.FlushEntry, []shamap.MissingNode, error) {
	added := false
	passProgress := false
	var stored []shamap.FlushEntry
	for {
		missing, err := m.GetMissingNodesContext(ctx, localFillBatch, nil)
		if err != nil && !missingWalkYield(err) {
			return added, stored, nil, err
		}
		if len(missing) == 0 {
			if err == nil && passProgress {
				passProgress = false
				continue
			}
			return added, stored, nil, err
		}
		passAdded := 0
		for i := range missing {
			data, ok := fetch(missing[i].Hash)
			if !ok {
				continue
			}
			res, entry, addErr := m.AddKnownNodeFromPrefixWithEntryContext(ctx, missing[i].NodeID, data)
			if addErr != nil {
				if errors.Is(addErr, shamap.ErrAcquisitionMemory) || errors.Is(addErr, context.Canceled) || errors.Is(addErr, context.DeadlineExceeded) {
					return added || passAdded > 0, stored, missing, addErr
				}
				continue
			}
			if res == shamap.NodeUseful {
				passAdded++
				stored = append(stored, entry)
			}
		}
		added = added || passAdded > 0
		passProgress = passProgress || passAdded > 0
		if passAdded == 0 || err != nil {
			return added, stored, missing, err
		}
	}
}
