package shamap

import (
	"errors"
)

// SerializeRoot serializes the root node for wire transmission.
// This is typically used when sending the tree's root to a peer
// to initiate synchronization.
//
// Returns the serialized wire format of the root node.
func (sm *SHAMap) SerializeRoot() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, errors.New("no root node")
	}

	return sm.root.SerializeForWire()
}

// WireNode is a node ready for wire transmission via TMLedgerData.
// NodeID is the SHAMap path-based identifier (33 bytes: 32 path + 1
// depth) used by the receiver to place the node in the partial tree.
// Data is the node's `SerializeForWire()` output.
type WireNode struct {
	NodeID []byte
	Data   []byte
}

func wireNodeAt(node Node, path [32]byte, depth int) (WireNode, error) {
	data, err := node.SerializeForWire()
	if err != nil {
		return WireNode{}, err
	}
	nodeID := make([]byte, 33)
	copy(nodeID[:32], path[:])
	nodeID[32] = byte(depth)
	return WireNode{NodeID: nodeID, Data: data}, nil
}

// WalkWireNodes performs a pre-order traversal returning every node as
// wire data. Each NodeID is 33 bytes per SHAMapNodeID::getRawString.
// For backed maps, hash-only branches are lazy-loaded from the family.
func (sm *SHAMap) WalkWireNodes() ([]WireNode, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, nil
	}

	var out []WireNode
	if err := sm.walkWireNodesRec(sm.root, [32]byte{}, 0, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (sm *SHAMap) walkWireNodesRec(node Node, path [32]byte, depth int, out *[]WireNode) error {
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
		child, err := sm.descend(inner, branch)
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
// For backed maps, hash-only branches are lazy-loaded from the family.
func (sm *SHAMap) GetNodeFatByPath(wantedPath [32]byte, wantedDepth int, depth int, fatLeaves bool) ([]WireNode, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, nil
	}

	// 1. Descend to the requested node.
	node := Node(sm.root)
	curDepth := 0
	curPath := [32]byte{}
	for node != nil && curDepth < wantedDepth {
		inner, ok := node.(*innerNode)
		if !ok {
			// Leaf reached before wantedDepth — not the requested node.
			return nil, nil
		}
		branch := selectBranchForPath(wantedPath, curDepth)
		child, err := sm.descend(inner, branch)
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
	// Empty inner: rippled rejects with "peer requests empty node".
	if inner, ok := node.(*innerNode); ok && inner.BranchCount() == 0 {
		return nil, nil
	}

	// 2-3. Stack walk with the depth budget.
	type fatStackEntry struct {
		node  Node
		path  [32]byte
		depth int
		// budget is the remaining child-descent levels, mirroring
		// rippled's `depth` local variable inside the while loop.
		budget int
	}
	stack := []fatStackEntry{{node: node, path: curPath, depth: curDepth, budget: depth}}
	var out []WireNode

	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		wn, err := wireNodeAt(e.node, e.path, e.depth)
		if err != nil {
			return nil, err
		}
		out = append(out, wn)

		inner, ok := e.node.(*innerNode)
		if !ok {
			continue
		}
		bc := inner.BranchCount()
		// Descend if budget>0 or single-child chain.
		if e.budget == 0 && bc != 1 {
			continue
		}
		// Reverse iteration → ascending-branch pop order.
		for i := BranchFactor - 1; i >= 0; i-- {
			child, err := sm.descend(inner, i)
			if err != nil {
				return nil, err
			}
			if child == nil {
				continue
			}
			childPath := childPathForBranch(e.path, e.depth, i)
			childInner, isInner := child.(*innerNode)
			if isInner && (e.budget > 1 || bc == 1) {
				// Push: budget-1 for multi-child, unchanged for chain.
				newBudget := e.budget - 1
				if bc == 1 {
					newBudget = e.budget
				}
				stack = append(stack, fatStackEntry{
					node:   childInner,
					path:   childPath,
					depth:  e.depth + 1,
					budget: newBudget,
				})
			} else if isInner || fatLeaves {
				// Include directly without descent.
				cwn, err := wireNodeAt(child, childPath, e.depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, cwn)
			}
		}
	}
	return out, nil
}
