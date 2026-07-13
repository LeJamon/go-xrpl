package shamap

import (
	"fmt"
)

// StoreDirty serializes dirty nodes and marks them clean only after store
// succeeds.
func (sm *SHAMap) StoreDirty(store func([]FlushEntry) error) error {
	if store == nil {
		return fmt.Errorf("shamap: nil dirty-node store")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.root == nil || sm.root.IsEmpty() {
		return nil
	}

	batch := &NodeBatch{}
	var nodes []Node
	if err := sm.collectDirtyNode(sm.root, batch, &nodes); err != nil {
		return fmt.Errorf("failed to flush: %w", err)
	}
	if len(batch.Entries) == 0 {
		return nil
	}
	var (
		gen  uint32
		done = func() {}
	)
	if sm.backed && sm.fullBelow != nil {
		gen, done = sm.fullBelow.Begin()
	}
	defer done()
	if err := store(batch.Entries); err != nil {
		return err
	}
	if sm.backed && sm.fullBelow != nil {
		for _, node := range nodes {
			if _, ok := node.(*innerNode); ok {
				sm.fullBelow.Insert(gen, node.Hash())
			}
		}
	}
	for _, node := range nodes {
		node.SetDirty(false)
	}
	return nil
}

// FlushDirty serializes every dirty node into the returned NodeBatch and marks
// them clean, retaining all in-memory child pointers.
func (sm *SHAMap) FlushDirty() (*NodeBatch, error) {
	return sm.flushDirty(false)
}

// FlushDirtyAndRelease is FlushDirty that additionally releases inner nodes'
// child pointers after flushing (retaining only hashes) so the GC can reclaim
// memory; children are lazily reloaded from the NodeStore on next access.
func (sm *SHAMap) FlushDirtyAndRelease() (*NodeBatch, error) {
	return sm.flushDirty(true)
}

// flushDirty performs a post-order traversal of the tree, collecting all dirty
// nodes. Each dirty node is serialized and added to the returned NodeBatch.
// After serialization, nodes are marked clean (dirty=false).
func (sm *SHAMap) flushDirty(releaseChildren bool) (*NodeBatch, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.root == nil || sm.root.IsEmpty() {
		return &NodeBatch{}, nil
	}

	batch := &NodeBatch{}
	var nodes []Node
	if err := sm.collectDirtyNode(sm.root, batch, &nodes); err != nil {
		return nil, fmt.Errorf("failed to flush: %w", err)
	}
	for _, node := range nodes {
		node.SetDirty(false)
		if releaseChildren {
			if inner, ok := node.(*innerNode); ok {
				inner.ReleaseChildren()
			}
		}
	}

	return batch, nil
}

func (sm *SHAMap) collectDirtyNode(node Node, batch *NodeBatch, nodes *[]Node) error {
	if node == nil || !node.IsDirty() {
		return nil
	}

	// For inner nodes: flush children first (post-order)
	if inner, ok := node.(*innerNode); ok {
		for i := range BranchFactor {
			child, _, _ := inner.LoadChild(i)
			if child != nil && child.IsDirty() {
				if err := sm.collectDirtyNode(child, batch, nodes); err != nil {
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
		Hash:      hash,
		Data:      data,
		LedgerSeq: sm.ledgerSeq,
		MapType:   sm.mapType,
	})
	*nodes = append(*nodes, node)

	return nil
}
