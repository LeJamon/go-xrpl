package shamap

import (
	"bytes"
	"fmt"
)

// stackEntry represents a pair of nodes being compared
type stackEntry struct {
	ourNode   Node
	otherNode Node
}

// Compare compares this SHAMap with another and returns differences
// maxCount limits the number of differences to find (0 = no limit)
// Returns complete=true if all differences found, false if truncated
func (sm *SHAMap) Compare(other *SHAMap, maxCount int) (*DifferenceSet, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return nil, fmt.Errorf("%w: cannot compare invalid SHAMaps", ErrInvalidState)
	}

	result := &DifferenceSet{
		Differences: make([]DifferenceItem, 0),
		Complete:    true,
	}
	complete, err := sm.diffUnsafe(other, func(diff DifferenceItem) bool {
		result.AddDifference(diff.Key, diff.Type, diff.FirstItem, diff.SecondItem)
		return maxCount <= 0 || result.Len() < maxCount
	})
	if err != nil {
		return nil, err
	}
	result.Complete = complete
	return result, nil
}

// FindDifference finds all keys that differ between this map and another.
// This is a convenience method that returns just the keys of items that
// differ (added, removed, or modified) between the two maps, without the
// full DifferenceItem details.
func (sm *SHAMap) FindDifference(other *SHAMap) ([]Key, error) {
	if other == nil {
		return nil, fmt.Errorf("cannot compare with nil map")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return nil, fmt.Errorf("%w: cannot compare invalid SHAMaps", ErrInvalidState)
	}

	var keys []Key
	if _, err := sm.diffUnsafe(other, func(diff DifferenceItem) bool {
		keys = append(keys, diff.Key)
		return true
	}); err != nil {
		return nil, err
	}
	return keys, nil
}

// diffUnsafe is the single diff walk shared by Compare and FindDifference.
// It calls emit for every difference between the two maps; emit returning
// false stops the walk early (complete=false). Both maps descend with lazy
// loading, so backed maps with released children diff correctly.
// Caller must hold both maps' read locks.
func (sm *SHAMap) diffUnsafe(other *SHAMap, emit func(DifferenceItem) bool) (complete bool, err error) {
	// Direct root hash comparison for early exit: identical hashes mean
	// identical maps.
	if sm.root.Hash() == other.root.Hash() {
		return true, nil
	}

	// Use a stack to track nodes we're comparing
	stack := []stackEntry{{ourNode: sm.root, otherNode: other.root}}

	for len(stack) > 0 {
		// Pop from stack
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		ourNode := entry.ourNode
		otherNode := entry.otherNode

		if ourNode == nil || otherNode == nil {
			return false, fmt.Errorf("missing node during comparison")
		}

		ourInner, ourIsInner := ourNode.(*innerNode)
		otherInner, otherIsInner := otherNode.(*innerNode)

		switch {
		case !ourIsInner && !otherIsInner:
			// Both are leaf nodes
			if !sm.emitLeafDiff(ourNode, otherNode, emit) {
				return false, nil
			}
		case ourIsInner && !otherIsInner:
			// Our node is inner, other is leaf - walk our branch
			otherLeaf, ok := otherNode.(LeafNode)
			if !ok {
				return false, fmt.Errorf("expected LeafNode, got %T", otherNode)
			}
			cont, err := sm.walkBranch(ourInner, otherLeaf.Item(), true, emit)
			if err != nil || !cont {
				return false, err
			}
		case !ourIsInner && otherIsInner:
			ourLeaf, ok := ourNode.(LeafNode)
			if !ok {
				return false, fmt.Errorf("expected LeafNode, got %T", ourNode)
			}
			cont, err := other.walkBranch(otherInner, ourLeaf.Item(), false, emit)
			if err != nil || !cont {
				return false, err
			}
		default:
			// Both are inner nodes - compare children
			newEntries, cont, err := sm.diffInner(ourInner, otherInner, other, emit)
			if err != nil || !cont {
				return false, err
			}
			stack = append(stack, newEntries...)
		}
	}

	return true, nil
}

// emitLeafDiff emits the difference(s) between two leaf nodes.
// Returns false if emit asked to stop.
func (sm *SHAMap) emitLeafDiff(ourNode, otherNode Node, emit func(DifferenceItem) bool) bool {
	ourLeaf, ok := ourNode.(LeafNode)
	if !ok {
		return false
	}
	otherLeaf, ok := otherNode.(LeafNode)
	if !ok {
		return false
	}

	ourItem := ourLeaf.Item()
	otherItem := otherLeaf.Item()
	ourKey := ourItem.Key()
	otherKey := otherItem.Key()

	if bytes.Equal(ourKey[:], otherKey[:]) {
		if !bytes.Equal(ourItem.DataUnsafe(), otherItem.DataUnsafe()) {
			return emit(DifferenceItem{Key: ourKey, Type: DiffModified, FirstItem: ourItem, SecondItem: otherItem})
		}
	} else {
		if !emit(DifferenceItem{Key: ourKey, Type: DiffRemoved, FirstItem: ourItem, SecondItem: nil}) {
			return false
		}
		if !emit(DifferenceItem{Key: otherKey, Type: DiffAdded, FirstItem: nil, SecondItem: otherItem}) {
			return false
		}
	}

	return true
}

// diffInner compares the children of two inner nodes, emitting differences
// for branches present on only one side and returning the branch pairs that
// need deeper comparison. cont is false once emit has asked to stop.
func (sm *SHAMap) diffInner(ourInner, otherInner *innerNode, other *SHAMap, emit func(DifferenceItem) bool) (newEntries []stackEntry, cont bool, err error) {
	for i := range BranchFactor {
		ourChild, err := sm.descend(ourInner, i)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get our child %d: %w", i, err)
		}
		otherChild, err := other.descend(otherInner, i)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get other child %d: %w", i, err)
		}

		switch {
		case ourChild != nil && otherChild != nil:
			if ourChild.Hash() != otherChild.Hash() {
				newEntries = append(newEntries, stackEntry{
					ourNode:   ourChild,
					otherNode: otherChild,
				})
			}
		case ourChild == nil && otherChild != nil:
			cont, err := other.walkBranch(otherChild, nil, false, emit)
			if err != nil || !cont {
				return nil, false, err
			}
		case ourChild != nil:
			cont, err := sm.walkBranch(ourChild, nil, true, emit)
			if err != nil || !cont {
				return nil, false, err
			}
		}
	}

	return newEntries, true, nil
}

// walkBranch walks a branch of a SHAMap that's matched by an empty branch
// or single item in the other map. emit is called for each difference;
// if it returns false the walk stops early (cont=false).
func (sm *SHAMap) walkBranch(node Node, otherMapItem *Item, isFirstMap bool, emit func(DifferenceItem) bool) (bool, error) {
	nodeStack := []Node{node}

	emptyBranch := otherMapItem == nil

	for len(nodeStack) > 0 {
		current := nodeStack[len(nodeStack)-1]
		nodeStack = nodeStack[:len(nodeStack)-1]

		if inner, ok := current.(*innerNode); ok {
			for i := range BranchFactor {
				child, err := sm.descend(inner, i)
				if err != nil {
					return false, fmt.Errorf("failed to get child %d: %w", i, err)
				}
				if child != nil {
					nodeStack = append(nodeStack, child)
				}
			}
			continue
		}

		leaf, ok := current.(LeafNode)
		if !ok {
			return false, fmt.Errorf("expected LeafNode, got %T", current)
		}

		item := leaf.Item()
		itemKey := item.Key()

		isUnmatched := emptyBranch
		if !isUnmatched && otherMapItem != nil {
			otherKey := otherMapItem.Key()
			isUnmatched = !bytes.Equal(itemKey[:], otherKey[:])
		}

		if isUnmatched {
			var diffType DifferenceType
			var firstItem, secondItem *Item

			if isFirstMap {
				diffType = DiffRemoved
				firstItem = item
				secondItem = nil
			} else {
				diffType = DiffAdded
				firstItem = nil
				secondItem = item
			}

			if !emit(DifferenceItem{Key: itemKey, Type: diffType, FirstItem: firstItem, SecondItem: secondItem}) {
				return false, nil
			}
		} else if otherMapItem != nil {
			if !bytes.Equal(item.DataUnsafe(), otherMapItem.DataUnsafe()) {
				var firstItem, secondItem *Item

				if isFirstMap {
					firstItem = item
					secondItem = otherMapItem
				} else {
					firstItem = otherMapItem
					secondItem = item
				}

				if !emit(DifferenceItem{Key: itemKey, Type: DiffModified, FirstItem: firstItem, SecondItem: secondItem}) {
					return false, nil
				}
			}
			emptyBranch = true
		} else {
			emptyBranch = true
		}
	}

	if !emptyBranch && otherMapItem != nil {
		otherKey := otherMapItem.Key()
		var diffType DifferenceType
		var firstItem, secondItem *Item

		if isFirstMap {
			diffType = DiffAdded
			firstItem = nil
			secondItem = otherMapItem
		} else {
			diffType = DiffRemoved
			firstItem = otherMapItem
			secondItem = nil
		}

		if !emit(DifferenceItem{Key: otherKey, Type: diffType, FirstItem: firstItem, SecondItem: secondItem}) {
			return false, nil
		}
	}

	return true, nil
}
