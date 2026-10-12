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
// to the next path. Callers own the Items returned by Get and Succ; callback
// Items passed to ForEach are independent copies.
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

// RootHash returns the authenticated root selected when the reader was made.
func (r *RootReader) RootHash() [32]byte {
	if r == nil {
		return [32]byte{}
	}
	return r.root
}

// Type returns the SHAMap type selected when the reader was made.
func (r *RootReader) Type() Type {
	if r == nil {
		return Type(0)
	}
	return r.mapType
}

type rootReaderNode struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	leaf     *Item
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
	node, err := decodeAndVerifyPrefixNode(data, hash)
	if err != nil {
		releaseCommitmentMemory(r.budget, charge)
		return rootReaderNode{}, fmt.Errorf("shamap: decode node %x: %w", hash[:8], err)
	}

	switch n := node.(type) {
	case *innerNode:
		n.mu.RLock()
		view := rootReaderNode{
			inner:    true,
			branches: n.isBranch,
			hashes:   n.hashes,
			charge:   charge,
		}
		n.mu.RUnlock()
		return view, nil
	case *leafNode:
		item := n.Item()
		if item == nil {
			releaseCommitmentMemory(r.budget, charge)
			return rootReaderNode{}, errors.New("shamap: decoded leaf has no item")
		}
		return rootReaderNode{
			leaf:     NewItem(item.Key(), item.dataBytes()),
			leafType: n.Type(),
			charge:   charge,
		}, nil
	default:
		releaseCommitmentMemory(r.budget, charge)
		return rootReaderNode{}, fmt.Errorf("shamap: unsupported node type %T", node)
	}
}

func (n *rootReaderNode) release(budget CommitmentBudget) {
	if n == nil || n.charge == 0 || budget == nil {
		return
	}
	budget.Release(n.charge)
	n.charge = 0
}

func (n rootReaderNode) isEmpty() bool {
	return !n.inner && n.leaf == nil
}

func (n rootReaderNode) childHash(branch int) ([32]byte, bool) {
	if !n.inner || branch < 0 || branch >= BranchFactor || n.branches&(1<<branch) == 0 {
		return [32]byte{}, false
	}
	return n.hashes[branch], true
}

func cloneReaderItem(item *Item) *Item {
	if item == nil {
		return nil
	}
	return NewItem(item.Key(), item.Data())
}

// Get returns the item at key. The bool is false when key is absent.
func (r *RootReader) Get(ctx context.Context, key [32]byte) (*Item, bool, error) {
	item, _, found, err := r.getLeaf(ctx, key)
	return item, found, err
}

// GetLeaf returns the item and its authenticated SHAMap leaf type at key. The
// bool is false when key is absent.
func (r *RootReader) GetLeaf(ctx context.Context, key [32]byte) (*Item, NodeType, bool, error) {
	return r.getLeaf(ctx, key)
}

func (r *RootReader) getLeaf(ctx context.Context, key [32]byte) (*Item, NodeType, bool, error) {
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
	for depth := 0; node.inner; depth++ {
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
	}
	if node.leaf == nil || node.leaf.Key() != key {
		node.release(r.budget)
		return nil, NodeType(0), false, nil
	}
	item := cloneReaderItem(node.leaf)
	node.release(r.budget)
	return item, node.leafType, true, nil
}

// ForEach visits every item in ascending key order. Returning false stops the
// walk without an error. The traversal keeps at most one decoded inner node
// per SHAMap depth and never attaches a fetched node to a retained tree.
func (r *RootReader) ForEach(ctx context.Context, fn func(*Item) bool) error {
	if fn == nil {
		return errors.New("shamap: nil root reader callback")
	}
	return r.forEachLeaf(ctx, func(item *Item, _ NodeType) bool {
		return fn(item)
	})
}

// ForEachLeaf visits every item and its authenticated SHAMap leaf type in
// ascending key order. Returning false stops the walk without an error.
func (r *RootReader) ForEachLeaf(ctx context.Context, fn func(*Item, NodeType) bool) error {
	if fn == nil {
		return errors.New("shamap: nil root reader callback")
	}
	return r.forEachLeaf(ctx, fn)
}

func (r *RootReader) forEachLeaf(ctx context.Context, fn func(*Item, NodeType) bool) error {
	return r.IterateLeaves(ctx, nil, fn)
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
	initial := iterateFrame{node: root, owned: true}
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
			if child.inner {
				if len(stack) >= maxDepth {
					child.release(r.budget)
					return fmt.Errorf("shamap: root reader exceeded maximum depth")
				}
				childSeek := top.seek && branch == getBranchAtDepth(afterKey, top.depth)
				frame := iterateFrame{node: child, depth: top.depth + 1, seek: childSeek, owned: true}
				if childSeek {
					frame.branch = getBranchAtDepth(afterKey, frame.depth)
				}
				stack = append(stack, frame)
				top = &stack[len(stack)-1]
				continue
			}
			if child.leaf == nil {
				child.release(r.budget)
				return errors.New("shamap: decoded branch has no leaf or inner node")
			}
			item := cloneReaderItem(child.leaf)
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
	branch int
	depth  int
	seek   bool
	owned  bool
}

// Count returns the number of leaves below the root. It uses one bounded DFS
// and does not retain decoded nodes after the operation returns.
func (r *RootReader) Count(ctx context.Context) (uint64, error) {
	var count uint64
	err := r.ForEach(ctx, func(*Item) bool {
		count++
		return true
	})
	return count, err
}

func (r *RootReader) minimum(ctx context.Context, node rootReaderNode) (*Item, bool, error) {
	ctx = normalizeCommitmentContext(ctx)
	defer node.release(r.budget)
	for node.inner {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		found := false
		for branch := 0; branch < BranchFactor; branch++ {
			hash, ok := node.childHash(branch)
			if !ok {
				continue
			}
			next, err := r.read(ctx, hash)
			if err != nil {
				return nil, false, err
			}
			old := node
			node = next
			old.release(r.budget)
			found = true
			break
		}
		if !found {
			return nil, false, nil
		}
	}
	if node.leaf == nil {
		return nil, false, nil
	}
	return cloneReaderItem(node.leaf), true, nil
}

func (r *RootReader) successor(ctx context.Context, node rootReaderNode, depth int, key [32]byte) (*Item, bool, error) {
	ctx = normalizeCommitmentContext(ctx)
	defer node.release(r.budget)
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !node.inner {
		if node.leaf != nil {
			leafKey := node.leaf.Key()
			if bytes.Compare(leafKey[:], key[:]) > 0 {
				return cloneReaderItem(node.leaf), true, nil
			}
		}
		return nil, false, nil
	}
	if depth >= maxDepth {
		return nil, false, fmt.Errorf("shamap: root reader exceeded maximum depth")
	}
	branch := getBranchAtDepth(key, depth)
	if hash, ok := node.childHash(branch); ok {
		child, err := r.read(ctx, hash)
		if err != nil {
			return nil, false, err
		}
		if item, found, err := r.successor(ctx, child, depth+1, key); err != nil || found {
			return item, found, err
		}
	}
	for branch++; branch < BranchFactor; branch++ {
		hash, ok := node.childHash(branch)
		if !ok {
			continue
		}
		child, err := r.read(ctx, hash)
		if err != nil {
			return nil, false, err
		}
		return r.minimum(ctx, child)
	}
	return nil, false, nil
}

// Succ returns the smallest item whose key is strictly greater than key.
func (r *RootReader) Succ(ctx context.Context, key [32]byte) (*Item, bool, error) {
	ctx = normalizeCommitmentContext(ctx)
	if r == nil || isZeroHash(r.root) {
		return nil, false, nil
	}
	root, err := r.read(ctx, r.root)
	if err != nil {
		return nil, false, err
	}
	if !root.inner {
		root.release(r.budget)
		return nil, false, fmt.Errorf("shamap: root %x is not an inner node", r.root[:8])
	}
	return r.successor(ctx, root, 0, key)
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
	return r.diffNodes(ctx, r.root, targetRoot, 0, emit)
}

func (r *RootReader) diffNodes(ctx context.Context, oldHash, newHash [32]byte, depth int, emit func(Change) error) error {
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

	switch {
	case isZeroHash(oldHash):
		return r.walkNode(ctx, newNode, func(item *Item, nodeType NodeType) error {
			return emitRootChange(ctx, emit, item, nodeType, false)
		})
	case isZeroHash(newHash):
		return r.walkNode(ctx, oldNode, func(item *Item, nodeType NodeType) error {
			return emitRootChange(ctx, emit, item, nodeType, true)
		})
	case !oldNode.inner && !newNode.inner:
		return diffReaderLeaves(ctx, emit, oldNode, newNode)
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
			if err := r.diffNodes(ctx, oldChild, newChild, depth+1, emit); err != nil {
				return err
			}
		}
		return nil
	case !oldNode.inner:
		return r.diffLeafAndSubtree(ctx, oldNode, newNode, true, emit)
	default:
		return r.diffLeafAndSubtree(ctx, newNode, oldNode, false, emit)
	}
}

type diffWalkFrame struct {
	node   rootReaderNode
	branch int
	owned  bool
}

// walkNode visits an already decoded subtree in ascending key order. The root
// view is borrowed from its caller; descendants fetched during the walk are
// released as soon as their branch is complete.
func (r *RootReader) walkNode(ctx context.Context, root rootReaderNode, emit func(*Item, NodeType) error) error {
	stack := []diffWalkFrame{{node: root}}
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
			if top.node.leaf == nil {
				return errors.New("shamap: root diff decoded branch has no leaf")
			}
			item := cloneReaderItem(top.node.leaf)
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
		hash, ok := top.node.childHash(branch)
		if !ok {
			continue
		}
		child, err := r.read(ctx, hash)
		if err != nil {
			return err
		}
		if child.inner && len(stack) >= maxDepth {
			child.release(r.budget)
			return errors.New("shamap: root diff exceeded maximum depth")
		}
		stack = append(stack, diffWalkFrame{node: child, owned: true})
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

func diffReaderLeaves(ctx context.Context, emit func(Change) error, oldNode, newNode rootReaderNode) error {
	if oldNode.leaf == nil || newNode.leaf == nil {
		return errors.New("shamap: root diff decoded leaf is empty")
	}
	oldItem := oldNode.leaf
	newItem := newNode.leaf
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
func (r *RootReader) diffLeafAndSubtree(ctx context.Context, leafNode, subtreeNode rootReaderNode, leafIsOld bool, emit func(Change) error) error {
	if leafNode.leaf == nil {
		return errors.New("shamap: root diff decoded leaf is empty")
	}
	leaf := leafNode.leaf
	leafKey := leaf.Key()
	matched := false
	err := r.walkNode(ctx, subtreeNode, func(item *Item, nodeType NodeType) error {
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
