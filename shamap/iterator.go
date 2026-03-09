package shamap

import (
	"bytes"
)

// Iterator provides forward iteration over SHAMap items in key order.
// Usage:
//
//	iter := sm.Begin()
//	for iter.Next() {
//	    item := iter.Item()
//	    // use item
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

		if top.node.IsLeaf() {
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
		inner, ok := top.node.(*InnerNode)
		if !ok {
			it.err = ErrInvalidType
			return false
		}

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

// Begin returns an iterator positioned before the first item.
// Call Next() to advance to the first item.
func (sm *SHAMap) Begin() *Iterator {
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

	// Walk toward the key, building the stack
	stack := make([]iterStackEntry, 0, MaxDepth)
	var node Node = sm.root
	nodeID := NewRootNodeID()

	for !node.IsLeaf() {
		inner, ok := node.(*InnerNode)
		if !ok {
			it.err = ErrInvalidType
			return it
		}

		stack = append(stack, iterStackEntry{
			node:   node,
			nodeID: nodeID,
			branch: -1, // will be set when we backtrack
		})

		branch := SelectBranch(nodeID, id)
		if inner.IsEmptyBranch(int(branch)) {
			break
		}

		child, err := sm.descend(inner, int(branch))
		if err != nil {
			it.err = err
			return it
		}
		if child == nil {
			break
		}

		childID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			it.err = err
			return it
		}

		node = child
		nodeID = childID
	}

	// Add the final node (leaf or inner where we stopped)
	stack = append(stack, iterStackEntry{
		node:   node,
		nodeID: nodeID,
		branch: -1,
	})

	// Now search for first key > id
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if entry.node.IsLeaf() {
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
		inner, ok := entry.node.(*InnerNode)
		if !ok {
			it.err = ErrInvalidType
			return it
		}

		startBranch := int(SelectBranch(entry.nodeID, id)) + 1
		for branch := startBranch; branch < BranchFactor; branch++ {
			child, err := sm.descend(inner, branch)
			if err != nil {
				it.err = err
				return it
			}
			if child != nil {
				// Found a branch - get first leaf below it
				leaf := sm.firstBelow(child, entry.nodeID, branch)
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

// LowerBound returns an iterator positioned at the greatest item with key < id.
// If no such item exists, the iterator will be invalid (Valid() returns false).
//
// Note: This matches rippled's SHAMap::lower_bound semantics, which differs from
// the standard C++ lower_bound (first element >= key).
func (sm *SHAMap) LowerBound(id [32]byte) *Iterator {
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

	// Walk toward the key, building the stack
	stack := make([]iterStackEntry, 0, MaxDepth)
	var node Node = sm.root
	nodeID := NewRootNodeID()

	for !node.IsLeaf() {
		inner, ok := node.(*InnerNode)
		if !ok {
			it.err = ErrInvalidType
			return it
		}

		stack = append(stack, iterStackEntry{
			node:   node,
			nodeID: nodeID,
			branch: -1,
		})

		branch := SelectBranch(nodeID, id)
		if inner.IsEmptyBranch(int(branch)) {
			break
		}

		child, err := sm.descend(inner, int(branch))
		if err != nil {
			it.err = err
			return it
		}
		if child == nil {
			break
		}

		childID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			it.err = err
			return it
		}

		node = child
		nodeID = childID
	}

	// Add the final node
	stack = append(stack, iterStackEntry{
		node:   node,
		nodeID: nodeID,
		branch: -1,
	})

	// Search for greatest key < id
	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if entry.node.IsLeaf() {
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
		inner, ok := entry.node.(*InnerNode)
		if !ok {
			it.err = ErrInvalidType
			return it
		}

		startBranch := int(SelectBranch(entry.nodeID, id)) - 1
		for branch := startBranch; branch >= 0; branch-- {
			child, err := sm.descend(inner, branch)
			if err != nil {
				it.err = err
				return it
			}
			if child != nil {
				// Found a branch - get last leaf below it
				leaf := sm.lastBelow(child, entry.nodeID, branch)
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

// firstBelow returns the first (smallest key) leaf node at or below the given node
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

	for i := 0; i < BranchFactor; i++ {
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

// lastBelow returns the last (largest key) leaf node at or below the given node
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

// compareKeys compares two 32-byte keys lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareKeys(a, b [32]byte) int {
	return bytes.Compare(a[:], b[:])
}
