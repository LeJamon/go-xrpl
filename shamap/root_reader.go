package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// RootReader provides immutable, non-retaining access to a SHAMap root.
//
// The reader keeps only the root hash and Family. Every operation fetches and
// decodes the nodes it needs, and releases those decoded nodes before moving
// to the next path. Returned Items and callback Items are independent copies.
type RootReader struct {
	mapType Type
	root    [32]byte
	access  *familyAccess
	budget  CommitmentBudget
}

// NewRootReader opens an immutable reader for root. A zero root represents an
// empty map and does not require a Family. A non-zero root is fetched lazily
// by each operation, so constructing a reader never retains a decoded tree.
func NewRootReader(mapType Type, root [32]byte, family Family, options ...CommitmentOption) *RootReader {
	settings := commitmentOptions{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	return &RootReader{
		mapType: mapType,
		root:    root,
		access:  bindFamily(family),
		budget:  settings.budget,
	}
}

type rootReaderNode struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	leafKey  [32]byte
	leafData []byte
	leafType NodeType
	charge   uint64
}

func (r *RootReader) read(ctx context.Context, hash [32]byte) (rootReaderNode, error) {
	ctx = normalizeCommitmentContext(ctx)
	if err := ctx.Err(); err != nil {
		return rootReaderNode{}, err
	}
	if isZeroHash(hash) {
		return rootReaderNode{}, nil
	}
	if r == nil || r.access == nil || !r.access.available() {
		return rootReaderNode{}, fmt.Errorf("shamap: root reader has no backing Family")
	}
	data, err := r.access.fetch(ctx, hash)
	if err != nil {
		return rootReaderNode{}, fmt.Errorf("shamap: fetch node %x: %w", hash[:8], err)
	}
	if len(data) == 0 {
		return rootReaderNode{}, fmt.Errorf("shamap: fetch node %x: %w", hash[:8], &MissingNodeError{Hash: hash})
	}
	charge := commitmentNodeCharge(data)
	if err := reserveCommitmentMemory(ctx, r.budget, charge); err != nil {
		return rootReaderNode{}, err
	}
	node, err := decodeCommitmentNode(data, hash)
	if err != nil {
		releaseCommitmentMemory(r.budget, charge)
		return rootReaderNode{}, fmt.Errorf("shamap: decode node %x: %w", hash[:8], err)
	}

	if node.inner {
		return rootReaderNode{
			inner:    true,
			branches: node.branches,
			hashes:   node.hashes,
			charge:   charge,
		}, nil
	}
	if err := validateCommitmentLeafType(r.mapType, node.leafType); err != nil {
		releaseCommitmentMemory(r.budget, charge)
		return rootReaderNode{}, err
	}
	return rootReaderNode{
		leafKey:  node.key,
		leafData: bytes.Clone(node.data),
		leafType: node.leafType,
		charge:   charge,
	}, nil
}

func (n *rootReaderNode) release(budget CommitmentBudget) {
	if n == nil || n.charge == 0 || budget == nil {
		return
	}
	budget.Release(n.charge)
	n.charge = 0
}

func (n rootReaderNode) childHash(branch int) ([32]byte, bool) {
	if !n.inner || branch < 0 || branch >= BranchFactor || n.branches&(1<<branch) == 0 {
		return [32]byte{}, false
	}
	return n.hashes[branch], true
}

// GetLeaf returns the item and its authenticated SHAMap leaf type at key. The
// bool is false when key is absent.
func (r *RootReader) GetLeaf(ctx context.Context, key [32]byte) (*Item, NodeType, bool, error) {
	ctx = normalizeCommitmentContext(ctx)
	if r == nil || isZeroHash(r.root) {
		return nil, NodeType(0), false, nil
	}
	node, err := r.read(ctx, r.root)
	if err != nil {
		return nil, NodeType(0), false, err
	}
	defer node.release(r.budget)
	if !node.inner {
		return nil, NodeType(0), false, fmt.Errorf("shamap: root %x is not an inner node", r.root[:8])
	}
	depth := 0
	for node.inner {
		if err := ctx.Err(); err != nil {
			return nil, NodeType(0), false, err
		}
		if depth >= maxDepth {
			return nil, NodeType(0), false, fmt.Errorf("shamap: root reader exceeded maximum depth")
		}
		branch := getBranchAtDepth(key, depth)
		hash, ok := node.childHash(branch)
		if !ok {
			node.release(r.budget)
			return nil, NodeType(0), false, nil
		}
		old := node
		node, err = r.read(ctx, hash)
		old.release(r.budget)
		if err != nil {
			return nil, NodeType(0), false, err
		}
		depth++
	}
	if err := validateCommitmentKeyPath(node.leafKey, key, depth); err != nil {
		node.release(r.budget)
		return nil, NodeType(0), false, err
	}
	if node.leafKey != key {
		node.release(r.budget)
		return nil, NodeType(0), false, nil
	}
	item := NewItem(node.leafKey, node.leafData)
	node.release(r.budget)
	return item, node.leafType, true, nil
}

// IterateLeaves visits leaves in strictly ascending key order. When after is
// non-nil, leaves at or below *after are skipped by descending directly to its
// key path; unrelated lower subtrees are never fetched. Returning false stops
// the walk without an error.
func (r *RootReader) IterateLeaves(ctx context.Context, after *[32]byte, fn func(*Item, NodeType) bool) error {
	ctx = normalizeCommitmentContext(ctx)
	if fn == nil {
		return errors.New("shamap: nil root reader callback")
	}
	if r == nil || isZeroHash(r.root) {
		return nil
	}
	hasAfter := after != nil
	var afterKey [32]byte
	if hasAfter {
		afterKey = *after
	}
	root, err := r.read(ctx, r.root)
	if err != nil {
		return err
	}
	if !root.inner {
		root.release(r.budget)
		return fmt.Errorf("shamap: root %x is not an inner node", r.root[:8])
	}
	stack := make([]iterateFrame, 0, maxDepth)
	initial := iterateFrame{node: root, path: [32]byte{}, owned: true}
	if hasAfter {
		initial.seek = true
		initial.branch = getBranchAtDepth(afterKey, 0)
	}
	stack = append(stack, initial)
	defer func() {
		for i := range stack {
			if stack[i].owned {
				stack[i].node.release(r.budget)
			}
		}
	}()
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		top := &stack[len(stack)-1]
		for top.branch < BranchFactor {
			branch := top.branch
			top.branch++
			if top.seek && branch < getBranchAtDepth(afterKey, top.depth) {
				continue
			}
			hash, ok := top.node.childHash(branch)
			if !ok {
				continue
			}
			child, err := r.read(ctx, hash)
			if err != nil {
				return err
			}
			childPath := childPathForBranch(top.path, top.depth, branch)
			childDepth := top.depth + 1
			if child.inner {
				if childDepth >= maxDepth {
					child.release(r.budget)
					return fmt.Errorf("shamap: root reader exceeded maximum depth")
				}
				childSeek := top.seek && branch == getBranchAtDepth(afterKey, top.depth)
				frame := iterateFrame{node: child, path: childPath, depth: childDepth, seek: childSeek, owned: true}
				if childSeek {
					frame.branch = getBranchAtDepth(afterKey, frame.depth)
				}
				stack = append(stack, frame)
				top = &stack[len(stack)-1]
				continue
			}
			if err := validateCommitmentKeyPath(child.leafKey, childPath, childDepth); err != nil {
				child.release(r.budget)
				return err
			}
			item := NewItem(child.leafKey, child.leafData)
			childType := child.leafType
			child.release(r.budget)
			if hasAfter && top.seek {
				itemKey := item.Key()
				if bytes.Compare(itemKey[:], afterKey[:]) <= 0 {
					continue
				}
			}
			if !fn(item, childType) {
				return nil
			}
		}
		stack[len(stack)-1].node.release(r.budget)
		stack = stack[:len(stack)-1]
	}
	return nil
}

type iterateFrame struct {
	node   rootReaderNode
	path   [32]byte
	branch int
	depth  int
	seek   bool
	owned  bool
}

// Count returns the number of leaves below the root. It uses one bounded DFS
// and does not retain decoded nodes after the operation returns.
func (r *RootReader) Count(ctx context.Context) (uint64, error) {
	var count uint64
	err := r.IterateLeaves(ctx, nil, func(*Item, NodeType) bool {
		count++
		return true
	})
	return count, err
}

// Diff emits the sorted final changes needed to transform this root into
// targetRoot. Equal subtree hashes are skipped without decoding either side.
// It retains only the current pair of nodes and a bounded traversal frontier;
// Change.Data is owned by the callback and can be retained after return.
func (r *RootReader) Diff(ctx context.Context, targetRoot [32]byte, emit func(Change) error) error {
	ctx = normalizeCommitmentContext(ctx)
	if emit == nil {
		return errors.New("shamap: nil root diff callback")
	}
	if r == nil {
		if isZeroHash(targetRoot) {
			return nil
		}
		return errors.New("shamap: nil root reader")
	}
	return r.diffNodes(ctx, r.root, targetRoot, [32]byte{}, 0, emit)
}

func (r *RootReader) diffNodes(ctx context.Context, oldHash, newHash, path [32]byte, depth int, emit func(Change) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if oldHash == newHash {
		return nil
	}
	if depth > maxDepth {
		return errors.New("shamap: root diff exceeded maximum depth")
	}

	var (
		oldNode rootReaderNode
		newNode rootReaderNode
		err     error
	)
	if !isZeroHash(oldHash) {
		oldNode, err = r.read(ctx, oldHash)
		if err != nil {
			return err
		}
		defer oldNode.release(r.budget)
	}
	if !isZeroHash(newHash) {
		newNode, err = r.read(ctx, newHash)
		if err != nil {
			return err
		}
		defer newNode.release(r.budget)
	}
	if depth == 0 && ((!isZeroHash(oldHash) && !oldNode.inner) || (!isZeroHash(newHash) && !newNode.inner)) {
		return errors.New("shamap: root diff root is not an inner node")
	}
	if oldNode.inner || newNode.inner {
		if depth >= maxDepth {
			return fmt.Errorf("shamap: root diff exceeded maximum depth")
		}
	} else {
		if err := validateCommitmentKeyPath(oldNode.leafKey, path, depth); err != nil && !isZeroHash(oldHash) {
			return err
		}
		if err := validateCommitmentKeyPath(newNode.leafKey, path, depth); err != nil && !isZeroHash(newHash) {
			return err
		}
	}

	switch {
	case isZeroHash(oldHash):
		return r.walkNode(ctx, newNode, path, depth, func(item *Item, nodeType NodeType) error {
			return emitRootChange(ctx, emit, item, nodeType, false)
		})
	case isZeroHash(newHash):
		return r.walkNode(ctx, oldNode, path, depth, func(item *Item, nodeType NodeType) error {
			return emitRootChange(ctx, emit, item, nodeType, true)
		})
	case !oldNode.inner && !newNode.inner:
		return diffReaderLeaves(ctx, emit, oldNode, newNode, path, depth)
	case oldNode.inner && newNode.inner:
		if depth >= maxDepth {
			return errors.New("shamap: root diff exceeded maximum depth")
		}
		for branch := 0; branch < BranchFactor; branch++ {
			oldChild, oldSet := oldNode.childHash(branch)
			newChild, newSet := newNode.childHash(branch)
			if !oldSet {
				oldChild = [32]byte{}
			}
			if !newSet {
				newChild = [32]byte{}
			}
			if oldChild == newChild {
				continue
			}
			childPath := childPathForBranch(path, depth, branch)
			if err := r.diffNodes(ctx, oldChild, newChild, childPath, depth+1, emit); err != nil {
				return err
			}
		}
		return nil
	case !oldNode.inner:
		return r.diffLeafAndSubtree(ctx, oldNode, newNode, path, depth, true, emit)
	default:
		return r.diffLeafAndSubtree(ctx, newNode, oldNode, path, depth, false, emit)
	}
}

type diffWalkFrame struct {
	node   rootReaderNode
	path   [32]byte
	depth  int
	branch int
	owned  bool
}

// walkNode visits an already decoded subtree in ascending key order. The root
// view is borrowed from its caller; descendants fetched during the walk are
// released as soon as their branch is complete.
func (r *RootReader) walkNode(ctx context.Context, root rootReaderNode, path [32]byte, depth int, emit func(*Item, NodeType) error) error {
	stack := []diffWalkFrame{{node: root, path: path, depth: depth}}
	defer func() {
		for i := range stack {
			if stack[i].owned {
				stack[i].node.release(r.budget)
			}
		}
	}()
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		top := &stack[len(stack)-1]
		if !top.node.inner {
			if err := validateCommitmentKeyPath(top.node.leafKey, top.path, top.depth); err != nil {
				return err
			}
			item := NewItem(top.node.leafKey, top.node.leafData)
			nodeType := top.node.leafType
			if top.owned {
				top.node.release(r.budget)
			}
			stack = stack[:len(stack)-1]
			if err := emit(item, nodeType); err != nil {
				return err
			}
			continue
		}
		if top.branch >= BranchFactor {
			if top.owned {
				top.node.release(r.budget)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		branch := top.branch
		top.branch++
		if top.depth >= maxDepth {
			return errors.New("shamap: root diff exceeded maximum depth")
		}
		hash, ok := top.node.childHash(branch)
		if !ok {
			continue
		}
		child, err := r.read(ctx, hash)
		if err != nil {
			return err
		}
		childPath := childPathForBranch(top.path, top.depth, branch)
		childDepth := top.depth + 1
		if child.inner && childDepth >= maxDepth {
			child.release(r.budget)
			return errors.New("shamap: root diff exceeded maximum depth")
		}
		stack = append(stack, diffWalkFrame{node: child, path: childPath, depth: childDepth, owned: true})
	}
	return nil
}

func emitRootChange(ctx context.Context, emit func(Change) error, item *Item, nodeType NodeType, deleted bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	change := Change{Key: item.Key(), Delete: deleted, Type: nodeType}
	if !deleted {
		change.Data = item.Data()
	}
	return emit(change)
}

func diffReaderLeaves(ctx context.Context, emit func(Change) error, oldNode, newNode rootReaderNode, path [32]byte, depth int) error {
	if err := validateCommitmentKeyPath(oldNode.leafKey, path, depth); err != nil {
		return err
	}
	if err := validateCommitmentKeyPath(newNode.leafKey, path, depth); err != nil {
		return err
	}
	oldItem := NewItem(oldNode.leafKey, oldNode.leafData)
	newItem := NewItem(newNode.leafKey, newNode.leafData)
	oldKey := oldItem.Key()
	newKey := newItem.Key()
	if oldKey != newKey {
		if bytes.Compare(oldKey[:], newKey[:]) < 0 {
			if err := emitRootChange(ctx, emit, oldItem, oldNode.leafType, true); err != nil {
				return err
			}
			return emitRootChange(ctx, emit, newItem, newNode.leafType, false)
		}
		if err := emitRootChange(ctx, emit, newItem, newNode.leafType, false); err != nil {
			return err
		}
		return emitRootChange(ctx, emit, oldItem, oldNode.leafType, true)
	}
	if oldNode.leafType == newNode.leafType && bytes.Equal(oldItem.dataBytes(), newItem.dataBytes()) {
		return nil
	}
	return emitRootChange(ctx, emit, newItem, newNode.leafType, false)
}

// diffLeafAndSubtree compares one leaf with the leaves under the other node.
// If leafIsOld is true, the leaf is removed/replaced and the subtree is the
// target; otherwise the subtree is the old side and the leaf is the target.
func (r *RootReader) diffLeafAndSubtree(ctx context.Context, leafNode, subtreeNode rootReaderNode, path [32]byte, depth int, leafIsOld bool, emit func(Change) error) error {
	if err := validateCommitmentKeyPath(leafNode.leafKey, path, depth); err != nil {
		return err
	}
	leaf := NewItem(leafNode.leafKey, leafNode.leafData)
	leafKey := leaf.Key()
	matched := false
	err := r.walkNode(ctx, subtreeNode, path, depth, func(item *Item, nodeType NodeType) error {
		if matched {
			if leafIsOld {
				return emitRootChange(ctx, emit, item, nodeType, false)
			}
			return emitRootChange(ctx, emit, item, nodeType, true)
		}
		itemKey := item.Key()
		cmp := bytes.Compare(itemKey[:], leafKey[:])
		if cmp == 0 {
			matched = true
			if leafIsOld {
				if leafNode.leafType == nodeType && bytes.Equal(leaf.dataBytes(), item.dataBytes()) {
					return nil
				}
				return emitRootChange(ctx, emit, item, nodeType, false)
			}
			if leafNode.leafType == nodeType && bytes.Equal(leaf.dataBytes(), item.dataBytes()) {
				return nil
			}
			return emitRootChange(ctx, emit, leaf, leafNode.leafType, false)
		}
		if leafIsOld {
			if cmp < 0 {
				return emitRootChange(ctx, emit, item, nodeType, false)
			}
			matched = true
			if err := emitRootChange(ctx, emit, leaf, leafNode.leafType, true); err != nil {
				return err
			}
			return emitRootChange(ctx, emit, item, nodeType, false)
		}
		if cmp < 0 {
			return emitRootChange(ctx, emit, item, nodeType, true)
		}
		matched = true
		if err := emitRootChange(ctx, emit, leaf, leafNode.leafType, false); err != nil {
			return err
		}
		return emitRootChange(ctx, emit, item, nodeType, true)
	})
	if err != nil {
		return err
	}
	if matched {
		return nil
	}
	return emitRootChange(ctx, emit, leaf, leafNode.leafType, leafIsOld)
}
