package shamap

import (
	"sync/atomic"
	"unsafe"
)

type acquisitionRetention struct {
	bytes    atomic.Uint64
	peak     atomic.Uint64
	nodes    atomic.Uint64
	pressure atomic.Bool
}

// Retained-node charges include allocations that a traversal may have stopped
// referencing since the last checkpoint. Keeping those credits until every
// cursor reference has been cleared makes the estimate conservative.
func (sm *SHAMap) retainAcquisitionNode(node mapNode) bool {
	budget := sm.acquisition.budget
	if budget == nil || sm.tree.state != stateSyncing {
		return true
	}
	charge := acquisitionNodeBytes(node)
	if !budget.tryReserveLoaded(charge) {
		sm.acquisition.retained.pressure.Store(true)
		return false
	}
	current := sm.acquisition.retained.bytes.Add(charge)
	sm.acquisition.retained.nodes.Add(1)
	for peak := sm.acquisition.retained.peak.Load(); current > peak && !sm.acquisition.retained.peak.CompareAndSwap(peak, current); peak = sm.acquisition.retained.peak.Load() {
	}
	return true
}

func acquisitionNodeBytes(node mapNode) uint64 {
	switch n := node.(type) {
	case *innerNode:
		return 2 * uint64(unsafe.Sizeof(innerNode{}))
	case *leafNode:
		item := n.Item()
		return 2 * uint64(unsafe.Sizeof(leafNode{})+unsafe.Sizeof(Item{})+uintptr(cap(item.data)))
	default:
		panic("unexpected acquisition node type")
	}
}

func (sm *SHAMap) releaseAcquisitionNode(node mapNode) {
	if sm.acquisition.budget == nil || sm.tree.state != stateSyncing {
		return
	}
	charge := acquisitionNodeBytes(node)
	sm.acquisition.retained.bytes.Add(^(charge - 1))
	sm.acquisition.retained.nodes.Add(^uint64(0))
	sm.acquisition.budget.releaseLoaded(charge)
}

func (sm *SHAMap) releaseAcquisitionNodesLocked() {
	bytes := sm.acquisition.retained.bytes.Swap(0)
	sm.acquisition.retained.nodes.Store(0)
	sm.acquisition.retained.pressure.Store(false)
	if bytes != 0 {
		sm.acquisition.budget.releaseLoaded(bytes)
	}
}

func (b *AcquisitionBudget) tryReserveLoaded(bytes uint64) bool {
	if b == nil || bytes == 0 {
		return b == nil || bytes == 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	used := b.loadedUsed.Load()
	if bytes > b.limit/4 || used > b.limit/4-bytes {
		return false
	}
	if !b.reserveCategoryLocked(acquisitionCategoryLoaded, bytes) {
		return false
	}
	b.loadedUsed.Store(used + bytes)
	for peak := b.loadedPeak.Load(); used+bytes > peak && !b.loadedPeak.CompareAndSwap(peak, used+bytes); peak = b.loadedPeak.Load() {
	}
	return true
}

func (b *AcquisitionBudget) releaseLoaded(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseCategoryLocked(acquisitionCategoryLoaded, bytes)
}
