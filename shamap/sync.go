package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Sync-related errors
var (
	ErrSyncNotInProgress = errors.New("sync not in progress")
	ErrInvalidNodeData   = errors.New("invalid node data")
	ErrNodeHashMismatch  = errors.New("node hash does not match expected")
	ErrRootAlreadySet    = errors.New("root node already set")
	ErrUnexpectedNode    = errors.New("unexpected node received")
	ErrEmptyBranchOnPath = errors.New("path descends into an empty branch")
	ErrParentNotInTree   = errors.New("parent node not yet loaded for path")
	ErrNodeSerialization = errors.New("verified node serialization failed")
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

// WalkMap walks the SHAMap and returns every non-empty branch whose
// child node is neither in memory nor recoverable from the local
// NodeStore. Returns nil when the root is empty or the map is in
// StateInvalid.
//
// Mirrors rippled's SHAMap::walkMap (SHAMapDelta.cpp:240): for backed
// maps, hash-only branches are lazy-loaded via the family before being
// declared missing, matching rippled's descendNoStore semantics. For
// unbacked maps the walk is purely in-memory.
//
// maxMissing == 0 is unbounded; otherwise the walk stops once that many
// entries have been collected. A nil filter behaves like DefaultSyncFilter.
func (sm *SHAMap) WalkMap(maxMissing int, filter SyncFilter) []MissingNode {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state == StateInvalid {
		return nil
	}
	return sm.getMissingNodesUnsafe(maxMissing, filter)
}

// WalkMapParallel is the parallel variant of WalkMap. It fans out across the
// root branches and lets each bounded worker walk its subtree independently.
// Results share a single slice guarded by a mutex, and exhausting maxMissing
// cancels sibling walks before they continue loading unrelated subtrees.
//
// Modeled on rippled's SHAMap::walkMapParallel (SHAMapDelta.cpp:282).
// One intentional divergence: hash-only branches at root depth 1 that
// the local store cannot satisfy are reported as missing here. Rippled's
// walkMapParallel silently drops them (its top-children capture at
// SHAMapDelta.cpp:290-318 skips any nullptr child without emitting a
// missing entry, which makes its result disagree with rippled's own
// serial walkMap). This Go walker stays consistent with the serial
// WalkMap so the two produce the same result set. As in WalkMap, backed
// maps lazy-load hash-only branches from the family before declaring
// them missing.
//
// A SHAMap therefore runs at most 16 workers. Store-loaded nodes are retained
// only along incomplete paths needed to attach later peer responses.
func (sm *SHAMap) WalkMapParallel(maxMissing int, filter SyncFilter) []MissingNode {
	missing, _ := sm.WalkMapParallelContext(context.Background(), maxMissing, filter)
	return missing
}

// WalkMapParallelContext is WalkMapParallel with cancellation propagated to
// each backing-store fetch and subtree worker.
func (sm *SHAMap) WalkMapParallelContext(ctx context.Context, maxMissing int, filter SyncFilter) ([]MissingNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter == nil {
		filter = &DefaultSyncFilter{}
	}
	sm.walkMu.Lock()
	defer sm.walkMu.Unlock()

	type subtreeStart struct {
		node     *innerNode
		nodeID   NodeID
		nodeHash [32]byte
	}

	sm.mu.RLock()
	sm.familyMu.RLock()
	if sm.root == nil || sm.state == StateInvalid {
		sm.familyMu.RUnlock()
		sm.mu.RUnlock()
		return nil, nil
	}
	root := sm.root
	family := sm.family
	cache := sm.fullBelow
	sm.mu.RUnlock()
	defer sm.familyMu.RUnlock()

	gen := uint32(0)
	done := func() {}
	if cache != nil {
		gen, done = cache.Begin()
	}
	defer done()
	backed := family != nil && cache != nil
	rootID := NewRootNodeID()
	rootHash := root.Hash()
	if backed {
		return sm.walkBackedContext(ctx, root, family, cache, gen, maxMissing, filter)
	}

	if root.isFullBelow(gen) {
		return nil, nil
	}

	var (
		mu           sync.Mutex
		missing      []MissingNode
		stopped      bool
		stopWalk     atomic.Bool
		rootComplete = true
		subtrees     = make([]subtreeStart, 0, BranchFactor)
	)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reportLocked := func(m MissingNode) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return true
		}
		missing = append(missing, m)
		if maxMissing > 0 && len(missing) >= maxMissing {
			stopped = true
			stopWalk.Store(true)
			return true
		}
		return false
	}

	for branch := range BranchFactor {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if stopWalk.Load() {
			rootComplete = false
			break
		}
		child, childHash, isSet := root.LoadChild(branch)
		if !isSet {
			continue
		}
		childNodeID, err := rootID.ChildNodeID(uint8(branch))
		if err != nil {
			continue
		}
		if child == nil {
			rootComplete = false
			if filter.ShouldFetch(childHash) {
				if reportLocked(MissingNode{
					Hash:       childHash,
					Depth:      1,
					ParentHash: rootHash,
					Branch:     branch,
					NodeID:     childNodeID,
				}) {
					break
				}
			}
			continue
		}
		inner, ok := child.(*innerNode)
		if !ok {
			continue
		}
		subtrees = append(subtrees, subtreeStart{
			node:     inner,
			nodeID:   childNodeID,
			nodeHash: childHash,
		})
	}

	if len(subtrees) == 0 {
		// No inner subtrees to fan out. Mark the root complete when every
		// depth-1 branch was resident-and-full-below and nothing stopped.
		mu.Lock()
		markRoot := rootComplete && !stopped
		mu.Unlock()
		if markRoot {
			markRootFullBelow(root, gen)
		}
		return missing, ctx.Err()
	}

	subFull := make([]bool, len(subtrees))
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(subtrees))
	for i, s := range subtrees {
		go func() {
			defer wg.Done()
			result, walkErr := walkFullBelowState(
				ctx, sm, nil, s.node, s.nodeID, s.nodeHash, 1, gen, filter, false, cache, false, false, reportLocked,
				func() bool { return stopWalk.Load() || ctx.Err() != nil },
			)
			subFull[i] = result.full
			if result.stopped {
				stopWalk.Store(true)
			}
			if walkErr != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = walkErr
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	for i := range subtrees {
		if !subFull[i] {
			rootComplete = false
		}
	}

	// Mark the root full-below only when every branch — those handled
	// inline and every fanned-out subtree — came back complete and no
	// worker was cut short by the missing cap.
	allSub := true
	for _, f := range subFull {
		if !f {
			allSub = false
			break
		}
	}
	mu.Lock()
	markRoot := rootComplete && allSub && !stopped && !stopWalk.Load()
	mu.Unlock()
	if markRoot {
		markRootFullBelow(root, gen)
	}

	return missing, nil
}

// markRootFullBelow records the root as full-below at gen.
func markRootFullBelow(root *innerNode, gen uint32) {
	root.setFullBelowGen(gen)
}

// GetMissingNodes returns the nodes referenced by the tree but not
// present locally. It is gated on StateSyncing — for any other state
// the map is assumed complete and the result is nil.
//
// The actual walk is performed by WalkMapParallel so the per-root-branch
// fan-out is shared with the lower-level WalkMap API. maxNodes == 0 is
// unbounded; a nil filter behaves like DefaultSyncFilter.
func (sm *SHAMap) GetMissingNodes(maxNodes int, filter SyncFilter) []MissingNode {
	missing, _ := sm.GetMissingNodesContext(context.Background(), maxNodes, filter)
	return missing
}

// GetMissingNodesContext is GetMissingNodes with cancellation propagated to
// the parallel traversal and backing Family.
func (sm *SHAMap) GetMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter) ([]MissingNode, error) {
	sm.mu.RLock()
	state := sm.state
	sm.mu.RUnlock()
	if state != StateSyncing {
		return nil, nil
	}
	return sm.WalkMapParallelContext(ctx, maxNodes, filter)
}

// getMissingNodesUnsafe collects up to maxNodes missing-node references
// using the same lazy-loading subtree walk as WalkMap and WalkMapParallel,
// so all sync entry points agree about whether a backed map is complete.
// Lenient on transient store errors (the request path). Caller must hold at
// least the read lock.
func (sm *SHAMap) getMissingNodesUnsafe(maxNodes int, filter SyncFilter) []MissingNode {
	missing, _ := sm.missingNodesLocked(maxNodes, filter, false)
	return missing
}

// missingNodesLocked is the shared walk behind the lenient request path and
// the strict completeness checks (FinishSync, IsComplete). strict=true
// aborts on a transient store error instead of reporting phantom missing
// nodes. Caller must hold at least the read lock.
func (sm *SHAMap) missingNodesLocked(maxNodes int, filter SyncFilter, strict bool) ([]MissingNode, error) {
	if filter == nil {
		filter = &DefaultSyncFilter{}
	}
	if sm.root == nil {
		return nil, nil
	}

	var missing []MissingNode
	_, err := walkSubtreeForMissing(
		context.Background(), sm,
		sm.root,
		NewRootNodeID(),
		sm.root.Hash(),
		0,
		filter,
		strict,
		func(m MissingNode) bool {
			missing = append(missing, m)
			return maxNodes > 0 && len(missing) >= maxNodes
		},
	)
	if err != nil {
		return nil, err
	}
	return missing, nil
}

// AddKnownNode adds a node received from an external source.
// This is used during synchronization to populate the tree with data from peers.
//
// Parameters:
//   - nodeHash: the expected hash of the node
//   - data: the serialized wire format of the node
//
// Returns an error if the node data is invalid or doesn't match the expected hash.
func (sm *SHAMap) AddKnownNode(nodeHash [32]byte, data []byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.attachmentMu.Lock()
	defer sm.attachmentMu.Unlock()

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}

	if len(data) == 0 {
		return ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
	}

	// Verify the hash matches
	if err := node.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := node.Hash()
	if !bytes.Equal(computedHash[:], nodeHash[:]) {
		return ErrNodeHashMismatch
	}

	// Find the location in the tree where this node belongs
	return sm.insertKnownNode(nodeHash, node)
}

// AddKnownNodeFromPrefix inserts a node from prefix-format data at the
// position identified by nodeID. Unlike AddKnownNode (which expects wire
// format and searches the tree for the parent by hash), this expects the
// [HashPrefix][body] serialization used by fetch-pack nodes and descends
// directly along the NodeID path. The node's computed hash must match the
// parent's stored child hash at the target branch.
//
// Returns the same results as AddKnownNodeByID.
func (sm *SHAMap) AddKnownNodeFromPrefix(nodeID NodeID, data []byte) (AddNodeResult, error) {
	result, _, err := sm.addKnownNodeFromPrefix(nodeID, data, false)
	return result, err
}

// AddKnownNodeFromPrefixWithEntry inserts and verifies a prefix-format node,
// returning the clean node's NodeStore entry for the caller to persist.
func (sm *SHAMap) AddKnownNodeFromPrefixWithEntry(nodeID NodeID, data []byte) (AddNodeResult, FlushEntry, error) {
	return sm.addKnownNodeFromPrefix(nodeID, data, true)
}

func (sm *SHAMap) addKnownNodeFromPrefix(nodeID NodeID, data []byte, withEntry bool) (AddNodeResult, FlushEntry, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.attachmentMu.Lock()
	defer sm.attachmentMu.Unlock()

	if sm.state != StateSyncing {
		return NodeInvalid, FlushEntry{}, ErrSyncNotInProgress
	}
	if nodeID.IsRoot() {
		return NodeInvalid, FlushEntry{}, ErrUnexpectedNode
	}
	if len(data) == 0 {
		return NodeInvalid, FlushEntry{}, ErrInvalidNodeData
	}

	var verified Node
	result, err := sm.attachKnownNodeAt(nodeID, !withEntry, func() (Node, error) {
		var derr error
		verified, derr = deserializeFromPrefix(data)
		return verified, derr
	})
	if err != nil || result != NodeUseful || !withEntry {
		return result, FlushEntry{}, err
	}
	entry, err := flushEntryForNode(verified, sm.ledgerSeq, sm.mapType)
	if err != nil {
		return result, FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return result, entry, err
}

// AddKnownNodeByID inserts a node from wire data at the position specified
// by the peer-supplied SHAMap NodeID (path + depth). The node's computed
// hash must match the parent's stored child hash at the target branch.
//
// Descent through the partial tree is driven by the NodeID, not by
// hash-searching.
//
// Returns an AddNodeResult and, only for NodeInvalid, the underlying error:
//   - NodeUseful, nil on a fresh attach
//   - NodeDuplicate, nil when the slot is already populated or the path
//     consolidated into a mid-path leaf
//   - NodeReRequest, nil when an ancestor on the path is still a hash-only
//     stub: not a reject, re-requested by the next getMissingNodes walk
//   - NodeInvalid with ErrEmptyBranchOnPath (path into a branch this map does
//     not reference), ErrNodeHashMismatch, ErrUnexpectedNode (inner where only
//     a leaf may live), or ErrSyncNotInProgress / ErrInvalidNodeData on misuse
func (sm *SHAMap) AddKnownNodeByID(nodeID NodeID, data []byte) (AddNodeResult, error) {
	result, _, err := sm.addKnownNodeByID(nodeID, data, false)
	return result, err
}

// AddKnownNodeByIDWithEntry inserts and verifies a wire node, returning the
// clean node's NodeStore entry for the caller to persist.
func (sm *SHAMap) AddKnownNodeByIDWithEntry(nodeID NodeID, data []byte) (AddNodeResult, FlushEntry, error) {
	return sm.addKnownNodeByID(nodeID, data, true)
}

func (sm *SHAMap) addKnownNodeByID(nodeID NodeID, data []byte, withEntry bool) (AddNodeResult, FlushEntry, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.attachmentMu.Lock()
	defer sm.attachmentMu.Unlock()

	if sm.state != StateSyncing {
		return NodeInvalid, FlushEntry{}, ErrSyncNotInProgress
	}
	if nodeID.IsRoot() {
		return NodeInvalid, FlushEntry{}, ErrUnexpectedNode
	}
	if len(data) == 0 {
		return NodeInvalid, FlushEntry{}, ErrInvalidNodeData
	}

	var verified Node
	result, err := sm.attachKnownNodeAt(nodeID, !withEntry, func() (Node, error) {
		var derr error
		verified, derr = deserializeNodeFromWire(data)
		return verified, derr
	})
	if err != nil || result != NodeUseful || !withEntry {
		return result, FlushEntry{}, err
	}
	entry, err := flushEntryForNode(verified, sm.ledgerSeq, sm.mapType)
	if err != nil {
		return result, FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return result, entry, err
}

// attachKnownNodeAt descends along nodeID's path and attaches the node
// produced by deserialize at the first hash-only slot, after verifying its
// hash against the parent's stored child hash. deserialize runs only once the
// target slot is known to be empty, so a duplicate (slot already populated, or
// a consolidated leaf mid-path) short-circuits without parsing the peer's
// data. Reaching a hash-only ancestor before the target depth is NodeReRequest,
// not an error, so off-frontier fat-reply nodes converge over re-requests
// instead of flooding the reject log. Shared by AddKnownNodeByID and
// AddKnownNodeFromPrefix. Caller must hold the write lock and have validated
// state and nodeID. markDirty is false when the caller receives and persists a
// FlushEntry; attaching verified immutable data does not modify the tree hash.
func (sm *SHAMap) attachKnownNodeAt(nodeID NodeID, markDirty bool, deserialize func() (Node, error)) (AddNodeResult, error) {
	if sm.root == nil {
		return NodeInvalid, ErrParentNotInTree
	}

	targetDepth := int(nodeID.Depth())
	targetPath := nodeID.ID()

	parent := sm.root
	ancestors := make([]*innerNode, 0, targetDepth)

	for curDepth := range targetDepth {
		ancestors = append(ancestors, parent)
		branch := selectBranchForPath(targetPath, curDepth)

		child, childHash, isSet := parent.LoadChild(branch)
		if !isSet {
			// A materialized ancestor has no child here: the offered node is
			// not part of this map (a node from another tree).
			return NodeInvalid, ErrEmptyBranchOnPath
		}

		if curDepth+1 == targetDepth {
			if child != nil {
				return NodeDuplicate, nil
			}
			newNode, err := deserialize()
			if err != nil {
				return NodeInvalid, fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
			}
			// At leaf depth, an inner node is provably invalid — mark the
			// map and bail.
			if _, isInner := newNode.(*innerNode); isInner && targetDepth == MaxDepth {
				sm.state = StateInvalid
				return NodeInvalid, ErrUnexpectedNode
			}
			if err := newNode.UpdateHash(); err != nil {
				return NodeInvalid, fmt.Errorf("failed to compute node hash: %w", err)
			}
			if newNode.Hash() != childHash {
				return NodeInvalid, ErrNodeHashMismatch
			}
			if parent.SetChildIfNil(branch, newNode) != newNode {
				return NodeDuplicate, nil
			}
			if markDirty {
				newNode.SetDirty(true)
				for _, ancestor := range ancestors {
					ancestor.SetDirty(true)
				}
			}
			return NodeUseful, nil
		}

		if child == nil {
			loaded, _, loadErr := fetchFromStoreContext(context.Background(), sm, sm.family, parent, branch)
			if loadErr != nil || loaded == nil {
				return NodeReRequest, nil
			}
			child = parent.SetChildIfNil(branch, loaded)
		}
		nextInner, ok := child.(*innerNode)
		if !ok {
			// A leaf encountered mid-path is the canonical content at this
			// slot (SHAMap consolidates lone leaves above leafDepth).
			return NodeDuplicate, nil
		}
		parent = nextInner
	}

	return NodeInvalid, ErrUnexpectedNode
}

// insertKnownNode inserts a node at the correct location in the tree.
// The caller must hold the write lock.
func (sm *SHAMap) insertKnownNode(nodeHash [32]byte, node Node) error {
	if sm.root == nil {
		return ErrUnexpectedNode
	}

	// Find the parent that references this hash
	return sm.insertNodeRecursive(sm.root, nodeHash, node, 0)
}

// insertNodeRecursive recursively finds and inserts a node at the correct location.
func (sm *SHAMap) insertNodeRecursive(current Node, targetHash [32]byte, newNode Node, depth int) error {
	if current == nil {
		return ErrUnexpectedNode
	}

	if depth > MaxDepth {
		return ErrMaxDepthExceeded
	}

	inner, ok := current.(*innerNode)
	if !ok {
		return ErrUnexpectedNode
	}

	for branch := range BranchFactor {
		child, childHash, isSet := inner.LoadChild(branch)
		if !isSet {
			continue
		}

		if bytes.Equal(childHash[:], targetHash[:]) {
			// Found the branch - insert the node here
			newNode.SetDirty(true)
			return inner.SetChild(branch, newNode)
		}

		if _, isInner := child.(*innerNode); isInner {
			// Recurse into this inner node
			err := sm.insertNodeRecursive(child, targetHash, newNode, depth+1)
			if err == nil {
				inner.SetDirty(true)
				return nil // Successfully inserted
			}
			// Continue searching other branches if not found
		}
	}

	return ErrUnexpectedNode
}

// AddRootNode sets the root from external data.
// This is used to initialize a SHAMap during synchronization when receiving
// the root hash/data from a peer.
//
// Parameters:
//   - hash: the expected hash of the root node
//   - data: the serialized wire format of the root node
//
// Returns an error if the root is already set, the data is invalid,
// or the hash doesn't match.
func (sm *SHAMap) AddRootNode(hash [32]byte, data []byte) error {
	_, err := sm.addRootNode(hash, data, false)
	return err
}

// AddRootNodeWithEntry sets and verifies a clean root and returns its NodeStore
// entry for the caller to persist without deserializing it a second time.
func (sm *SHAMap) AddRootNodeWithEntry(hash [32]byte, data []byte) (FlushEntry, error) {
	return sm.addRootNode(hash, data, true)
}

func (sm *SHAMap) addRootNode(hash [32]byte, data []byte, withEntry bool) (FlushEntry, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.attachmentMu.Lock()
	defer sm.attachmentMu.Unlock()

	if sm.root != nil && sm.root.HasChildren() {
		return FlushEntry{}, ErrRootAlreadySet
	}

	if len(data) == 0 {
		return FlushEntry{}, ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		return FlushEntry{}, fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
	}

	// Must be an inner node for root
	root, ok := node.(*innerNode)
	if !ok {
		return FlushEntry{}, fmt.Errorf("root must be an inner node, got %T", node)
	}

	if err := root.UpdateHash(); err != nil {
		return FlushEntry{}, fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := root.Hash()
	if !bytes.Equal(computedHash[:], hash[:]) {
		return FlushEntry{}, ErrNodeHashMismatch
	}

	sm.root = root
	root.SetDirty(!withEntry)
	sm.state = StateSyncing
	// Clear full as StartSync does: a stale full lets IsComplete report
	// complete while the FinishSync walk still finds missing nodes.
	sm.full = false

	if !withEntry {
		return FlushEntry{}, nil
	}
	entry, err := flushEntryForNode(root, sm.ledgerSeq, sm.mapType)
	if err != nil {
		return FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return entry, nil
}

// StartSync prepares the SHAMap for synchronization.
// This sets the state to StateSyncing and allows nodes to be added.
func (sm *SHAMap) StartSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state == StateInvalid {
		return fmt.Errorf("%w: cannot start sync on invalid map", ErrInvalidState)
	}

	sm.state = StateSyncing
	sm.full = false

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
	sm.mu.RLock()
	if sm.state != StateSyncing {
		sm.mu.RUnlock()
		return ErrSyncNotInProgress
	}
	backed := sm.backed && sm.family != nil
	sm.mu.RUnlock()

	var missingNodes []MissingNode
	var err error
	if backed {
		missingNodes, err = sm.WalkMapParallelContext(ctx, 1, nil)
	} else {
		sm.mu.RLock()
		missingNodes, err = sm.missingNodesLocked(1, nil, true)
		sm.mu.RUnlock()
	}
	if err != nil {
		return fmt.Errorf("sync completeness walk: %w", err)
	}
	if len(missingNodes) > 0 {
		return fmt.Errorf("sync incomplete: still have %d missing nodes", len(missingNodes))
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}
	sm.state = StateModifying
	sm.full = true

	return nil
}

// IsSyncing returns true if the map is in sync mode.
func (sm *SHAMap) IsSyncing() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state == StateSyncing
}

// IsComplete returns true if the map has all nodes (no missing references).
func (sm *SHAMap) IsComplete() bool {
	sm.mu.RLock()
	state := sm.state
	full := sm.full
	backed := sm.backed && sm.family != nil
	sm.mu.RUnlock()
	// A partially-built acquisition map can carry a stale full, so while
	// syncing defer to the missing-node walk rather than trust full.
	if full && state != StateSyncing {
		return true
	}
	if backed && state == StateSyncing {
		missing, err := sm.WalkMapParallelContext(context.Background(), 1, nil)
		return err == nil && len(missing) == 0
	}

	// Strict walk: a transient store error means completeness is unknown —
	// conservatively incomplete.
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	missing, err := sm.missingNodesLocked(1, nil, true)
	return err == nil && len(missing) == 0
}

// SyncProgress returns the estimated sync progress as a fraction.
// This is an approximation based on the ratio of present nodes to total references.
func (sm *SHAMap) SyncProgress() (present, total int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	queue := make([]*innerNode, 0, 64)

	if sm.root != nil {
		queue = append(queue, sm.root)
		total++
		present++
	}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		for branch := range BranchFactor {
			child, _, isSet := node.LoadChild(branch)
			if !isSet {
				continue
			}

			total++

			if child != nil {
				present++
				if inner, ok := child.(*innerNode); ok {
					queue = append(queue, inner)
				}
			}
		}
	}

	return present, total
}
