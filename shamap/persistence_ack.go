package shamap

import "context"

// AcknowledgePersistedContext marks the currently loaded tree clean after an
// external ordering barrier has confirmed that every node StoreBatch completed.
// Nodes are cleared post-order so cancellation always leaves a dirty ancestor
// and a later StoreDirty cannot skip an unacknowledged descendant.
func (sm *SHAMap) AcknowledgePersistedContext(ctx context.Context) error {
	return sm.acknowledgePersistedContext(ctx, false)
}

// AcknowledgePersistedAndReleaseContext also drops acknowledged acquisition
// nodes whose subtrees are incomplete. Their hashes and cursor positions are
// retained; subsequent attachment hydrates ancestors from durable storage.
func (sm *SHAMap) AcknowledgePersistedAndReleaseContext(ctx context.Context) error {
	return sm.acknowledgePersistedContext(ctx, true)
}

func (sm *SHAMap) acknowledgePersistedContext(ctx context.Context, releaseNodes bool) error {
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
	if cursor := sm.acquisition.cursor; !releaseNodes && cursor != nil && cursor.budget != nil {
		entries := uint64(BranchFactor + 1)
		for i := range cursor.lanes {
			entries += uint64(cursor.lanes[i].proofs.count() + cursor.lanes[i].retryProofs.count())
		}
		if entries > cursor.budget.limit/128 || !cursor.budget.TryReserve(entries*128) {
			releaseNodes = true
		} else {
			defer cursor.budget.Release(entries * 128)
		}
	}
	complete, release := sm.publishAcknowledgedFullBelow(!releaseNodes)
	if !releaseNodes {
		sm.releaseAcknowledgedFullBelow(release)
	}
	if releaseNodes || complete {
		sm.clearBackedWalkNodeReferencesLocked()
		if sm.tree.root != nil {
			sm.tree.root.ReleaseChildren()
		}
		sm.releaseAcquisitionNodesLocked()
	}
	if complete {
		sm.releaseBackedWalkCursorLocked()
	} else {
		sm.updateCursorCapacityStats(sm.acquisition.cursor)
	}
	return nil
}

func (sm *SHAMap) publishAcknowledgedFullBelow(collectRelease bool) (bool, map[[32]byte]struct{}) {
	cache := sm.backing.fullBelow
	cursor := sm.acquisition.cursor
	root := sm.tree.root
	if cache == nil || root == nil {
		return false, nil
	}
	rootHash := root.Hash()

	gen, done := cache.Begin()
	defer done()
	if cursor == nil {
		return cache.Has(gen, rootHash), nil
	}
	if cursor.rootHash != rootHash || cursor.generation != gen {
		return false, nil
	}

	var release map[[32]byte]struct{}
	if collectRelease {
		release = make(map[[32]byte]struct{})
	}
	complete := true
	for i := range BranchFactor {
		lane := &cursor.lanes[i]
		for _, proofs := range []*backedWalkProofs{&lane.proofs, &lane.retryProofs} {
			for _, chunk := range proofs.chunks {
				for _, proof := range chunk {
					if release != nil {
						release[proof.hash] = struct{}{}
					}
					cacheFullBelow(cache, gen, proof.hash, int(proof.depth))
				}
			}
			proofs.clearWithRelease(func(bytes uint64) { sm.releaseCursorBytes(cursor, bytes) })
		}
		for _, stack := range [][]backedWalkFrame{lane.stack, lane.retryStack} {
			for j := range stack {
				stack[j].proofStart = 0
			}
		}
		if !lane.complete {
			complete = false
			continue
		}
		lane.durable = true
		if lane.root.hash != ([32]byte{}) {
			if release != nil {
				release[lane.root.hash] = struct{}{}
			}
			cacheFullBelow(cache, gen, lane.root.hash, lane.root.depth)
		}
		lane.root.node = nil
		lane.root.parent = nil
	}
	if complete {
		root.setFullBelowGen(gen)
		if release != nil {
			release[rootHash] = struct{}{}
		}
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
