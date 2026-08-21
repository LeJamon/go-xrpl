package shamap

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrSyncNotInProgress reports a sync-only operation on a map not syncing.
	ErrSyncNotInProgress = errors.New("sync not in progress")
	// ErrInvalidNodeData reports malformed serialized node data.
	ErrInvalidNodeData = errors.New("invalid node data")
	// ErrNodeHashMismatch reports node data that does not match its expected hash.
	ErrNodeHashMismatch = errors.New("node hash does not match expected")
	// ErrRootAlreadySet reports an attempt to replace a populated root.
	ErrRootAlreadySet = errors.New("root node already set")
	// ErrUnexpectedNode reports a valid node that does not fit the requested path.
	ErrUnexpectedNode = errors.New("unexpected node received")
	// ErrEmptyBranchOnPath reports a path through an empty branch.
	ErrEmptyBranchOnPath = errors.New("path descends into an empty branch")
	// ErrParentNotInTree reports acquisition data whose parent is not loaded.
	ErrParentNotInTree = errors.New("parent node not yet loaded for path")
	// ErrNodeSerialization reports failure to serialize an already-verified node.
	ErrNodeSerialization = errors.New("verified node serialization failed")
	// ErrTraversalBudget means a resumable backed walk reached its visit limit.
	ErrTraversalBudget = errors.New("SHAMap traversal budget exhausted")
	// ErrNodeNotInStore marks a backed-map descend whose child is genuinely
	// absent from the family store, distinguishing a true miss from a
	// transient fetch failure (I/O error, cancellation).
	ErrNodeNotInStore = errors.New("node not found in store")
)

// AddNodeResult classifies the outcome of placing a peer-supplied node by
// NodeID. It lets the inbound caller tell a genuinely bad node from one that
// is simply ahead of its frontier and should be re-requested rather than
// counted as a reject.
type AddNodeResult int

const (
	// NodeInvalid marks a node that is provably wrong for this map — a hash
	// mismatch, an inner node where only a leaf may live, or a path through a
	// branch this map does not reference (a node from another tree). The caller
	// should stop harvesting the rest of the reply.
	NodeInvalid AddNodeResult = iota
	// NodeUseful means a fresh node was attached at its slot.
	NodeUseful
	// NodeDuplicate means the slot was already populated, or the path
	// consolidated into a mid-path leaf — nothing to do.
	NodeDuplicate
	// NodeReRequest marks a well-formed node whose ancestor on the path is
	// still a hash-only stub, so it cannot be hooked yet. Not an error: the
	// next getMissingNodes walk re-requests the correct frontier and the node
	// returns on a later reply.
	NodeReRequest
)

// SyncFilter is an interface for filtering which nodes should be fetched during sync.
// This allows callers to avoid fetching nodes they already have locally.
type SyncFilter interface {
	// ShouldFetch returns true if the node with the given hash should be fetched.
	// This is called for each missing node discovered during sync traversal.
	ShouldFetch(nodeHash [32]byte) bool
}

// DefaultSyncFilter always returns true, fetching all missing nodes.
type DefaultSyncFilter struct{}

// ShouldFetch implements SyncFilter, always returning true.
func (f *DefaultSyncFilter) ShouldFetch(nodeHash [32]byte) bool {
	return true
}

// MissingNode represents a node that is referenced but not locally available.
// This is used during sync to track which nodes need to be fetched from peers.
type MissingNode struct {
	// Hash is the hash of the missing node
	Hash [32]byte
	// Depth is the depth in the tree where this node should exist
	Depth int
	// ParentHash is the hash of the parent node that references this node
	ParentHash [32]byte
	// Branch is the branch index in the parent node (0-15 for inner nodes)
	Branch int
	// Path-based ID; TMGetLedger locates by path, not hash.
	NodeID NodeID
}

// String returns a string representation of the MissingNode.
func (m *MissingNode) String() string {
	return fmt.Sprintf("MissingNode(hash=%x, depth=%d, parent=%x, branch=%d)",
		m.Hash[:8], m.Depth, m.ParentHash[:8], m.Branch)
}

// StartSync prepares the SHAMap for synchronization.
// This sets the state to syncing and allows nodes to be added.
func (sm *SHAMap) StartSync() error {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state == stateInvalid {
		return fmt.Errorf("%w: cannot start sync on invalid map", ErrInvalidState)
	}

	sm.tree.state = stateSyncing

	return nil
}

// FinishSync completes synchronization and validates the tree.
// This should be called after all missing nodes have been added.
func (sm *SHAMap) FinishSync() error {
	return sm.FinishSyncContext(context.Background())
}

// FinishSyncContext completes synchronization while allowing a backed-tree
// completeness walk to be canceled by its owner.
func (sm *SHAMap) FinishSyncContext(ctx context.Context) error {
	sm.tree.mu.RLock()
	if sm.tree.state != stateSyncing {
		sm.tree.mu.RUnlock()
		return ErrSyncNotInProgress
	}
	sm.tree.mu.RUnlock()
	sm.backing.mu.RLock()
	backed := sm.backing.access.available()
	sm.backing.mu.RUnlock()

	var missingNodes []MissingNode
	var err error
	if backed {
		missingNodes, err = sm.walkMapParallelContext(ctx, 1, nil)
	} else {
		sm.tree.mu.RLock()
		missingNodes, err = sm.missingNodesLocked(1, nil, true)
		sm.tree.mu.RUnlock()
	}
	if err != nil {
		return fmt.Errorf("sync completeness walk: %w", err)
	}
	if len(missingNodes) > 0 {
		return fmt.Errorf("sync incomplete: still have %d missing nodes", len(missingNodes))
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	if sm.tree.state != stateSyncing {
		return ErrSyncNotInProgress
	}
	sm.tree.state = stateModifying

	return nil
}
