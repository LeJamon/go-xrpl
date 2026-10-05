package shamap

import (
	"context"
)

// walkMapParallelContext is the legacy slice-returning view of the bounded
// scanner. It collects bounded scanner slices until the requested legacy
// result window is full or discovery reaches a stable status. A non-positive
// maxMissing preserves the historical unbounded result window. Traversal and
// admission yields retain the old error contract, with useful IDs preserved.
func (sm *SHAMap) walkMapParallelContext(ctx context.Context, maxMissing int, filter SyncFilter) ([]MissingNode, error) {
	unlimited := maxMissing <= 0
	var missing []MissingNode
	restarted := false
	for {
		if !unlimited && len(missing) >= maxMissing {
			return missing[:maxMissing], nil
		}
		limit := backedScanDefaultResultLimit
		if !unlimited && maxMissing-len(missing) < limit {
			limit = maxMissing - len(missing)
		}
		result, err := sm.ScanMissingNodesContext(ctx, limit, filter)
		missing = append(missing, result.Nodes...)
		if err != nil {
			return missing, err
		}
		switch result.Status {
		case MissingNodesMore:
			if result.reason == missingNodesTraversalYield {
				return missing, ErrTraversalBudget
			}
			continue
		case MissingNodesWaiting:
			if len(missing) == 0 && !restarted {
				restarted = true
				continue
			}
			return missing, nil
		case MissingNodesMemory:
			return missing, ErrAcquisitionMemory
		default:
			return missing, nil
		}
	}
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

// GetMissingNodes returns the nodes referenced by the tree but not
// present locally. It is gated on the syncing state — for any other state
// the map is assumed complete and the result is nil.
//
// The bounded scanner may use several slices internally; this compatibility
// wrapper collects them into the requested result window. maxNodes == 0 is
// unbounded, preserving the historical API. A nil filter accepts every
// missing node.
func (sm *SHAMap) GetMissingNodes(maxNodes int, filter SyncFilter) []MissingNode {
	missing, _ := sm.GetMissingNodesContext(context.Background(), maxNodes, filter)
	return missing
}

// GetMissingNodesContext may return valid partial results with ErrTraversalBudget
// or ErrAcquisitionMemory; neither error means the frontier is exhausted.
// It is GetMissingNodes with cancellation propagated to the scanner and
// backing Family.
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
