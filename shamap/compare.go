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
		return nil, fmt.Errorf("cannot compare invalid SHAMaps")
	}

	return sm.compareUnsafe(other, maxCount)
}

// handleLeafComparison handles comparison of two leaf nodes
func (sm *SHAMap) handleLeafComparison(ourNode, otherNode Node, result *DifferenceSet, maxCount int) bool {
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
		// Same key, check if content differs
		ourData := ourItem.Data()
		otherData := otherItem.Data()
		if !bytes.Equal(ourData, otherData) {
			result.AddDifference(ourKey, DiffModified, ourItem, otherItem)

			if maxCount > 0 && result.Len() >= maxCount {
				return false
			}
		}
	} else {
		// Different keys - both items are unique to their respective maps
		result.AddDifference(ourKey, DiffRemoved, ourItem, nil)

		if maxCount > 0 && result.Len() >= maxCount {
			return false
		}

		result.AddDifference(otherKey, DiffAdded, nil, otherItem)

		if maxCount > 0 && result.Len() >= maxCount {
			return false
		}
	}

	return true
}

// handleInnerComparison handles comparison of two inner nodes
func (sm *SHAMap) handleInnerComparison(ourNode, otherNode Node, other *SHAMap, result *DifferenceSet, maxCount *int) ([]stackEntry, error) {
	ourInner, ok := ourNode.(*InnerNode)
	if !ok {
		return nil, fmt.Errorf("expected InnerNode, got %T", ourNode)
	}
	otherInner, ok := otherNode.(*InnerNode)
	if !ok {
		return nil, fmt.Errorf("expected InnerNode, got %T", otherNode)
	}

	var newEntries []stackEntry

	for i := 0; i < BranchFactor; i++ {
		ourChild, err := sm.descend(ourInner, i)
		if err != nil {
			return nil, fmt.Errorf("failed to get our child %d: %w", i, err)
		}
		otherChild, err := other.descend(otherInner, i)
		if err != nil {
			return nil, fmt.Errorf("failed to get other child %d: %w", i, err)
		}

		// Compare child hashes if both exist
		if ourChild != nil && otherChild != nil {
			ourChildHash := ourChild.Hash()
			otherChildHash := otherChild.Hash()

			if !bytes.Equal(ourChildHash[:], otherChildHash[:]) {
				// Different non-empty branches - recurse
				newEntries = append(newEntries, stackEntry{
					ourNode:   ourChild,
					otherNode: otherChild,
				})
			}
		} else if ourChild == nil && otherChild != nil {
			// Other has a branch, we don't
			complete, err := other.walkBranch(otherInner, nil, false, result, maxCount)
			if err != nil {
				return nil, err
			}
			if !complete {
				return nil, nil // Signal truncation
			}
		} else if ourChild != nil {
			// We have a branch, other doesn't
			complete, err := sm.walkBranch(ourInner, nil, true, result, maxCount)
			if err != nil {
				return nil, err
			}
			if !complete {
				return nil, nil // Signal truncation
			}
		}
		// If both are nil, no difference to record
	}

	return newEntries, nil
}

// compareUnsafe is the internal comparison function without locking
// Used by both Compare() and DeepEqual()
func (sm *SHAMap) compareUnsafe(other *SHAMap, maxCount int) (*DifferenceSet, error) {
	result := &DifferenceSet{
		Differences: make([]DifferenceItem, 0),
		Complete:    true,
	}

	// Direct root hash comparison for early exit
	ourRootHash := sm.root.Hash()
	otherRootHash := other.root.Hash()

	// If root hashes are identical, maps are identical
	if bytes.Equal(ourRootHash[:], otherRootHash[:]) {
		return result, nil
	}

	// Use a stack to track nodes we're comparing
	stack := make([]stackEntry, 0)
	stack = append(stack, stackEntry{
		ourNode:   sm.root,
		otherNode: other.root,
	})

	for len(stack) > 0 {
		// Pop from stack
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		ourNode := entry.ourNode
		otherNode := entry.otherNode

		if ourNode == nil || otherNode == nil {
			return nil, fmt.Errorf("missing node during comparison")
		}

		// Both are leaf nodes
		if ourNode.IsLeaf() && otherNode.IsLeaf() {
			if !sm.handleLeafComparison(ourNode, otherNode, result, maxCount) {
				result.Complete = false
				return result, nil
			}
		} else if !ourNode.IsLeaf() && otherNode.IsLeaf() {
			// Our node is inner, other is leaf - walk our branch
			ourInner, ok := ourNode.(*InnerNode)
			if !ok {
				return nil, fmt.Errorf("expected InnerNode, got %T", ourNode)
			}
			otherLeaf, ok := otherNode.(LeafNode)
			if !ok {
				return nil, fmt.Errorf("expected LeafNode, got %T", otherNode)
			}

			complete, err := sm.walkBranch(ourInner, otherLeaf.Item(), true, result, &maxCount)
			if err != nil {
				return nil, err
			}
			if !complete {
				result.Complete = false
				return result, nil
			}
		} else if ourNode.IsLeaf() && !otherNode.IsLeaf() {
			// Our node is leaf, other is inner - walk other's branch
			ourLeaf, ok := ourNode.(LeafNode)
			if !ok {
				return nil, fmt.Errorf("expected LeafNode, got %T", ourNode)
			}
			otherInner, ok := otherNode.(*InnerNode)
			if !ok {
				return nil, fmt.Errorf("expected InnerNode, got %T", otherNode)
			}

			complete, err := other.walkBranch(otherInner, ourLeaf.Item(), false, result, &maxCount)
			if err != nil {
				return nil, err
			}
			if !complete {
				result.Complete = false
				return result, nil
			}
		} else if !ourNode.IsLeaf() && !otherNode.IsLeaf() {
			// Both are inner nodes - compare children
			newEntries, err := sm.handleInnerComparison(ourNode, otherNode, other, result, &maxCount)
			if err != nil {
				return nil, err
			}
			if newEntries == nil {
				// Truncated due to maxCount
				result.Complete = false
				return result, nil
			}
			stack = append(stack, newEntries...)
		} else {
			return nil, fmt.Errorf("invalid node combination during comparison")
		}
	}

	return result, nil
}

// walkBranch walks a branch of a SHAMap that's matched by an empty branch
// or single item in the other map
func (sm *SHAMap) walkBranch(node *InnerNode, otherMapItem *Item, isFirstMap bool, differences *DifferenceSet, maxCount *int) (bool, error) {
	// Use a stack to traverse the branch
	nodeStack := make([]Node, 0)
	nodeStack = append(nodeStack, node)

	emptyBranch := otherMapItem == nil

	for len(nodeStack) > 0 {
		// Pop from stack
		current := nodeStack[len(nodeStack)-1]
		nodeStack = nodeStack[:len(nodeStack)-1]

		if !current.IsLeaf() {
			// Inner node - add all non-empty branches to stack
			inner, ok := current.(*InnerNode)
			if !ok {
				return false, fmt.Errorf("expected InnerNode, got %T", current)
			}

			for i := 0; i < BranchFactor; i++ {
				child, err := sm.descend(inner, i)
				if err != nil {
					return false, fmt.Errorf("failed to get child %d: %w", i, err)
				}
				if child != nil {
					nodeStack = append(nodeStack, child)
				}
			}
		} else {
			// Leaf node - process its item
			leaf, ok := current.(LeafNode)
			if !ok {
				return false, fmt.Errorf("expected LeafNode, got %T", current)
			}

			item := leaf.Item()
			itemKey := item.Key()

			// Check for unmatched item - need to handle nil safely
			isUnmatched := emptyBranch
			if !isUnmatched && otherMapItem != nil {
				otherKey := otherMapItem.Key()
				isUnmatched = !bytes.Equal(itemKey[:], otherKey[:])
			}

			if isUnmatched {
				// Unmatched item
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

				differences.AddDifference(itemKey, diffType, firstItem, secondItem)

				if *maxCount > 0 && differences.Len() >= *maxCount {
					return false, nil
				}
			} else if otherMapItem != nil {
				// Same key, check if data differs
				otherData := otherMapItem.Data()
				if !bytes.Equal(item.Data(), otherData) {
					// Non-matching items with same key
					var firstItem, secondItem *Item

					if isFirstMap {
						firstItem = item
						secondItem = otherMapItem
					} else {
						firstItem = otherMapItem
						secondItem = item
					}

					differences.AddDifference(itemKey, DiffModified, firstItem, secondItem)

					if *maxCount > 0 && differences.Len() >= *maxCount {
						return false, nil
					}

					emptyBranch = true
				} else {
					// Exact match
					emptyBranch = true
				}
			} else {
				// otherMapItem is nil, so exact match (both empty)
				emptyBranch = true
			}
		}
	}

	if !emptyBranch && otherMapItem != nil {
		// otherMapItem was unmatched, must add it
		otherKey := otherMapItem.Key() // Extract key safely
		var diffType DifferenceType
		var firstItem, secondItem *Item

		if isFirstMap {
			// This is first map, so other item is from second
			diffType = DiffAdded
			firstItem = nil
			secondItem = otherMapItem
		} else {
			diffType = DiffRemoved
			firstItem = otherMapItem
			secondItem = nil
		}

		differences.AddDifference(otherKey, diffType, firstItem, secondItem)

		if *maxCount > 0 && differences.Len() >= *maxCount {
			return false, nil
		}
	}

	return true, nil
}

// Equal returns true if this SHAMap is identical to the other SHAMap
// This is more efficient than Compare() when you only need to know equality
func (sm *SHAMap) Equal(other *SHAMap) (bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return false, fmt.Errorf("cannot compare invalid SHAMaps")
	}

	// Direct root hash comparison - most efficient check
	ourRootHash := sm.root.Hash()
	otherRootHash := other.root.Hash()

	// If root hashes are identical, maps are identical
	return bytes.Equal(ourRootHash[:], otherRootHash[:]), nil
}

// DeepEqual performs a deep structural comparison without relying on hashes
// This is useful for testing or when hash integrity is in question
func (sm *SHAMap) DeepEqual(other *SHAMap) (bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return false, fmt.Errorf("cannot compare invalid SHAMaps")
	}

	// Use Compare with limit of 1 - if any difference found, not equal
	differences, err := sm.compareUnsafe(other, 1)
	if err != nil {
		return false, err
	}

	return differences.IsEmpty(), nil
}

// HasDifferences returns true if there are any differences between the maps
// More efficient than getting the full difference set when you only care about existence
func (sm *SHAMap) HasDifferences(other *SHAMap) (bool, error) {
	equal, err := sm.Equal(other)
	if err != nil {
		return false, err
	}
	return !equal, nil
}

// Differences returns a channel that yields differences between the maps as they're discovered.
// The channel is closed when all differences have been found or an error occurs.
// This is memory-efficient for processing large difference sets.
//
// Usage:
//
//	for diff := range map1.Differences(map2) {
//	    processDifference(diff)
//	}
//
// To handle errors, use DifferencesWithError instead.
func (sm *SHAMap) Differences(other *SHAMap) <-chan DifferenceItem {
	ch := make(chan DifferenceItem)

	go func() {
		defer close(ch)
		_ = sm.DifferencesWithError(other, ch)
	}()

	return ch
}

// DifferencesWithError sends differences to the provided channel and returns any error encountered.
// The caller is responsible for closing the channel.
// This version allows proper error handling while maintaining the streaming benefits.
//
// Usage:
//
//	ch := make(chan DifferenceItem, 100)
//	go func() {
//	    defer close(ch)
//	    if err := map1.DifferencesWithError(map2, ch); err != nil {
//	        log.Printf("Error comparing maps: %v", err)
//	    }
//	}()
//
//	for diff := range ch {
//	    processDifference(diff)
//	}
func (sm *SHAMap) DifferencesWithError(other *SHAMap, ch chan<- DifferenceItem) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return fmt.Errorf("cannot compare invalid SHAMaps")
	}

	// Direct root hash comparison for early exit
	ourRootHash := sm.root.Hash()
	otherRootHash := other.root.Hash()

	// If root hashes are identical, maps are identical - no differences to send
	if bytes.Equal(ourRootHash[:], otherRootHash[:]) {
		return nil
	}

	// Use a stack to track nodes we're comparing
	stack := make([]stackEntry, 0)
	stack = append(stack, stackEntry{
		ourNode:   sm.root,
		otherNode: other.root,
	})

	for len(stack) > 0 {
		// Pop from stack
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		ourNode := entry.ourNode
		otherNode := entry.otherNode

		if ourNode == nil || otherNode == nil {
			return fmt.Errorf("missing node during comparison")
		}

		// Both are leaf nodes
		if ourNode.IsLeaf() && otherNode.IsLeaf() {
			if err := sm.handleLeafComparisonWithChannel(ourNode, otherNode, ch); err != nil {
				return err
			}
		} else if !ourNode.IsLeaf() && otherNode.IsLeaf() {
			// Our node is inner, other is leaf - walk our branch
			ourInner, ok := ourNode.(*InnerNode)
			if !ok {
				return fmt.Errorf("expected InnerNode, got %T", ourNode)
			}
			otherLeaf, ok := otherNode.(LeafNode)
			if !ok {
				return fmt.Errorf("expected LeafNode, got %T", otherNode)
			}

			if err := sm.walkBranchWithChannel(ourInner, otherLeaf.Item(), true, ch); err != nil {
				return err
			}
		} else if ourNode.IsLeaf() && !otherNode.IsLeaf() {
			// Our node is leaf, other is inner - walk other's branch
			ourLeaf, ok := ourNode.(LeafNode)
			if !ok {
				return fmt.Errorf("expected LeafNode, got %T", ourNode)
			}
			otherInner, ok := otherNode.(*InnerNode)
			if !ok {
				return fmt.Errorf("expected InnerNode, got %T", otherNode)
			}

			if err := other.walkBranchWithChannel(otherInner, ourLeaf.Item(), false, ch); err != nil {
				return err
			}
		} else if !ourNode.IsLeaf() && !otherNode.IsLeaf() {
			// Both are inner nodes - compare children
			newEntries, err := sm.handleInnerComparisonWithChannel(ourNode, otherNode, other, ch)
			if err != nil {
				return err
			}
			stack = append(stack, newEntries...)
		} else {
			return fmt.Errorf("invalid node combination during comparison")
		}
	}

	return nil
}

// handleLeafComparisonWithChannel handles comparison of two leaf nodes using channel
func (sm *SHAMap) handleLeafComparisonWithChannel(ourNode, otherNode Node, ch chan<- DifferenceItem) error {
	ourLeaf, ok := ourNode.(LeafNode)
	if !ok {
		return fmt.Errorf("expected LeafNode, got %T", ourNode)
	}
	otherLeaf, ok := otherNode.(LeafNode)
	if !ok {
		return fmt.Errorf("expected LeafNode, got %T", otherNode)
	}

	ourItem := ourLeaf.Item()
	otherItem := otherLeaf.Item()
	ourKey := ourItem.Key()
	otherKey := otherItem.Key()

	if bytes.Equal(ourKey[:], otherKey[:]) {
		// Same key, check if content differs
		ourData := ourItem.Data()
		otherData := otherItem.Data()
		if !bytes.Equal(ourData, otherData) {
			diff := DifferenceItem{
				Key:        ourKey,
				Type:       DiffModified,
				FirstItem:  ourItem,
				SecondItem: otherItem,
			}
			select {
			case ch <- diff:
			default:
				return fmt.Errorf("channel blocked while sending difference")
			}
		}
	} else {
		// Different keys - both items are unique to their respective maps
		diff1 := DifferenceItem{
			Key:        ourKey,
			Type:       DiffRemoved,
			FirstItem:  ourItem,
			SecondItem: nil,
		}
		select {
		case ch <- diff1:
		default:
			return fmt.Errorf("channel blocked while sending difference")
		}

		diff2 := DifferenceItem{
			Key:        otherKey,
			Type:       DiffAdded,
			FirstItem:  nil,
			SecondItem: otherItem,
		}
		select {
		case ch <- diff2:
		default:
			return fmt.Errorf("channel blocked while sending difference")
		}
	}

	return nil
}

// handleInnerComparisonWithChannel handles comparison of two inner nodes using channel
func (sm *SHAMap) handleInnerComparisonWithChannel(ourNode, otherNode Node, other *SHAMap, ch chan<- DifferenceItem) ([]stackEntry, error) {
	ourInner, ok := ourNode.(*InnerNode)
	if !ok {
		return nil, fmt.Errorf("expected InnerNode, got %T", ourNode)
	}
	otherInner, ok := otherNode.(*InnerNode)
	if !ok {
		return nil, fmt.Errorf("expected InnerNode, got %T", otherNode)
	}

	var newEntries []stackEntry

	for i := 0; i < BranchFactor; i++ {
		ourChild, err := sm.descend(ourInner, i)
		if err != nil {
			return nil, fmt.Errorf("failed to get our child %d: %w", i, err)
		}
		otherChild, err := other.descend(otherInner, i)
		if err != nil {
			return nil, fmt.Errorf("failed to get other child %d: %w", i, err)
		}

		// Compare child hashes if both exist
		if ourChild != nil && otherChild != nil {
			ourChildHash := ourChild.Hash()
			otherChildHash := otherChild.Hash()

			if !bytes.Equal(ourChildHash[:], otherChildHash[:]) {
				// Different non-empty branches - recurse
				newEntries = append(newEntries, stackEntry{
					ourNode:   ourChild,
					otherNode: otherChild,
				})
			}
		} else if ourChild == nil && otherChild != nil {
			// Other has a branch, we don't
			if err := other.walkBranchWithChannel(otherInner, nil, false, ch); err != nil {
				return nil, err
			}
		} else if ourChild != nil {
			// We have a branch, other doesn't
			if err := sm.walkBranchWithChannel(ourInner, nil, true, ch); err != nil {
				return nil, err
			}
		}
		// If both are nil, no difference to record
	}

	return newEntries, nil
}

// walkBranchWithChannel walks a branch using channel to send differences
func (sm *SHAMap) walkBranchWithChannel(node *InnerNode, otherMapItem *Item, isFirstMap bool, ch chan<- DifferenceItem) error {
	// Use a stack to traverse the branch
	nodeStack := make([]Node, 0)
	nodeStack = append(nodeStack, node)

	emptyBranch := otherMapItem == nil

	for len(nodeStack) > 0 {
		// Pop from stack
		current := nodeStack[len(nodeStack)-1]
		nodeStack = nodeStack[:len(nodeStack)-1]

		if !current.IsLeaf() {
			// Inner node - add all non-empty branches to stack
			inner, ok := current.(*InnerNode)
			if !ok {
				return fmt.Errorf("expected InnerNode, got %T", current)
			}

			for i := 0; i < BranchFactor; i++ {
				child, err := sm.descend(inner, i)
				if err != nil {
					return fmt.Errorf("failed to get child %d: %w", i, err)
				}
				if child != nil {
					nodeStack = append(nodeStack, child)
				}
			}
		} else {
			// Leaf node - process its item
			leaf, ok := current.(LeafNode)
			if !ok {
				return fmt.Errorf("expected LeafNode, got %T", current)
			}

			item := leaf.Item()
			itemKey := item.Key()

			// Check for unmatched item - need to handle nil safely
			isUnmatched := emptyBranch
			if !isUnmatched && otherMapItem != nil {
				otherKey := otherMapItem.Key()
				isUnmatched = !bytes.Equal(itemKey[:], otherKey[:])
			}

			if isUnmatched {
				// Unmatched item
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

				diff := DifferenceItem{
					Key:        itemKey,
					Type:       diffType,
					FirstItem:  firstItem,
					SecondItem: secondItem,
				}

				select {
				case ch <- diff:
				default:
					return fmt.Errorf("channel blocked while sending difference")
				}
			} else if otherMapItem != nil {
				// Same key, check if data differs
				otherData := otherMapItem.Data()
				if !bytes.Equal(item.Data(), otherData) {
					// Non-matching items with same key
					var firstItem, secondItem *Item

					if isFirstMap {
						firstItem = item
						secondItem = otherMapItem
					} else {
						firstItem = otherMapItem
						secondItem = item
					}

					diff := DifferenceItem{
						Key:        itemKey,
						Type:       DiffModified,
						FirstItem:  firstItem,
						SecondItem: secondItem,
					}

					select {
					case ch <- diff:
					default:
						return fmt.Errorf("channel blocked while sending difference")
					}

					emptyBranch = true
				} else {
					// Exact match
					emptyBranch = true
				}
			} else {
				// otherMapItem is nil, so exact match (both empty)
				emptyBranch = true
			}
		}
	}

	if !emptyBranch && otherMapItem != nil {
		// otherMapItem was unmatched, must add it
		otherKey := otherMapItem.Key() // Extract key safely
		var diffType DifferenceType
		var firstItem, secondItem *Item

		if isFirstMap {
			// This is first map, so other item is from second
			diffType = DiffAdded
			firstItem = nil
			secondItem = otherMapItem
		} else {
			diffType = DiffRemoved
			firstItem = otherMapItem
			secondItem = nil
		}

		diff := DifferenceItem{
			Key:        otherKey,
			Type:       diffType,
			FirstItem:  firstItem,
			SecondItem: secondItem,
		}

		select {
		case ch <- diff:
		default:
			return fmt.Errorf("channel blocked while sending difference")
		}
	}

	return nil
}
