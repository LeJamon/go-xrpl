package overlay

import (
	"bytes"
	"fmt"
	"math"
	"sync/atomic"
)

type valueBlob struct {
	refs   atomic.Int64
	data   []byte
	owner  *budget
	charge uint64
}

func newValueBlob(owner *budget, data []byte) (*valueBlob, error) {
	charge, ok := addCharge(blobOverhead, uint64(len(data)))
	if !ok || !owner.reserveMemory(charge) {
		return nil, ErrMemoryBudget
	}
	copyData := make([]byte, len(data))
	copy(copyData, data)
	b := &valueBlob{data: copyData, owner: owner, charge: charge}
	b.refs.Store(1)
	return b, nil
}

func retainBlob(b *valueBlob) *valueBlob {
	if b != nil {
		b.refs.Add(1)
	}
	return b
}

func releaseBlob(b *valueBlob) {
	if b == nil || b.refs.Add(-1) != 0 {
		return
	}
	b.data = nil
	if b.owner != nil {
		b.owner.releaseMemory(b.charge)
	}
}

type treeNode struct {
	refs atomic.Int64

	key     [32]byte
	value   *valueBlob
	deleted bool
	left    *treeNode
	right   *treeNode
	height  int8
	owner   *budget
	charge  uint64
}

func addCharge(a, b uint64) (uint64, bool) {
	if math.MaxUint64-a < b {
		return 0, false
	}
	return a + b, true
}

func nodeHeight(n *treeNode) int8 {
	if n == nil {
		return 0
	}
	return n.height
}

func maxHeight(a, b int8) int8 {
	if a > b {
		return a
	}
	return b
}

// makeTreeNode increments references to all supplied children and value. The
// returned node owns one reference; callers release temporary child/blob
// references after construction when they supplied newly created objects.
func makeTreeNode(owner *budget, key [32]byte, value *valueBlob, deleted bool, left, right *treeNode) (*treeNode, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: nil value blob", ErrCorruptSegment)
	}
	charge := nodeOverhead
	if !owner.reserveMemory(charge) {
		return nil, ErrMemoryBudget
	}
	n := &treeNode{
		key: key, value: retainBlob(value), deleted: deleted,
		left: retainTreeNode(left), right: retainTreeNode(right), owner: owner,
		charge: charge,
	}
	n.refs.Store(1)
	n.height = maxHeight(nodeHeight(left), nodeHeight(right)) + 1
	return n, nil
}

func retainTreeNode(n *treeNode) *treeNode {
	if n != nil {
		n.refs.Add(1)
	}
	return n
}

func releaseTreeNode(n *treeNode) {
	if n == nil || n.refs.Add(-1) != 0 {
		return
	}
	left, right, value, owner, charge := n.left, n.right, n.value, n.owner, n.charge
	n.left, n.right, n.value = nil, nil, nil
	releaseTreeNode(left)
	releaseTreeNode(right)
	releaseBlob(value)
	if owner != nil {
		owner.releaseMemory(charge)
	}
}

func makeLeaf(owner *budget, key [32]byte, value []byte, deleted bool) (*treeNode, error) {
	blob, err := newValueBlob(owner, value)
	if err != nil {
		return nil, err
	}
	n, err := makeTreeNode(owner, key, blob, deleted, nil, nil)
	releaseBlob(blob)
	return n, err
}

func sameTreeValue(n *treeNode, value []byte, deleted bool) bool {
	return n.deleted == deleted && bytes.Equal(n.value.data, value)
}

func cloneTreeNode(owner *budget, old *treeNode, left, right *treeNode, value *valueBlob, deleted bool) (*treeNode, error) {
	if value == nil {
		value = old.value
	}
	return makeTreeNode(owner, old.key, value, deleted, left, right)
}

func balanceFactor(n *treeNode) int8 {
	if n == nil {
		return 0
	}
	return nodeHeight(n.left) - nodeHeight(n.right)
}

func rotateRight(n *treeNode) (*treeNode, error) {
	left := n.left
	if left == nil {
		return n, nil
	}
	right, err := cloneTreeNode(n.owner, n, left.right, n.right, nil, n.deleted)
	if err != nil {
		return nil, err
	}
	root, err := cloneTreeNode(n.owner, left, left.left, right, nil, left.deleted)
	if err != nil {
		releaseTreeNode(right)
		return nil, err
	}
	releaseTreeNode(right)
	releaseTreeNode(n)
	return root, nil
}

func rotateLeft(n *treeNode) (*treeNode, error) {
	right := n.right
	if right == nil {
		return n, nil
	}
	left, err := cloneTreeNode(n.owner, n, n.left, right.left, nil, n.deleted)
	if err != nil {
		return nil, err
	}
	root, err := cloneTreeNode(n.owner, right, left, right.right, nil, right.deleted)
	if err != nil {
		releaseTreeNode(left)
		return nil, err
	}
	releaseTreeNode(left)
	releaseTreeNode(n)
	return root, nil
}

func rebalanceTreeNode(n *treeNode) (*treeNode, error) {
	factor := balanceFactor(n)
	if factor > 1 {
		if balanceFactor(n.left) < 0 {
			rotating := retainTreeNode(n.left)
			rotated, err := rotateLeft(rotating)
			if err != nil {
				releaseTreeNode(rotating)
				return nil, err
			}
			// rotateLeft consumed its input reference. Replace the child while
			// preserving the caller's ownership of n.
			copyNode, err := cloneTreeNode(n.owner, n, rotated, n.right, nil, n.deleted)
			releaseTreeNode(rotated)
			if err != nil {
				return nil, err
			}
			releaseTreeNode(n)
			n = copyNode
		}
		return rotateRight(n)
	}
	if factor < -1 {
		if balanceFactor(n.right) > 0 {
			rotating := retainTreeNode(n.right)
			rotated, err := rotateRight(rotating)
			if err != nil {
				releaseTreeNode(rotating)
				return nil, err
			}
			copyNode, err := cloneTreeNode(n.owner, n, n.left, rotated, nil, n.deleted)
			releaseTreeNode(rotated)
			if err != nil {
				return nil, err
			}
			releaseTreeNode(n)
			n = copyNode
		}
		return rotateLeft(n)
	}
	return n, nil
}

func rotateRightExclusive(n *treeNode) *treeNode {
	left := n.left
	if left == nil {
		return retainTreeNode(n)
	}
	retainTreeNode(n)
	n.left = left.right
	left.right = n
	n.height = maxHeight(nodeHeight(n.left), nodeHeight(n.right)) + 1
	left.height = maxHeight(nodeHeight(left.left), nodeHeight(left.right)) + 1
	return left
}

func rotateLeftExclusive(n *treeNode) *treeNode {
	right := n.right
	if right == nil {
		return retainTreeNode(n)
	}
	retainTreeNode(n)
	n.right = right.left
	right.left = n
	n.height = maxHeight(nodeHeight(n.left), nodeHeight(n.right)) + 1
	right.height = maxHeight(nodeHeight(right.left), nodeHeight(right.right)) + 1
	return right
}

func rebalanceExclusive(n *treeNode) *treeNode {
	n.height = maxHeight(nodeHeight(n.left), nodeHeight(n.right)) + 1
	factor := balanceFactor(n)
	if factor > 1 {
		if balanceFactor(n.left) < 0 {
			oldLeft := n.left
			n.left = rotateLeftExclusive(oldLeft)
			releaseTreeNode(oldLeft)
		}
		return rotateRightExclusive(n)
	}
	if factor < -1 {
		if balanceFactor(n.right) > 0 {
			oldRight := n.right
			n.right = rotateRightExclusive(oldRight)
			releaseTreeNode(oldRight)
		}
		return rotateLeftExclusive(n)
	}
	return retainTreeNode(n)
}

// An insertion can require rebalancing only when a changed child grew. The
// pivot is then on that changed path and is private or newly allocated. Value
// updates and tombstones do not change heights, so no fallible operation can
// follow an in-place mutation here.
func insertTreeNodeCOW(owner *budget, old *treeNode, key [32]byte, value []byte, deleted, exclusive bool) (*treeNode, bool, error) {
	if old == nil {
		n, err := makeLeaf(owner, key, value, deleted)
		return n, err == nil, err
	}
	exclusive = exclusive && old.refs.Load() == 1
	if key == old.key {
		if sameTreeValue(old, value, deleted) {
			return nil, false, nil
		}
		blob, err := newValueBlob(owner, value)
		if err != nil {
			return nil, false, err
		}
		if exclusive {
			oldValue := old.value
			old.value = blob
			old.deleted = deleted
			releaseBlob(oldValue)
			return retainTreeNode(old), true, nil
		}
		n, err := cloneTreeNode(owner, old, old.left, old.right, blob, deleted)
		releaseBlob(blob)
		return n, err == nil, err
	}
	leftBranch := bytes.Compare(key[:], old.key[:]) < 0
	oldChild := old.right
	if leftBranch {
		oldChild = old.left
	}
	child, changed, err := insertTreeNodeCOW(owner, oldChild, key, value, deleted, exclusive)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return nil, false, nil
	}
	if exclusive {
		if leftBranch {
			old.left = child
		} else {
			old.right = child
		}
		releaseTreeNode(oldChild)
		return rebalanceExclusive(old), true, nil
	}
	left, right := old.left, child
	if leftBranch {
		left, right = child, old.right
	}
	n, err := cloneTreeNode(owner, old, left, right, nil, old.deleted)
	releaseTreeNode(child)
	if err != nil {
		return nil, false, err
	}
	balanced, err := rebalanceTreeNode(n)
	if err != nil {
		releaseTreeNode(n)
		return nil, false, err
	}
	return balanced, true, nil
}

func insertTreeNode(owner *budget, old *treeNode, key [32]byte, value []byte, deleted bool) (*treeNode, error) {
	updated, changed, err := insertTreeNodeCOW(owner, old, key, value, deleted, true)
	if err != nil {
		return nil, err
	}
	if !changed {
		return retainTreeNode(old), nil
	}
	return updated, nil
}

func findTreeNode(root *treeNode, key [32]byte) *treeNode {
	for root != nil {
		if key == root.key {
			return root
		}
		if bytes.Compare(key[:], root.key[:]) < 0 {
			root = root.left
		} else {
			root = root.right
		}
	}
	return nil
}

type treeCursor struct {
	stack []*treeNode
}

func newTreeCursor(root *treeNode, after *[32]byte) *treeCursor {
	c := &treeCursor{stack: make([]*treeNode, 0, int(nodeHeight(root))+1)}
	c.pushFirst(root, after)
	return c
}

func (c *treeCursor) pushFirst(root *treeNode, after *[32]byte) {
	for root != nil {
		if after == nil || bytes.Compare(root.key[:], after[:]) > 0 {
			c.stack = append(c.stack, root)
			root = root.left
		} else {
			root = root.right
		}
	}
}

func (c *treeCursor) next() (key [32]byte, value []byte, deleted bool, ok bool) {
	if len(c.stack) == 0 {
		return key, nil, false, false
	}
	n := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	c.pushFirst(n.right, nil)
	return n.key, n.value.data, n.deleted, true
}
