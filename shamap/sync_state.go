package shamap

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrSyncNotInProgress reports a sync-only operation on a map not syncing.
	errSyncNotInProgress = errors.New("sync not in progress")
	// ErrInvalidNodeData reports malformed serialized node data.
	ErrInvalidNodeData = errors.New("invalid node data")
	// ErrNodeHashMismatch reports node data that does not match its expected hash.
	errNodeHashMismatch = errors.New("node hash does not match expected")
	// ErrRootAlreadySet reports an attempt to replace a populated root.
	ErrRootAlreadySet = errors.New("root node already set")
	// ErrUnexpectedNode reports a valid node that does not fit the requested path.
	errUnexpectedNode = errors.New("unexpected node received")
	// ErrEmptyBranchOnPath reports a path through an empty branch.
	errEmptyBranchOnPath = errors.New("path descends into an empty branch")
	// ErrParentNotInTree reports acquisition data whose parent is not loaded.
	errParentNotInTree = errors.New("parent node not yet loaded for path")
	// ErrNodeSerialization reports failure to serialize an already-verified node.
	ErrNodeSerialization = errors.New("verified node serialization failed")
	// ErrTraversalBudget means a resumable backed walk reached its visit limit.
	ErrTraversalBudget = errors.New("SHAMap traversal budget exhausted")
	// ErrNodeNotInStore marks a backed-map descend whose child is genuinely
	// absent from the family store, distinguishing a true miss from a
	// transient fetch failure (I/O error, cancellation).
	ErrNodeNotInStore = errors.New("node not found in store")
)

// MissingNodeError identifies the exact node that was referenced by a backed
// map but absent from its family store. It unwraps to ErrNodeNotInStore so
// callers can classify the failure without losing the full repair key.
type MissingNodeError struct {
	Hash [32]byte
}

func (e *MissingNodeError) Error() string {
	if e == nil {
		return ErrNodeNotInStore.Error()
	}
	return fmt.Sprintf("node %x is missing: %s", e.Hash, ErrNodeNotInStore)
}

func (e *MissingNodeError) Unwrap() error {
	return ErrNodeNotInStore
}

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

type defaultSyncFilter struct{}

// ShouldFetch implements SyncFilter, always returning true.
func (f *defaultSyncFilter) ShouldFetch(nodeHash [32]byte) bool {
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

// MissingNodesStatus describes the amount of discovery work left after a
// resumable missing-node scan. The acquisition owner decides whether to call
// again immediately or wait for an external event.
type MissingNodesStatus uint8

const (
	// MissingNodesMore means the caller's result window or traversal slice was
	// exhausted while useful discovery work remains.
	MissingNodesMore MissingNodesStatus = iota
	// MissingNodesWaiting means a complete pass found unresolved or filtered
	// paths and is waiting for attachments, persistence, or a later retry.
	MissingNodesWaiting
	// MissingNodesComplete means every referenced descendant is verified and
	// the durable completion barrier has been satisfied.
	MissingNodesComplete
	// MissingNodesMemory means the scanner could not admit bounded state under
	// the shared acquisition byte budget.
	MissingNodesMemory
	// MissingNodesPersistence means the tree was discovered completely but its
	// attached nodes still need the caller's persistence acknowledgement.
	MissingNodesPersistence
)

// MissingNodesResult is the bounded output and progress state of one scanner
// slice. Nodes is owned by the caller and may be empty for More, Waiting or
// Complete outcomes.
type MissingNodesResult struct {
	Nodes  []MissingNode
	Status MissingNodesStatus
	reason missingNodesMoreReason
}

// StartSync prepares the SHAMap for synchronization.
// This sets the state to syncing and allows nodes to be added.
func (sm *SHAMap) StartSync() error {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state == stateInvalid {
		return fmt.Errorf("%w: cannot start sync on invalid map", errInvalidState)
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
		return errSyncNotInProgress
	}
	sm.tree.mu.RUnlock()
	result, err := sm.ScanMissingNodesContext(ctx, 1, nil)
	if err != nil {
		return fmt.Errorf("sync completeness walk: %w", err)
	}
	switch result.Status {
	case MissingNodesMemory:
		return fmt.Errorf("sync completeness walk: %w", ErrAcquisitionMemory)
	case MissingNodesMore:
		if result.reason == missingNodesTraversalYield {
			return fmt.Errorf("sync completeness walk: %w", ErrTraversalBudget)
		}
		return fmt.Errorf("sync incomplete: still have %d missing nodes", len(result.Nodes))
	case MissingNodesWaiting:
		return fmt.Errorf("sync incomplete: unresolved paths remain")
	case MissingNodesPersistence:
		return fmt.Errorf("sync incomplete: persistence acknowledgement is pending")
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	if sm.tree.state != stateSyncing {
		return errSyncNotInProgress
	}
	sm.tree.state = stateModifying

	return nil
}
