// Package inbound provides lightweight ledger acquisition from peers.
// It fetches the full ledger header and state tree via the TMGetLedger/TMLedgerData
// peer protocol, matching rippled's InboundLedger behavior.
package inbound

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/shamap"
)

const acquisitionTimeout = 10 * time.Second

// State tracks the acquisition progress.
type State int

const (
	StateWantBase  State = iota // Waiting for header + root nodes
	StateWantState              // Have header, fetching state tree nodes
	StateComplete               // Fully acquired
	StateFailed                 // Unrecoverable error
)

// Ledger manages the acquisition of a single ledger from a peer.
// It progresses through: WantBase → WantState → Complete.
//
// Field lock guarantees:
//   - hash, seq, peerID, created, logger are set at construction and never
//     mutated thereafter; the accessors below (Hash, Seq, PeerID) read them
//     without taking mu and are safe under concurrent State() callers.
//   - header, stateMap, state, err are written under mu and must be read
//     through accessors that take mu (State, IsTimedOut, GotBase, etc.).
type Ledger struct {
	hash     [32]byte
	seq      uint32
	header   *header.LedgerHeader
	stateMap *shamap.SHAMap
	peerID   uint64
	state    State
	err      error
	created  time.Time
	mu       sync.Mutex
	logger   *slog.Logger
}

// New creates a new InboundLedger acquisition for the given ledger hash.
func New(hash [32]byte, seq uint32, peerID uint64, logger *slog.Logger) *Ledger {
	return &Ledger{
		hash:    hash,
		seq:     seq,
		peerID:  peerID,
		state:   StateWantBase,
		created: time.Now(),
		logger:  logger,
	}
}

// IsTimedOut returns true if the acquisition has been running too long.
func (l *Ledger) IsTimedOut() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state != StateComplete && l.state != StateFailed && time.Since(l.created) > acquisitionTimeout
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

// GotBase processes the LedgerInfoBase response containing the header and root nodes.
// Rippled sends: node[0]=header, node[1]=state root, node[2]=tx root.
func (l *Ledger) GotBase(nodes []message.LedgerNode) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Ignore duplicate responses after we've moved past WantBase
	if l.state != StateWantBase {
		return nil
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
	// The wire format doesn't include the hash — set it from our known hash.
	h.Hash = l.hash
	l.header = h

	l.logger.Info("inbound ledger: got header",
		"seq", h.LedgerIndex,
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)

	// Create state SHAMap and add the root node
	sm, err := shamap.New(shamap.TypeState)
	if err != nil {
		l.state = StateFailed
		l.err = fmt.Errorf("create state map: %w", err)
		return l.err
	}

	if err := sm.AddRootNode(h.AccountHash, nodes[1].NodeData); err != nil {
		l.state = StateFailed
		l.err = fmt.Errorf("add state root node: %w", err)
		return l.err
	}

	l.stateMap = sm

	// Always transition to WantState. Even if the root has no missing children,
	// the caller will check IsComplete() and finalize via GotStateNodes path.
	l.state = StateWantState

	l.logger.Info("inbound ledger: state root added, fetching missing nodes",
		"seq", h.LedgerIndex,
		"missing", len(sm.GetMissingNodes(16, nil)),
	)

	return nil
}

// GotStateNodes processes state tree nodes received from the peer.
func (l *Ledger) GotStateNodes(nodes []message.LedgerNode) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state == StateComplete {
		return nil // Already done
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
		if err := l.stateMap.AddKnownNodeByID(parsedID, node.NodeData); err != nil {
			l.logger.Debug("inbound ledger: state node rejected",
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err.Error())
			continue
		}
		added++
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
		l.logger.Debug("inbound ledger: still incomplete", "error", err)
		return nil
	}
	l.state = StateComplete
	l.logger.Info("inbound ledger: acquisition complete", "seq", l.header.LedgerIndex)

	return nil
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

	if l.stateMap == nil || l.state != StateWantState {
		return nil
	}

	missing := l.stateMap.GetMissingNodes(missingNodeBatch, nil)
	if len(missing) == 0 {
		return nil
	}

	nodeIDs := make([][]byte, 0, len(missing))
	for i := range missing {
		nodeIDs = append(nodeIDs, missing[i].NodeID.Bytes())
	}
	return nodeIDs
}

// Snapshot is a point-in-time view of an acquisition's progress, used by
// the fetch_info RPC (mirrors the per-ledger fields rippled emits from
// InboundLedger::getJson). goXRPL's classic acquisition fetches only the
// header + state tree, so there is no have_transactions/needed_transaction
// counterpart, and it reaps on first timeout rather than counting
// re-request cycles, so there is no timeouts counter.
type Snapshot struct {
	Hash        [32]byte
	Seq         uint32
	HaveHeader  bool
	HaveState   bool
	Complete    bool
	Failed      bool
	TimedOut    bool
	NeededState [][32]byte // hashes of up to missingNodeBatch missing state nodes
}

// Snapshot returns a consistent view of the acquisition's progress under
// the lock, safe to call from any goroutine.
func (l *Ledger) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := Snapshot{
		Hash:       l.hash,
		Seq:        l.seq,
		HaveHeader: l.header != nil,
		HaveState:  l.state == StateComplete,
		Complete:   l.state == StateComplete,
		Failed:     l.state == StateFailed,
		TimedOut: l.state != StateComplete && l.state != StateFailed &&
			time.Since(l.created) > acquisitionTimeout,
	}

	if l.state == StateWantState && l.stateMap != nil {
		for _, m := range l.stateMap.GetMissingNodes(missingNodeBatch, nil) {
			s.NeededState = append(s.NeededState, m.Hash)
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

// Result returns the acquired header and state map.
// Only valid after IsComplete() returns true.
func (l *Ledger) Result() (*header.LedgerHeader, *shamap.SHAMap, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateComplete {
		return nil, nil, fmt.Errorf("acquisition not complete (state=%d)", l.state)
	}

	return l.header, l.stateMap, nil
}

// Err returns the error if the acquisition failed.
func (l *Ledger) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}
