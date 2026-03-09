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

// CachingSyncFilter wraps another filter and caches results to avoid repeated lookups.
type CachingSyncFilter struct {
	mu     sync.RWMutex
	inner  SyncFilter
	cache  map[[32]byte]bool
	maxSize int
}

// NewCachingSyncFilter creates a new CachingSyncFilter with the given inner filter.
func NewCachingSyncFilter(inner SyncFilter, maxSize int) *CachingSyncFilter {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &CachingSyncFilter{
		inner:   inner,
		cache:   make(map[[32]byte]bool),
		maxSize: maxSize,
	}
}

// ShouldFetch implements SyncFilter with caching.
func (f *CachingSyncFilter) ShouldFetch(nodeHash [32]byte) bool {
	f.mu.RLock()
	result, found := f.cache[nodeHash]
	f.mu.RUnlock()

	if found {
		return result
	}

	result = f.inner.ShouldFetch(nodeHash)

	f.mu.Lock()
	if len(f.cache) < f.maxSize {
		f.cache[nodeHash] = result
	}
	f.mu.Unlock()

	return result
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
}

// String returns a string representation of the MissingNode.
func (m *MissingNode) String() string {
	return fmt.Sprintf("MissingNode(hash=%x, depth=%d, parent=%x, branch=%d)",
		m.Hash[:8], m.Depth, m.ParentHash[:8], m.Branch)
}

// SyncState tracks the state of a sync operation.
type SyncState struct {
	mu            sync.RWMutex
	pendingNodes  map[[32]byte]*MissingNode // Nodes we've requested but not received
	receivedCount int
	totalMissing  int
}

// NewSyncState creates a new SyncState.
func NewSyncState() *SyncState {
	return &SyncState{
		pendingNodes: make(map[[32]byte]*MissingNode),
	}
}

// GetMissingNodes finds nodes that are referenced by the tree but not locally available.
// This is used during synchronization to determine which nodes need to be fetched from peers.
//
// Parameters:
//   - maxNodes: maximum number of missing nodes to return (0 = no limit)
//   - filter: optional filter to control which nodes to fetch (nil uses default)
//
// Returns a slice of MissingNode structures describing nodes that need to be fetched.
func (sm *SHAMap) GetMissingNodes(maxNodes int, filter SyncFilter) []MissingNode {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state != StateSyncing {
		// If not in sync mode, we assume the map is complete
		return nil
	}

	if filter == nil {
		filter = &DefaultSyncFilter{}
	}

	var missing []MissingNode

	// Use a work queue to traverse the tree looking for missing nodes
	type workItem struct {
		node       Node
		nodeHash   [32]byte
		parentHash [32]byte
		depth      int
		branch     int
	}

	queue := make([]workItem, 0, 64)

	// Start from root
	if sm.root != nil {
		rootHash := sm.root.Hash()
		queue = append(queue, workItem{
			node:     sm.root,
			nodeHash: rootHash,
			depth:    0,
			branch:   -1,
		})
	}

	for len(queue) > 0 {
		// Check if we've found enough missing nodes
		if maxNodes > 0 && len(missing) >= maxNodes {
			break
		}

		// Pop from queue
		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		if item.node.IsLeaf() {
			// Leaf nodes are always considered complete
			continue
		}

		// Inner node - check each branch
		inner, ok := item.node.(*InnerNode)
		if !ok {
			continue
		}

		for branch := 0; branch < BranchFactor; branch++ {
			if inner.IsEmptyBranch(branch) {
				continue
			}

			childHash, err := inner.ChildHash(branch)
			if err != nil {
				continue
			}

			// Check if child is missing (hash present but no child node)
			child, err := inner.Child(branch)
			if err != nil {
				continue
			}

			if child == nil {
				// Child is referenced by hash but not loaded - this is a missing node
				if filter.ShouldFetch(childHash) {
					missing = append(missing, MissingNode{
						Hash:       childHash,
						Depth:      item.depth + 1,
						ParentHash: item.nodeHash,
						Branch:     branch,
					})

					if maxNodes > 0 && len(missing) >= maxNodes {
						break
					}
				}
			} else {
				// Child exists - add to queue for further traversal
				queue = append(queue, workItem{
					node:       child,
					nodeHash:   childHash,
					parentHash: item.nodeHash,
					depth:      item.depth + 1,
					branch:     branch,
				})
			}
		}
	}

	return missing
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

	// Check each branch for a matching hash
	for branch := 0; branch < BranchFactor; branch++ {
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

		// Check if we need to recurse into this child
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

	// Check if we already have a root with content
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

	// Verify the hash matches
	if err := innerNode.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := innerNode.Hash()
	if !bytes.Equal(computedHash[:], hash[:]) {
		return ErrNodeHashMismatch
	}

	// Set the root
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

		for branch := 0; branch < BranchFactor; branch++ {
			if inner.IsEmptyBranch(branch) {
				continue
			}

			childHash, err := inner.ChildHash(branch)
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
					})

					if maxNodes > 0 && len(missing) >= maxNodes {
						break
					}
				}
			} else {
				queue = append(queue, workItem{
					node:       child,
					nodeHash:   childHash,
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

		for branch := 0; branch < BranchFactor; branch++ {
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
