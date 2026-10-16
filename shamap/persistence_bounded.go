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

const (
	dirtyBatchEntries        = 256
	dirtyBatchBytes          = 8 << 20
	dirtyBatchStructureBytes = dirtyBatchEntries * uint64(unsafe.Sizeof(FlushEntry{})+unsafe.Sizeof(mapNode(nil)))
)

// StoreDirtyBatched stores dirty nodes in post-order with bounded temporary copies.
// A successful batch becomes clean; a failed batch and its ancestors remain dirty.
func (sm *SHAMap) StoreDirtyBatched(ctx context.Context, budget DirtyMemoryBudget, store func([]FlushEntry) error) error {
	if budget == nil {
		return sm.StoreDirty(store)
	}
	if store == nil {
		return fmt.Errorf("shamap: nil dirty-node store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workspace := min(uint64(DirtyWorkspaceBytes), budget.Limit())
	var batchLimit uint64
	if workspace > dirtyBatchStructureBytes {
		batchLimit = min(uint64(dirtyBatchBytes), workspace-dirtyBatchStructureBytes)
	}
	var entries []FlushEntry
	var nodes []mapNode
	var charged uint64
	var reserved uint64
	releaseBatch := func() {
		toRelease := reserved
		reserved = 0
		charged = 0
		clear(entries)
		clear(nodes)
		entries = nil
		nodes = nil
		if toRelease != 0 {
			budget.Release(toRelease)
		}
	}
	defer releaseBatch()
	flush := func() error {
		if len(entries) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			releaseBatch()
			return err
		}
		if err := store(entries); err != nil {
			releaseBatch()
			return err
		}
		for _, node := range nodes {
			node.SetDirty(false)
		}
		releaseBatch()
		return nil
	}
	reserveNode := func(charge uint64) error {
		if charge > workspace-dirtyBatchStructureBytes {
			return fmt.Errorf("shamap: dirty node requires %d bytes of workspace, budget is %d", charge+dirtyBatchStructureBytes, workspace)
		}
		for {
			if len(entries) == 0 {
				firstReservation := charge + dirtyBatchStructureBytes
				if err := reserveDirtyMemory(ctx, budget, firstReservation); err != nil {
					return err
				}
				entries = make([]FlushEntry, 0, dirtyBatchEntries)
				nodes = make([]mapNode, 0, dirtyBatchEntries)
				reserved = firstReservation
				charged = charge
				return nil
			}
			if len(entries) == dirtyBatchEntries || charged > batchLimit || charge > batchLimit-charged {
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			if !budget.TryReserve(charge) {
				// Do not wait while retaining a partial batch. The flush either
				// publishes and releases it or returns the store/cancellation error.
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				budget.Release(charge)
				return err
			}
			reserved += charge
			charged += charge
			return nil
		}
	}

	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	if sm.tree.root == nil || sm.tree.root.IsEmpty() {
		return nil
	}
	if workspace <= dirtyBatchStructureBytes {
		return fmt.Errorf("shamap: dirty workspace requires more than %d bytes, budget is %d", dirtyBatchStructureBytes, workspace)
	}
	var visit func(mapNode) error
	visit = func(node mapNode) error {
		if node == nil || !node.IsDirty() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var size uint64
		switch n := node.(type) {
		case *innerNode:
			for branch := range BranchFactor {
				child, _, _ := n.LoadChild(branch)
				if err := visit(child); err != nil {
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
			size = uint64(n.item.DataSize()) + 4
			if n.keyOnWire() {
				size += 32
			}
			n.mu.RUnlock()
		default:
			return fmt.Errorf("shamap: unsupported dirty node type %T", node)
		}
		// Prefix serialization plus backend copies and per-node indexing overhead.
		charge := 3 * (size + 128)
		if err := reserveNode(charge); err != nil {
			return err
		}
		entry, err := flushEntryForNode(node, sm.tree.ledgerSeq, sm.tree.mapType)
		if err != nil {
			return err
		}
		if uint64(len(entry.Data)) != size {
			return fmt.Errorf("shamap: dirty node serialized size changed from %d to %d", size, len(entry.Data))
		}
		entries = append(entries, entry)
		nodes = append(nodes, node)
		if len(entries) == dirtyBatchEntries || charged >= batchLimit {
			return flush()
		}
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
			if err := ctx.Err(); err != nil {
				budget.Release(bytes)
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
