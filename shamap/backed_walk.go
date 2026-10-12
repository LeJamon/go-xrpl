package shamap

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"sync/atomic"
)

var errVerifiedBaseUnavailable = errors.New("verified SHAMap base is unavailable")

// BackedWalkStats reports local discovery work performed by a backed SHAMap.
type BackedWalkStats struct {
	// TraversalDiscoveries counts first observations on paths retained by the
	// cursor. Repeated reads are excluded; untracked retries are conservative.
	TraversalDiscoveries  uint64
	EqualSubtreesSkipped  uint64
	NodesDescended        uint64
	DurableReads          uint64
	MissingNodes          uint64
	VerifiedBaseFallbacks uint64
	RetainedNodes         uint64
	RetainedBytes         uint64
	RetainedPeakBytes     uint64
	RetentionPressure     bool
	BlockedItems          uint64
	BlockedCapacity       uint64
	ActiveFrames          uint64
	ActiveCapacity        uint64
	ProofEntries          uint64
	ProofCapacity         uint64
	CursorBytes           uint64
	CursorPeakBytes       uint64
}

type backedWalkStats struct {
	traversalDiscoveries  atomic.Uint64
	equalSubtreesSkipped  atomic.Uint64
	nodesDescended        atomic.Uint64
	durableReads          atomic.Uint64
	missingNodes          atomic.Uint64
	verifiedBaseFallbacks atomic.Uint64
	blockedItems          atomic.Uint64
	blockedCapacity       atomic.Uint64
	activeFrames          atomic.Uint64
	activeCapacity        atomic.Uint64
	proofEntries          atomic.Uint64
	proofCapacity         atomic.Uint64
	cursorBytes           atomic.Uint64
	cursorPeakBytes       atomic.Uint64
}

type verifiedWalkBase struct {
	rootHash [32]byte
	root     traversalNode
}

type traversalBudgetKey struct{}

type traversalBudget struct {
	remaining atomic.Int64
}

// WithTraversalBudget bounds the number of uncached nodes examined by backed
// SHAMap walks sharing the returned context. Exhaustion preserves the walk
// cursor and returns ErrTraversalBudget so the caller can resume later.
func WithTraversalBudget(ctx context.Context, maxVisits int64) context.Context {
	if maxVisits <= 0 {
		return ctx
	}
	budget := &traversalBudget{}
	budget.remaining.Store(maxVisits)
	return context.WithValue(ctx, traversalBudgetKey{}, budget)
}

func takeTraversalVisit(ctx context.Context) bool {
	budget, _ := ctx.Value(traversalBudgetKey{}).(*traversalBudget)
	return budget == nil || budget.remaining.Add(-1) >= 0
}

type backedWalkItem struct {
	hash       [32]byte
	baseHash   [32]byte
	parentHash [32]byte
	nodeID     NodeID
	depth      int
	branch     int
	parent     *innerNode
	node       mapNode
}

type traversalNode struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	children [BranchFactor]mapNode
}

// SetVerifiedBaseContext installs a durable, position-aligned comparison base
// for subsequent backed walks. The caller must keep managed destructive store
// mutations excluded until the acquisition using the base has completed.
func (sm *SHAMap) SetVerifiedBaseContext(ctx context.Context, rootHash [32]byte) error {
	if rootHash == ([32]byte{}) {
		return errors.New("verified SHAMap base has an empty root hash")
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	if !access.available() || access.durable == nil {
		return errors.New("verified SHAMap base requires a durable backing family")
	}
	sm.acquisition.stats.durableReads.Add(1)
	data, err := access.fetchDurable(ctx, rootHash)
	if err != nil {
		return fmt.Errorf("load verified SHAMap base root: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: %w", errVerifiedBaseUnavailable, &MissingNodeError{Hash: rootHash})
	}
	view, err := decodeTraversalNode(data, rootHash)
	if err != nil {
		return fmt.Errorf("%w: root %x: %v", errVerifiedBaseUnavailable, rootHash[:8], err)
	}
	if !view.inner {
		return fmt.Errorf("%w: root %x is not an inner node", errVerifiedBaseUnavailable, rootHash[:8])
	}
	sm.releaseBackedWalkCursorLocked()
	sm.acquisition.base = &verifiedWalkBase{rootHash: rootHash, root: view}
	return nil
}

// BackedWalkStats returns a consistent snapshot of the acquisition counters.
func (sm *SHAMap) BackedWalkStats() BackedWalkStats {
	return BackedWalkStats{
		TraversalDiscoveries:  sm.acquisition.stats.traversalDiscoveries.Load(),
		EqualSubtreesSkipped:  sm.acquisition.stats.equalSubtreesSkipped.Load(),
		NodesDescended:        sm.acquisition.stats.nodesDescended.Load(),
		DurableReads:          sm.acquisition.stats.durableReads.Load(),
		MissingNodes:          sm.acquisition.stats.missingNodes.Load(),
		VerifiedBaseFallbacks: sm.acquisition.stats.verifiedBaseFallbacks.Load(),
		RetainedNodes:         sm.acquisition.retained.nodes.Load(),
		RetainedBytes:         sm.acquisition.retained.bytes.Load(),
		RetainedPeakBytes:     sm.acquisition.retained.peak.Load(),
		RetentionPressure:     sm.acquisition.retained.pressure.Load(),
		BlockedItems:          sm.acquisition.stats.blockedItems.Load(),
		BlockedCapacity:       sm.acquisition.stats.blockedCapacity.Load(),
		ActiveFrames:          sm.acquisition.stats.activeFrames.Load(),
		ActiveCapacity:        sm.acquisition.stats.activeCapacity.Load(),
		ProofEntries:          sm.acquisition.stats.proofEntries.Load(),
		ProofCapacity:         sm.acquisition.stats.proofCapacity.Load(),
		CursorBytes:           sm.acquisition.stats.cursorBytes.Load(),
		CursorPeakBytes:       sm.acquisition.stats.cursorPeakBytes.Load(),
	}
}

type cursorBudget struct {
	budget *AcquisitionBudget
}

func (sm *SHAMap) reserveCursorBytes(cursor *cursorBudget, bytes uint64) bool {
	if cursor == nil || cursor.budget == nil || bytes == 0 {
		return true
	}
	if !cursor.budget.tryReserveCursor(bytes) {
		return false
	}
	sm.recordCursorReservation(bytes)
	return true
}

func (sm *SHAMap) recordCursorReservation(bytes uint64) {
	current := sm.acquisition.stats.cursorBytes.Add(bytes)
	for {
		peak := sm.acquisition.stats.cursorPeakBytes.Load()
		if current <= peak || sm.acquisition.stats.cursorPeakBytes.CompareAndSwap(peak, current) {
			break
		}
	}
}

func (sm *SHAMap) releaseCursorBytes(cursor *cursorBudget, bytes uint64) {
	if cursor == nil || cursor.budget == nil || bytes == 0 {
		return
	}
	for {
		used := sm.acquisition.stats.cursorBytes.Load()
		if bytes > used {
			panic("shamap: acquisition cursor release exceeds reservation")
		}
		if sm.acquisition.stats.cursorBytes.CompareAndSwap(used, used-bytes) {
			cursor.budget.releaseCursor(bytes)
			return
		}
	}
}

func (sm *SHAMap) releaseBackedWalkCursorLocked() {
	sm.releaseBackedScanCursorLocked()
	sm.acquisition.base = nil
	sm.acquisition.stats.blockedItems.Store(0)
	sm.acquisition.stats.blockedCapacity.Store(0)
	sm.acquisition.stats.activeFrames.Store(0)
	sm.acquisition.stats.activeCapacity.Store(0)
	sm.acquisition.stats.proofEntries.Store(0)
	sm.acquisition.stats.proofCapacity.Store(0)
}

func (sm *SHAMap) loadTraversalNode(ctx context.Context, access *familyAccess, root *innerNode, item backedWalkItem) (traversalNode, bool, bool, error) {
	sm.acquisition.stats.durableReads.Add(1)
	data, stored, err := access.fetchPreferDurable(ctx, item.hash)
	if err != nil {
		return traversalNode{}, false, false, err
	}
	if len(data) != 0 {
		sm.backing.loads.Add(1)
		view, err := decodeTraversalNode(data, item.hash)
		if err != nil {
			return traversalNode{}, false, false, err
		}
		return view, true, stored, nil
	}
	if item.node != nil {
		return traversalNodeFromNode(item.node), true, false, nil
	}
	if attached := attachedNode(root, item.nodeID); attached != nil {
		return traversalNodeFromNode(attached), true, false, nil
	}
	return traversalNode{}, false, false, nil
}

func (sm *SHAMap) loadVerifiedBaseTraversalNode(
	ctx context.Context,
	access *familyAccess,
	hash [32]byte,
) (traversalNode, error) {
	sm.acquisition.stats.durableReads.Add(1)
	data, err := access.fetchDurable(ctx, hash)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return traversalNode{}, ctxErr
		}
		return traversalNode{}, fmt.Errorf("%w: read %x: %v", errVerifiedBaseUnavailable, hash[:8], err)
	}
	if len(data) == 0 {
		return traversalNode{}, fmt.Errorf("%w: %w", errVerifiedBaseUnavailable, &MissingNodeError{Hash: hash})
	}
	view, err := decodeTraversalNode(data, hash)
	if err != nil {
		return traversalNode{}, fmt.Errorf("%w: node %x: %v", errVerifiedBaseUnavailable, hash[:8], err)
	}
	return view, nil
}

func attachedNode(root *innerNode, nodeID NodeID) mapNode {
	if root == nil {
		return nil
	}
	current := mapNode(root)
	path := nodeID.ID()
	for depth := 0; depth < int(nodeID.Depth()); depth++ {
		inner, ok := current.(*innerNode)
		if !ok {
			return nil
		}
		child, _, set := inner.LoadChild(selectBranchForPath(path, depth))
		if !set || child == nil {
			return nil
		}
		current = child
	}
	return current
}

func traversalNodeFromNode(node mapNode) traversalNode {
	inner, ok := node.(*innerNode)
	if !ok {
		return traversalNode{}
	}
	inner.mu.RLock()
	defer inner.mu.RUnlock()
	return traversalNode{
		inner: true, branches: inner.isBranch, hashes: inner.hashes, children: inner.children,
	}
}

func decodeTraversalNode(data []byte, expected [32]byte) (traversalNode, error) {
	body, err := decodePrefixBody(data)
	if err != nil {
		return traversalNode{}, fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
	}
	view := traversalNode{}
	switch body.kind {
	case storedInner:
		view.inner = true
		view.branches, err = decodeInnerBranches(body, &view.hashes)
		if err != nil {
			return traversalNode{}, fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
		}
	case storedAccountState:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: account-state leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	case storedTransaction:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: transaction leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	case storedTransactionWithMeta:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: transaction-with-metadata leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	}
	digest := sha512.Sum512(data)
	var actual [32]byte
	copy(actual[:], digest[:32])
	if actual != expected {
		return traversalNode{}, fmt.Errorf("%w: expected %x, got %x", ErrInvalidNodeData, expected[:8], actual[:8])
	}
	return view, nil
}
