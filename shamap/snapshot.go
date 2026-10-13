package shamap

import (
	"context"
	"fmt"
)

// SnapshotMutable returns a structurally-shared copy that may be modified.
// See snapshot for the sharing and flushing semantics.
func (sm *SHAMap) SnapshotMutable() (*SHAMap, error) {
	return sm.SnapshotMutableContext(context.Background())
}

// SnapshotMutableContext is SnapshotMutable with caller-controlled
// cancellation for dirty-node persistence.
func (sm *SHAMap) SnapshotMutableContext(ctx context.Context) (*SHAMap, error) {
	return sm.snapshot(ctx, true, nil)
}

// SnapshotImmutable returns a read-only structurally-shared copy.
// See snapshot for the sharing and flushing semantics.
func (sm *SHAMap) SnapshotImmutable() (*SHAMap, error) {
	return sm.SnapshotImmutableContext(context.Background())
}

// SnapshotImmutableContext is SnapshotImmutable with caller-controlled
// cancellation for dirty-node persistence.
func (sm *SHAMap) SnapshotImmutableContext(ctx context.Context) (*SHAMap, error) {
	return sm.snapshot(ctx, false, nil)
}

// SnapshotMutableWithLedgerSeq returns a mutable snapshot stamped with
// ledgerSeq without changing the source map's sequence metadata.
func (sm *SHAMap) SnapshotMutableWithLedgerSeq(ledgerSeq uint32) (*SHAMap, error) {
	return sm.snapshot(context.Background(), true, &ledgerSeq)
}

// SnapshotImmutableWithLedgerSeqContext returns an immutable snapshot stamped
// with ledgerSeq without changing the source map's sequence metadata.
func (sm *SHAMap) SnapshotImmutableWithLedgerSeqContext(ctx context.Context, ledgerSeq uint32) (*SHAMap, error) {
	return sm.snapshot(ctx, false, &ledgerSeq)
}

// snapshot returns a structurally-shared copy of the SHAMap in O(1).
// The source and the returned map share the same root pointer; mutation
// paths in either map are path-copy persistent (dirtyUp shallow-clones
// each touched inner node), so the snapshot's tree is never observed
// being mutated.
//
// For backed maps, dirty nodes present at entry are flushed before the root is
// shared. The tree and backing remain pinned across both operations so a
// concurrent Family replacement cannot bind the clean snapshot to a different
// store.
// Flushing a structurally-shared subtree from either map is safe: the
// dirty flag is atomic and node hashes are read and written under each
// node's own lock.
func (sm *SHAMap) snapshot(ctx context.Context, mutable bool, ledgerSeq *uint32) (*SHAMap, error) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	if ledgerSeq != nil {
		sourceLedgerSeq := sm.tree.ledgerSeq
		sm.tree.ledgerSeq = *ledgerSeq
		defer func() { sm.tree.ledgerSeq = sourceLedgerSeq }()
	}

	if sm.backing.access.available() {
		if err := sm.storeDirtyLocked(func(entries []FlushEntry) error {
			return sm.backing.access.storeBatch(ctx, entries)
		}, false); err != nil {
			return nil, fmt.Errorf("failed to store dirty nodes: %w", err)
		}
	}
	return sm.snapshotLocked(mutable)
}

// MutableFork returns a mutable, structurally shared copy without flushing
// dirty backed nodes. It is intended for short-lived transactional staging;
// path-copy persistence keeps subsequent mutations isolated. Neither map may
// be flushed until the caller has discarded one of them.
func (sm *SHAMap) MutableFork() (*SHAMap, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	return sm.snapshotLocked(true)
}

// snapshotLocked returns a shared snapshot while the caller holds the tree and
// backing read locks (or their write-lock equivalents).
func (sm *SHAMap) snapshotLocked(mutable bool) (*SHAMap, error) {
	if sm.tree.state == stateInvalid {
		return nil, fmt.Errorf("%w: cannot snapshot invalid map", errInvalidState)
	}

	newState := stateImmutable
	if mutable {
		newState = stateModifying
	}

	out := &SHAMap{
		tree: treeState{
			root:      sm.tree.root,
			mapType:   sm.tree.mapType,
			state:     newState,
			ledgerSeq: sm.tree.ledgerSeq,
		},
		backing: backingState{
			access:    sm.backing.access,
			fullBelow: sm.backing.fullBelow,
		},
	}
	if out.backing.access.available() {
		out.acquisition.budget = NewAcquisitionBudget(0)
	}
	out.tree.cachedSize.Store(-1)
	// Immutable→immutable snapshot observes the same leaf set; carry the
	// cached count across so the snapshot is O(1) on first Size() too.
	if !mutable && sm.tree.state == stateImmutable {
		if n := sm.tree.cachedSize.Load(); n >= 0 {
			out.tree.cachedSize.Store(n)
		}
	}
	return out, nil
}
