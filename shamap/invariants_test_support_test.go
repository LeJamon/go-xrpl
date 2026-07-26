package shamap

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

func cloneNode(n mapNode) (mapNode, error) {
	cloneable, ok := n.(interface{ Clone() (mapNode, error) })
	if !ok {
		return nil, fmt.Errorf("%T does not support cloning", n)
	}
	return cloneable.Clone()
}

func checkNodeInvariants(n mapNode, isRoot bool) error {
	checkable, ok := n.(interface{ Invariants(bool) error })
	if !ok {
		return fmt.Errorf("%T does not support invariant checks", n)
	}
	return checkable.Invariants(isRoot)
}

func (n *innerNode) firstStalePreimage() (branch int, cached, live [32]byte, ok bool) {
	for i := range BranchFactor {
		child := n.children[i]
		if child == nil {
			continue
		}
		if live := child.Hash(); live != n.hashes[i] {
			return i, n.hashes[i], live, true
		}
	}
	return 0, [32]byte{}, [32]byte{}, false
}

func (b *baseNode) String(id NodeID) string {
	return fmt.Sprintf("NodeID: %s, Hash: %s", id.String(), hex.EncodeToString(b.hash[:]))
}

func (n *leafNode) SetItem(item *Item) (bool, error) {
	if item == nil {
		return false, ErrNilItem
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	oldHash := n.hash
	n.item = item
	n.SetDirty(true)
	if err := n.updateHashUnsafe(); err != nil {
		return false, fmt.Errorf("failed to update hash: %w", err)
	}
	return n.hash != oldHash, nil
}

func (n *leafNode) Invariants(bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return fmt.Errorf("leaf has nil item")
	}
	if n.IsZeroHash() {
		return fmt.Errorf("leaf has zero hash")
	}
	return nil
}

func (n *leafNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s ID: %s\n", leafKinds[n.kind].label, id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))
	if n.item != nil {
		key := n.item.Key()
		sb.WriteString(fmt.Sprintf("Key: %s\n", hex.EncodeToString(key[:])))
		sb.WriteString(fmt.Sprintf("Data Size: %d bytes\n", n.item.Size()))
	}
	return sb.String()
}

func (n *leafNode) Clone() (mapNode, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return nil, ErrNilItem
	}
	clonedItem, err := n.item.Clone()
	if err != nil {
		return nil, fmt.Errorf("failed to clone item: %w", err)
	}
	return newLeafNode(n.kind, clonedItem)
}

func (n *innerNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("innerNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))
	sb.WriteString("Branches:\n")
	for i := range BranchFactor {
		if n.isBranch&(1<<i) != 0 {
			sb.WriteString(fmt.Sprintf("  %d: %s\n", i, hex.EncodeToString(n.hashes[i][:])))
		}
	}
	return sb.String()
}

func (n *innerNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	count := 0
	for i := range BranchFactor {
		hasChild := n.children[i] != nil
		hasBit := (n.isBranch & (1 << i)) != 0
		hasHash := !isZeroHash(n.hashes[i])
		if hasBit && !hasHash {
			return fmt.Errorf("branch %d: bit set but no hash", i)
		}
		if hasChild && !hasBit {
			return fmt.Errorf("branch %d: child present but bit not set", i)
		}
		if !hasBit && hasChild {
			return fmt.Errorf("branch %d: child present in empty branch", i)
		}
		if hasChild || hasBit {
			count++
		}
	}
	if branch, _, _, stale := n.firstStalePreimage(); stale {
		return fmt.Errorf("branch %d hash mismatch", branch)
	}
	if count == 0 && !isRoot {
		return ErrEmptyNonRoot
	}
	if !n.IsZeroHash() {
		temp := &innerNode{
			isBranch: n.isBranch,
			hashes:   n.hashes,
			children: n.children,
		}
		if err := temp.updateHashUnsafe(); err != nil {
			return fmt.Errorf("failed to verify hash: %w", err)
		}
		if temp.hash != n.hash {
			return fmt.Errorf("stored hash does not match computed hash")
		}
	}
	return nil
}

func (n *innerNode) Clone() (mapNode, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	clone := &innerNode{
		isBranch:     n.isBranch,
		hashes:       n.hashes,
		fullBelowGen: n.fullBelowGen,
	}
	clone.hash = n.hash
	clone.SetDirty(true)
	for i := range BranchFactor {
		if n.children[i] != nil {
			childClone, err := cloneNode(n.children[i])
			if err != nil {
				return nil, fmt.Errorf("failed to clone child at branch %d: %w", i, err)
			}
			clone.children[i] = childClone
		}
	}
	return clone, nil
}

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
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	return sm.invariantsUnsafe()
}

// invariantsUnsafe performs invariant checking without locking.
// Caller must hold the read lock.
func (sm *SHAMap) invariantsUnsafe() error {
	if sm.tree.root == nil {
		if sm.tree.state != stateInvalid {
			return nil // Empty map is valid
		}
		return fmt.Errorf("%w: invalid state with nil root", ErrInvalidState)
	}

	var firstErr error
	sm.walkInvariantsUnsafe(sm.tree.root, NewRootNodeID(), true, false, nil, func(e *invariantError) bool {
		firstErr = e
		return false
	})
	return firstErr
}

// invariantsDetailed performs a comprehensive invariant check and returns
// detailed results. Unlike invariants(), this continues checking even after
// finding errors.
func (sm *SHAMap) invariantsDetailed() *invariantCheckResult {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	result := &invariantCheckResult{
		Errors: make([]*invariantError, 0),
	}

	if sm.tree.root == nil {
		return result
	}

	sm.walkInvariantsUnsafe(sm.tree.root, NewRootNodeID(), true, false, result, func(e *invariantError) bool {
		result.Errors = append(result.Errors, e)
		return true
	})
	return result
}

// verifyHashes walks the entire tree and verifies all hashes are correct.
// This is a simpler check than full invariants, focusing only on hash integrity.
func (sm *SHAMap) verifyHashes() error {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.root == nil {
		return nil
	}

	var firstErr error
	sm.walkInvariantsUnsafe(sm.tree.root, NewRootNodeID(), true, true, nil, func(e *invariantError) bool {
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
func (sm *SHAMap) walkInvariantsUnsafe(node mapNode, nodeID NodeID, isRoot, hashOnly bool, res *invariantCheckResult, report func(*invariantError) bool) bool {
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
		if err := checkNodeInvariants(node, isRoot); err != nil {
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
			if hasChild && child == nil && sm.tree.state != stateSyncing && !sm.backing.access.available() {
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
	if !hashOnly && !nodeID.IsRoot() && childCount == 0 && !sm.backing.access.available() {
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
func (sm *SHAMap) verifyNodeHash(node mapNode, nodeID NodeID) *invariantError {
	// Clone the node and recompute its hash
	cloned, err := cloneNode(node)
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
func checkLeafNodeInvariants(node mapNode, nodeID NodeID) *invariantError {
	leaf, ok := node.(mapLeaf)
	if !ok {
		return &invariantError{
			NodeID:      nodeID,
			Description: "non-inner node doesn't implement leaf",
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
