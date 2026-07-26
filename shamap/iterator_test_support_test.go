package shamap

import "context"

func newTestIterator(sm *SHAMap) *Iterator {
	it := &Iterator{
		sm:    sm,
		stack: make([]iterStackEntry, 0, MaxDepth),
		ctx:   context.Background(),
	}

	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if sm.tree.root != nil {
		it.stack = append(it.stack, iterStackEntry{
			node:   sm.tree.root,
			nodeID: NewRootNodeID(),
			branch: 0,
		})
	}
	return it
}
