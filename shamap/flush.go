package shamap

import (
	"context"
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
	if err := store(batch.Entries); err != nil {
		return err
	}
	// A stored inner node can still reference descendants absent from a
	// partially synced map. Only a complete traversal may publish its hash as
	// full-below.
	for _, node := range nodes {
		node.SetDirty(false)
	}
	return nil
}

// AcknowledgePersistedContext marks the currently loaded tree clean after an
// external ordering barrier has confirmed that every node StoreBatch completed.
// Nodes are cleared post-order so cancellation always leaves a dirty ancestor
// and a later StoreDirty cannot skip an unacknowledged descendant.
func (sm *SHAMap) AcknowledgePersistedContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sm.walkMu.Lock()
	defer sm.walkMu.Unlock()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.familyMu.RLock()
	defer sm.familyMu.RUnlock()

	if err := acknowledgePersistedNode(ctx, sm.root); err != nil {
		return err
	}
	complete, release := sm.publishAcknowledgedFullBelow()
	sm.releaseAcknowledgedFullBelow(release)
	if complete {
		sm.backedWalk = nil
	}
	return nil
}

func (sm *SHAMap) publishAcknowledgedFullBelow() (bool, map[[32]byte]struct{}) {
	cache := sm.fullBelow
	cursor := sm.backedWalk
	root := sm.root
	if cache == nil || cursor == nil || root == nil {
		return false, nil
	}
	rootHash := root.Hash()
	if cursor.rootHash != rootHash {
		return false, nil
	}

	gen, done := cache.Begin()
	defer done()
	if cursor.generation != gen {
		return false, nil
	}

	release := make(map[[32]byte]struct{})
	complete := true
	for i := range BranchFactor {
		lane := &cursor.lanes[i]
		for _, chunk := range lane.proofs.chunks {
			for _, proof := range chunk {
				release[proof.hash] = struct{}{}
				cacheFullBelow(cache, gen, proof.hash, int(proof.depth))
			}
		}
		lane.proofs.clear()
		if !lane.complete {
			complete = false
			continue
		}
		lane.durable = true
		if lane.root.hash != ([32]byte{}) {
			release[lane.root.hash] = struct{}{}
			cacheFullBelow(cache, gen, lane.root.hash, lane.root.depth)
		}
		lane.root.node = nil
		lane.root.parent = nil
	}
	if complete {
		root.setFullBelowGen(gen)
		release[rootHash] = struct{}{}
		cacheFullBelow(cache, gen, rootHash, 0)
	}
	return complete, release
}

func (sm *SHAMap) releaseAcknowledgedFullBelow(release map[[32]byte]struct{}) {
	cache := sm.fullBelow
	root := sm.root
	if cache == nil || root == nil {
		return
	}
	gen, done := cache.Begin()
	defer done()
	rootHash := root.Hash()
	if _, ok := release[rootHash]; ok || cache.Has(gen, rootHash) {
		root.ReleaseChildren()
		return
	}
	sm.releaseAcknowledgedChildren(root, cache, gen, release)
}

func (sm *SHAMap) releaseAcknowledgedChildren(
	parent *innerNode,
	cache *FullBelowCache,
	generation uint32,
	release map[[32]byte]struct{},
) {
	for branch := range BranchFactor {
		child, hash, set := parent.LoadChild(branch)
		if !set || child == nil {
			continue
		}
		if _, ok := release[hash]; ok || cache.Has(generation, hash) {
			sm.releaseChild(parent, branch, child)
			continue
		}
		if inner, ok := child.(*innerNode); ok {
			sm.releaseAcknowledgedChildren(inner, cache, generation, release)
		}
	}
}

func acknowledgePersistedNode(ctx context.Context, node Node) error {
	if node == nil || !node.IsDirty() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if inner, ok := node.(*innerNode); ok {
		for branch := range BranchFactor {
			child, _, _ := inner.LoadChild(branch)
			if err := acknowledgePersistedNode(ctx, child); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	node.SetDirty(false)
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
