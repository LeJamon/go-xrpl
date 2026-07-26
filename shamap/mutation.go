package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// Put adds or updates an item in the SHAMap
func (sm *SHAMap) Put(key [32]byte, data []byte) error {
	item := NewItem(key, data)
	return sm.PutItem(item)
}

// PutWithNodeType adds an item with a specific node type (for transaction+metadata)
func (sm *SHAMap) PutWithNodeType(key [32]byte, data []byte, nodeType NodeType) error {
	item := NewItem(key, data)
	return sm.putItemWithNodeType(item, nodeType)
}

// putItemWithNodeType adds an item with a specific node type
func (sm *SHAMap) putItemWithNodeType(item *Item, nodeType NodeType) error {
	if item == nil {
		return ErrNilItem
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state != stateModifying {
		return ErrImmutable
	}

	return sm.putItemWithNodeTypeUnsafe(item, nodeType)
}

// putItemWithNodeTypeUnsafe adds an item with specific node type without locking
func (sm *SHAMap) putItemWithNodeTypeUnsafe(item *Item, nodeType NodeType) error {
	key := item.Key()
	stack := newNodeStack()

	node, err := sm.walkToKey(context.Background(), key, stack, false)
	if err != nil {
		return fmt.Errorf("failed to walk to key: %w", err)
	}

	if node == nil {
		// Empty slot - create new leaf with specified node type
		newLeaf, err := sm.createTypedLeaf(nodeType, item)
		if err != nil {
			return fmt.Errorf("failed to create leaf: %w", err)
		}

		newRoot, err := sm.dirtyUp(stack, key, newLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	leaf, ok := node.(mapLeaf)
	if !ok {
		return ErrInvalidType
	}

	existingItem := leaf.Item()
	existingKey := existingItem.Key()

	// Case 1: Same key - update existing item
	if bytes.Equal(key[:], existingKey[:]) {
		newLeaf, err := sm.createTypedLeaf(nodeType, item)
		if err != nil {
			return err
		}

		newRoot, err := sm.dirtyUp(stack, key, newLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	// Case 2: Different key - need to split
	currentDepth := stack.Len()
	splitDepth := findSplitDepth(key, existingKey, currentDepth)

	// Create new leaf for the new item
	newLeaf, err := sm.createTypedLeaf(nodeType, item)
	if err != nil {
		return err
	}

	// Build the chain of inner nodes from currentDepth down to splitDepth,
	// then attach both leaves to the deepest node. Each SetChild during the
	// top-down build records the (then-empty) child's zero hash, so once
	// the leaves are attached the ancestors carry stale branch hashes; the
	// bottom-up refresh loop below re-runs SetChild so every cached hash
	// tracks the live child. Serialization also prefers the live child's
	// hash (childPreimageHash), so wire bytes can never diverge from the
	// in-memory root hash even if a cache entry lags.
	topInner := newInnerNode()
	deepestInner := topInner
	chain := []*innerNode{topInner}

	for d := currentDepth; d < splitDepth; d++ {
		branch := getBranchAtDepth(key, d)
		child := newInnerNode()
		if err := deepestInner.SetChild(branch, child); err != nil {
			return err
		}
		deepestInner = child
		chain = append(chain, child)
	}

	// Place both leaves in the deepest inner node
	newBranch := getBranchAtDepth(key, splitDepth)
	existingBranch := getBranchAtDepth(existingKey, splitDepth)

	if err := deepestInner.SetChild(newBranch, newLeaf); err != nil {
		return err
	}
	if err := deepestInner.SetChild(existingBranch, leaf); err != nil {
		return err
	}

	// Refresh stale chain hashes bottom-up (no-op for deepestInner, which
	// is already current from the leaf attachments above).
	for i := len(chain) - 1; i > 0; i-- {
		branch := getBranchAtDepth(key, currentDepth+i-1)
		if err := chain[i-1].SetChild(branch, chain[i]); err != nil {
			return err
		}
	}

	// Dirty up from the top inner node
	newRoot, err := sm.dirtyUp(stack, key, topInner)
	if err != nil {
		return fmt.Errorf("failed to dirty up: %w", err)
	}

	return sm.assignRoot(newRoot, key)
}

// PutItem adds or updates an item in the SHAMap
func (sm *SHAMap) PutItem(item *Item) error {
	if item == nil {
		return ErrNilItem
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state != stateModifying {
		return ErrImmutable
	}

	return sm.putItemUnsafe(item)
}

// putItemUnsafe adds an item without locking (caller must hold lock).
// It delegates to putItemWithNodeTypeUnsafe using the default node type for the map.
func (sm *SHAMap) putItemUnsafe(item *Item) error {
	nodeType, err := sm.getLeafNodeType()
	if err != nil {
		return err
	}
	return sm.putItemWithNodeTypeUnsafe(item, nodeType)
}

// dirtyUp updates the tree from leaf to root
func (sm *SHAMap) dirtyUp(stack *nodeStack, target [32]byte, child mapNode) (mapNode, error) {
	if sm.tree.state == stateSyncing || sm.tree.state == stateImmutable {
		return nil, ErrInvalidState
	}
	if child == nil {
		return nil, errors.New("cannot propagate hash update through a nil child")
	}

	currentChild := child
	for !stack.IsEmpty() {
		node, nodeID, ok := stack.Pop()
		if !ok {
			return nil, errors.New("stack unexpectedly empty")
		}

		inner, ok := node.(*innerNode)
		if !ok {
			return nil, errors.New("expected inner node on stack")
		}

		// Path-copy persistence: rebuild a fresh inner node along the
		// mutated path so any snapshot still referencing this subtree
		// keeps its original view. Untouched siblings stay shared via
		// the copied child pointers.
		cloned := inner.shallowClone()
		branch := selectBranch(nodeID, target)
		if err := cloned.SetChild(int(branch), currentChild); err != nil {
			return nil, fmt.Errorf("failed to set child: %w", err)
		}

		currentChild = cloned
	}

	return currentChild, nil
}

// assignRoot safely assigns a new root
func (sm *SHAMap) assignRoot(newRoot mapNode, key [32]byte) error {
	if innerRoot, ok := newRoot.(*innerNode); ok {
		sm.tree.root = innerRoot
		return nil
	}

	// If newRoot is a leaf, wrap it in an inner node
	sm.tree.root = newInnerNode()
	rootNodeID := NewRootNodeID()
	branch := selectBranch(rootNodeID, key)

	if err := sm.tree.root.SetChild(int(branch), newRoot); err != nil {
		return fmt.Errorf("failed to set child in new root: %w", err)
	}

	return nil
}

// Delete removes the item associated with the given key from the SHAMap.
// It first locates and removes the corresponding leaf node, then reconstructs
// the tree from the leaf's parent up to the root, consolidating as needed.
func (sm *SHAMap) Delete(key [32]byte) error {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state != stateModifying {
		return ErrImmutable
	}

	stack, _, err := sm.findAndRemoveLeaf(key)
	if err != nil {
		return err
	}

	newRoot, err := sm.consolidateAfterDelete(stack, key)
	if err != nil {
		return err
	}

	if rootInner, ok := newRoot.(*innerNode); ok {
		sm.tree.root = rootInner
	} else {
		return fmt.Errorf("expected root to be an inner node, got %T", newRoot)
	}

	return nil
}

// findAndRemoveLeaf walks the SHAMap to locate the leaf node matching the key.
// It verifies the key, removes the leaf from the traversal stack, and returns
// the remaining stack for further processing.
func (sm *SHAMap) findAndRemoveLeaf(key [32]byte) (*nodeStack, mapLeaf, error) {
	stack := newNodeStack()
	_, err := sm.walkToKey(context.Background(), key, stack, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to walk to key: %w", err)
	}

	if stack.IsEmpty() {
		return nil, nil, ErrItemNotFound
	}

	node, _, ok := stack.Pop()
	if !ok {
		return nil, nil, ErrItemNotFound
	}

	leaf, ok := node.(mapLeaf)
	if !ok {
		return nil, nil, ErrItemNotFound
	}

	existingItem := leaf.Item()
	existingKey := existingItem.Key()
	if !bytes.Equal(key[:], existingKey[:]) {
		return nil, nil, ErrItemNotFound
	}

	return stack, leaf, nil
}

// consolidateAfterDelete reconstructs the SHAMap from a given node stack after
// a deletion. It applies bottom-up logic to restructure the tree and optimize
// it where possible (e.g., collapsing single-child inner nodes).
func (sm *SHAMap) consolidateAfterDelete(stack *nodeStack, key [32]byte) (mapNode, error) {
	var prevNode mapNode

	for !stack.IsEmpty() {
		node, nodeID, ok := stack.Pop()
		if !ok {
			break
		}

		inner, ok := node.(*innerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		// Path-copy: shallow-clone so untouched siblings stay shared
		// with any snapshot that still references this subtree.
		clonedInner := inner.shallowClone()

		branch := selectBranch(nodeID, key)
		if err := clonedInner.SetChild(int(branch), prevNode); err != nil {
			return nil, fmt.Errorf("failed to set child: %w", err)
		}

		if !nodeID.IsRoot() {
			switch clonedInner.BranchCount() {
			case 0:
				prevNode = nil
			case 1:
				onlyItem, err := sm.onlyBelow(clonedInner)
				if err != nil {
					return nil, fmt.Errorf("failed to check onlyBelow: %w", err)
				}

				if onlyItem != nil {
					nodeType, err := sm.getLeafNodeType()
					if err != nil {
						return nil, err
					}
					newLeaf, err := sm.createTypedLeaf(nodeType, onlyItem)
					if err != nil {
						return nil, fmt.Errorf("failed to create replacement leaf: %w", err)
					}
					prevNode = newLeaf
				} else {
					prevNode = clonedInner
				}
			default:
				prevNode = clonedInner
			}
		} else {
			// Always retain root
			prevNode = clonedInner
		}
	}

	if prevNode == nil {
		return nil, errors.New("unexpected nil root after deletion")
	}

	return prevNode, nil
}
