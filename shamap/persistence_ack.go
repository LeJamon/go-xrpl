package shamap

import "context"

// AcknowledgePersistedContext marks the currently loaded tree clean after an
// external ordering barrier has confirmed that every node StoreBatch completed.
// Nodes are cleared post-order so cancellation always leaves a dirty ancestor
// and a later StoreDirty cannot skip an unacknowledged descendant.
func (sm *SHAMap) AcknowledgePersistedContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()

	if err := acknowledgePersistedNode(ctx, sm.tree.root); err != nil {
		return err
	}
	complete, release := sm.publishAcknowledgedFullBelow()
	sm.releaseAcknowledgedFullBelow(release)
	if complete {
		sm.acquisition.cursor = nil
	}
	return nil
}

func (sm *SHAMap) publishAcknowledgedFullBelow() (bool, map[[32]byte]struct{}) {
	cache := sm.backing.fullBelow
	cursor := sm.acquisition.cursor
	root := sm.tree.root
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
	cache := sm.backing.fullBelow
	root := sm.tree.root
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

func acknowledgePersistedNode(ctx context.Context, node mapNode) error {
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
