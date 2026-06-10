package shamap

import (
	"bytes"
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
	stack   []iterStackEntry
	current *Item
	err     error
	started bool
}

type iterStackEntry struct {
	node   Node
	nodeID NodeID
	branch int // next branch to visit (-1 means visit node itself first)
}

// Next advances the iterator to the next item.
// Returns true if there is a next item, false if iteration is complete or an error occurred.
func (it *Iterator) Next() bool {
	if it.err != nil {
		return false
	}

	it.sm.mu.RLock()
	defer it.sm.mu.RUnlock()

	if !it.started {
		it.started = true
		return it.advance()
	}

	// Move past current leaf and find next
	return it.advance()
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

// advance moves to the next leaf in key order
func (it *Iterator) advance() bool {
	for len(it.stack) > 0 {
		top := &it.stack[len(it.stack)-1]

		inner, ok := top.node.(*innerNode)
		if !ok {
			// We're at a leaf - return it and pop
			leafNode, ok := top.node.(LeafNode)
			if !ok {
				it.err = ErrInvalidType
				return false
			}
			it.current = leafNode.Item()
			it.stack = it.stack[:len(it.stack)-1]
			return true
		}

		// Inner node - find next non-empty branch
		found := false
		for i := top.branch; i < BranchFactor; i++ {
			child, err := it.sm.descend(inner, i)
			if err != nil {
				it.err = err
				return false
			}
			if child != nil {
				// Update branch for next iteration of this node
				top.branch = i + 1

				// Push child onto stack
				childID, err := top.nodeID.ChildNodeID(uint8(i))
				if err != nil {
					it.err = err
					return false
				}
				it.stack = append(it.stack, iterStackEntry{
					node:   child,
					nodeID: childID,
					branch: 0,
				})
				found = true
				break
			}
		}

		if !found {
			// No more branches in this node, pop it
			it.stack = it.stack[:len(it.stack)-1]
		}
	}

	it.current = nil
	return false
}

// begin returns an iterator positioned before the first item.
// Call Next() to advance to the first item.
func (sm *SHAMap) begin() *Iterator {
	it := &Iterator{
		sm:    sm,
		stack: make([]iterStackEntry, 0, MaxDepth),
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root != nil {
		it.stack = append(it.stack, iterStackEntry{
			node:   sm.root,
			nodeID: NewRootNodeID(),
			branch: 0,
		})
	}

	return it
}

// walkBoundStack walks from the root toward id, returning the traversal
// stack ending at the node where the descent stopped (a leaf, an empty
// branch, or an unloadable child). Shared prologue of UpperBound and
// lowerBound. Caller must hold the read lock.
func (sm *SHAMap) walkBoundStack(it *Iterator, id [32]byte) []iterStackEntry {
	stack := make([]iterStackEntry, 0, MaxDepth)
	var node Node = sm.root
	nodeID := NewRootNodeID()

	for {
		inner, ok := node.(*innerNode)
		if !ok {
			break
		}

		branch := selectBranch(nodeID, id)
		// Resume continued iteration after the branch that leads toward
		// id: everything at or below it is covered by deeper entries.
		stack = append(stack, iterStackEntry{
			node:   node,
			nodeID: nodeID,
			branch: int(branch) + 1,
		})

		if inner.IsEmptyBranch(int(branch)) {
			break
		}

		child, err := sm.descend(inner, int(branch))
		if err != nil {
			it.err = err
			return nil
		}
		if child == nil {
			break
		}

		childID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			it.err = err
			return nil
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
	return stack
}

// UpperBound returns an iterator positioned at the first item with key > id.
// If no such item exists, the iterator will be invalid (Valid() returns false).
//
// This matches rippled's SHAMap::upper_bound semantics.
func (sm *SHAMap) UpperBound(id [32]byte) *Iterator {
	it := &Iterator{
		sm:      sm,
		stack:   make([]iterStackEntry, 0, MaxDepth),
		started: true,
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return it
	}

	stack := sm.walkBoundStack(it, id)
	if it.err != nil {
		return it
	}

	// Now search for first key > id
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(LeafNode)
			if !ok {
				it.err = ErrInvalidType
				return it
			}
			item := leafNode.Item()
			if item != nil && compareKeys(item.Key(), id) > 0 {
				it.current = item
				it.stack = stack
				return it
			}
			continue
		}

		// Inner node - search for next branch after the one leading to id
		startBranch := int(selectBranch(entry.nodeID, id)) + 1
		for branch := startBranch; branch < BranchFactor; branch++ {
			child, err := sm.descend(inner, branch)
			if err != nil {
				it.err = err
				return it
			}
			if child != nil {
				// Found a branch - get first leaf below it
				leaf := sm.boundBelow(child, true)
				if leaf != nil {
					it.current = leaf.Item()
					// Rebuild stack for continued iteration
					it.stack = stack
					it.stack = append(it.stack, iterStackEntry{
						node:   entry.node,
						nodeID: entry.nodeID,
						branch: branch + 1,
					})
					return it
				}
			}
		}
	}

	return it
}

// lowerBound returns an iterator positioned at the greatest item with key < id.
// If no such item exists, the iterator will be invalid (Valid() returns false).
//
// Note: This matches rippled's SHAMap::lower_bound semantics, which differs from
// the standard C++ lower_bound (first element >= key).
func (sm *SHAMap) lowerBound(id [32]byte) *Iterator {
	it := &Iterator{
		sm:      sm,
		stack:   make([]iterStackEntry, 0, MaxDepth),
		started: true,
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return it
	}

	stack := sm.walkBoundStack(it, id)
	if it.err != nil {
		return it
	}

	// Search for greatest key < id
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(LeafNode)
			if !ok {
				it.err = ErrInvalidType
				return it
			}
			item := leafNode.Item()
			if item != nil && compareKeys(item.Key(), id) < 0 {
				it.current = item
				it.stack = stack
				return it
			}
			continue
		}

		// Inner node - search for previous branch before the one leading to id
		startBranch := int(selectBranch(entry.nodeID, id)) - 1
		for branch := startBranch; branch >= 0; branch-- {
			child, err := sm.descend(inner, branch)
			if err != nil {
				it.err = err
				return it
			}
			if child != nil {
				// Found a branch - get last leaf below it
				leaf := sm.boundBelow(child, false)
				if leaf != nil {
					it.current = leaf.Item()
					it.stack = stack
					return it
				}
			}
		}
	}

	return it
}

// compareKeys compares two 32-byte keys lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareKeys(a, b [32]byte) int {
	return bytes.Compare(a[:], b[:])
}
