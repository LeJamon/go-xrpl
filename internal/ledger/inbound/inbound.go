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

	added := 0
	for _, node := range nodes {
		if len(node.NodeID) == shamap.NodeIDSize {
			// Response includes nodeID — extract the hash from the data itself
			// The AddKnownNode method verifies the hash matches
		}

		if len(node.NodeData) == 0 {
			continue
		}

		// Deserialize to get the hash, then add
		n, err := shamap.DeserializeNodeFromWire(node.NodeData)
		if err != nil {
			l.logger.Debug("inbound ledger: skip invalid node", "error", err)
			continue
		}
		if err := n.UpdateHash(); err != nil {
			l.logger.Debug("inbound ledger: skip node with bad hash", "error", err)
			continue
		}

		nodeHash := n.Hash()
		if err := l.stateMap.AddKnownNode(nodeHash, node.NodeData); err != nil {
			// May already have this node — not an error
			l.logger.Debug("inbound ledger: AddKnownNode", "error", err, "hash", fmt.Sprintf("%x", nodeHash[:8]))
			continue
		}
		added++
	}

	l.logger.Info("inbound ledger: added state nodes",
		"added", added,
		"total_received", len(nodes),
		"complete", l.stateMap.IsComplete(),
	)

	if l.stateMap.IsComplete() {
		if err := l.stateMap.FinishSync(); err != nil {
			l.state = StateFailed
			l.err = fmt.Errorf("finish sync: %w", err)
			return l.err
		}
		l.state = StateComplete
		l.logger.Info("inbound ledger: acquisition complete", "seq", l.header.LedgerIndex)
	}

	return nil
}

// missingNodeBatch caps how many missing-node IDs we ship per
// TMGetLedger request. Each request fans out to one peer response
// containing the requested subtree(s) plus QueryDepth=2 worth of
// descendants, so 16 keeps the response well under rippled's
// 256-node-per-reply ceiling while still amortizing several missing
// nodes per round trip.
const missingNodeBatch = 16

// NeedsMissingNodeIDs returns the wire-encoded NodeIDs of missing
// SHAMap inner nodes that the peer should ship back, ordered by depth
// so root-adjacent subtrees fill before deeper ones.
//
// Earlier this function unconditionally requested the SHAMap root with
// QueryDepth=2 and ignored the actual missing-node enumeration. That
// worked for tiny test ledgers (everything fits in root + 2 levels)
// but on a real network it deadlocked catch-up: once depth 0..2 was
// populated, any node at depth ≥3 stayed permanently missing because
// we kept asking for the same root subtree the peer had already
// served. The "1 missing node" log loop in issue #395 surfaced
// exactly this — `IsComplete()` never flips to true and the inbound
// acquisition wedges until the outer timeout.
//
// Returns nil if no nodes are missing or the map isn't ready to be
// fed; otherwise returns up to missingNodeBatch path-based NodeIDs
// from GetMissingNodes.
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
