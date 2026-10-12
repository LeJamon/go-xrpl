package shamap

import (
	"fmt"
	"unsafe"
)

const (
	retainedMemoryMaxUint64    = ^uint64(0)
	retainedMemoryWrapperSize  = uint64(unsafe.Sizeof(SHAMap{}))
	retainedMemoryInnerSize    = 2 * uint64(unsafe.Sizeof(innerNode{}))
	retainedMemoryLeafBaseSize = uint64(unsafe.Sizeof(leafNode{})) + uint64(unsafe.Sizeof(Item{}))
)

// RetainedMemoryBytes returns a conservative estimate of the memory currently
// retained by the SHAMap and its resident tree nodes. Hash-only branches in a
// backed map are excluded; inspecting them never fetches from the backing
// Family.
func (sm *SHAMap) RetainedMemoryBytes() (uint64, error) {
	if sm == nil {
		return 0, fmt.Errorf("shamap: cannot estimate retained memory of a nil map")
	}

	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.state < stateModifying || sm.tree.state >= stateInvalid {
		return 0, fmt.Errorf("shamap: invalid map state %d", sm.tree.state)
	}
	if sm.tree.root == nil {
		return 0, fmt.Errorf("shamap: cannot estimate retained memory without a root")
	}

	nodeBytes, err := retainedMemoryNodeBytes(sm.tree.root, 0)
	if err != nil {
		return 0, err
	}
	return retainedMemoryAdd(retainedMemoryWrapperSize, nodeBytes)
}

func retainedMemoryNodeBytes(node mapNode, depth int) (uint64, error) {
	if node == nil {
		return 0, fmt.Errorf("shamap: nil node at depth %d", depth)
	}
	if depth > maxDepth {
		return 0, fmt.Errorf("shamap: node depth %d exceeds maximum depth %d", depth, maxDepth)
	}

	switch n := node.(type) {
	case *innerNode:
		if n == nil {
			return 0, fmt.Errorf("shamap: nil inner node at depth %d", depth)
		}
		if depth >= maxDepth {
			return 0, fmt.Errorf("shamap: inner node at depth %d exceeds maximum depth %d", depth, maxDepth)
		}
		total := retainedMemoryInnerSize
		for branch := 0; branch < BranchFactor; branch++ {
			child, _, _ := n.LoadChild(branch)
			if child == nil {
				continue
			}
			childBytes, err := retainedMemoryNodeBytes(child, depth+1)
			if err != nil {
				return 0, err
			}
			total, err = retainedMemoryAdd(total, childBytes)
			if err != nil {
				return 0, err
			}
		}
		return total, nil

	case *leafNode:
		if n == nil {
			return 0, fmt.Errorf("shamap: nil leaf node at depth %d", depth)
		}
		n.mu.RLock()
		defer n.mu.RUnlock()
		if n.item == nil {
			return 0, fmt.Errorf("shamap: leaf at depth %d has no item", depth)
		}
		total, err := retainedMemoryAdd(retainedMemoryLeafBaseSize, uint64(cap(n.item.data)))
		if err != nil {
			return 0, err
		}
		if total > retainedMemoryMaxUint64/2 {
			return 0, fmt.Errorf("shamap: retained memory estimate overflows uint64")
		}
		return total * 2, nil

	default:
		return 0, fmt.Errorf("shamap: invalid node type %T at depth %d", node, depth)
	}
}

func retainedMemoryAdd(left, right uint64) (uint64, error) {
	if right > retainedMemoryMaxUint64-left {
		return 0, fmt.Errorf("shamap: retained memory estimate overflows uint64")
	}
	return left + right, nil
}
