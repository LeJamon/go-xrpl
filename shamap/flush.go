package shamap

import (
	"fmt"
)

// FlushDirty performs a post-order traversal of the tree, collecting all dirty nodes.
// Each dirty node is serialized and added to the returned NodeBatch.
// After serialization, nodes are marked clean (dirty=false).
// If releaseChildren is true, inner nodes release their child pointers after flush
// (retaining only hashes), allowing GC to reclaim memory. Children will be
// lazily reloaded from NodeStore on next access.
func (sm *SHAMap) FlushDirty(releaseChildren bool) (*NodeBatch, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.root == nil {
		return &NodeBatch{}, nil
	}

	batch := &NodeBatch{}

	if err := sm.flushNode(sm.root, releaseChildren, batch); err != nil {
		return nil, fmt.Errorf("failed to flush: %w", err)
	}

	return batch, nil
}

// flushNode recursively flushes a dirty node and its dirty children (post-order).
func (sm *SHAMap) flushNode(node Node, releaseChildren bool, batch *NodeBatch) error {
	if node == nil || !node.IsDirty() {
		return nil
	}

	// For inner nodes: flush children first (post-order)
	if inner, ok := node.(*innerNode); ok {
		for i := range BranchFactor {
			child, _, _ := inner.LoadChild(i)
			if child != nil && child.IsDirty() {
				if err := sm.flushNode(child, releaseChildren, batch); err != nil {
					return err
				}
			}
		}

		// Synchronize the cached preimage with the just-flushed children
		// before serializing, so the flushed bytes hash to the in-memory
		// node hash even if some mutation path left a stale hashes[i].
		// Mirrors rippled's walkSubTree.
		if err := inner.updateHashDeep(); err != nil {
			return fmt.Errorf("failed to update inner node hash: %w", err)
		}
	}

	// Serialize this node
	data, err := node.SerializeWithPrefix()
	if err != nil {
		return fmt.Errorf("failed to serialize node: %w", err)
	}

	hash := node.Hash()
	batch.Entries = append(batch.Entries, FlushEntry{
		Hash: hash,
		Data: data,
	})

	// Mark clean
	node.SetDirty(false)

	// Release children pointers for inner nodes (retain hashes for lazy reload).
	if releaseChildren {
		if inner, ok := node.(*innerNode); ok {
			inner.ReleaseChildren()
		}
	}

	return nil
}
