package shamap

import "context"

// Size returns the number of leaf items in the SHAMap.
// O(1) on immutable maps (memoised after the first call), O(n) on mutable.
func (sm *SHAMap) Size() int {
	if n := sm.tree.cachedSize.Load(); n >= 0 {
		return int(n)
	}

	sm.tree.mu.RLock()
	count := 0
	err := sm.walkLeavesUnsafe(context.Background(), sm.tree.root, func(*Item) bool {
		count++
		return true
	})
	isImmutable := sm.tree.state == stateImmutable
	sm.tree.mu.RUnlock()

	// Never cache a partial count: descend() can fail mid-walk on a backed
	// map, and a poisoned cache would persist for the map's lifetime.
	if isImmutable && err == nil {
		sm.tree.cachedSize.Store(int64(count))
	}
	return count
}

// ForEach calls fn for every item in the tree.
// If fn returns false, iteration stops early.
// Equivalent to ForEachCtx(context.Background(), fn).
func (sm *SHAMap) ForEach(fn func(*Item) bool) error {
	return sm.ForEachCtx(context.Background(), fn)
}

// ForEachCtx is the context-aware variant of ForEach: iteration aborts
// with ctx.Err() whenever the context is cancelled. The check fires
// before each child descend so a long-running scan can be interrupted
// even when leaf callbacks return true.
func (sm *SHAMap) ForEachCtx(ctx context.Context, fn func(*Item) bool) error {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	return sm.walkLeavesUnsafe(ctx, sm.tree.root, fn)
}
