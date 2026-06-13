// Package inbound provides lightweight ledger acquisition from peers.
// It fetches the full ledger header, account-state tree, and transaction tree
// via the TMGetLedger/TMLedgerData peer protocol, matching rippled's
// InboundLedger behavior.
package inbound

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const acquisitionTimeout = 10 * time.Second

// hardMaxReplyNodes is rippled's per-message cap on the nodes a peer may pack
// into a single TMLedgerData reply (Tuning::hardMaxReplyNodes, Tuning.h:42).
const hardMaxReplyNodes = 12288

// checkReplyNodeCount enforces the bounds rippled places on a single
// TMLedgerData reply — at least one node, at most hardMaxReplyNodes — so the
// router can charge an offending peer badData. Mirrors the ingress guard in
// rippled's PeerImp::onMessage(TMLedgerData) (PeerImp.cpp:1628), which rejects
// both nodes_size() <= 0 and nodes_size() > Tuning::hardMaxReplyNodes.
func checkReplyNodeCount(nodes []message.LedgerNode) error {
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
)

// State tracks the acquisition progress.
type State int

const (
	StateWantBase  State = iota // Waiting for header + root nodes
	StateWantState              // Have header, fetching state tree nodes
	StateComplete               // Fully acquired
	StateFailed                 // Unrecoverable error
)

// Ledger manages the acquisition of a single ledger from a peer.
// It progresses through: WantBase → WantState → Complete. Like rippled's
// InboundLedger, it fetches the account-state and transaction trees in
// parallel once the header is in hand; the acquisition is Complete only when
// both have been fully fetched (rippled InboundLedger.cpp:734,946).
//
// Field lock guarantees:
//   - hash, seq, peerID, clock, logger are set at construction and never
//     mutated thereafter; the accessors below (Hash, Seq, PeerID) read them
//     without taking mu and are safe under concurrent State() callers.
//   - header, stateMap, txMap, haveState, haveTx, state, err, created,
//     fetchPackRequested are written under mu and must be read through
//     accessors that take mu (State, IsTimedOut, GotBase, etc.).
type Ledger struct {
	hash      [32]byte
	seq       uint32
	header    *header.LedgerHeader
	stateMap  *shamap.SHAMap
	txMap     *shamap.SHAMap // nil when the transaction tree is empty (TxHash zero)
	haveState bool
	haveTx    bool
	peerID    uint64
	reason    Reason
	state     State
	err       error
	created   time.Time
	clock     Clock
	mu        sync.Mutex
	logger    *slog.Logger

	// fetchPackRequested records that the router escalated this stalled
	// acquisition to a fetch-pack (at most once). Guarded by mu.
	fetchPackRequested bool
}

// New creates a new InboundLedger acquisition for the given ledger hash.
// The acquisition reason defaults to ReasonConsensus.
func New(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger) *Ledger {
	return &Ledger{
		hash:    hash,
		seq:     seq,
		peerID:  peerID,
		state:   StateWantBase,
		clock:   SystemClock,
		created: SystemClock.Now(),
		logger:  logger,
	}
}

// NewGeneric creates an RPC-driven (ReasonGeneric) acquisition: on completion
// the ledger is persisted for querying but not adopted into the active chain.
func NewGeneric(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger) *Ledger {
	l := New(hash, seq, peerID, logger)
	l.reason = ReasonGeneric
	return l
}

// Reason returns why this acquisition was started.
func (l *Ledger) Reason() Reason {
	return l.reason
}

// IsTimedOut returns true if the acquisition has been running too long.
func (l *Ledger) IsTimedOut() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state != StateComplete && l.state != StateFailed && l.clock.Now().Sub(l.created) > acquisitionTimeout
}

// State returns the current acquisition state.
func (l *Ledger) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// PeerID returns the peer we're fetching from.
func (l *Ledger) PeerID() uint64 {
	return l.peerID
}

// Seq returns the ledger sequence being acquired.
func (l *Ledger) Seq() uint32 {
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
	l.mu.Lock()
	defer l.mu.Unlock()

	// Ignore duplicate responses after we've moved past WantBase
	if l.state != StateWantBase {
		return nil
	}

	if len(nodes) > hardMaxReplyNodes {
		l.state = StateFailed
		l.err = fmt.Errorf("ledger data exceeds hardMaxReplyNodes: %d > %d", len(nodes), hardMaxReplyNodes)
		return l.err
	}

	if len(nodes) < 2 {
		l.state = StateFailed
		l.err = fmt.Errorf("need at least 2 nodes (header + state root), got %d", len(nodes))
		return l.err
	}

	// Parse header from node[0].
	// Rippled's sendLedgerBase() serializes with addRaw(info, s) — no prefix, no hash.
	// The data is exactly 118 bytes (SizeBase).
	h, err := header.DeserializeHeader(nodes[0].NodeData, false)
	if err != nil {
		// Try with prefix (some sources add a 4-byte prefix)
		h, err = header.DeserializePrefixedHeader(nodes[0].NodeData, false)
		if err != nil {
			l.state = StateFailed
			l.err = fmt.Errorf("deserialize header: %w (data_len=%d)", err, len(nodes[0].NodeData))
			return l.err
		}
	}
	// The wire format doesn't include the hash, so recompute it and reject a
	// peer that supplied a header whose true hash (or seq, when known) doesn't
	// match what we asked for. Mirrors rippled's takeHeader (InboundLedger.cpp:830).
	//
	// Hash the canonical on-the-wire header bytes with the ledgerMaster prefix
	// rather than going through CalculateLedgerHash on the parsed struct: the
	// parse path runs close times through xrplEpochToTime, which collapses an
	// epoch of 0 (the XRPL ripple epoch) into a Go zero time and defeats the
	// reverse arithmetic CalculateLedgerHash relies on. AddRaw re-emits the exact
	// bytes a peer signs, so the byte-level hash is the only round-trip-safe
	// invariant (same approach as the LedgerReplay path in replay_delta.go).
	computed := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), header.AddRaw(*h, false))
	if computed != l.hash || (l.seq != 0 && l.seq != h.LedgerIndex) {
		l.state = StateFailed
		l.err = fmt.Errorf("acquire hash mismatch: computed %x != requested %x (seq %d, requested %d)",
			computed[:8], l.hash[:8], h.LedgerIndex, l.seq)
		return l.err
	}
	h.Hash = computed
	// When acquiring by hash alone (seq unknown), adopt the verified header's
	// seq, mirroring rippled's takeHeader (InboundLedger.cpp:839-840).
	if l.seq == 0 {
		l.seq = h.LedgerIndex
	}
	l.header = h

	l.logger.Info("inbound ledger: got header",
		"seq", h.LedgerIndex,
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)

	// Create state SHAMap and add the root node
	sm := shamap.New(shamap.TypeState)

	if err := sm.AddRootNode(h.AccountHash, nodes[1].NodeData); err != nil {
		l.state = StateFailed
		l.err = fmt.Errorf("add state root node: %w", err)
		return l.err
	}

	l.stateMap = sm
	l.haveState = sm.FinishSync() == nil

	// Set up the transaction tree. An empty tx tree has a zero TxHash and is
	// complete immediately (rippled InboundLedger.cpp:850); otherwise the peer
	// ships its root as node[2].
	if h.TxHash == ([32]byte{}) {
		l.haveTx = true
	} else {
		tm := shamap.New(shamap.TypeTransaction)
		if len(nodes) >= 3 && len(nodes[2].NodeData) > 0 {
			if err := tm.AddRootNode(h.TxHash, nodes[2].NodeData); err != nil {
				l.state = StateFailed
				l.err = fmt.Errorf("add tx root node: %w", err)
				return l.err
			}
			l.txMap = tm
			l.haveTx = tm.FinishSync() == nil
		} else {
			// A well-behaved peer always ships the tx root alongside the state
			// root. Adopting with an empty tx map would complete a ledger that
			// advertises a non-zero TxHash over an empty tx tree — internally
			// inconsistent state served to tx/tx_history RPCs and to peers
			// re-requesting the ledger. Fail the acquisition instead: the router
			// penalizes the peer and re-requests from another (rippled never
			// completes a ledger with a missing tx tree).
			l.state = StateFailed
			l.err = fmt.Errorf("inbound ledger %d: tx root absent for non-empty tx tree", h.LedgerIndex)
			return l.err
		}
	}

	if l.haveState && l.haveTx {
		l.state = StateComplete
	} else {
		l.state = StateWantState
	}

	l.logger.Info("inbound ledger: roots added, fetching missing nodes",
		"seq", h.LedgerIndex,
		"have_state", l.haveState,
		"have_tx", l.haveTx,
	)

	return nil
}

// GotStateNodes processes state tree nodes received from the peer.
func (l *Ledger) GotStateNodes(nodes []message.LedgerNode) error {
	if err := checkReplyNodeCount(nodes); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state == StateComplete || l.haveState {
		return nil // State tree already acquired
	}
	if l.state != StateWantState {
		return fmt.Errorf("unexpected state %d for GotStateNodes", l.state)
	}

	// Mirrors the tx-set sync fix in router.handleTxSetData (issue #413):
	// drive placement by the peer-supplied NodeID via AddKnownNodeByID
	// rather than the hash-search AddKnownNodeUnchecked, which silently
	// drops nodes whose direct parent isn't loaded yet.
	added := 0
	for _, node := range nodes {
		if len(node.NodeData) == 0 {
			continue
		}
		parsedID, err := shamap.UnmarshalBinary(node.NodeID)
		if err != nil {
			l.logger.Debug("inbound ledger: malformed state node ID",
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			continue
		}
		if parsedID.IsRoot() {
			continue
		}
		fresh, err := l.stateMap.AddKnownNodeByID(parsedID, node.NodeData)
		if err != nil {
			l.logger.Debug("inbound ledger: state node rejected",
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err.Error())
			continue
		}
		if fresh {
			added++
		}
	}

	complete := l.stateMap.IsComplete()
	l.logger.Info("inbound ledger: added state nodes",
		"added", added,
		"total_received", len(nodes),
		"complete", complete,
	)

	// Always attempt FinishSync — it is the only authoritative check
	// (IsComplete reads under RLock and can race a concurrent insert
	// before the FinishSync write lock). A failure here is treated as
	// "still missing nodes", not fatal.
	if err := l.stateMap.FinishSync(); err != nil {
		l.logger.Debug("inbound ledger: state still incomplete", "error", err)
		return nil
	}
	l.haveState = true
	l.recomputeComplete()

	return nil
}

// GotTransactionNodes processes transaction tree nodes received from the peer.
// It mirrors GotStateNodes: drive placement by the peer-supplied NodeID, then
// FinishSync as the authoritative completeness check.
func (l *Ledger) GotTransactionNodes(nodes []message.LedgerNode) error {
	if err := checkReplyNodeCount(nodes); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state == StateComplete || l.haveTx {
		return nil // Transaction tree already acquired (or empty)
	}
	if l.state != StateWantState || l.txMap == nil {
		return fmt.Errorf("unexpected state %d for GotTransactionNodes", l.state)
	}

	added := 0
	for _, node := range nodes {
		if len(node.NodeData) == 0 {
			continue
		}
		parsedID, err := shamap.UnmarshalBinary(node.NodeID)
		if err != nil {
			l.logger.Debug("inbound ledger: malformed tx node ID",
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			continue
		}
		if parsedID.IsRoot() {
			continue
		}
		fresh, err := l.txMap.AddKnownNodeByID(parsedID, node.NodeData)
		if err != nil {
			l.logger.Debug("inbound ledger: tx node rejected",
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err.Error())
			continue
		}
		if fresh {
			added++
		}
	}

	l.logger.Info("inbound ledger: added tx nodes",
		"added", added,
		"total_received", len(nodes),
	)

	if err := l.txMap.FinishSync(); err != nil {
		l.logger.Debug("inbound ledger: tx still incomplete", "error", err)
		return nil
	}
	l.haveTx = true
	l.recomputeComplete()

	return nil
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
}

// missingNodeBatch caps NodeIDs per TMGetLedger request. Sits between
// rippled's blind-request cap (reqNodes=12) and reply cap
// (reqNodesReply=128, InboundLedger.cpp).
const missingNodeBatch = 16

// NeedsMissingNodeIDs returns up to missingNodeBatch wire-encoded
// path-based NodeIDs of missing SHAMap inner nodes, ordered by depth.
// Returns nil if the state map is complete or not yet ready (issue #395).
func (l *Ledger) NeedsMissingNodeIDs() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stateMap == nil || l.haveState || l.state != StateWantState {
		return nil
	}
	return missingNodeIDs(l.stateMap)
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
	return missingNodeIDs(l.txMap)
}

// missingNodeIDs returns up to missingNodeBatch wire-encoded path-based NodeIDs
// of missing inner nodes in m, or nil when the map is complete.
func missingNodeIDs(m *shamap.SHAMap) [][]byte {
	missing := m.GetMissingNodes(missingNodeBatch, nil)
	if len(missing) == 0 {
		return nil
	}
	nodeIDs := make([][]byte, 0, len(missing))
	for i := range missing {
		nodeIDs = append(nodeIDs, missing[i].NodeID.Bytes())
	}
	return nodeIDs
}

// Snapshot is a point-in-time view of an acquisition's progress, used by the
// fetch_info RPC (mirrors the per-ledger fields rippled emits from
// InboundLedger::getJson). The acquisition reaps on first timeout rather than
// counting re-request cycles, so the timeouts field is always reported as zero,
// and it fetches from a single source peer (Peers == 1 while in flight).
type Snapshot struct {
	Hash             [32]byte
	Seq              uint32
	HaveHeader       bool
	HaveState        bool
	HaveTransactions bool
	Complete         bool
	Failed           bool
	TimedOut         bool
	Peers            int
	NeededState      [][32]byte // hashes of up to missingNodeBatch missing state nodes
	NeededTx         [][32]byte // hashes of up to missingNodeBatch missing tx nodes
}

// Snapshot returns a consistent view of the acquisition's progress under
// the lock, safe to call from any goroutine.
func (l *Ledger) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := Snapshot{
		Hash:             l.hash,
		Seq:              l.seq,
		HaveHeader:       l.header != nil,
		HaveState:        l.haveState,
		HaveTransactions: l.haveTx,
		Complete:         l.state == StateComplete,
		Failed:           l.state == StateFailed,
		TimedOut: l.state != StateComplete && l.state != StateFailed &&
			l.clock.Now().Sub(l.created) > acquisitionTimeout,
		Peers: 1, // classic acquisition fetches from a single source peer (l.peerID)
	}

	if !l.haveState && l.stateMap != nil {
		for _, m := range l.stateMap.GetMissingNodes(missingNodeBatch, nil) {
			s.NeededState = append(s.NeededState, m.Hash)
		}
	}
	if !l.haveTx && l.txMap != nil {
		for _, m := range l.txMap.GetMissingNodes(missingNodeBatch, nil) {
			s.NeededTx = append(s.NeededTx, m.Hash)
		}
	}

	return s
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

// MarkFetchPackRequested records that a fetch-pack was requested and grants
// one more acquisitionTimeout window for the reply to arrive and complete the
// acquisition locally via CheckLocal. Mirrors rippled arming an aggressive
// fetch-pack fallback (LedgerMaster::getFetchPack) without immediately
// abandoning the InboundLedger.
func (l *Ledger) MarkFetchPackRequested() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fetchPackRequested = true
	l.created = l.clock.Now()
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
	if fetch == nil {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateWantState {
		return false
	}

	progressed := false
	if !l.haveState && l.stateMap != nil {
		if fillFromLocal(l.stateMap, fetch) {
			progressed = true
			if l.stateMap.FinishSync() == nil {
				l.haveState = true
			}
		}
	}
	if !l.haveTx && l.txMap != nil {
		if fillFromLocal(l.txMap, fetch) {
			progressed = true
			if l.txMap.FinishSync() == nil {
				l.haveTx = true
			}
		}
	}
	if progressed {
		l.recomputeComplete()
	}
	return progressed
}

// fillFromLocal repeatedly pulls a map's missing node hashes from fetch and
// attaches any that resolve, until a pass attaches nothing. Returns whether it
// attached at least one node. Each pass widens the resolved frontier — an
// attached inner node exposes its children as the next batch's missing set —
// so a connected subtree present in the source drains fully.
func fillFromLocal(m *shamap.SHAMap, fetch func(hash [32]byte) ([]byte, bool)) bool {
	added := false
	for {
		missing := m.GetMissingNodes(localFillBatch, nil)
		if len(missing) == 0 {
			return added
		}
		passAdded := 0
		for i := range missing {
			data, ok := fetch(missing[i].Hash)
			if !ok {
				continue
			}
			if fresh, err := m.AddKnownNodeFromPrefix(missing[i].NodeID, data); err == nil && fresh {
				passAdded++
			}
		}
		if passAdded == 0 {
			return added
		}
		added = true
	}
}
