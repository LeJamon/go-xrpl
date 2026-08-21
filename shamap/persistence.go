package shamap

import "fmt"

// StoreDirty serializes dirty nodes and marks them clean only after store
// succeeds.
func (sm *SHAMap) StoreDirty(store func([]FlushEntry) error) error {
	return sm.storeDirty(store, false)
}

// StoreDirtyAndRelease stores dirty nodes and releases resident child pointers
// only after the store succeeds.
func (sm *SHAMap) StoreDirtyAndRelease(store func([]FlushEntry) error) error {
	return sm.storeDirty(store, true)
}

func (sm *SHAMap) storeDirty(store func([]FlushEntry) error, releaseChildren bool) error {
	if store == nil {
		return fmt.Errorf("shamap: nil dirty-node store")
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	return sm.storeDirtyLocked(store, releaseChildren)
}

// storeDirtyLocked serializes and stores dirty nodes while the caller holds
// the tree write lock.
func (sm *SHAMap) storeDirtyLocked(store func([]FlushEntry) error, releaseChildren bool) error {
	if sm.tree.root == nil || sm.tree.root.IsEmpty() {
		return nil
	}

	var entries []FlushEntry
	var nodes []mapNode
	if err := sm.collectDirtyNode(sm.tree.root, &entries, &nodes); err != nil {
		return fmt.Errorf("failed to flush: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := store(entries); err != nil {
		return err
	}
	// A stored inner node can still reference descendants absent from a
	// partially synced map. Only a complete traversal may publish its hash as
	// full-below.
	for _, node := range nodes {
		node.SetDirty(false)
		if releaseChildren {
			if inner, ok := node.(*innerNode); ok {
				inner.ReleaseChildren()
			}
		}
	}
	return nil
}

func (sm *SHAMap) collectDirtyNode(node mapNode, entries *[]FlushEntry, nodes *[]mapNode) error {
	if node == nil || !node.IsDirty() {
		return nil
	}

	// For inner nodes: flush children first (post-order)
	if inner, ok := node.(*innerNode); ok {
		for i := range BranchFactor {
			child, _, _ := inner.LoadChild(i)
			if child != nil && child.IsDirty() {
				if err := sm.collectDirtyNode(child, entries, nodes); err != nil {
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
	*entries = append(*entries, FlushEntry{
		Hash:      hash,
		Data:      data,
		LedgerSeq: sm.tree.ledgerSeq,
		MapType:   sm.tree.mapType,
	})
	*nodes = append(*nodes, node)

	return nil
}
