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
	if cursor := sm.acquisition.scan; !releaseNodes && cursor != nil && cursor.budget != nil {
		entries := uint64(BranchFactor + 1)
		if entries > cursor.budget.limit/128 || !cursor.budget.TryReserve(entries*128) {
			releaseNodes = true
		} else {
			defer cursor.budget.Release(entries * 128)
		}
	}
	if cursor := sm.acquisition.scan; cursor != nil && sm.tree.root != nil && !sm.tree.root.IsDirty() {
		cursor.rootDurable = true
	}
	complete, _ := sm.publishAcknowledgedScanHints(false)
	if !complete && sm.acquisition.scan == nil && sm.tree.root != nil && sm.backing.fullBelow != nil {
		gen, done := sm.backing.fullBelow.Begin()
		complete = sm.backing.fullBelow.Has(gen, sm.tree.root.Hash())
		done()
	}
	if releaseNodes || complete {
		sm.clearBackedScanNodeReferencesLocked()
		if sm.tree.root != nil {
			sm.tree.root.ReleaseChildren()
		}
		sm.releaseAcquisitionNodesLocked()
	}
	if complete {
		sm.releaseBackedScanCursorLocked()
	} else {
		sm.updateBackedScanStats(sm.acquisition.scan)
	}
	return nil
}

// publishAcknowledgedScanHints publishes at most one maximal completion hint
// per lane. A hint is only an optimization: if it cannot be retained, the
// next scan can prove the same subtree again from durable storage.
func (sm *SHAMap) publishAcknowledgedScanHints(collectRelease bool) (bool, map[[32]byte]struct{}) {
	cache := sm.backing.fullBelow
	cursor := sm.acquisition.scan
	root := sm.tree.root
	if cache == nil || cursor == nil || root == nil {
		return false, nil
	}
	gen, done := cache.Begin()
	defer done()
	rootHash := root.Hash()
	if cursor.rootHash != rootHash || cursor.generation != gen {
		return false, nil
	}
	var release map[[32]byte]struct{}
	if collectRelease {
		release = make(map[[32]byte]struct{})
	}
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		if !lane.hint.pending {
			continue
		}
		cache.Insert(gen, lane.hint.hash)
		if release != nil {
			release[lane.hint.hash] = struct{}{}
		}
		lane.hint.pending = false
		lane.durable = true
		lane.root.node = nil
		lane.root.parent = nil
	}
	complete := true
	for i := range cursor.lanes {
		if !cursor.lanes[i].complete || !cursor.lanes[i].durable {
			complete = false
			break
		}
	}
	if !complete || !cursor.rootDurable {
		return false, release
	}
	root.setFullBelowGen(gen)
	cache.Insert(gen, rootHash)
	if release != nil {
		release[rootHash] = struct{}{}
	}
	return true, release
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
