package shamap

import (
	"context"
	"errors"
)

// SerializeRoot serializes the root node for wire transmission.
// This is typically used when sending the tree's root to a peer
// to initiate synchronization.
//
// Returns the serialized wire format of the root node.
func (sm *SHAMap) SerializeRoot() ([]byte, error) {
	return sm.SerializeRootContext(context.Background())
}

// SerializeRootContext is the cancellation-aware form of SerializeRoot.
func (sm *SHAMap) SerializeRootContext(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if sm.tree.root == nil {
		return nil, errors.New("no root node")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := sm.tree.root.SerializeForWire()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return data, err
}

// WireNode is a node ready for wire transmission via TMLedgerData.
// NodeID is the SHAMap path-based identifier (33 bytes: 32 path + 1
// depth) used by the receiver to place the node in the partial tree.
// Data is the node's `SerializeForWire()` output.
type WireNode struct {
	NodeID []byte
	Data   []byte
}

const wireNodeIDSize = 33

// ErrWireBudget reports a deliberate stop after a bounded reply consumed its
// node, wire-byte, or traversal-work allowance. The returned nodes are valid
// and may be sent as a truncated reply.
var ErrWireBudget = errors.New("SHAMap wire traversal budget exhausted")

// WireBudget is shared by all requested subtrees in one peer reply. WireBytes
// includes both the 33-byte NodeID and SerializeForWire data. Materialized
// leaves omitted by fatLeaves also consume the node and byte allowances. Work
// counts branch probes and processed nodes; callers should set it to a finite value.
type WireBudget struct {
	RemainingNodes     int
	RemainingWireBytes int
	RemainingWork      int
}

func (b *WireBudget) spendWork() bool {
	if b == nil || b.RemainingWork <= 0 {
		return false
	}
	b.RemainingWork--
	return true
}

func (b *WireBudget) reserveNode(wireBytes int) bool {
	if b == nil || wireBytes < wireNodeIDSize || b.RemainingNodes <= 0 {
		return false
	}
	if b.RemainingWireBytes < wireBytes {
		return false
	}
	b.RemainingNodes--
	b.RemainingWireBytes -= wireBytes
	return true
}

func wireDataSize(node mapNode) (int, error) {
	switch n := node.(type) {
	case *innerNode:
		branches := n.BranchCount()
		if branches == 0 {
			return 0, errEmptyNonRoot
		}
		if branches < 12 {
			return branches*33 + 1, nil
		}
		return BranchFactor*32 + 1, nil
	case *leafNode:
		n.mu.RLock()
		defer n.mu.RUnlock()
		if n.item == nil {
			return 0, errNilItem
		}
		if n.keyOnWire() {
			return n.item.DataSize() + 33, nil
		}
		return n.item.DataSize() + 1, nil
	default:
		return 0, errors.New("unsupported SHAMap node implementation")
	}
}

func wireNodeSize(node mapNode) (int, error) {
	dataBytes, err := wireDataSize(node)
	if err != nil {
		return 0, err
	}
	return wireNodeIDSize + dataBytes, nil
}

// A leaf prefix record is 30 bytes smaller than its complete wire node (the
// four-byte hash prefix replaces the one-byte wire type and the NodeID adds
// 33 bytes). Inner records are always 516 bytes on disk, so a cold read must
// also be allowed to retrieve the largest inner wire node.
func maxBoundedPrefixBytes(remainingWireBytes int) int {
	const maxInnerWireBytes = wireNodeIDSize + BranchFactor*32 + 1
	if remainingWireBytes < maxInnerWireBytes {
		return 0
	}
	return remainingWireBytes - (wireNodeIDSize - 3)
}

func wireNodeAt(node mapNode, path [32]byte, depth int) (WireNode, error) {
	data, err := node.SerializeForWire()
	if err != nil {
		return WireNode{}, err
	}
	nodeID := make([]byte, 33)
	copy(nodeID[:32], path[:])
	nodeID[32] = byte(depth)
	return WireNode{NodeID: nodeID, Data: data}, nil
}

func wireNodeAtContext(ctx context.Context, node mapNode, path [32]byte, depth int) (WireNode, error) {
	if err := ctx.Err(); err != nil {
		return WireNode{}, err
	}
	wireNode, err := wireNodeAt(node, path, depth)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return WireNode{}, ctxErr
	}
	return wireNode, err
}

// WalkWireNodes performs a pre-order traversal returning every node as
// wire data. Each NodeID is 33 bytes per SHAMapNodeID::getRawString.
// For backed maps, hash-only branches are read from the family without
// attaching them to the shared tree. Retention belongs to the bounded cache.
func (sm *SHAMap) WalkWireNodes() ([]WireNode, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.root == nil {
		return nil, nil
	}

	var out []WireNode
	if err := sm.walkWireNodesRec(sm.tree.root, [32]byte{}, 0, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (sm *SHAMap) walkWireNodesRec(node mapNode, path [32]byte, depth int, out *[]WireNode) error {
	if node == nil {
		return nil
	}
	wn, err := wireNodeAt(node, path, depth)
	if err != nil {
		return err
	}
	*out = append(*out, wn)

	inner, ok := node.(*innerNode)
	if !ok {
		return nil
	}
	for branch := range BranchFactor {
		child, err := sm.readChild(context.Background(), inner, branch, false, ReadPeer)
		if err != nil {
			return err
		}
		if child == nil {
			continue
		}
		childPath := childPathForBranch(path, depth, branch)
		if err := sm.walkWireNodesRec(child, childPath, depth+1, out); err != nil {
			return err
		}
	}
	return nil
}

// GetNodeFatByPath returns the SHAMap node at (wantedPath, wantedDepth)
// plus descendants out to `depth` levels, each as a (33-byte NodeID, wire
// blob) pair. Mirrors SHAMap::getNodeFat.
//
// Peers identify subtrees by SHAMapNodeID in TMGetLedger.nodeids, never
// by node hash. Single-child chains follow
// without spending budget. Leaves at the budget boundary are included
// only when fatLeaves is true (liTS_CANDIDATE callers pass false).
// For backed maps, hash-only branches are read without attaching them to the
// shared tree, both when locating the requested subtree and serving its nodes.
func (sm *SHAMap) GetNodeFatByPath(wantedPath [32]byte, wantedDepth int, depth int, fatLeaves bool) ([]WireNode, error) {
	return sm.getNodeFatByPath(context.Background(), wantedPath, wantedDepth, depth, fatLeaves, nil)
}

// GetNodeFatByPathContext is the cancellation-aware form of GetNodeFatByPath.
func (sm *SHAMap) GetNodeFatByPathContext(ctx context.Context, wantedPath [32]byte, wantedDepth int, depth int, fatLeaves bool) ([]WireNode, error) {
	return sm.getNodeFatByPath(ctx, wantedPath, wantedDepth, depth, fatLeaves, nil)
}

// GetNodeFatByPathBounded is the bounded peer-serving variant of
// GetNodeFatByPath. A non-nil budget is consumed across this subtree and may
// be shared by the caller across several requested NodeIDs. On ErrWireBudget,
// the returned nodes are the valid prefix accepted before truncation.
func (sm *SHAMap) GetNodeFatByPathBounded(ctx context.Context, wantedPath [32]byte, wantedDepth int, depth int, fatLeaves bool, budget *WireBudget) ([]WireNode, error) {
	if budget == nil {
		return nil, ErrWireBudget
	}
	return sm.getNodeFatByPath(ctx, wantedPath, wantedDepth, depth, fatLeaves, budget)
}

func (sm *SHAMap) getNodeFatByPath(ctx context.Context, wantedPath [32]byte, wantedDepth int, depth int, fatLeaves bool, budget *WireBudget) ([]WireNode, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if sm.tree.root == nil {
		return nil, nil
	}

	readChild := func(inner *innerNode, branch int) (mapNode, error) {
		if budget == nil {
			return sm.readChild(ctx, inner, branch, false, ReadPeer)
		}
		return sm.readChildForWire(ctx, inner, branch, budget.RemainingWireBytes)
	}
	spendBranch := func() error {
		if budget == nil {
			return nil
		}
		if budget.RemainingNodes <= 0 || budget.RemainingWireBytes < wireNodeIDSize {
			return ErrWireBudget
		}
		if !budget.spendWork() {
			return ErrWireBudget
		}
		return nil
	}

	// 1. Descend to the requested node.
	node := mapNode(sm.tree.root)
	curDepth := 0
	curPath := [32]byte{}
	for node != nil && curDepth < wantedDepth {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inner, ok := node.(*innerNode)
		if !ok {
			// Leaf reached before wantedDepth — not the requested node.
			return nil, nil
		}
		if err := spendBranch(); err != nil {
			return nil, err
		}
		branch := selectBranchForPath(wantedPath, curDepth)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child, err := readChild(inner, branch)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, err
		}
		if child == nil {
			return nil, nil
		}
		curPath = childPathForBranch(curPath, curDepth, branch)
		node = child
		curDepth++
	}
	if node == nil || curDepth != wantedDepth {
		return nil, nil
	}
	// Verify path matches: the descent above only matched as far as
	// curDepth; ensure the path nibbles agree with wantedPath.
	if !pathPrefixEq(curPath, wantedPath, wantedDepth) {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Empty inner: rippled rejects with "peer requests empty node".
	if inner, ok := node.(*innerNode); ok {
		empty := inner.BranchCount() == 0
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if empty {
			return nil, nil
		}
	}

	// 2-3. Stack walk with the depth budget. Each entry is a suspended frame,
	// rather than a fetched child list: this keeps cold reads proportional to
	// nodes that can still fit the allowance.
	type fatStackEntry struct {
		node   mapNode
		path   [32]byte
		depth  int
		budget int
		// BranchFactor means the node has not been emitted yet; otherwise this
		// is the next branch to inspect in reverse order.
		nextBranch int
	}
	stack := []fatStackEntry{{node: node, path: curPath, depth: curDepth, budget: depth, nextBranch: BranchFactor}}
	var out []WireNode

	fail := func(err error) ([]WireNode, error) {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		if budget != nil && errors.Is(err, ErrWireBudget) {
			return out, err
		}
		return nil, err
	}
	chargeNode := func(node mapNode) error {
		if budget == nil {
			return nil
		}
		if !budget.spendWork() {
			return ErrWireBudget
		}
		wireBytes, err := wireNodeSize(node)
		if err != nil {
			return err
		}
		if !budget.reserveNode(wireBytes) {
			return ErrWireBudget
		}
		return nil
	}
	emit := func(node mapNode, path [32]byte, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := chargeNode(node); err != nil {
			return err
		}
		wn, err := wireNodeAtContext(ctx, node, path, depth)
		if err != nil {
			return err
		}
		out = append(out, wn)
		return nil
	}

	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		last := len(stack) - 1
		e := &stack[last]
		if e.nextBranch == BranchFactor {
			if err := emit(e.node, e.path, e.depth); err != nil {
				return fail(err)
			}
			e.nextBranch = BranchFactor - 1
		}

		inner, ok := e.node.(*innerNode)
		if !ok {
			stack = stack[:last]
			continue
		}
		bc := inner.BranchCount()
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		// Descend if budget>0 or single-child chain.
		if e.budget == 0 && bc != 1 {
			stack = stack[:last]
			continue
		}
		if e.nextBranch < 0 {
			stack = stack[:last]
			continue
		}
		branch := e.nextBranch
		e.nextBranch--
		if err := spendBranch(); err != nil {
			return fail(err)
		}
		child, err := readChild(inner, branch)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fail(ctxErr)
		}
		if err != nil {
			return fail(err)
		}
		if child == nil {
			continue
		}
		childPath := childPathForBranch(e.path, e.depth, branch)
		childInner, isInner := child.(*innerNode)
		if isInner && (e.budget > 1 || bc == 1) {
			// Siblings remain as unfetched branches on the parent frame.
			newBudget := e.budget - 1
			if bc == 1 {
				newBudget = e.budget
			}
			stack = append(stack, fatStackEntry{
				node:       childInner,
				path:       childPath,
				depth:      e.depth + 1,
				budget:     newBudget,
				nextBranch: BranchFactor,
			})
		} else if isInner || fatLeaves {
			// Include directly without descent.
			if err := emit(child, childPath, e.depth+1); err != nil {
				return fail(err)
			}
		} else if err := chargeNode(child); err != nil {
			return fail(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return out, nil
}
