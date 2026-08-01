package shamap

import (
	"context"
	"errors"
	"fmt"
)

// SnapshotMutable returns a structurally-shared copy that may be modified.
// See snapshot for the sharing and flushing semantics.
func (sm *SHAMap) SnapshotMutable() (*SHAMap, error) {
	return sm.snapshot(true)
}

// SnapshotImmutable returns a read-only structurally-shared copy.
// See snapshot for the sharing and flushing semantics.
func (sm *SHAMap) SnapshotImmutable() (*SHAMap, error) {
	return sm.snapshot(false)
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
func (sm *SHAMap) snapshot(mutable bool) (*SHAMap, error) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()

	if sm.backing.access.available() {
		if err := sm.storeDirtyLocked(func(entries []FlushEntry) error {
			return sm.backing.access.storeBatch(context.Background(), entries)
		}); err != nil {
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

// DetachedMutable returns a mutable copy with an independent in-memory tree.
// Every loaded node and item is deep-cloned, while unloaded backed branches
// remain represented by their stored hashes. The operation does not flush the
// source, and either map may subsequently be mutated or persisted through the
// retained backing independently.
func (sm *SHAMap) DetachedMutable() (*SHAMap, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()

	if sm.tree.state == stateInvalid {
		return nil, fmt.Errorf("%w: cannot detach invalid map", ErrInvalidState)
	}

	root, err := cloneLoadedInner(sm.tree.root)
	if err != nil {
		return nil, fmt.Errorf("clone loaded tree: %w", err)
	}

	out := &SHAMap{
		tree: treeState{
			root:      root,
			mapType:   sm.tree.mapType,
			state:     stateModifying,
			ledgerSeq: sm.tree.ledgerSeq,
			full:      sm.tree.full,
		},
		backing: backingState{
			access:    sm.backing.access,
			fullBelow: sm.backing.fullBelow,
		},
	}
	out.tree.cachedSize.Store(-1)
	return out, nil
}

func cloneLoadedInner(source *innerNode) (*innerNode, error) {
	if source == nil {
		return nil, errors.New("nil inner node")
	}

	source.mu.RLock()
	children := source.children
	clone := &innerNode{
		hashes:       source.hashes,
		isBranch:     source.isBranch,
		fullBelowGen: source.fullBelowGen,
	}
	clone.hash = source.hash
	clone.SetDirty(source.IsDirty())
	source.mu.RUnlock()

	for branch, child := range children {
		if child == nil {
			continue
		}
		childClone, err := cloneLoadedNode(child)
		if err != nil {
			return nil, fmt.Errorf("branch %d: %w", branch, err)
		}
		clone.children[branch] = childClone
	}
	return clone, nil
}

func cloneLoadedNode(source mapNode) (mapNode, error) {
	switch node := source.(type) {
	case *innerNode:
		return cloneLoadedInner(node)
	case *leafNode:
		node.mu.RLock()
		defer node.mu.RUnlock()
		item, err := node.item.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone leaf item: %w", err)
		}
		clone := &leafNode{item: item, kind: node.kind}
		clone.hash = node.hash
		clone.SetDirty(node.IsDirty())
		return clone, nil
	default:
		return nil, fmt.Errorf("unsupported loaded node type %T", source)
	}
}

// snapshotLocked returns a shared snapshot while the caller holds the tree and
// backing read locks (or their write-lock equivalents).
func (sm *SHAMap) snapshotLocked(mutable bool) (*SHAMap, error) {
	if sm.tree.state == stateInvalid {
		return nil, fmt.Errorf("%w: cannot snapshot invalid map", ErrInvalidState)
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
			full:      sm.tree.full,
		},
		backing: backingState{
			access:    sm.backing.access,
			fullBelow: sm.backing.fullBelow,
		},
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
