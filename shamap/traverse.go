package shamap

import (
	"context"
	"fmt"
)

// pathEntry represents an entry in the traversal path
type pathEntry struct {
	node   Node
	nodeID NodeID
}

// nodeStack holds the path from the root to a node during tree traversal
type nodeStack struct {
	entries []pathEntry
}

// newNodeStack creates a new empty node stack
func newNodeStack() *nodeStack {
	return &nodeStack{
		entries: make([]pathEntry, 0, MaxDepth), // Pre-allocate for efficiency
	}
}

// Push adds a node and its ID to the stack
func (s *nodeStack) Push(node Node, id NodeID) {
	s.entries = append(s.entries, pathEntry{node, id})
}

// Pop removes and returns the top node and ID from the stack
func (s *nodeStack) Pop() (Node, NodeID, bool) {
	if len(s.entries) == 0 {
		return nil, NodeID{}, false
	}

	idx := len(s.entries) - 1
	entry := s.entries[idx]
	s.entries = s.entries[:idx]

	return entry.node, entry.nodeID, true
}

// Top returns the top node and ID without removing them
func (s *nodeStack) Top() (Node, NodeID, bool) {
	if len(s.entries) == 0 {
		return nil, NodeID{}, false
	}

	entry := s.entries[len(s.entries)-1]
	return entry.node, entry.nodeID, true
}

// IsEmpty returns true if the stack is empty
func (s *nodeStack) IsEmpty() bool {
	return len(s.entries) == 0
}

// Clear removes all entries from the stack
func (s *nodeStack) Clear() {
	s.entries = s.entries[:0]
}

// Len returns the number of entries in the stack
func (s *nodeStack) Len() int {
	return len(s.entries)
}

// walkToKey traverses the tree toward a specific key and returns the leaf node.
// If stack is non-nil, it is filled with the path from root to (but not including)
// the leaf.  If pushLeaf is true, the final leaf is also pushed onto the stack.
func (sm *SHAMap) walkToKey(ctx context.Context, key [32]byte, stack *nodeStack, pushLeaf bool) (Node, error) {
	if stack != nil && !stack.IsEmpty() {
		stack.Clear()
	}

	var node Node = sm.root
	nodeID := NewRootNodeID()

	for {
		inner, ok := node.(*innerNode)
		if !ok {
			break
		}

		if stack != nil {
			stack.Push(node, nodeID)
		}

		branch := selectBranch(nodeID, key)
		if inner.IsEmptyBranch(int(branch)) {
			return nil, nil
		}

		child, err := sm.descendCtx(ctx, inner, int(branch))
		if err != nil {
			return nil, fmt.Errorf("failed to get child: %w", err)
		}
		if child == nil {
			return nil, nil
		}

		node = child
		childNodeID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get child node ID: %w", err)
		}
		nodeID = childNodeID
	}

	if stack != nil && pushLeaf {
		stack.Push(node, nodeID)
	}

	return node, nil
}

// walkLeavesUnsafe visits every leaf in the subtree rooted at start, calling
// fn for each item. If fn returns false iteration stops early. The check on
// ctx fires before each child descend so a long-running scan can be
// interrupted. Caller must hold the read lock.
func (sm *SHAMap) walkLeavesUnsafe(ctx context.Context, start Node, fn func(*Item) bool) error {
	_, err := sm.walkLeavesRec(ctx, start, fn)
	return err
}

// walkLeavesRec reports whether the walk should continue into further
// siblings (false once fn has asked to stop).
func (sm *SHAMap) walkLeavesRec(ctx context.Context, node Node, fn func(*Item) bool) (bool, error) {
	if node == nil {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	inner, ok := node.(*innerNode)
	if !ok {
		leaf, ok := node.(LeafNode)
		if !ok {
			return false, ErrInvalidType
		}
		return fn(leaf.Item()), nil
	}

	for i := range BranchFactor {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		child, err := sm.descendCtx(ctx, inner, i)
		if err != nil {
			return false, fmt.Errorf("failed to get child %d: %w", i, err)
		}
		if child != nil {
			cont, err := sm.walkLeavesRec(ctx, child, fn)
			if err != nil || !cont {
				return cont, err
			}
		}
	}
	return true, nil
}

// onlyBelow checks if there's exactly one item below the given node
// Returns the item if found, nil if there are 0 or multiple items
func (sm *SHAMap) onlyBelow(node Node) (*Item, error) {
	if node == nil {
		return nil, nil
	}

	current := node
	for {
		inner, ok := current.(*innerNode)
		if !ok {
			break
		}

		var nextNode Node = nil
		for i := range BranchFactor {
			child, err := sm.descend(inner, i)
			if err != nil {
				return nil, fmt.Errorf("failed to get child %d: %w", i, err)
			}

			if child != nil {
				if nextNode != nil {
					// Found second child - multiple items below
					return nil, nil
				}
				nextNode = child
			}
		}

		if nextNode == nil {
			// No children found
			return nil, nil
		}

		current = nextNode
	}

	// Found exactly one leaf
	leaf, ok := current.(LeafNode)
	if !ok {
		return nil, ErrInvalidType
	}

	return leaf.Item(), nil
}

// boundBelow returns the extreme leaf below the given node: the
// smallest-key leaf when ascending is true (branch 0 first), the
// largest-key leaf otherwise (branch 15 first). A non-empty branch whose
// child cannot be loaded is an error, matching rippled's descendThrow in
// belowHelper (SHAMap.cpp:481).
func (sm *SHAMap) boundBelowCtx(ctx context.Context, node Node, ascending bool) (LeafNode, error) {
	inner, ok := node.(*innerNode)
	if !ok {
		leaf, _ := node.(LeafNode)
		return leaf, nil
	}

	start, end, step := 0, BranchFactor, 1
	if !ascending {
		start, end, step = BranchFactor-1, -1, -1
	}
	for i := start; i != end; i += step {
		child, err := sm.descendCtx(ctx, inner, i)
		if err != nil {
			return nil, err
		}
		if child != nil {
			result, err := sm.boundBelowCtx(ctx, child, ascending)
			if err != nil {
				return nil, err
			}
			if result != nil {
				return result, nil
			}
		}
	}
	return nil, nil
}

// walkFullBelow is the full-below-aware DFS behind WalkMap, WalkMapParallel,
// GetMissingNodes and the sync completeness checks. It walks the subtree
// rooted at node, invoking report for every non-empty branch whose child is
// neither in memory nor recoverable from sm's family, and marking each
// subtree it proves complete full-below at generation gen so a later walk
// prunes it in O(1) instead of re-descending it. Mirrors rippled's
// SHAMap::gmn_ProcessNodes (xrpld/shamap/detail/SHAMapSync.cpp).
//
// Returns:
//   - fullBelow: every descendant of node is resident AND the walk covered
//     them all. Only then is node marked; an ancestor is marked only once
//     all of its children come back fullBelow. A missing child, a filtered
//     child, a report-driven stop, or a strict error all leave fullBelow
//     false so a node is never marked while a descendant is absent.
//   - stopped: report asked to stop (the maxMissing cap). The caller must
//     unwind without marking anything above.
//   - err: a transient family error in strict mode.
//
// On a transient family fetch failure: strict=false treats the branch as
// missing (rippled's getMissingNodes collapse, self-correcting via the
// wire); strict=true aborts so FinishSync/IsComplete never fabricate a
// missing node or conclude complete over a skipped subtree.
func walkFullBelow(
	ctx context.Context,
	sm *SHAMap,
	node *innerNode,
	nodeID NodeID,
	nodeHash [32]byte,
	depth int,
	gen uint32,
	filter SyncFilter,
	strict bool,
	cache *FullBelowCache,
	report func(MissingNode) bool,
	shouldStop func() bool,
) (fullBelow, stopped bool, err error) {
	canRelease := sm != nil && sm.backed && sm.family != nil && cache != nil
	result, err := walkFullBelowState(
		ctx, sm, sm.family, node, nodeID, nodeHash, depth, gen, filter, strict, cache, false, canRelease, report, shouldStop,
	)
	return result.full, result.stopped, err
}

type fullBelowWalkResult struct {
	full    bool
	stored  bool
	stopped bool
}

func walkFullBelowState(
	ctx context.Context,
	sm *SHAMap,
	family Family,
	node *innerNode,
	nodeID NodeID,
	nodeHash [32]byte,
	depth int,
	gen uint32,
	filter SyncFilter,
	strict bool,
	cache *FullBelowCache,
	nodeStored bool,
	canRelease bool,
	report func(MissingNode) bool,
	shouldStop func() bool,
) (fullBelowWalkResult, error) {
	if err := ctx.Err(); err != nil {
		return fullBelowWalkResult{stopped: true}, err
	}
	if node == nil {
		return fullBelowWalkResult{full: true, stored: true}, nil
	}
	if shouldStop != nil && shouldStop() {
		return fullBelowWalkResult{stopped: true}, nil
	}
	backed := family != nil && cache != nil
	if backed && cache.Has(gen, nodeHash) {
		return fullBelowWalkResult{full: true, stored: true}, nil
	}
	if !backed && node.isFullBelow(gen) {
		return fullBelowWalkResult{full: true}, nil
	}
	if backed && !nodeStored {
		nodeStored = storedNodeExistsContext(ctx, family, nodeHash)
		if err := ctx.Err(); err != nil {
			return fullBelowWalkResult{stopped: true}, err
		}
	}
	result := fullBelowWalkResult{full: true, stored: nodeStored}

	for branch := range BranchFactor {
		if err := ctx.Err(); err != nil {
			return fullBelowWalkResult{stopped: true}, err
		}
		if shouldStop != nil && shouldStop() {
			return fullBelowWalkResult{stopped: true}, nil
		}
		child, childHash, isSet := node.LoadChild(branch)
		if !isSet {
			continue
		}
		childNodeID, cerr := nodeID.ChildNodeID(uint8(branch))
		if cerr != nil {
			continue
		}

		if backed && cache.Has(gen, childHash) {
			if canRelease && child != nil {
				sm.releaseChild(node, branch, child)
			}
			continue
		}

		attached := child != nil
		childStored := false
		if child == nil {
			loaded, stored, lerr := fetchFromStoreContext(ctx, sm, family, node, branch)
			if err := ctx.Err(); err != nil {
				return fullBelowWalkResult{stopped: true}, err
			}
			if lerr != nil {
				if strict {
					return fullBelowWalkResult{}, lerr
				}
			}
			if loaded != nil {
				child = node.SetChildIfNil(branch, loaded)
				attached = true
				childStored = stored
			}
		}

		if child == nil {
			result.full = false
			result.stored = false
			if !filter.ShouldFetch(childHash) {
				continue
			}
			if report(MissingNode{
				Hash:       childHash,
				Depth:      depth + 1,
				ParentHash: nodeHash,
				Branch:     branch,
				NodeID:     childNodeID,
			}) {
				return fullBelowWalkResult{stopped: true}, nil
			}
			continue
		}

		inner, ok := child.(*innerNode)
		if !ok {
			if backed && !childStored {
				childStored = storedNodeExistsContext(ctx, family, childHash)
				if err := ctx.Err(); err != nil {
					return fullBelowWalkResult{stopped: true}, err
				}
			}
			if !childStored {
				result.stored = false
			} else if canRelease && attached {
				sm.releaseChild(node, branch, child)
			}
			continue
		}

		childResult, childErr := walkFullBelowState(
			ctx, sm, family, inner, childNodeID, childHash, depth+1, gen, filter, strict, cache, childStored, canRelease, report, shouldStop,
		)
		if childErr != nil {
			return fullBelowWalkResult{}, childErr
		}
		if !childResult.full {
			result.full = false
		}
		if !childResult.stored {
			result.stored = false
		}
		if canRelease && childResult.full && childResult.stored {
			if attached {
				sm.releaseChild(node, branch, child)
			}
		}
		if childResult.stopped {
			return fullBelowWalkResult{stopped: true}, nil
		}
	}

	if result.full {
		node.setFullBelowGen(gen)
		if result.stored && cache != nil {
			cacheFullBelow(cache, gen, nodeHash, depth)
		}
	}
	return result, nil
}

// walkSubtreeForMissing walks the subtree rooted at start, reporting every
// non-empty branch whose child node is neither in memory nor recoverable
// from sm's family, and pruning/marking completed subtrees via the map's
// full-below cache. It returns (stopped, err) — stopped reports whether
// report asked to stop. Callers that need the subtree's full-below result
// (to mark the parent) call walkFullBelow directly.
func walkSubtreeForMissing(
	ctx context.Context,
	sm *SHAMap,
	start *innerNode,
	startID NodeID,
	startHash [32]byte,
	startDepth int,
	filter SyncFilter,
	strict bool,
	report func(MissingNode) bool,
) (bool, error) {
	gen, cache, done := fullBelowContext(sm)
	defer done()
	_, stopped, err := walkFullBelow(ctx, sm, start, startID, startHash, startDepth, gen, filter, strict, cache, report, nil)
	return stopped, err
}

// fullBelowContext returns the generation and cache a walk over sm should
// use. Every constructed SHAMap installs a cache, but defend against a
// zero-value map by falling back to generation 0 (which no node ever
// matches, disabling the prune) and a nil cache.
func fullBelowContext(sm *SHAMap) (uint32, *FullBelowCache, func()) {
	if sm == nil || sm.fullBelow == nil {
		return 0, nil, func() {}
	}
	gen, done := sm.fullBelow.Begin()
	return gen, sm.fullBelow, done
}

func fetchFromStoreContext(ctx context.Context, sm *SHAMap, family Family, parent *innerNode, branch int) (Node, bool, error) {
	if sm == nil || family == nil {
		return nil, false, nil
	}
	child, hash, hasBranch := parent.LoadChild(branch)
	if child != nil {
		return child, false, nil
	}
	if !hasBranch || isZeroHash(hash) {
		return nil, false, nil
	}
	data, stored, err := fetchWithDurability(ctx, family, hash)
	if err != nil {
		return nil, false, err
	}
	if data == nil {
		return nil, false, nil
	}
	node, err := deserializeFromPrefix(data)
	if err != nil {
		return nil, false, fmt.Errorf("%w: failed to deserialize child node: %v", ErrInvalidNodeData, err)
	}
	if actual := node.Hash(); actual != hash {
		return nil, false, fmt.Errorf("%w: child node hash mismatch: expected %x, got %x", ErrInvalidNodeData, hash[:8], actual[:8])
	}
	sm.familyLoads.Add(1)
	return node, stored, nil
}

func fetchWithDurability(ctx context.Context, family Family, hash [32]byte) ([]byte, bool, error) {
	if durable, ok := family.(durableFamily); ok {
		data, err := durable.FetchDurable(ctx, hash)
		if err != nil || len(data) > 0 {
			return data, len(data) > 0, err
		}
		data, err = family.Fetch(ctx, hash)
		return data, false, err
	}
	data, err := family.Fetch(ctx, hash)
	return data, len(data) > 0, err
}

func storedNodeExistsContext(ctx context.Context, family Family, hash [32]byte) bool {
	if family == nil || isZeroHash(hash) {
		return false
	}
	data, err := fetchDurable(ctx, family, hash)
	if err != nil || len(data) == 0 {
		return false
	}
	node, err := deserializeFromPrefix(data)
	return err == nil && node.Hash() == hash
}

func (sm *SHAMap) releaseChild(parent *innerNode, branch int, child Node) bool {
	sm.attachmentMu.Lock()
	defer sm.attachmentMu.Unlock()
	return parent.ReleaseChild(branch, child)
}
