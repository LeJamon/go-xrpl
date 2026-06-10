package shamap

import (
	"bytes"
	"fmt"
)

// invariantError describes a single invariant violation found during a check.
type invariantError struct {
	NodeID      NodeID
	Description string
	Err         error
}

// Error implements the error interface.
func (e *invariantError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("invariant violation at %s: %s: %v", e.NodeID.String(), e.Description, e.Err)
	}
	return fmt.Sprintf("invariant violation at %s: %s", e.NodeID.String(), e.Description)
}

// Unwrap returns the underlying error.
func (e *invariantError) Unwrap() error {
	return e.Err
}

// invariantCheckResult contains the results of a detailed invariant check.
type invariantCheckResult struct {
	Errors            []*invariantError
	NodesChecked      int
	LeavesChecked     int
	InnerNodesChecked int
}

// HasErrors returns true if any invariant violations were found.
func (r *invariantCheckResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// String returns a summary of the invariant check results.
func (r *invariantCheckResult) String() string {
	if r.HasErrors() {
		return fmt.Sprintf("InvariantCheck: FAILED - %d errors found (%d nodes checked: %d inner, %d leaves)",
			len(r.Errors), r.NodesChecked, r.InnerNodesChecked, r.LeavesChecked)
	}
	return fmt.Sprintf("InvariantCheck: PASSED (%d nodes checked: %d inner, %d leaves)",
		r.NodesChecked, r.InnerNodesChecked, r.LeavesChecked)
}

// invariants performs a comprehensive consistency check on the SHAMap.
// It verifies:
//   - All node hashes are computed correctly
//   - All child references are consistent (hash matches actual child)
//   - No empty non-root inner nodes exist
//   - All leaf nodes have valid items
//   - Tree structure is valid (no cycles, proper depth)
//
// Returns an error describing the first inconsistency found, or nil if valid.
func (sm *SHAMap) invariants() error {
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
		return fmt.Errorf("%w: invalid state with nil root", ErrInvalidState)
	}

	var firstErr error
	sm.walkInvariantsUnsafe(sm.root, NewRootNodeID(), true, false, nil, func(e *invariantError) bool {
		firstErr = e
		return false
	})
	return firstErr
}

// invariantsDetailed performs a comprehensive invariant check and returns
// detailed results. Unlike invariants(), this continues checking even after
// finding errors.
func (sm *SHAMap) invariantsDetailed() *invariantCheckResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := &invariantCheckResult{
		Errors: make([]*invariantError, 0),
	}

	if sm.root == nil {
		return result
	}

	sm.walkInvariantsUnsafe(sm.root, NewRootNodeID(), true, false, result, func(e *invariantError) bool {
		result.Errors = append(result.Errors, e)
		return true
	})
	return result
}

// verifyHashes walks the entire tree and verifies all hashes are correct.
// This is a simpler check than full invariants, focusing only on hash integrity.
func (sm *SHAMap) verifyHashes() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil
	}

	var firstErr error
	sm.walkInvariantsUnsafe(sm.root, NewRootNodeID(), true, true, nil, func(e *invariantError) bool {
		firstErr = e
		return false
	})
	return firstErr
}

// walkInvariantsUnsafe is the single recursive invariants walk shared by
// invariants (stop on first violation), invariantsDetailed (collect all)
// and verifyHashes (hashOnly: hash integrity checks only). report receives
// every violation and returns false to stop the walk; res, when non-nil,
// accumulates node counters. The walk returns false once stopped.
// Caller must hold the read lock.
func (sm *SHAMap) walkInvariantsUnsafe(node Node, nodeID NodeID, isRoot, hashOnly bool, res *invariantCheckResult, report func(*invariantError) bool) bool {
	if node == nil {
		return true
	}

	if res != nil {
		res.NodesChecked++
	}

	// Check depth limit; a too-deep subtree is not descended into.
	if nodeID.Depth() > MaxDepth {
		return report(&invariantError{
			NodeID:      nodeID,
			Description: fmt.Sprintf("depth %d exceeds maximum %d", nodeID.Depth(), MaxDepth),
		})
	}

	// Check node-specific invariants
	if !hashOnly {
		if err := node.Invariants(isRoot); err != nil {
			if !report(&invariantError{
				NodeID:      nodeID,
				Description: "node invariants check failed",
				Err:         err,
			}) {
				return false
			}
		}
	}

	// Verify hash is correctly computed
	if invErr := sm.verifyNodeHash(node, nodeID); invErr != nil {
		if !report(invErr) {
			return false
		}
	}

	inner, isInner := node.(*innerNode)
	if !isInner {
		if res != nil {
			res.LeavesChecked++
		}
		if !hashOnly {
			if invErr := checkLeafNodeInvariants(node, nodeID); invErr != nil {
				if !report(invErr) {
					return false
				}
			}
		}
		return true
	}

	if res != nil {
		res.InnerNodesChecked++
	}

	childCount := 0
	for branch := range BranchFactor {
		hasChild := !inner.IsEmptyBranch(branch)
		child, err := sm.descend(inner, branch)
		if err != nil {
			if !report(&invariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("failed to get child at branch %d", branch),
				Err:         err,
			}) {
				return false
			}
			continue
		}

		// Verify bitmap matches actual children. A set bit with a nil child
		// is legal while syncing and for backed maps (hash-only branches).
		if !hashOnly {
			if hasChild && child == nil && sm.state != StateSyncing && !sm.backed {
				if !report(&invariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("branch %d marked as non-empty but child is nil", branch),
				}) {
					return false
				}
			}
			if !hasChild && child != nil {
				if !report(&invariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("branch %d marked as empty but child exists", branch),
				}) {
					return false
				}
			}
		}

		if child == nil {
			continue
		}
		childCount++

		// Verify stored hash matches child's actual hash
		if !hashOnly {
			storedHash, err := inner.ChildHash(branch)
			if err != nil {
				if !report(&invariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("failed to get stored hash for branch %d", branch),
					Err:         err,
				}) {
					return false
				}
			} else if childHash := child.Hash(); !bytes.Equal(storedHash[:], childHash[:]) {
				if !report(&invariantError{
					NodeID:      nodeID,
					Description: fmt.Sprintf("branch %d: stored hash %x != child hash %x", branch, storedHash[:8], childHash[:8]),
				}) {
					return false
				}
			}
		}

		// Recursively check child
		childNodeID, err := nodeID.ChildNodeID(uint8(branch))
		if err != nil {
			if !report(&invariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("failed to compute child node ID for branch %d", branch),
				Err:         err,
			}) {
				return false
			}
			continue
		}

		if !sm.walkInvariantsUnsafe(child, childNodeID, false, hashOnly, res, report) {
			return false
		}
	}

	// Non-root inner nodes must have at least one child.
	// For backed maps, hash-only branches may legitimately have no
	// in-memory children.
	if !hashOnly && !nodeID.IsRoot() && childCount == 0 && !sm.backed {
		if !report(&invariantError{
			NodeID:      nodeID,
			Description: "non-root inner node has no children",
		}) {
			return false
		}
	}

	return true
}

// verifyNodeHash verifies that a node's hash is correctly computed.
func (sm *SHAMap) verifyNodeHash(node Node, nodeID NodeID) *invariantError {
	// Clone the node and recompute its hash
	cloned, err := node.Clone()
	if err != nil {
		return &invariantError{
			NodeID:      nodeID,
			Description: "failed to clone node for hash verification",
			Err:         err,
		}
	}

	if err := cloned.UpdateHash(); err != nil {
		return &invariantError{
			NodeID:      nodeID,
			Description: "failed to recompute hash",
			Err:         err,
		}
	}

	originalHash := node.Hash()
	recomputedHash := cloned.Hash()

	if !bytes.Equal(originalHash[:], recomputedHash[:]) {
		return &invariantError{
			NodeID:      nodeID,
			Description: fmt.Sprintf("hash mismatch: stored %x, computed %x", originalHash[:8], recomputedHash[:8]),
		}
	}

	// Clone()+UpdateHash() recomputes from live children and so cannot detect a
	// stale cached preimage: hashes[i] disagreeing with children[i].Hash() is
	// invisible to the in-memory hash. childPreimageHash keeps serialization
	// from emitting it, but the cache must still be reconciled before
	// ReleaseChildren drops the live child (see updateHashDeep), so fail loud
	// on the divergence here.
	if inner, ok := node.(*innerNode); ok {
		inner.mu.RLock()
		branch, cached, live, stale := inner.firstStalePreimage()
		inner.mu.RUnlock()
		if stale {
			return &invariantError{
				NodeID:      nodeID,
				Description: fmt.Sprintf("branch %d stale preimage: cached %x, child %x", branch, cached[:8], live[:8]),
			}
		}
	}

	return nil
}

// checkLeafNodeInvariants checks invariants specific to leaf nodes.
func checkLeafNodeInvariants(node Node, nodeID NodeID) *invariantError {
	leaf, ok := node.(LeafNode)
	if !ok {
		return &invariantError{
			NodeID:      nodeID,
			Description: "non-inner node doesn't implement LeafNode",
		}
	}

	item := leaf.Item()
	if item == nil {
		return &invariantError{
			NodeID:      nodeID,
			Description: "leaf node has nil item",
		}
	}

	// Validate the item
	if err := item.Validate(); err != nil {
		return &invariantError{
			NodeID:      nodeID,
			Description: "leaf item validation failed",
			Err:         err,
		}
	}

	return nil
}
