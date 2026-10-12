package shamap

import (
	"context"
	"fmt"
	"unsafe"
)

// DirtyMemoryBudget supplies bounded serialization and storage workspace.
type DirtyMemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
	Changed() <-chan struct{}
	Limit() uint64
}

// DirtyWorkspaceBytes admits one maximum-size peer payload and storage copies.
const DirtyWorkspaceBytes = 256 << 20

// StoreDirtyBatched stores dirty nodes in post-order with bounded temporary copies.
// A successful batch becomes clean; a failed batch and its ancestors remain dirty.
func (sm *SHAMap) StoreDirtyBatched(ctx context.Context, budget DirtyMemoryBudget, store func([]FlushEntry) error) error {
	return sm.storeBatched(ctx, budget, store, false)
}

// StoreAllBatched copies a complete tree, including clean acquisition nodes,
// without retaining descendants loaded while walking a backed source.
func (sm *SHAMap) StoreAllBatched(ctx context.Context, budget DirtyMemoryBudget, store func([]FlushEntry) error) error {
	return sm.storeBatched(ctx, budget, store, true)
}

func (sm *SHAMap) storeBatched(ctx context.Context, budget DirtyMemoryBudget, store func([]FlushEntry) error, all bool) error {
	if budget == nil {
		if all {
			return fmt.Errorf("shamap: full-tree copy requires a memory budget")
		}
		return sm.StoreDirty(store)
	}
	if store == nil {
		return fmt.Errorf("shamap: nil dirty-node store")
	}
	const batchEntries = 256
	const batchBytes = 8 << 20
	const structureBytes = batchEntries * uint64(unsafe.Sizeof(FlushEntry{})+unsafe.Sizeof(mapNode(nil)))
	workspace := min(uint64(DirtyWorkspaceBytes), budget.Limit())
	if workspace <= structureBytes {
		return fmt.Errorf("shamap: dirty workspace requires more than %d bytes, budget is %d", structureBytes, workspace)
	}
	if err := reserveDirtyMemory(ctx, budget, workspace); err != nil {
		return err
	}
	defer budget.Release(workspace)
	entries := make([]FlushEntry, 0, batchEntries)
	nodes := make([]mapNode, 0, batchEntries)
	var charged uint64
	flush := func() error {
		if len(entries) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store(entries); err != nil {
			return err
		}
		for _, node := range nodes {
			node.SetDirty(false)
		}
		clear(entries)
		clear(nodes)
		entries, nodes = entries[:0], nodes[:0]
		charged = 0
		return nil
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	if sm.tree.root == nil || sm.tree.root.IsEmpty() {
		return nil
	}
	var visit func(mapNode) error
	visit = func(node mapNode) error {
		if node == nil || (!all && !node.IsDirty()) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var size uint64
		switch n := node.(type) {
		case *innerNode:
			for branch := range BranchFactor {
				child, _, present := n.LoadChild(branch)
				loaded := all && present && child == nil
				if loaded {
					var err error
					child, err = sm.descendCtx(ctx, n, branch)
					if err != nil {
						return err
					}
				}
				err := visit(child)
				if loaded {
					sm.releaseChild(n, branch, child)
				}
				if err != nil {
					return err
				}
			}
			if err := n.updateHashDeep(); err != nil {
				return err
			}
			size = fullInnerSerializedSize
		case *leafNode:
			n.mu.RLock()
			if n.item == nil {
				n.mu.RUnlock()
				return errNilItem
			}
			size = uint64(n.item.DataSize()) + 4 + 32
			n.mu.RUnlock()
		default:
			return fmt.Errorf("shamap: unsupported dirty node type %T", node)
		}
		// Prefix serialization plus backend copies and per-node indexing overhead.
		charge := 3 * (size + 128)
		if charge > workspace-structureBytes {
			return fmt.Errorf("shamap: dirty node requires %d bytes of workspace, budget is %d", charge+structureBytes, workspace)
		}
		if len(entries) == batchEntries || charged+charge > min(uint64(batchBytes), workspace-structureBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		charged += charge
		entry, err := flushEntryForNode(node, sm.tree.ledgerSeq, sm.tree.mapType)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		nodes = append(nodes, node)
		return nil
	}
	if err := visit(sm.tree.root); err != nil {
		return err
	}
	return flush()
}

func reserveDirtyMemory(ctx context.Context, budget DirtyMemoryBudget, bytes uint64) error {
	if bytes > budget.Limit() {
		return fmt.Errorf("shamap: dirty workspace requires %d bytes, budget is %d", bytes, budget.Limit())
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wake := budget.Changed()
		if budget.TryReserve(bytes) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
