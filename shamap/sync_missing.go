package shamap

import (
	"context"
)

// walkMapParallelContext is the legacy slice-returning view of the bounded
// scanner. Keep its historical error contract for callers that still use the
// internal helper: traversal and admission yields are returned as errors with
// any useful IDs preserved in the result.
func (sm *SHAMap) walkMapParallelContext(ctx context.Context, maxMissing int, filter SyncFilter) ([]MissingNode, error) {
	result, err := sm.ScanMissingNodesContext(ctx, maxMissing, filter)
	if err != nil {
		return result.Nodes, err
	}
	switch result.Status {
	case MissingNodesMore:
		if result.reason == missingNodesTraversalYield {
			return result.Nodes, ErrTraversalBudget
		}
	case MissingNodesMemory:
		return result.Nodes, ErrAcquisitionMemory
	}
	return result.Nodes, nil
}

// acquireBackedWalkGeneration admits one bounded walk against a stable
// durable generation. The durable snapshot is acquired before the cache read
// admission, matching the destructive mutation lock order. A cache generation
// with no matching durable fingerprint is bound while both are held; a stale
// binding is reset once and admitted again so a resumable cursor keeps its
// generation across slices.
func (sm *SHAMap) acquireBackedWalkGeneration(
	ctx context.Context,
	access *familyAccess,
	cache *FullBelowCache,
	backed bool,
) (uint32, func(), error) {
	if cache == nil {
		return 0, func() {}, nil
	}
	if !backed {
		generation, releaseCache := cache.Begin()
		return generation, releaseCache, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		fingerprint, releaseDurable, durablePinned, err := access.acquireDurableSnapshot(ctx)
		if err != nil {
			return 0, nil, err
		}
		if !durablePinned {
			generation, releaseCache := cache.Begin()
			return generation, releaseCache, nil
		}
		generation, releaseCache := cache.Begin()
		boundFingerprint, bound := cache.DurableFingerprint(generation)
		if bound && boundFingerprint == fingerprint {
			return generation, func() {
				releaseDurable()
				releaseCache()
			}, nil
		}
		releaseCache()
		cache.EnsureDurableFingerprint(fingerprint)
		generation, releaseCache = cache.Begin()
		boundFingerprint, bound = cache.DurableFingerprint(generation)
		if bound && boundFingerprint == fingerprint {
			return generation, func() {
				releaseDurable()
				releaseCache()
			}, nil
		}
		releaseCache()
		releaseDurable()
	}
}

// markRootFullBelow records the root as full-below at gen.
func markRootFullBelow(root *innerNode, gen uint32) {
	root.setFullBelowGen(gen)
}

// GetMissingNodes returns the nodes referenced by the tree but not
// present locally. It is gated on the syncing state — for any other state
// the map is assumed complete and the result is nil.
//
// The actual walk is performed by walkMapParallel so the per-root-branch
// fan-out is shared with the lower-level walkMap API. maxNodes == 0 is
// unbounded; a nil filter accepts every missing node.
func (sm *SHAMap) GetMissingNodes(maxNodes int, filter SyncFilter) []MissingNode {
	missing, _ := sm.GetMissingNodesContext(context.Background(), maxNodes, filter)
	return missing
}

// GetMissingNodesContext may return valid partial results with ErrTraversalBudget
// or ErrAcquisitionMemory; neither error means the frontier is exhausted.
// It is GetMissingNodes with cancellation propagated to
// the parallel traversal and backing Family.
func (sm *SHAMap) GetMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter) ([]MissingNode, error) {
	sm.tree.mu.RLock()
	state := sm.tree.state
	sm.tree.mu.RUnlock()
	if state != stateSyncing {
		return nil, nil
	}
	return sm.walkMapParallelContext(ctx, maxNodes, filter)
}

// getMissingNodesUnsafe collects up to maxNodes missing-node references
// missingNodesLocked is the shared walk behind the lenient request path and
// the strict completeness checks (FinishSync, CheckComplete). strict=true
// aborts on a transient store error instead of reporting phantom missing
// nodes. Caller must hold at least the read lock.
func (sm *SHAMap) missingNodesLocked(maxNodes int, filter SyncFilter, strict bool) ([]MissingNode, error) {
	if filter == nil {
		filter = &defaultSyncFilter{}
	}
	if sm.tree.root == nil {
		return nil, nil
	}

	var missing []MissingNode
	_, err := walkSubtreeForMissing(
		context.Background(), sm,
		sm.tree.root,
		newRootNodeID(),
		sm.tree.root.Hash(),
		0,
		filter,
		strict,
		func(m MissingNode) bool {
			missing = append(missing, m)
			return maxNodes > 0 && len(missing) >= maxNodes
		},
	)
	if err != nil {
		return nil, err
	}
	return missing, nil
}
