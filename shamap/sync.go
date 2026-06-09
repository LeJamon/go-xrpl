package shamap

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
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
	if filter == nil {
		filter = &DefaultSyncFilter{}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil || sm.state == StateInvalid {
		return nil
	}

	var missing []MissingNode
	walkSubtreeForMissing(
		sm,
		sm.root,
		NewRootNodeID(),
		sm.root.Hash(),
		0,
		filter,
		func(m MissingNode) bool {
			missing = append(missing, m)
			return maxMissing > 0 && len(missing) >= maxMissing
		},
	)
	return missing
}

// WalkMapParallel is the parallel variant of WalkMap. It fans out one
// goroutine per non-empty root branch and lets each worker walk its
// subtree independently; results share a single slice guarded by a
// mutex. An in-mutex stop flag prevents over-appending once maxMissing
// entries have been collected — workers that walk missing-node-free
// subtrees still run their stacks to completion, since the flag is
// checked only inside the report callback.
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
// On a 16-way branched tree the speedup approaches a factor of 16 for
// cold in-memory scans; for small trees the goroutine startup overhead
// is negligible since at most 16 workers ever run.
func (sm *SHAMap) WalkMapParallel(maxMissing int, filter SyncFilter) []MissingNode {
	if filter == nil {
		filter = &DefaultSyncFilter{}
	}

	type subtreeStart struct {
		node     *InnerNode
		nodeID   NodeID
		nodeHash [32]byte
		branch   int
	}

	sm.mu.RLock()
	if sm.root == nil || sm.state == StateInvalid {
		sm.mu.RUnlock()
		return nil
	}
	rootID := NewRootNodeID()
	rootHash := sm.root.Hash()

	// Capture every non-empty root branch under the source-map lock.
	// Hash-only branches at depth 1 are reported synchronously here
	// because they have no subtree to walk.
	var (
		mu       sync.Mutex
		missing  []MissingNode
		stopped  bool
		subtrees = make([]subtreeStart, 0, BranchFactor)
	)

	reportLocked := func(m MissingNode) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return true
		}
		missing = append(missing, m)
		if maxMissing > 0 && len(missing) >= maxMissing {
			stopped = true
			return true
		}
		return false
	}

	for branch := range BranchFactor {
		child, childHash, isSet := sm.root.LoadChild(branch)
		if !isSet {
			continue
		}
		childNodeID, err := rootID.ChildNodeID(uint8(branch))
		if err != nil {
			continue
		}
		if child == nil {
			if loaded := loadFromStore(sm, sm.root, branch); loaded != nil {
				child = loaded
			}
		}
		if child == nil {
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
		if child.IsLeaf() {
			continue
		}
		inner, ok := child.(*InnerNode)
		if !ok {
			continue
		}
		subtrees = append(subtrees, subtreeStart{
			node:     inner,
			nodeID:   childNodeID,
			nodeHash: childHash,
			branch:   branch,
		})
	}
	sm.mu.RUnlock()

	if len(subtrees) == 0 {
		return missing
	}

	var wg sync.WaitGroup
	wg.Add(len(subtrees))
	for _, s := range subtrees {
		go func() {
			defer wg.Done()
			walkSubtreeForMissing(
				sm,
				s.node,
				s.nodeID,
				s.nodeHash,
				1,
				filter,
				reportLocked,
			)
		}()
	}
	wg.Wait()

	return missing
}

// GetMissingNodes returns the nodes referenced by the tree but not
// present locally. It is gated on StateSyncing — for any other state
// the map is assumed complete and the result is nil.
//
// The actual walk is performed by WalkMapParallel so the per-root-branch
// fan-out is shared with the lower-level WalkMap API. maxNodes == 0 is
// unbounded; a nil filter behaves like DefaultSyncFilter.
func (sm *SHAMap) GetMissingNodes(maxNodes int, filter SyncFilter) []MissingNode {
	sm.mu.RLock()
	state := sm.state
	sm.mu.RUnlock()
	if state != StateSyncing {
		return nil
	}
	return sm.WalkMapParallel(maxNodes, filter)
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

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}

	if len(data) == 0 {
		return ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := DeserializeNodeFromWire(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
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

// AddKnownNodeFromPrefix inserts a node from prefix-format data.
// Unlike AddKnownNode (which expects wire format), this expects the
// [HashPrefix][body] serialization used by fetch-pack nodes.
// Verifies that sha512Half(data) matches nodeHash before inserting.
func (sm *SHAMap) AddKnownNodeFromPrefix(nodeHash [32]byte, data []byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}

	if len(data) == 0 {
		return ErrInvalidNodeData
	}

	node, err := DeserializeFromPrefix(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
	}

	if err := node.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := node.Hash()
	if !bytes.Equal(computedHash[:], nodeHash[:]) {
		return ErrNodeHashMismatch
	}

	return sm.insertKnownNode(nodeHash, node)
}

// AddKnownNodeByID inserts a node from wire data at the position specified
// by the peer-supplied SHAMap NodeID (path + depth). The node's computed
// hash must match the parent's stored child hash at the target branch.
//
// Mirrors rippled's SHAMap::addKnownNode (SHAMapSync.cpp:578-673): descent
// through the partial tree is driven by the NodeID, not by hash-searching.
//
// Returns:
//   - nil on successful attach, or when the slot is already populated
//     (duplicate, matching rippled's SHAMapAddNode::duplicate())
//   - ErrEmptyBranchOnPath when descent hits an empty branch — peer sent
//     a node we never asked for
//   - ErrParentNotInTree when an intermediate ancestor on the path is
//     still a hash-only stub — caller must acquire ancestors first
//   - ErrNodeHashMismatch when the computed hash doesn't match what the
//     parent expects at the target branch
//   - ErrSyncNotInProgress / ErrInvalidNodeData on misuse
func (sm *SHAMap) AddKnownNodeByID(nodeID NodeID, data []byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}
	if nodeID.IsRoot() {
		return ErrUnexpectedNode
	}
	if len(data) == 0 {
		return ErrInvalidNodeData
	}
	if sm.root == nil {
		return ErrParentNotInTree
	}

	targetDepth := int(nodeID.Depth())
	targetPath := nodeID.ID()

	parent := sm.root

	for curDepth := range targetDepth {
		branch := selectBranchForPath(targetPath, curDepth)

		parent.mu.RLock()
		empty := parent.isBranch&(1<<uint(branch)) == 0
		var childHash [32]byte
		var child Node
		if !empty {
			childHash = parent.hashes[branch]
			child = parent.children[branch]
		}
		parent.mu.RUnlock()

		if empty {
			return ErrEmptyBranchOnPath
		}

		if curDepth+1 == targetDepth {
			if child != nil {
				return nil
			}
			newNode, err := DeserializeNodeFromWire(data)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
			}
			// Mirrors rippled SHAMapSync.cpp:632-638: at leaf depth, an
			// inner node is provably invalid — mark the map and bail.
			if !newNode.IsLeaf() && targetDepth == MaxDepth {
				sm.state = StateInvalid
				return ErrUnexpectedNode
			}
			if err := newNode.UpdateHash(); err != nil {
				return fmt.Errorf("failed to compute node hash: %w", err)
			}
			if newNode.Hash() != childHash {
				return ErrNodeHashMismatch
			}
			// rippled SHAMapSync.cpp:653 canonicalizeChild
			parent.SetChildIfNil(branch, newNode)
			return nil
		}

		if child == nil {
			return ErrParentNotInTree
		}
		// A leaf encountered mid-path is the canonical content at this
		// slot (SHAMap consolidates lone leaves above leafDepth). Rippled
		// exits the !isInner() loop and returns duplicate (SHAMapSync.cpp:597,
		// 671-672).
		if child.IsLeaf() {
			return nil
		}
		nextInner, ok := child.(*InnerNode)
		if !ok {
			return ErrUnexpectedNode
		}
		parent = nextInner
	}

	return ErrUnexpectedNode
}

// AddKnownNodeUnchecked adds a node from wire data trusting its computed
// hash for tree placement. Use when no authoritative external hash is
// available; AddKnownNode performs the comparison when one is supplied.
func (sm *SHAMap) AddKnownNodeUnchecked(data []byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}
	if len(data) == 0 {
		return ErrInvalidNodeData
	}
	node, err := DeserializeNodeFromWire(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
	}
	if err := node.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}
	return sm.insertKnownNode(node.Hash(), node)
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
		return ErrMaxDepthReached
	}

	if current.IsLeaf() {
		return ErrUnexpectedNode
	}

	inner, ok := current.(*InnerNode)
	if !ok {
		return ErrInvalidType
	}

	for branch := range BranchFactor {
		if inner.IsEmptyBranch(branch) {
			continue
		}

		childHash, err := inner.ChildHash(branch)
		if err != nil {
			continue
		}

		if bytes.Equal(childHash[:], targetHash[:]) {
			// Found the branch - insert the node here
			return inner.SetChild(branch, newNode)
		}

		child, err := inner.Child(branch)
		if err != nil {
			continue
		}

		if child != nil && !child.IsLeaf() {
			// Recurse into this inner node
			err := sm.insertNodeRecursive(child, targetHash, newNode, depth+1)
			if err == nil {
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
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.root != nil && sm.root.HasChildren() {
		return ErrRootAlreadySet
	}

	if len(data) == 0 {
		return ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := DeserializeNodeFromWire(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
	}

	// Must be an inner node for root
	innerNode, ok := node.(*InnerNode)
	if !ok {
		return fmt.Errorf("root must be an inner node, got %T", node)
	}

	if err := innerNode.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := innerNode.Hash()
	if !bytes.Equal(computedHash[:], hash[:]) {
		return ErrNodeHashMismatch
	}

	sm.root = innerNode
	sm.state = StateSyncing

	return nil
}

// StartSync prepares the SHAMap for synchronization.
// This sets the state to StateSyncing and allows nodes to be added.
func (sm *SHAMap) StartSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state == StateInvalid {
		return errors.New("cannot start sync on invalid map")
	}

	sm.state = StateSyncing
	sm.full = false

	return nil
}

// FinishSync completes synchronization and validates the tree.
// This should be called after all missing nodes have been added.
func (sm *SHAMap) FinishSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateSyncing {
		return ErrSyncNotInProgress
	}

	// Verify the tree is complete
	missingNodes := sm.getMissingNodesUnsafe(1, nil)
	if len(missingNodes) > 0 {
		return fmt.Errorf("sync incomplete: still have %d missing nodes", len(missingNodes))
	}

	sm.state = StateModifying
	sm.full = true

	return nil
}

// getMissingNodesUnsafe is the internal version without locking.
func (sm *SHAMap) getMissingNodesUnsafe(maxNodes int, filter SyncFilter) []MissingNode {
	if filter == nil {
		filter = &DefaultSyncFilter{}
	}

	var missing []MissingNode

	type workItem struct {
		node       Node
		nodeHash   [32]byte
		nodeID     NodeID
		parentHash [32]byte
		depth      int
		branch     int
	}

	queue := make([]workItem, 0, 64)

	if sm.root != nil {
		rootHash := sm.root.Hash()
		queue = append(queue, workItem{
			node:     sm.root,
			nodeHash: rootHash,
			nodeID:   NewRootNodeID(),
			depth:    0,
			branch:   -1,
		})
	}

	for len(queue) > 0 {
		if maxNodes > 0 && len(missing) >= maxNodes {
			break
		}

		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		if item.node.IsLeaf() {
			continue
		}

		inner, ok := item.node.(*InnerNode)
		if !ok {
			continue
		}

		for branch := range BranchFactor {
			if inner.IsEmptyBranch(branch) {
				continue
			}

			childHash, err := inner.ChildHash(branch)
			if err != nil {
				continue
			}

			childNodeID, err := item.nodeID.ChildNodeID(uint8(branch))
			if err != nil {
				continue
			}

			child, err := inner.Child(branch)
			if err != nil {
				continue
			}

			if child == nil {
				if filter.ShouldFetch(childHash) {
					missing = append(missing, MissingNode{
						Hash:       childHash,
						Depth:      item.depth + 1,
						ParentHash: item.nodeHash,
						Branch:     branch,
						NodeID:     childNodeID,
					})

					if maxNodes > 0 && len(missing) >= maxNodes {
						break
					}
				}
			} else {
				queue = append(queue, workItem{
					node:       child,
					nodeHash:   childHash,
					nodeID:     childNodeID,
					parentHash: item.nodeHash,
					depth:      item.depth + 1,
					branch:     branch,
				})
			}
		}
	}

	return missing
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
	defer sm.mu.RUnlock()

	if sm.full {
		return true
	}

	missing := sm.getMissingNodesUnsafe(1, nil)
	return len(missing) == 0
}

// SyncProgress returns the estimated sync progress as a fraction.
// This is an approximation based on the ratio of present nodes to total references.
func (sm *SHAMap) SyncProgress() (present, total int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	present = 0
	total = 0

	type workItem struct {
		node Node
	}

	queue := make([]workItem, 0, 64)

	if sm.root != nil {
		queue = append(queue, workItem{node: sm.root})
		total++
		present++
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		if item.node.IsLeaf() {
			continue
		}

		inner, ok := item.node.(*InnerNode)
		if !ok {
			continue
		}

		for branch := range BranchFactor {
			if inner.IsEmptyBranch(branch) {
				continue
			}

			total++

			child, err := inner.Child(branch)
			if err != nil {
				continue
			}

			if child != nil {
				present++
				queue = append(queue, workItem{node: child})
			}
		}
	}

	return present, total
}
