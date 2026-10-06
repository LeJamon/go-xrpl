package shamap

import (
	"fmt"
	"unsafe"
)

const (
	retainedMemoryMaxUint64   = ^uint64(0)
	retainedMemoryWrapperSize = uint64(unsafe.Sizeof(SHAMap{}))
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

	if sm.tree.state < stateModifying || sm.tree.state > stateInvalid {
		return 0, fmt.Errorf("shamap: invalid map state %d", sm.tree.state)
	}
	if sm.tree.state == stateInvalid {
		return 0, fmt.Errorf("shamap: cannot estimate retained memory of an invalid map")
	}
	if sm.tree.root == nil {
		return 0, fmt.Errorf("shamap: cannot estimate retained memory without a root")
	}

	nodeBytes, err := retainedMemoryNodeBytes(sm.tree.root, 0, true)
	if err != nil {
		return 0, err
	}
	return retainedMemoryAdd(retainedMemoryWrapperSize, nodeBytes)
}

func retainedMemoryNodeBytes(node mapNode, depth int, root bool) (uint64, error) {
	if node == nil {
		return 0, fmt.Errorf("shamap: nil node at depth %d", depth)
	}
	if depth < 0 || depth > maxDepth {
		return 0, fmt.Errorf("shamap: node depth %d exceeds the maximum depth %d", depth, maxDepth)
	}

	switch n := node.(type) {
	case *innerNode:
		if n == nil {
			return 0, fmt.Errorf("shamap: nil inner node at depth %d", depth)
		}
		if depth >= maxDepth {
			return 0, fmt.Errorf("shamap: inner node at depth %d exceeds the maximum depth %d", depth, maxDepth)
		}

		total, err := retainedMemoryDouble(uint64(unsafe.Sizeof(innerNode{})))
		if err != nil {
			return 0, fmt.Errorf("shamap: inner node at depth %d: %w", depth, err)
		}
		activeBranches := 0
		for branch := 0; branch < BranchFactor; branch++ {
			child, hash, active := n.LoadChild(branch)
			if active {
				activeBranches++
				if child == nil && isZeroHash(hash) {
					return 0, fmt.Errorf("shamap: active branch %d at depth %d has no child or hash", branch, depth)
				}
			} else if child != nil || !isZeroHash(hash) {
				return 0, fmt.Errorf("shamap: inactive branch %d at depth %d has resident data", branch, depth)
			}

			if child == nil {
				continue
			}
			childBytes, err := retainedMemoryNodeBytes(child, depth+1, false)
			if err != nil {
				return 0, err
			}
			total, err = retainedMemoryAdd(total, childBytes)
			if err != nil {
				return 0, fmt.Errorf("shamap: inner node at depth %d: %w", depth, err)
			}
		}
		if !root && activeBranches == 0 {
			return 0, fmt.Errorf("shamap: non-root inner node at depth %d is empty", depth)
		}
		return total, nil

	case *leafNode:
		if n == nil {
			return 0, fmt.Errorf("shamap: nil leaf node at depth %d", depth)
		}
		return retainedMemoryLeafBytes(n, depth)

	default:
		return 0, fmt.Errorf("shamap: invalid node type %T at depth %d", node, depth)
	}
}

func retainedMemoryLeafBytes(node *leafNode, depth int) (uint64, error) {
	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.item == nil {
		return 0, fmt.Errorf("shamap: leaf at depth %d has no item", depth)
	}
	if node.item.DataSize() < 12 {
		return 0, fmt.Errorf("shamap: leaf at depth %d has an undersized item", depth)
	}
	switch node.kind {
	case leafAccountState, leafTransaction, leafTransactionWithMeta:
	default:
		return 0, fmt.Errorf("shamap: leaf at depth %d has invalid kind %d", depth, node.kind)
	}

	total, err := retainedMemoryAdd(
		uint64(unsafe.Sizeof(leafNode{})),
		uint64(unsafe.Sizeof(Item{})),
	)
	if err != nil {
		return 0, fmt.Errorf("shamap: leaf at depth %d: %w", depth, err)
	}
	total, err = retainedMemoryAdd(total, uint64(cap(node.item.data)))
	if err != nil {
		return 0, fmt.Errorf("shamap: leaf at depth %d: %w", depth, err)
	}
	return retainedMemoryDouble(total)
}

func retainedMemoryAdd(left, right uint64) (uint64, error) {
	if right > retainedMemoryMaxUint64-left {
		return 0, fmt.Errorf("retained memory estimate overflows uint64")
	}
	return left + right, nil
}

func retainedMemoryDouble(value uint64) (uint64, error) {
	if value > retainedMemoryMaxUint64/2 {
		return 0, fmt.Errorf("retained memory estimate overflows uint64")
	}
	return value * 2, nil
}
