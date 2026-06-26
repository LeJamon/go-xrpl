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
	// bound marks iterators positioned by UpperBound/LowerBound. Their
	// Next() recomputes the successor from the current key instead of
	// consuming a saved stack, mirroring rippled's const_iterator++
	// (SHAMap.cpp:589-596): a saved stack cannot cover the leaves inside
	// the subtree boundBelow descended into.
	bound bool
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

	if it.bound {
		if it.current == nil {
			return false
		}
		item, err := it.sm.upperBoundUnsafe(it.current.Key())
		if err != nil {
			it.err = err
			it.current = nil
			return false
		}
		it.current = item
		return item != nil
	}

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
// branch, or an unloadable child). Shared prologue of upperBoundUnsafe and
// lowerBoundUnsafe. Caller must hold the read lock.
func (sm *SHAMap) walkBoundStack(id [32]byte) ([]iterStackEntry, error) {
	stack := make([]iterStackEntry, 0, MaxDepth)
	var node Node = sm.root
	nodeID := NewRootNodeID()

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

		child, err := sm.descend(inner, int(branch))
		if err != nil {
			return nil, err
		}
		if child == nil {
			break
		}

		childID, err := nodeID.ChildNodeID(branch)
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
	it := &Iterator{sm: sm, started: true, bound: true}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	it.current, it.err = sm.upperBoundUnsafe(id)
	return it
}

// upperBoundUnsafe returns the first item with key > id, or nil when none
// exists (rippled SHAMap::upper_bound, SHAMap.cpp:639-668). Also the
// successor step for bound iterators. Caller must hold the read lock.
func (sm *SHAMap) upperBoundUnsafe(id [32]byte) (*Item, error) {
	if sm.root == nil {
		return nil, nil
	}

	stack, err := sm.walkBoundStack(id)
	if err != nil {
		return nil, err
	}

	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			if item := leafNode.Item(); item != nil && compareKeys(item.Key(), id) > 0 {
				return item, nil
			}
			continue
		}

		// Search the branches after the one leading toward id.
		for branch := int(selectBranch(entry.nodeID, id)) + 1; branch < BranchFactor; branch++ {
			child, err := sm.descend(inner, branch)
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			leaf, err := sm.boundBelow(child, true)
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
	it := &Iterator{sm: sm, started: true, bound: true}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	it.current, it.err = sm.lowerBoundUnsafe(id)
	return it
}

// lowerBoundUnsafe returns the greatest item with key < id, or nil when none
// exists (rippled SHAMap::lower_bound, SHAMap.cpp:670-705). Caller must hold
// the read lock.
func (sm *SHAMap) lowerBoundUnsafe(id [32]byte) (*Item, error) {
	if sm.root == nil {
		return nil, nil
	}

	stack, err := sm.walkBoundStack(id)
	if err != nil {
		return nil, err
	}

	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		inner, isInner := entry.node.(*innerNode)
		if !isInner {
			leafNode, ok := entry.node.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			if item := leafNode.Item(); item != nil && compareKeys(item.Key(), id) < 0 {
				return item, nil
			}
			continue
		}

		// Search the branches before the one leading toward id.
		for branch := int(selectBranch(entry.nodeID, id)) - 1; branch >= 0; branch-- {
			child, err := sm.descend(inner, branch)
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			leaf, err := sm.boundBelow(child, false)
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
