package shamap

import "context"

func (sm *SHAMap) walkMap(maxMissing int, filter SyncFilter) []MissingNode {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.state == stateInvalid {
		return nil
	}
	missing, _ := sm.missingNodesLocked(maxMissing, filter, false)
	return missing
}

func (sm *SHAMap) walkMapParallel(maxMissing int, filter SyncFilter) []MissingNode {
	missing, _ := sm.walkMapParallelContext(context.Background(), maxMissing, filter)
	return missing
}
