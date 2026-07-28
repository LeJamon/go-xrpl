// Package inbound provides lightweight ledger acquisition from peers.
// It fetches the full ledger header, account-state tree, and transaction tree
// via the TMGetLedger/TMLedgerData peer protocol, matching rippled's
// InboundLedger behavior.
package inbound

import (
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
	// ledgerTimeoutRetriesMax bounds no-progress timer fires before the
	// acquisition fails cleanly (rippled ledgerTimeoutRetriesMax).
	ledgerTimeoutRetriesMax = 6
	// ledgerBecomeAggressiveThreshold is the no-progress timeout count past
	// which the acquisition abandons path-based requests and asks every peer
	// for the missing nodes by content hash (rippled
	// ledgerBecomeAggressiveThreshold).
	ledgerBecomeAggressiveThreshold = 4
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
	StateWantBase  State = iota // Waiting for header + root nodes
	StateWantState              // Have header, fetching state tree nodes
	StateComplete               // Fully acquired
	StateFailed                 // Unrecoverable error
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
	hash      [32]byte
	seq       uint32
	header    *header.LedgerHeader
	stateMap  *shamap.SHAMap
	txMap     *shamap.SHAMap // nil when the transaction tree is empty (TxHash zero)
	haveState bool
	haveTx    bool
	peers     []uint64 // source peers, broadened on no-progress; peers[0] is the original
	reason    Reason
	state     State
	err       error
	mu        sync.Mutex
	logger    *slog.Logger
	snapshot  atomic.Pointer[Snapshot]

	// family backs the acquisition SHAMaps with the persistent node store when
	// set, so getMissingNodes only reports nodes not already held locally. nil
	// leaves the maps unbacked. Set once at construction, never mutated after.
	family          shamap.Family
	headerAdmission func(uint32) error

	// Retry-loop bookkeeping ported from rippled's TimeoutCounter. lastTimer
	// is when OnTimer last evaluated; progress records a fresh node attach
	// since then; timeouts is the cumulative no-progress count used for both
	// escalation and terminal failure.
	// byHash latches eligibility for a by-hash escalation on the next aggressive
	// request. All guarded by mu.
	lastTimer time.Time
	progress  bool
	timeouts  int
	byHash    bool

	// recentNodes keeps request frontiers disjoint within a timer interval.
	// requestPeers caps the acquisition at one active request per peer and six
	// active peer streams. Both are cleared when the timer advances.
	recentNodes      map[[32]byte]uint64
	requestPeers     map[uint64]struct{}
	neededState      [][32]byte
	neededTx         [][32]byte
	stateRecv        uint64
	stateUseful      uint64
	txRecv           uint64
	txUseful         uint64
	checkpointUseful uint64

	// Rejection diagnostics, surfaced on the no-progress tick so a stuck
	// acquisition names which node it cannot place and why (the signal the
	// swallowed Debug logs hid). Guarded by mu.
	rejectCount   int
	lastRejectErr string

	// fetchPackRequested records that the router escalated this stalled
	// acquisition to a fetch-pack (at most once). Guarded by mu.
	fetchPackRequested bool
}

// Option configures an acquisition at construction.
type Option func(*Ledger)

// ErrHeaderRejected identifies a locally valid ledger header that acquisition
// policy declined. The source peer did not send malformed data, but the
// acquisition must terminate without accepting or persisting its roots.
var ErrHeaderRejected = errors.New("inbound ledger header rejected")

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

// WithHeaderAdmission installs a policy check that runs after the header hash
// and any requested sequence have been verified, but before the acquisition
// adopts a hash-only header's sequence or constructs its SHAMaps.
func WithHeaderAdmission(admit func(uint32) error) Option {
	return func(l *Ledger) {
		l.headerAdmission = admit
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
		hash:         hash,
		seq:          seq,
		state:        StateWantBase,
		lastTimer:    SystemClock.Now(),
		logger:       logger,
		recentNodes:  make(map[[32]byte]uint64),
		requestPeers: make(map[uint64]struct{}),
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
func (l *Ledger) newSyncMap(t shamap.Type) (*shamap.SHAMap, error) {
	if l.family != nil {
		return shamap.NewBacked(t, l.family)
	}
	return shamap.New(t), nil
}

// Reason returns why this acquisition was started.
func (l *Ledger) Reason() Reason {
	return l.reason
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
	for hash, owner := range l.recentNodes {
		if owner == peerID {
			delete(l.recentNodes, hash)
		}
	}
	delete(l.requestPeers, peerID)
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
}

// Timeouts returns the number of no-progress timer intervals counted so far,
// mirroring rippled's timeouts_. The router gates qtINDIRECT relaying on
// timeouts > 0, matching InboundLedger::trigger.
func (l *Ledger) Timeouts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.timeouts
}

// TimerDue reports whether the acquisition retry interval has elapsed.
func (l *Ledger) TimerDue(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state != StateComplete && l.state != StateFailed && now.Sub(l.lastTimer) >= acquireTimerInterval
}

// OnTimer advances the acquisition's retry loop, mirroring rippled's
// TimeoutCounter::invokeOnTimer + InboundLedger::onTimer. It is a no-op until
// acquireTimerInterval has elapsed since the last fire, so it can be polled
// from the router's maintenance tick. On a due fire it either records that
// forward progress was made this interval (and keeps relying on the
// reply-driven path) or counts a no-progress timeout; once the budget is
// exhausted the acquisition transitions cleanly to StateFailed instead of
// re-arming the same stall forever. The returned TimerAction tells the router
// whether to reap (TimerFailed), escalate (TimerEscalate), or do nothing.
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

	// Reset the per-interval de-dup set, pacing re-requests at ~once/interval.
	// Mirrors rippled onTimer's mRecentNodes.clear() (InboundLedger.cpp:368).
	clear(l.recentNodes)
	clear(l.requestPeers)

	if l.progress {
		l.lastTimer = now
		l.progress = false
		return TimerRefresh
	}

	l.timeouts++
	if l.timeouts > ledgerTimeoutRetriesMax {
		l.state = StateFailed
		l.err = fmt.Errorf("inbound ledger %d: acquisition failed after %d timeouts (have_state=%t have_tx=%t last_reject=%q)",
			l.seq, l.timeouts, l.haveState, l.haveTx, l.lastRejectErr)
		l.logger.Warn("inbound ledger: acquisition failed, retry budget exhausted",
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
		return TimerFailed
	}

	// No progress, budget remains: arm a by-hash escalation and surface the
	// diagnostic that the swallowed Debug-level rejections used to hide.
	l.byHash = true
	l.logger.Warn("inbound ledger: no acquisition progress",
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

// markProgressLocked records that a fresh node was attached this interval, so
// the next OnTimer fire treats the acquisition as progressing rather than
// timing out (rippled sets progress_ on a useful received node). Caller holds mu.
func (l *Ledger) markProgressLocked() {
	l.progress = true
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
			return nil, nil, err
		}
	}
	if txMap != nil {
		tx, err = neededHashesContext(ctx, txMap, max)
		if err != nil {
			l.restoreByHashAfterInterruptedWalk()
			return nil, nil, err
		}
	}
	return state, tx, nil
}

func neededHashesContext(ctx context.Context, m *shamap.SHAMap, max int) ([][32]byte, error) {
	missing, err := m.GetMissingNodesContext(ctx, max, nil)
	if err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return nil, nil
	}
	out := make([][32]byte, 0, len(missing))
	for i := range missing {
		out = append(out, missing[i].Hash)
	}
	return out, nil
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
	l.mu.Lock()
	defer func() {
		if useful > 0 {
			l.markProgressLocked()
		}
		l.persistReceived(ctx, stored, "roots")
		l.mu.Unlock()
	}()

	// Ignore duplicate responses after we've moved past WantBase
	if l.state != StateWantBase {
		return 0, nil
	}

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

	if l.stateMap == nil && len(nodes) >= 2 && len(nodes[1].NodeData) > 0 {
		sm, createErr := l.newSyncMap(shamap.TypeState)
		if createErr != nil {
			return useful, fmt.Errorf("create state map: %w", createErr)
		}
		sm.SetLedgerSeq(h.LedgerIndex)
		stateRoot, rootErr := sm.AddRootNodeWithEntry(h.AccountHash, nodes[1].NodeData)
		if rootErr != nil {
			return useful, fmt.Errorf("add state root node: %w", rootErr)
		}
		l.stateMap = sm
		l.publishSnapshotLocked()
		l.haveState = sm.FinishSyncContext(ctx) == nil
		stored = append(stored, stateRoot)
		useful++
	}

	if h.TxHash == ([32]byte{}) {
		l.haveTx = true
	} else if l.txMap == nil && len(nodes) >= 3 && len(nodes[2].NodeData) > 0 {
		tm, terr := l.newSyncMap(shamap.TypeTransaction)
		if terr != nil {
			return useful, fmt.Errorf("create tx map: %w", terr)
		}
		tm.SetLedgerSeq(h.LedgerIndex)
		txRoot, rootErr := tm.AddRootNodeWithEntry(h.TxHash, nodes[2].NodeData)
		if rootErr != nil {
			return useful, fmt.Errorf("add tx root node: %w", rootErr)
		}
		l.txMap = tm
		l.publishSnapshotLocked()
		l.haveTx = tm.FinishSyncContext(ctx) == nil
		stored = append(stored, txRoot)
		useful++
	}

	if l.stateMap == nil || (h.TxHash != ([32]byte{}) && l.txMap == nil) {
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

func (l *Ledger) GotStateNodesUsefulContext(ctx context.Context, nodes []message.LedgerNode) (int, error) {
	if err := ValidateReplyNodeCount(nodes); err != nil {
		return 0, err
	}

	var stored []shamap.FlushEntry
	l.mu.Lock()
	defer func() {
		l.persistReceived(ctx, stored, "state nodes")
		l.mu.Unlock()
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
	if applyErr != nil {
		l.state = StateFailed
		l.err = applyErr
		return added, applyErr
	}

	if added > 0 {
		l.markProgressLocked()
	}
	l.publishSnapshotLocked()
	l.logger.Debug("inbound ledger: added state nodes",
		"added", added,
		"total_received", len(nodes),
		"useful_total", l.stateUseful,
		"received_total", l.stateRecv,
	)

	return added, nil
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

func (l *Ledger) GotTransactionNodesUsefulContext(ctx context.Context, nodes []message.LedgerNode) (int, error) {
	if err := ValidateReplyNodeCount(nodes); err != nil {
		return 0, err
	}

	var stored []shamap.FlushEntry
	l.mu.Lock()
	defer func() {
		l.persistReceived(ctx, stored, "transaction nodes")
		l.mu.Unlock()
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
	if applyErr != nil {
		l.state = StateFailed
		l.err = applyErr
		return added, applyErr
	}

	if added > 0 {
		l.markProgressLocked()
	}
	l.publishSnapshotLocked()
	l.logger.Debug("inbound ledger: added tx nodes",
		"added", added,
		"total_received", len(nodes),
		"useful_total", l.txUseful,
		"received_total", l.txRecv,
	)

	return added, nil
}

// applyKnownNodes places peer-supplied tree nodes by NodeID, returning the
// number freshly attached. A node whose ancestor is still a hash-only stub
// (NodeReRequest) is dropped without counting as a reject — the next
// getMissingNodes walk re-requests the correct frontier and it returns on a
// later reply. The first genuinely invalid node stops harvesting the rest of
// the reply. Caller holds l.mu.
func (l *Ledger) applyKnownNodes(ctx context.Context, m *shamap.SHAMap, nodes []message.LedgerNode, label string) (int, []shamap.FlushEntry, error) {
	added := 0
	stored := make([]shamap.FlushEntry, 0, len(nodes))
	for _, node := range nodes {
		if len(node.NodeData) == 0 {
			continue
		}
		parsedID, err := shamap.ParseNodeID(node.NodeID)
		if err != nil {
			l.logger.Debug("inbound ledger: malformed "+label+" node ID",
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			continue
		}
		if parsedID.IsRoot() {
			continue
		}
		res, entry, err := m.AddKnownNodeByIDWithEntryContext(ctx, parsedID, node.NodeData)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return added, stored, err
		}
		switch res {
		case shamap.NodeUseful:
			added++
			if err != nil {
				return added, stored, fmt.Errorf("prepare verified %s node for persistence: %w", label, err)
			}
			stored = append(stored, entry)
		case shamap.NodeDuplicate, shamap.NodeReRequest:
			// Already present, or ahead of its frontier: neither progress nor
			// a reject. Re-requested by the next missing-node walk.
		default: // NodeInvalid, or any unrecognized result — reject conservatively.
			l.rejectCount++
			if err != nil {
				l.lastRejectErr = err.Error()
			}
			l.logger.Debug("inbound ledger: "+label+" node rejected",
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err)
			return added, stored, nil
		}
	}
	return added, stored, nil
}

func (l *Ledger) persistReceived(ctx context.Context, entries []shamap.FlushEntry, label string) {
	if l.family == nil || len(entries) == 0 {
		return
	}
	if err := l.family.StoreBatch(ctx, entries); err != nil {
		l.logger.Warn("inbound ledger: failed to persist verified "+label, "error", err)
	}
}

// FlushPersistence waits for every verified node queued by this acquisition.
func (l *Ledger) FlushPersistence(ctx context.Context) error {
	flusher, ok := l.family.(interface {
		Flush(context.Context) error
	})
	if !ok {
		return nil
	}
	return flusher.Flush(ctx)
}

// CheckpointPersistence bounds the resident acquisition tree after enough
// newly useful nodes have been queued for durable storage.
func (l *Ledger) CheckpointPersistence(ctx context.Context, useful int) error {
	if l.family == nil || useful <= 0 {
		return nil
	}

	l.mu.Lock()
	l.checkpointUseful += uint64(useful)
	if l.checkpointUseful < persistenceCheckpointNodes {
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
		if err := m.AcknowledgePersistedContext(ctx); err != nil {
			return err
		}
	}

	l.mu.Lock()
	if l.checkpointUseful >= checkpointed {
		l.checkpointUseful -= checkpointed
	} else {
		l.checkpointUseful = 0
	}
	l.mu.Unlock()
	return nil
}

// PromotePersistence seals a successfully flushed acquisition for use by its
// adopted ledger. Future reads use durable storage and future writes pass
// synchronously through to that storage.
func (l *Ledger) PromotePersistence(ctx context.Context) error {
	promoter, ok := l.family.(interface {
		Promote(context.Context) error
	})
	if !ok {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := promoter.Promote(ctx); err != nil {
		return err
	}
	if l.stateMap != nil {
		if err := l.stateMap.AcknowledgePersistedContext(ctx); err != nil {
			return err
		}
	}
	if l.txMap != nil {
		if err := l.txMap.AcknowledgePersistedContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RetirePersistence releases persistence state owned by an acquisition that
// will perform no more writes. It is idempotent and does not surface an
// abandoned acquisition's write failure as a failure of another acquisition.
func (l *Ledger) RetirePersistence(ctx context.Context) error {
	retirer, ok := l.family.(interface {
		Retire(context.Context) error
	})
	if !ok {
		return nil
	}
	return retirer.Retire(ctx)
}

// recomputeComplete promotes the acquisition to StateComplete once both the
// account-state and transaction trees are in hand, mirroring rippled's
// complete_ = mHaveHeader && mHaveState && mHaveTransactions
// (InboundLedger.cpp:734,946). Caller must hold l.mu.
func (l *Ledger) recomputeComplete() {
	if l.haveState && l.haveTx && l.state != StateFailed {
		l.state = StateComplete
		l.logger.Info("inbound ledger: acquisition complete", "seq", l.header.LedgerIndex)
	}
	l.publishSnapshotLocked()
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
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stateMap == nil || l.haveState || l.state != StateWantState {
		return nil
	}
	missing := l.stateMap.GetMissingNodes(missingNodeBatch, nil)
	l.cacheMissingLocked(false, missing)
	return missingNodeIDs(missing)
}

// NeedsMissingTxNodeIDs returns up to missingNodeBatch wire-encoded NodeIDs of
// missing transaction-tree inner nodes, mirroring NeedsMissingNodeIDs for the
// tx map. Returns nil once the tx tree is complete (or empty).
func (l *Ledger) NeedsMissingTxNodeIDs() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.txMap == nil || l.haveTx || l.state != StateWantState {
		return nil
	}
	missing := l.txMap.GetMissingNodes(missingNodeBatch, nil)
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
// holding the acquisition mutex. Peer replies may therefore continue attaching
// nodes while a cold scan is in flight. The active tree is checked again before
// recent-node filtering or completion is committed.
func (l *Ledger) CollectMissingRequestContext(ctx context.Context, isReply bool) (state, txn [][]byte, complete bool, err error) {
	l.mu.Lock()
	if l.state != StateWantState {
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
	l.mu.Unlock()
	if m == nil {
		return nil, nil, false, nil
	}

	started := time.Now()
	missing, err := m.GetMissingNodesContext(ctx, missingNodesFind, nil)
	if err != nil {
		return nil, nil, false, err
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		l.logger.Info("inbound ledger: missing-node walk delayed", "elapsed", elapsed, "missing", len(missing), "reply", isReply)
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
		if finishErr := m.FinishSyncContext(ctx); finishErr == nil {
			if stateTree {
				l.haveState = true
			} else {
				l.haveTx = true
			}
			l.recomputeComplete()
			return nil, nil, l.state == StateComplete, nil
		}
		return nil, nil, false, nil
	}

	nodeIDs := l.filterMissingResultLocked(missing, isReply)
	if stateTree {
		l.cacheMissingLocked(false, missing)
		return nodeIDs, nil, false, nil
	}
	l.cacheMissingLocked(true, missing)
	return nil, nodeIDs, false, nil
}

// filterMissingResultLocked applies the request throttle to a frontier already
// collected outside the acquisition lock. Caller holds l.mu.
func (l *Ledger) filterMissingResultLocked(missing []shamap.MissingNode, isReply bool) [][]byte {
	fresh := make([]shamap.MissingNode, 0, len(missing))
	for i := range missing {
		if _, dup := l.recentNodes[missing[i].Hash]; !dup {
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
		nodeIDs = append(nodeIDs, use[i].NodeID.Bytes())
		l.recentNodes[use[i].Hash] = 0
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
	return l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodesReply, false)
}

// CollectMissingAddedRequestsContext reserves small blind frontiers for peers
// newly admitted to an active acquisition.
func (l *Ledger) CollectMissingAddedRequestsContext(ctx context.Context, peerIDs []uint64) ([]MissingRequest, bool, error) {
	return l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodes, true)
}

func (l *Ledger) collectMissingPeerRequestsContext(ctx context.Context, peerIDs []uint64, batchLimit int, blind bool) ([]MissingRequest, bool, error) {
	if len(peerIDs) == 0 {
		return nil, l.IsComplete(), nil
	}
	l.mu.Lock()
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
	complete := l.state == StateComplete
	l.mu.Unlock()
	if len(peers) == 0 {
		return nil, complete, nil
	}
	requests := make([]MissingRequest, 0, len(peers))
	for _, transaction := range []bool{false, true} {
		if len(peers) == 0 {
			break
		}
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

		var filter shamap.SyncFilter
		for len(peers) > 0 {
			missing, err := m.GetMissingNodesContext(ctx, missingNodesFind, filter)
			if err != nil {
				l.releaseMissingRequests(requests)
				return nil, false, err
			}
			traversalExhausted := len(missing) < missingNodesFind
			if len(missing) == 0 {
				if filter != nil {
					break
				}
				l.mu.Lock()
				if l.state != StateWantState || (!transaction && (l.stateMap != m || l.haveState)) ||
					(transaction && (l.txMap != m || l.haveTx)) {
					complete := l.state == StateComplete
					l.mu.Unlock()
					return requests, complete, nil
				}
				if finishErr := m.FinishSyncContext(ctx); finishErr != nil {
					l.mu.Unlock()
					return requests, false, nil
				}
				if transaction {
					l.haveTx = true
				} else {
					l.haveState = true
					clear(l.requestPeers)
					clear(l.recentNodes)
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
			l.cacheMissingLocked(transaction, missing)
			fresh := missing[:0]
			for i := range missing {
				if _, exists := l.recentNodes[missing[i].Hash]; !exists {
					fresh = append(fresh, missing[i])
				}
			}
			for len(peers) > 0 && len(fresh) > 0 {
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
					return requests, false, nil
				}
				n := min(batchLimit, len(fresh))
				if !blind || traversalExhausted {
					n = min(n, (len(fresh)+len(peers))/(len(peers)+1))
				}
				batch := fresh[:n]
				request := MissingRequest{
					PeerID:      peerID,
					NodeIDs:     make([][]byte, 0, n),
					NodeHashes:  make([][32]byte, 0, n),
					Transaction: transaction,
					Blind:       blind,
				}
				for i := range batch {
					request.NodeIDs = append(request.NodeIDs, batch[i].NodeID.Bytes())
					request.NodeHashes = append(request.NodeHashes, batch[i].Hash)
					l.recentNodes[batch[i].Hash] = request.PeerID
				}
				l.requestPeers[request.PeerID] = struct{}{}
				requests = append(requests, request)
				fresh = fresh[n:]
			}
			if len(peers) == 0 {
				l.mu.Unlock()
				return requests, false, nil
			}
			if traversalExhausted {
				l.mu.Unlock()
				break
			}
			excluded := make(missingNodeExclusionFilter, len(l.recentNodes))
			for hash := range l.recentNodes {
				excluded[hash] = struct{}{}
			}
			filter = excluded
			l.mu.Unlock()
		}
	}
	return requests, false, nil
}

func (l *Ledger) releaseMissingRequests(requests []MissingRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, request := range requests {
		delete(l.requestPeers, request.PeerID)
		for _, hash := range request.NodeHashes {
			if l.recentNodes[hash] == request.PeerID {
				delete(l.recentNodes, hash)
			}
		}
	}
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
	for hash, owner := range l.recentNodes {
		if owner == 0 {
			delete(l.recentNodes, hash)
		}
	}
}

// ReleaseMissingPeer marks a peer's previous request as answered. Its node
// reservations remain until they arrive or the acquisition timer advances.
func (l *Ledger) ReleaseMissingPeer(peerID uint64) {
	l.mu.Lock()
	delete(l.requestPeers, peerID)
	l.mu.Unlock()
}

// Snapshot is a point-in-time view of an acquisition's progress, used by the
// fetch_info RPC (mirrors the per-ledger fields rippled emits from
// InboundLedger::getJson). Timeouts is the live no-progress retry count, and
// Peers is the current broadened source-peer set size.
type Snapshot struct {
	Hash             [32]byte
	Seq              uint32
	HaveHeader       bool
	HaveState        bool
	HaveTransactions bool
	Complete         bool
	Failed           bool
	Timeouts         int
	Peers            int
	RequestPeers     int
	StateReceived    uint64
	StateUseful      uint64
	TxReceived       uint64
	TxUseful         uint64
	NeededState      [][32]byte // hashes of up to missingNodeBatch missing state nodes
	NeededTx         [][32]byte // hashes of up to missingNodeBatch missing tx nodes
}

// Phase reports the current acquisition traversal phase.
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

// SnapshotContext returns the acquisition fields under its mutex, then gathers
// diagnostic missing hashes without holding that mutex across node-store reads.
func (l *Ledger) SnapshotContext(ctx context.Context) (Snapshot, error) {
	l.mu.Lock()
	s := l.snapshotLocked()
	var stateMap, txMap *shamap.SHAMap
	if !l.haveState && l.stateMap != nil {
		stateMap = l.stateMap
	}
	if !l.haveTx && l.txMap != nil {
		txMap = l.txMap
	}
	l.mu.Unlock()

	var stateMissing, txMissing []shamap.MissingNode
	if stateMap != nil {
		missing, err := stateMap.GetMissingNodesContext(ctx, missingNodeBatch, nil)
		if err != nil {
			return s, err
		}
		stateMissing = missing
		s.NeededState = missingHashes(missing)
	}
	if txMap != nil {
		missing, err := txMap.GetMissingNodesContext(ctx, missingNodeBatch, nil)
		if err != nil {
			return s, err
		}
		txMissing = missing
		s.NeededTx = missingHashes(missing)
	}

	l.mu.Lock()
	if stateMap != nil && l.state == StateWantState && l.stateMap == stateMap && !l.haveState {
		l.cacheMissingLocked(false, stateMissing)
	}
	if txMap != nil && l.state == StateWantState && l.txMap == txMap && !l.haveTx {
		l.cacheMissingLocked(true, txMissing)
	}
	l.mu.Unlock()

	return s, nil
}

func (l *Ledger) snapshotLocked() Snapshot {
	s := Snapshot{
		Hash:             l.hash,
		Seq:              l.seq,
		HaveHeader:       l.header != nil,
		HaveState:        l.haveState,
		HaveTransactions: l.haveTx,
		Complete:         l.state == StateComplete,
		Failed:           l.state == StateFailed,
		Timeouts:         l.timeouts,
		Peers:            len(l.peers),
		RequestPeers:     len(l.requestPeers),
		StateReceived:    l.stateRecv,
		StateUseful:      l.stateUseful,
		TxReceived:       l.txRecv,
		TxUseful:         l.txUseful,
	}
	if !l.haveState {
		s.NeededState = cloneHashes(l.neededState)
	}
	if !l.haveTx {
		s.NeededTx = cloneHashes(l.neededTx)
	}
	return s
}

func (l *Ledger) cacheMissingLocked(transaction bool, missing []shamap.MissingNode) {
	hashes := missingHashes(missing)
	if transaction {
		l.neededTx = hashes
	} else {
		l.neededState = hashes
	}
	l.publishSnapshotLocked()
}

func (l *Ledger) publishSnapshotLocked() {
	l.snapshot.Store(snapshotCopy(l.snapshotLocked()))
}

func snapshotCopy(s Snapshot) *Snapshot {
	s.NeededState = cloneHashes(s.NeededState)
	s.NeededTx = cloneHashes(s.NeededTx)
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
	if stateMap != nil {
		loadsBefore := stateMap.FamilyLoadCount()
		stateProgress, stateStored, err = fillFromLocalContext(ctx, stateMap, fetch)
		stateProgress = stateProgress || stateMap.FamilyLoadCount() > loadsBefore
		l.persistReceived(ctx, stateStored, "locally recovered state nodes")
		if err != nil {
			l.recordLocalProgress(stateMap, txMap, stateProgress)
			return stateProgress, false, err
		}
		stateComplete = stateMap.FinishSyncContext(ctx) == nil
	}
	txProgress, txComplete := false, false
	var txStored []shamap.FlushEntry
	if txMap != nil {
		loadsBefore := txMap.FamilyLoadCount()
		txProgress, txStored, err = fillFromLocalContext(ctx, txMap, fetch)
		txProgress = txProgress || txMap.FamilyLoadCount() > loadsBefore
		l.persistReceived(ctx, txStored, "locally recovered transaction nodes")
		if err != nil {
			progressed = stateProgress || txProgress
			l.recordLocalProgress(stateMap, txMap, progressed)
			return progressed, false, err
		}
		txComplete = txMap.FinishSyncContext(ctx) == nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != StateWantState {
		return false, l.state == StateComplete, nil
	}
	if stateMap != nil && l.stateMap == stateMap && stateComplete {
		l.haveState = true
	}
	if txMap != nil && l.txMap == txMap && txComplete {
		l.haveTx = true
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

func fillFromLocalContext(ctx context.Context, m *shamap.SHAMap, fetch func(hash [32]byte) ([]byte, bool)) (bool, []shamap.FlushEntry, error) {
	added := false
	var stored []shamap.FlushEntry
	for {
		missing, err := m.GetMissingNodesContext(ctx, localFillBatch, nil)
		if err != nil {
			return added, stored, err
		}
		if len(missing) == 0 {
			return added, stored, nil
		}
		passAdded := 0
		for i := range missing {
			data, ok := fetch(missing[i].Hash)
			if !ok {
				continue
			}
			res, entry, addErr := m.AddKnownNodeFromPrefixWithEntry(missing[i].NodeID, data)
			if addErr != nil {
				continue
			}
			if res == shamap.NodeUseful {
				passAdded++
				stored = append(stored, entry)
			}
		}
		if passAdded == 0 {
			return added, stored, nil
		}
		added = true
	}
}
