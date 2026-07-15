package shamap

import (
	"context"
	"errors"
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
) (fullBelow, stopped bool, err error) {
	if node == nil {
		return true, false, nil
	}
	// Proven complete this generation — prune the whole subtree.
	if node.isFullBelow(gen) {
		return true, false, nil
	}

	backed := sm != nil && sm.backed && cache != nil
	fullBelow = true

	for branch := range BranchFactor {
		child, childHash, isSet := node.LoadChild(branch)
		if !isSet {
			continue
		}
		childNodeID, cerr := nodeID.ChildNodeID(uint8(branch))
		if cerr != nil {
			continue
		}

		// Backed short-circuit: a released (hash-only) child proven
		// full-below in the family cache needs neither a store fetch nor a
		// re-descent of its subtree.
		if child == nil && backed && cache.Has(gen, childHash) {
			continue
		}

		if child == nil {
			loaded, lerr := loadFromStore(sm, node, branch)
			if lerr != nil {
				if strict {
					return false, false, lerr
				}
				// lenient: treat as missing below
			}
			if loaded != nil {
				child = loaded
			}
		}

		if child == nil {
			fullBelow = false
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
				return false, true, nil
			}
			continue
		}

		inner, ok := child.(*innerNode)
		if !ok {
			// Leaf: resident, contributes to full-below.
			continue
		}

		childFull, childStopped, cerr2 := walkFullBelow(
			sm, inner, childNodeID, childHash, depth+1, gen, filter, strict, cache, report,
		)
		if cerr2 != nil {
			return false, false, cerr2
		}
		if childStopped {
			return false, true, nil
		}
		if !childFull {
			fullBelow = false
		}
	}

	if fullBelow {
		node.setFullBelowGen(gen)
	}
	return fullBelow, false, nil
}

// walkSubtreeForMissing walks the subtree rooted at start, reporting every
// non-empty branch whose child node is neither in memory nor recoverable
// from sm's family, and pruning/marking completed subtrees via the map's
// full-below cache. It returns (stopped, err) — stopped reports whether
// report asked to stop. Callers that need the subtree's full-below result
// (to mark the parent) call walkFullBelow directly.
func walkSubtreeForMissing(
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
	_, stopped, err := walkFullBelow(sm, start, startID, startHash, startDepth, gen, filter, strict, cache, report)
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

// loadFromStore lazy-fetches a hash-only branch from the backing store and
// installs it on the parent via SetChildIfNil. Returns (nil, nil) for
// unbacked maps and true store misses; a non-nil error marks a TRANSIENT
// fetch failure — the node may well exist, so callers deciding completeness
// must not treat it as missing.
func loadFromStore(sm *SHAMap, parent *innerNode, branch int) (Node, error) {
	if sm == nil || !sm.backed || sm.family == nil {
		return nil, nil
	}
	loaded, err := sm.descend(parent, branch)
	if err != nil {
		if errors.Is(err, ErrNodeNotInStore) {
			return nil, nil
		}
		return nil, err
	}
	return loaded, nil
}
