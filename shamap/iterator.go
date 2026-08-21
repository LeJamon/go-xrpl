package shamap

import (
	"bytes"
	"context"
)

// Iterator provides forward iteration over SHAMap items in key order.
// Usage:
//
//	iter := sm.UpperBound(key)
//	for iter.Valid() {
//	    item := iter.Item()
//	    // use item
//	    iter.Next()
//	}
//	if err := iter.Err(); err != nil {
//	    // handle error
//	}
type Iterator struct {
	sm      *SHAMap
	current *Item
	err     error
	ctx     context.Context
}

type iterStackEntry struct {
	node   mapNode
	nodeID NodeID
	branch int // next branch to visit (-1 means visit node itself first)
}

// Next advances the iterator to the next item.
// Returns true if there is a next item, false if iteration is complete or an error occurred.
func (it *Iterator) Next() bool {
	if it.err != nil {
		return false
	}

	it.sm.tree.mu.RLock()
	defer it.sm.tree.mu.RUnlock()

	if it.current == nil {
		return false
	}
	item, err := it.sm.upperBoundUnsafe(it.ctx, it.current.Key())
	if err != nil {
		it.err = err
		it.current = nil
		return false
	}
	it.current = item
	return item != nil
}

// Item returns the current item. Only valid after Next() returns true.
func (it *Iterator) Item() *Item {
	return it.current
}

// Err returns any error that occurred during iteration.
func (it *Iterator) Err() error {
	return it.err
}

// Valid returns true if the iterator is positioned at a valid item.
func (it *Iterator) Valid() bool {
	return it.current != nil && it.err == nil
}

// walkBoundStack walks from the root toward id, returning the traversal
// stack ending at the node where the descent stopped (a leaf, an empty
// branch, or an unloadable child). Shared prologue of upperBoundUnsafe and
// lowerBoundUnsafe. Caller must hold the read lock.
func (sm *SHAMap) walkBoundStack(ctx context.Context, id [32]byte) ([]iterStackEntry, error) {
	stack := make([]iterStackEntry, 0, maxDepth)
	var node mapNode = sm.tree.root
	nodeID := newRootNodeID()

	for {
		inner, ok := node.(*innerNode)
		if !ok {
			break
		}

		branch := selectBranch(nodeID, id)
		stack = append(stack, iterStackEntry{
			node:   node,
			nodeID: nodeID,
			branch: int(branch) + 1,
		})

		if inner.IsEmptyBranch(int(branch)) {
			break
		}

		child, err := sm.descendCtx(ctx, inner, int(branch))
		if err != nil {
			return nil, err
		}
		if child == nil {
			break
		}

		childID, err := nodeID.childNodeID(branch)
		if err != nil {
			return nil, err
		}

		node = child
		nodeID = childID
	}

	// Add the final leaf when the descent reached one; an inner node
	// where the descent stopped is already on the stack.
	if _, isInner := node.(*innerNode); !isInner {
		stack = append(stack, iterStackEntry{
			node:   node,
			nodeID: nodeID,
			branch: 0,
		})
	}
	return stack, nil
}

// UpperBound returns an iterator positioned at the first item with key > id.
// If no such item exists, the iterator will be invalid (Valid() returns false).
// Next() yields the remaining items in ascending key order.
//
// This matches rippled's SHAMap::upper_bound semantics.
func (sm *SHAMap) UpperBound(id [32]byte) *Iterator {
	return sm.UpperBoundContext(context.Background(), id)
}

// UpperBoundContext returns the first item above id while forwarding ctx to
// lazy storage fetches performed by this iterator.
func (sm *SHAMap) UpperBoundContext(ctx context.Context, id [32]byte) *Iterator {
	it := &Iterator{sm: sm, ctx: ctx}

	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	it.current, it.err = sm.upperBoundUnsafe(ctx, id)
	return it
}

// upperBoundUnsafe returns the first item with key > id, or nil when none
// exists (rippled SHAMap::upper_bound, SHAMap.cpp:639-668). Also the
// successor step for bound iterators. Caller must hold the read lock.
func (sm *SHAMap) upperBoundUnsafe(ctx context.Context, id [32]byte) (*Item, error) {
	if sm.tree.root == nil {
		return nil, nil
	}

	stack, err := sm.walkBoundStack(ctx, id)
	if err != nil {
		return nil, err
	}

	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(mapLeaf)
			if !ok {
				return nil, errInvalidType
			}
			if item := leafNode.Item(); item != nil && compareKeys(item.Key(), id) > 0 {
				return item, nil
			}
			continue
		}

		// Search the branches after the one leading toward id.
		for branch := int(selectBranch(entry.nodeID, id)) + 1; branch < BranchFactor; branch++ {
			child, err := sm.descendCtx(ctx, inner, branch)
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			leaf, err := sm.boundBelowCtx(ctx, child, true)
			if err != nil {
				return nil, err
			}
			if leaf != nil {
				return leaf.Item(), nil
			}
		}
	}

	return nil, nil
}

// LowerBound returns an iterator positioned at the greatest item with key < id.
// If no such item exists, the iterator will be invalid (Valid() returns false).
// Next() ascends: it yields the items after the current one in ascending key
// order (including id itself when present), like ++ on rippled's lower_bound
// iterator.
//
// Note: This matches rippled's SHAMap::lower_bound semantics, which differs from
// the standard C++ lower_bound (first element >= key).
func (sm *SHAMap) LowerBound(id [32]byte) *Iterator {
	return sm.LowerBoundContext(context.Background(), id)
}

// LowerBoundContext returns the first item below id while forwarding ctx to
// lazy storage fetches performed by this iterator.
func (sm *SHAMap) LowerBoundContext(ctx context.Context, id [32]byte) *Iterator {
	it := &Iterator{sm: sm, ctx: ctx}

	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	it.current, it.err = sm.lowerBoundUnsafe(ctx, id)
	return it
}

// lowerBoundUnsafe returns the greatest item with key < id, or nil when none
// exists (rippled SHAMap::lower_bound, SHAMap.cpp:670-705). Caller must hold
// the read lock.
func (sm *SHAMap) lowerBoundUnsafe(ctx context.Context, id [32]byte) (*Item, error) {
	if sm.tree.root == nil {
		return nil, nil
	}

	stack, err := sm.walkBoundStack(ctx, id)
	if err != nil {
		return nil, err
	}

	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(mapLeaf)
			if !ok {
				return nil, errInvalidType
			}
			if item := leafNode.Item(); item != nil && compareKeys(item.Key(), id) < 0 {
				return item, nil
			}
			continue
		}

		// Search the branches before the one leading toward id.
		for branch := int(selectBranch(entry.nodeID, id)) - 1; branch >= 0; branch-- {
			child, err := sm.descendCtx(ctx, inner, branch)
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			leaf, err := sm.boundBelowCtx(ctx, child, false)
			if err != nil {
				return nil, err
			}
			if leaf != nil {
				return leaf.Item(), nil
			}
		}
	}

	return nil, nil
}

// compareKeys compares two 32-byte keys lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareKeys(a, b [32]byte) int {
	return bytes.Compare(a[:], b[:])
}
