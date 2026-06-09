package shamap

import (
	"context"
	"fmt"
)

// walkToKey traverses the tree toward a specific key and returns the leaf node.
// If stack is non-nil, it is filled with the path from root to (but not including)
// the leaf.  If pushLeaf is true, the final leaf is also pushed onto the stack.
func (sm *SHAMap) walkToKey(ctx context.Context, key [32]byte, stack *NodeStack, pushLeaf bool) (Node, error) {
	if stack != nil && !stack.IsEmpty() {
		stack.Clear()
	}

	var node Node = sm.root
	nodeID := NewRootNodeID()

	for !node.IsLeaf() {
		if stack != nil {
			stack.Push(node, nodeID)
		}

		inner, ok := node.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		branch := SelectBranch(nodeID, key)
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

// WalkLeaves visits every leaf in the tree starting from start, calling fn
// for each item.  If fn returns false iteration stops early.
// Equivalent to ForEach but can start from an arbitrary node.
func (sm *SHAMap) WalkLeaves(ctx context.Context, start Node, fn func(*Item) bool) error {
	if start == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if start.IsLeaf() {
		leafNode, ok := start.(LeafNode)
		if !ok {
			return ErrInvalidType
		}
		if !fn(leafNode.Item()) {
			return nil
		}
		return nil
	}

	inner, ok := start.(*InnerNode)
	if !ok {
		return ErrInvalidType
	}

	for i := range BranchFactor {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := sm.descendCtx(ctx, inner, i)
		if err != nil {
			return fmt.Errorf("failed to get child %d: %w", i, err)
		}
		if child != nil {
			if err := sm.WalkLeaves(ctx, child, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// forEachUnsafe is retained for backward compatibility — it wraps WalkLeaves.
// Caller must hold the read lock.
func (sm *SHAMap) forEachUnsafe(ctx context.Context, node Node, fn func(*Item) bool) error {
	return sm.WalkLeaves(ctx, node, fn)
}

// collectAllKeysUnsafe returns all leaf keys in the subtree rooted at node.
// Caller must hold the read lock.
func (sm *SHAMap) collectAllKeysUnsafe(node Node) ([]Key, error) {
	var keys []Key
	err := sm.WalkLeaves(context.Background(), node, func(item *Item) bool {
		keys = append(keys, item.Key())
		return true
	})
	return keys, err
}

// collectAllKeysExceptUnsafe returns all leaf keys except the given key.
// Caller must hold the read lock.
func (sm *SHAMap) collectAllKeysExceptUnsafe(node Node, exceptKey Key) ([]Key, error) {
	allKeys, err := sm.collectAllKeysUnsafe(node)
	if err != nil {
		return nil, err
	}
	filtered := make([]Key, 0, len(allKeys))
	for _, key := range allKeys {
		if key != exceptKey {
			filtered = append(filtered, key)
		}
	}
	return filtered, nil
}

// firstBelow returns the first (smallest-key) leaf below the given node.
func (sm *SHAMap) firstBelow(node Node, parentID NodeID, branch int) LeafNode {
	if node.IsLeaf() {
		if leaf, ok := node.(LeafNode); ok {
			return leaf
		}
		return nil
	}

	inner, ok := node.(*InnerNode)
	if !ok {
		return nil
	}

	nodeID, err := parentID.ChildNodeID(uint8(branch))
	if err != nil {
		return nil
	}

	for i := range BranchFactor {
		child, err := sm.descend(inner, i)
		if err != nil {
			return nil
		}
		if child != nil {
			result := sm.firstBelow(child, nodeID, i)
			if result != nil {
				return result
			}
		}
	}
	return nil
}

// lastBelow returns the last (largest-key) leaf below the given node.
func (sm *SHAMap) lastBelow(node Node, parentID NodeID, branch int) LeafNode {
	if node.IsLeaf() {
		if leaf, ok := node.(LeafNode); ok {
			return leaf
		}
		return nil
	}

	inner, ok := node.(*InnerNode)
	if !ok {
		return nil
	}

	nodeID, err := parentID.ChildNodeID(uint8(branch))
	if err != nil {
		return nil
	}

	for i := BranchFactor - 1; i >= 0; i-- {
		child, err := sm.descend(inner, i)
		if err != nil {
			return nil
		}
		if child != nil {
			result := sm.lastBelow(child, nodeID, i)
			if result != nil {
				return result
			}
		}
	}
	return nil
}

// walkSubtreeForMissing is the BFS-over-one-subtree primitive used by
// WalkMap, WalkMapParallel and GetMissingNodes. It walks the subtree
// rooted at start and invokes report for every non-empty branch whose
// child node is neither in memory nor recoverable from sm's family.
func walkSubtreeForMissing(
	sm *SHAMap,
	start *InnerNode,
	startID NodeID,
	startHash [32]byte,
	startDepth int,
	filter SyncFilter,
	report func(MissingNode) bool,
) bool {
	type workItem struct {
		node     *InnerNode
		nodeID   NodeID
		nodeHash [32]byte
		depth    int
	}

	queue := make([]workItem, 0, 64)
	queue = append(queue, workItem{
		node:     start,
		nodeID:   startID,
		nodeHash: startHash,
		depth:    startDepth,
	})

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		for branch := range BranchFactor {
			child, childHash, isSet := item.node.LoadChild(branch)
			if !isSet {
				continue
			}

			childNodeID, err := item.nodeID.ChildNodeID(uint8(branch))
			if err != nil {
				continue
			}

			if child == nil {
				if loaded := loadFromStore(sm, item.node, branch); loaded != nil {
					child = loaded
				}
			}

			if child == nil {
				if !filter.ShouldFetch(childHash) {
					continue
				}
				if report(MissingNode{
					Hash:       childHash,
					Depth:      item.depth + 1,
					ParentHash: item.nodeHash,
					Branch:     branch,
					NodeID:     childNodeID,
				}) {
					return true
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
			queue = append(queue, workItem{
				node:     inner,
				nodeID:   childNodeID,
				nodeHash: childHash,
				depth:    item.depth + 1,
			})
		}
	}
	return false
}

// loadFromStore lazy-fetches a hash-only branch from the backing store
// and installs it on the parent via SetChildIfNil. Returns nil for
// unbacked maps, missing-from-store, or any fetch error.
func loadFromStore(sm *SHAMap, parent *InnerNode, branch int) Node {
	if sm == nil || !sm.backed || sm.family == nil {
		return nil
	}
	loaded, err := sm.descend(parent, branch)
	if err != nil {
		return nil
	}
	return loaded
}
