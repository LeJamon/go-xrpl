package shamap

import (
	"bytes"
	"fmt"
)

// InvariantError represents an error found during invariant checking.
type InvariantError struct {
	NodeID      NodeID
	Description string
	Err         error
}

// Error implements the error interface.
func (e *InvariantError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("invariant violation at %s: %s: %v", e.NodeID.String(), e.Description, e.Err)
	}
	return fmt.Sprintf("invariant violation at %s: %s", e.NodeID.String(), e.Description)
}

// Unwrap returns the underlying error.
func (e *InvariantError) Unwrap() error {
	return e.Err
}

// InvariantCheckResult contains the results of an invariant check.
type InvariantCheckResult struct {
	Errors      []*InvariantError
	NodesChecked int
	LeavesChecked int
	InnerNodesChecked int
}

// HasErrors returns true if any invariant violations were found.
func (r *InvariantCheckResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// String returns a summary of the invariant check results.
func (r *InvariantCheckResult) String() string {
	if r.HasErrors() {
		return fmt.Sprintf("InvariantCheck: FAILED - %d errors found (%d nodes checked: %d inner, %d leaves)",
			len(r.Errors), r.NodesChecked, r.InnerNodesChecked, r.LeavesChecked)
	}
	return fmt.Sprintf("InvariantCheck: PASSED (%d nodes checked: %d inner, %d leaves)",
		r.NodesChecked, r.InnerNodesChecked, r.LeavesChecked)
}

// Invariants performs a comprehensive consistency check on the SHAMap.
// It verifies:
//   - All node hashes are computed correctly
//   - All child references are consistent (hash matches actual child)
//   - No empty non-root inner nodes exist
//   - All leaf nodes have valid items
//   - Tree structure is valid (no cycles, proper depth)
//
// Returns an error describing the first inconsistency found, or nil if valid.
func (sm *SHAMap) Invariants() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.invariantsUnsafe()
}

// invariantsUnsafe performs invariant checking without locking.
// Caller must hold the read lock.
func (sm *SHAMap) invariantsUnsafe() error {
	if sm.root == nil {
		if sm.state != StateInvalid {
			return nil // Empty map is valid
		}
		return fmt.Errorf("invalid state with nil root")
	}

	// Check root node
	if err := sm.checkNodeInvariants(sm.root, NewRootNodeID(), true); err != nil {
		return err
	}

	return nil
}

// checkNodeInvariants recursively checks invariants for a node and its descendants.
func (sm *SHAMap) checkNodeInvariants(node Node, nodeID NodeID, isRoot bool) error {
	if node == nil {
		return nil
	}

	// Check depth limit
	if nodeID.Depth() > MaxDepth {
		return &InvariantError{
			NodeID:      nodeID,
			Description: fmt.Sprintf("depth %d exceeds maximum %d", nodeID.Depth(), MaxDepth),
		}
	}

	// Check node-specific invariants
	if err := node.Invariants(isRoot); err != nil {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "node invariants check failed",
			Err:         err,
		}
	}

	// Verify hash is correctly computed
	if err := sm.verifyNodeHash(node, nodeID); err != nil {
		return err
	}

	// For inner nodes, recursively check children
	if !node.IsLeaf() {
		inner, ok := node.(*InnerNode)
		if !ok {
			return &InvariantError{
				NodeID:      nodeID,
				Description: "node reports IsLeaf()=false but is not InnerNode",
			}
		}

		return sm.checkInnerNodeInvariants(inner, nodeID)
	}

	// For leaf nodes, check leaf-specific invariants
	return sm.checkLeafNodeInvariants(node, nodeID)
}

// verifyNodeHash verifies that a node's hash is correctly computed.
func (sm *SHAMap) verifyNodeHash(node Node, nodeID NodeID) error {
	// Clone the node and recompute its hash
	cloned, err := node.Clone()
	if err != nil {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "failed to clone node for hash verification",
			Err:         err,
		}
	}

	if err := cloned.UpdateHash(); err != nil {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "failed to recompute hash",
			Err:         err,
		}
	}

	originalHash := node.Hash()
	recomputedHash := cloned.Hash()

	if !bytes.Equal(originalHash[:], recomputedHash[:]) {
		return &InvariantError{
			NodeID:      nodeID,
			Description: fmt.Sprintf("hash mismatch: stored %x, computed %x", originalHash[:8], recomputedHash[:8]),
		}
	}

	return nil
}

// checkInnerNodeInvariants checks invariants specific to inner nodes.
func (sm *SHAMap) checkInnerNodeInvariants(inner *InnerNode, nodeID NodeID) error {
	childCount := 0

	for branch := 0; branch < BranchFactor; branch++ {
		// Check branch bitmap consistency
		hasChild := !inner.IsEmptyBranch(branch)
		child, err := sm.descend(inner, branch)
		if err != nil {
			return &InvariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("failed to get child at branch %d", branch),
				Err:         err,
			}
		}

		// Verify bitmap matches actual children
		if hasChild && child == nil {
			// This can happen during sync or for backed maps with hash-only branches
			if sm.state != StateSyncing && !sm.backed {
				return &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("branch %d marked as non-empty but child is nil", branch),
				}
			}
		}

		if !hasChild && child != nil {
			return &InvariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("branch %d marked as empty but child exists", branch),
			}
		}

		if child != nil {
			childCount++

			// Verify stored hash matches child's actual hash
			storedHash, err := inner.ChildHash(branch)
			if err != nil {
				return &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("failed to get stored hash for branch %d", branch),
					Err:         err,
				}
			}

			childHash := child.Hash()
			if !bytes.Equal(storedHash[:], childHash[:]) {
				return &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("branch %d: stored hash %x != child hash %x", branch, storedHash[:8], childHash[:8]),
				}
			}

			// Recursively check child
			childNodeID, err := nodeID.ChildNodeID(uint8(branch))
			if err != nil {
				return &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("failed to compute child node ID for branch %d", branch),
					Err:         err,
				}
			}

			if err := sm.checkNodeInvariants(child, childNodeID, false); err != nil {
				return err
			}
		}
	}

	// Non-root inner nodes must have at least one child
	// For backed maps, count includes lazily loaded children
	if !nodeID.IsRoot() && childCount == 0 && !sm.backed {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "non-root inner node has no children",
		}
	}

	return nil
}

// checkLeafNodeInvariants checks invariants specific to leaf nodes.
func (sm *SHAMap) checkLeafNodeInvariants(node Node, nodeID NodeID) error {
	leaf, ok := node.(LeafNode)
	if !ok {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "node reports IsLeaf()=true but doesn't implement LeafNode",
		}
	}

	item := leaf.Item()
	if item == nil {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "leaf node has nil item",
		}
	}

	// Validate the item
	if err := item.Validate(); err != nil {
		return &InvariantError{
			NodeID:      nodeID,
			Description: "leaf item validation failed",
			Err:         err,
		}
	}

	return nil
}

// InvariantsDetailed performs a comprehensive invariant check and returns detailed results.
// Unlike Invariants(), this continues checking even after finding errors.
func (sm *SHAMap) InvariantsDetailed() *InvariantCheckResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := &InvariantCheckResult{
		Errors: make([]*InvariantError, 0),
	}

	if sm.root == nil {
		return result
	}

	sm.checkNodeInvariantsDetailed(sm.root, NewRootNodeID(), true, result)
	return result
}

// checkNodeInvariantsDetailed recursively checks invariants and collects all errors.
func (sm *SHAMap) checkNodeInvariantsDetailed(node Node, nodeID NodeID, isRoot bool, result *InvariantCheckResult) {
	if node == nil {
		return
	}

	result.NodesChecked++

	// Check depth limit
	if nodeID.Depth() > MaxDepth {
		result.Errors = append(result.Errors, &InvariantError{
			NodeID:      nodeID,
			Description: fmt.Sprintf("depth %d exceeds maximum %d", nodeID.Depth(), MaxDepth),
		})
		return
	}

	// Check node-specific invariants
	if err := node.Invariants(isRoot); err != nil {
		result.Errors = append(result.Errors, &InvariantError{
			NodeID:      nodeID,
			Description: "node invariants check failed",
			Err:         err,
		})
	}

	// Verify hash
	if err := sm.verifyNodeHash(node, nodeID); err != nil {
		if invErr, ok := err.(*InvariantError); ok {
			result.Errors = append(result.Errors, invErr)
		}
	}

	if node.IsLeaf() {
		result.LeavesChecked++
		// Check leaf invariants
		if err := sm.checkLeafNodeInvariants(node, nodeID); err != nil {
			if invErr, ok := err.(*InvariantError); ok {
				result.Errors = append(result.Errors, invErr)
			}
		}
	} else {
		result.InnerNodesChecked++
		// Check inner node and recurse
		inner, ok := node.(*InnerNode)
		if !ok {
			result.Errors = append(result.Errors, &InvariantError{
				NodeID:      nodeID,
				Description: "node reports IsLeaf()=false but is not InnerNode",
			})
			return
		}

		sm.checkInnerNodeInvariantsDetailed(inner, nodeID, result)
	}
}

// checkInnerNodeInvariantsDetailed checks inner node invariants and collects all errors.
func (sm *SHAMap) checkInnerNodeInvariantsDetailed(inner *InnerNode, nodeID NodeID, result *InvariantCheckResult) {
	childCount := 0

	for branch := 0; branch < BranchFactor; branch++ {
		hasChild := !inner.IsEmptyBranch(branch)
		child, err := sm.descend(inner, branch)
		if err != nil {
			result.Errors = append(result.Errors, &InvariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("failed to get child at branch %d", branch),
				Err:         err,
			})
			continue
		}

		// Check bitmap consistency
		if hasChild && child == nil && sm.state != StateSyncing && !sm.backed {
			result.Errors = append(result.Errors, &InvariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("branch %d marked as non-empty but child is nil", branch),
			})
		}

		if !hasChild && child != nil {
			result.Errors = append(result.Errors, &InvariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("branch %d marked as empty but child exists", branch),
			})
		}

		if child != nil {
			childCount++

			// Verify stored hash
			storedHash, err := inner.ChildHash(branch)
			if err != nil {
				result.Errors = append(result.Errors, &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("failed to get stored hash for branch %d", branch),
					Err:         err,
				})
			} else {
				childHash := child.Hash()
				if !bytes.Equal(storedHash[:], childHash[:]) {
					result.Errors = append(result.Errors, &InvariantError{
						NodeID:      nodeID,
						Description: fmt.Sprintf("branch %d: stored hash %x != child hash %x", branch, storedHash[:8], childHash[:8]),
					})
				}
			}

			// Recursively check child
			childNodeID, err := nodeID.ChildNodeID(uint8(branch))
			if err != nil {
				result.Errors = append(result.Errors, &InvariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("failed to compute child node ID for branch %d", branch),
					Err:         err,
				})
				continue
			}

			sm.checkNodeInvariantsDetailed(child, childNodeID, false, result)
		}
	}

	// Non-root inner nodes must have at least one child
	if !nodeID.IsRoot() && childCount == 0 && !sm.backed {
		result.Errors = append(result.Errors, &InvariantError{
			NodeID:      nodeID,
			Description: "non-root inner node has no children",
		})
	}
}

// VerifyHashes walks the entire tree and verifies all hashes are correct.
// This is a simpler check than full invariants, focusing only on hash integrity.
func (sm *SHAMap) VerifyHashes() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil
	}

	return sm.verifyHashesRecursive(sm.root, NewRootNodeID())
}

// verifyHashesRecursive recursively verifies hashes.
func (sm *SHAMap) verifyHashesRecursive(node Node, nodeID NodeID) error {
	if node == nil {
		return nil
	}

	// Verify this node's hash
	if err := sm.verifyNodeHash(node, nodeID); err != nil {
		return err
	}

	// Recurse into children for inner nodes
	if !node.IsLeaf() {
		inner, ok := node.(*InnerNode)
		if !ok {
			return &InvariantError{
				NodeID:      nodeID,
				Description: "node is not InnerNode",
			}
		}

		for branch := 0; branch < BranchFactor; branch++ {
			child, err := sm.descend(inner, branch)
			if err != nil {
				return err
			}
			if child == nil {
				continue
			}

			childNodeID, err := nodeID.ChildNodeID(uint8(branch))
			if err != nil {
				return err
			}

			if err := sm.verifyHashesRecursive(child, childNodeID); err != nil {
				return err
			}
		}
	}

	return nil
}
