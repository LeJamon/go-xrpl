package shamap

import (
	"context"
	"sync"
	"sync/atomic"
)

// walkMapParallelContext fans out across the root branches and lets each
// bounded worker walk its subtree independently.
// Results share a single slice guarded by a mutex, and exhausting maxMissing
// cancels sibling walks before they continue loading unrelated subtrees.
//
// Modeled on rippled's SHAMap::walkMapParallel (SHAMapDelta.cpp:282).
// One intentional divergence: hash-only branches at root depth 1 that
// the local store cannot satisfy are reported as missing here. Rippled's
// walkMapParallel silently drops them (its top-children capture at
// SHAMapDelta.cpp:290-318 skips any nullptr child without emitting a
// missing entry, which makes its result disagree with rippled's own
// serial walkMap). This Go walker stays consistent with the serial
// walkMap so the two produce the same result set. As in walkMap, backed
// maps lazy-load hash-only branches from the family before declaring
// them missing.
//
// A SHAMap therefore runs at most 16 workers. Store-loaded nodes are retained
// only along incomplete paths needed to attach later peer responses.
func (sm *SHAMap) walkMapParallelContext(ctx context.Context, maxMissing int, filter SyncFilter) ([]MissingNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter == nil {
		filter = &defaultSyncFilter{}
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()

	type subtreeStart struct {
		node     *innerNode
		nodeID   NodeID
		nodeHash [32]byte
	}

	sm.tree.mu.RLock()
	sm.backing.mu.RLock()
	if sm.tree.root == nil || sm.tree.state == stateInvalid {
		sm.backing.mu.RUnlock()
		sm.tree.mu.RUnlock()
		return nil, nil
	}
	root := sm.tree.root
	access := sm.backing.access
	cache := sm.backing.fullBelow
	sm.tree.mu.RUnlock()
	defer sm.backing.mu.RUnlock()

	gen := uint32(0)
	done := func() {}
	if cache != nil {
		gen, done = cache.Begin()
	}
	defer done()
	backed := access.available() && cache != nil
	rootID := newRootNodeID()
	rootHash := root.Hash()
	if backed {
		return sm.walkBackedContext(ctx, root, access, cache, gen, maxMissing, filter)
	}

	if root.isFullBelow(gen) {
		return nil, nil
	}

	var (
		mu           sync.Mutex
		missing      []MissingNode
		stopped      bool
		stopWalk     atomic.Bool
		rootComplete = true
		subtrees     = make([]subtreeStart, 0, BranchFactor)
	)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reportLocked := func(m MissingNode) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return true
		}
		missing = append(missing, m)
		if maxMissing > 0 && len(missing) >= maxMissing {
			stopped = true
			stopWalk.Store(true)
			return true
		}
		return false
	}

	for branch := range BranchFactor {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if stopWalk.Load() {
			rootComplete = false
			break
		}
		child, childHash, isSet := root.LoadChild(branch)
		if !isSet {
			continue
		}
		childNodeID, err := rootID.childNodeID(uint8(branch))
		if err != nil {
			continue
		}
		if child == nil {
			rootComplete = false
			if filter.ShouldFetch(childHash) {
				if reportLocked(MissingNode{
					Hash:       childHash,
					Depth:      1,
					ParentHash: rootHash,
					Branch:     branch,
					NodeID:     childNodeID,
				}) {
					break
				}
			}
			continue
		}
		inner, ok := child.(*innerNode)
		if !ok {
			continue
		}
		subtrees = append(subtrees, subtreeStart{
			node:     inner,
			nodeID:   childNodeID,
			nodeHash: childHash,
		})
	}

	if len(subtrees) == 0 {
		// No inner subtrees to fan out. Mark the root complete when every
		// depth-1 branch was resident-and-full-below and nothing stopped.
		mu.Lock()
		markRoot := rootComplete && !stopped
		mu.Unlock()
		if markRoot {
			markRootFullBelow(root, gen)
		}
		return missing, ctx.Err()
	}

	subFull := make([]bool, len(subtrees))
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(subtrees))
	for i, s := range subtrees {
		go func() {
			defer wg.Done()
			result, walkErr := walkFullBelowStateAccess(
				ctx, sm, nil, s.node, s.nodeID, s.nodeHash, 1, gen, filter, false, cache, false, false, reportLocked,
				func() bool { return stopWalk.Load() || ctx.Err() != nil },
			)
			subFull[i] = result.full
			if result.stopped {
				stopWalk.Store(true)
			}
			if walkErr != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = walkErr
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	for i := range subtrees {
		if !subFull[i] {
			rootComplete = false
		}
	}

	// Mark the root full-below only when every branch — those handled
	// inline and every fanned-out subtree — came back complete and no
	// worker was cut short by the missing cap.
	allSub := true
	for _, f := range subFull {
		if !f {
			allSub = false
			break
		}
	}
	mu.Lock()
	markRoot := rootComplete && allSub && !stopped && !stopWalk.Load()
	mu.Unlock()
	if markRoot {
		markRootFullBelow(root, gen)
	}

	return missing, nil
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

// GetMissingNodesContext is GetMissingNodes with cancellation propagated to
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
// the strict completeness checks (FinishSync, IsComplete). strict=true
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
