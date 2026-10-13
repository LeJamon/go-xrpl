package shamap

import "context"

func (sm *SHAMap) walkMap(maxMissing int, filter SyncFilter) []MissingNode {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.state == stateInvalid || sm.tree.root == nil {
		return nil
	}
	if filter == nil {
		filter = &defaultSyncFilter{}
	}

	var missing []MissingNode
	_, _ = walkSubtreeForMissing(
		context.Background(), sm,
		sm.tree.root,
		newRootNodeID(),
		sm.tree.root.Hash(),
		0,
		filter,
		false,
		func(m MissingNode) bool {
			missing = append(missing, m)
			return maxMissing > 0 && len(missing) >= maxMissing
		},
	)
	return missing
}

func (sm *SHAMap) walkMapParallel(maxMissing int, filter SyncFilter) []MissingNode {
	missing, _ := sm.walkMapParallelContext(context.Background(), maxMissing, filter)
	return missing
}
