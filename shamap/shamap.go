package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Common errors
var (
	ErrImmutable       = errors.New("cannot modify immutable SHAMap")
	ErrNilItem         = errors.New("cannot add nil item")
	ErrItemNotFound    = errors.New("item not found")
	ErrInvalidType     = errors.New("invalid node type")
	ErrNodeNotFound    = errors.New("node not found while traversing tree")
	ErrMaxDepthReached = errors.New("maximum tree depth reached")
	ErrInvalidState    = errors.New("invalid state for operation")
	ErrUnknownNodeType = errors.New("unknown node type")
	ErrItemTooSmall    = errors.New("item data too small (minimum 12 bytes)")
)

// State defines the state of the SHAMap
type State int

const (
	StateModifying State = iota
	StateImmutable
	StateSyncing
	StateInvalid
)

// String returns a string representation of the state
func (s State) String() string {
	switch s {
	case StateModifying:
		return "modifying"
	case StateImmutable:
		return "immutable"
	case StateSyncing:
		return "syncing"
	case StateInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Type defines the SHAMap type
type Type int

const (
	TypeTransaction Type = iota
	TypeState
)

// String returns a string representation of the type
func (t Type) String() string {
	switch t {
	case TypeTransaction:
		return "transaction"
	case TypeState:
		return "state"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// SHAMap is the main structure representing the tree
type SHAMap struct {
	mu        sync.RWMutex
	root      *InnerNode
	mapType   Type
	state     State
	ledgerSeq uint32
	full      bool
	backed    bool
	family    Family // nil for unbacked maps
	// cachedSize memoises Size(); -1 = uncached. Only written once the
	// map is immutable, so concurrent first-readers race benignly on a
	// frozen tree.
	cachedSize atomic.Int64
}

// New creates a new empty SHAMap with the specified type
func New(mapType Type) (*SHAMap, error) {
	root := NewInnerNode()

	sm := &SHAMap{
		root:      root,
		mapType:   mapType,
		state:     StateModifying,
		ledgerSeq: 0,
		full:      true,
		backed:    false,
	}
	sm.cachedSize.Store(-1)
	return sm, nil
}

// NewBacked creates a new empty backed SHAMap with the specified type and Family.
// Unlike New(), this map will flush dirty nodes to the Family and support lazy loading.
func NewBacked(mapType Type, family Family) (*SHAMap, error) {
	if family == nil {
		return nil, errors.New("family is required for backed SHAMap")
	}
	root := NewInnerNode()
	sm := &SHAMap{
		root:    root,
		mapType: mapType,
		state:   StateModifying,
		full:    true,
		backed:  true,
		family:  family,
	}
	sm.cachedSize.Store(-1)
	return sm, nil
}

// SetFamily sets the Family on an existing SHAMap, enabling backed mode.
// This allows converting an unbacked map to a backed map.
func (sm *SHAMap) SetFamily(family Family) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.family = family
	sm.backed = family != nil
}

// NewFromRootHash creates a backed SHAMap from a root hash and a Family.
// The root inner node is fetched from the store with child pointers nil (hash-only).
// Children are loaded lazily on demand via descend().
func NewFromRootHash(mapType Type, rootHash [32]byte, family Family) (*SHAMap, error) {
	if family == nil {
		return nil, errors.New("family is required for backed SHAMap")
	}

	// Fetch root node from store
	data, err := family.Fetch(context.Background(), rootHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch root node: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("root node %x not found in store", rootHash[:8])
	}

	// Deserialize — creates InnerNode with hashes set, children nil
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize root node: %w", err)
	}

	root, ok := node.(*InnerNode)
	if !ok {
		return nil, fmt.Errorf("root node is not an InnerNode, got %T", node)
	}

	sm := &SHAMap{
		root:    root,
		mapType: mapType,
		state:   StateModifying,
		full:    true,
		backed:  true,
		family:  family,
	}
	sm.cachedSize.Store(-1)
	return sm, nil
}

func (sm *SHAMap) descend(inner *InnerNode, branch int) (Node, error) {
	return sm.descendCtx(context.Background(), inner, branch)
}

// descendCtx returns the child node at the given branch of an inner node.
// For backed maps, if the child pointer is nil but the hash is set, the
// node is fetched from the Family and deserialized.
//
// Safe to call while holding only the SHAMap RLock: all child/hash access
// goes through InnerNode.LoadChild and the lazy attach uses SetChildIfNil,
// so concurrent readers racing on the same branch all return the same
// installed child. Each SHAMap retains its own deserialised subtree.
func (sm *SHAMap) descendCtx(ctx context.Context, inner *InnerNode, branch int) (Node, error) {
	child, hash, hasBranch := inner.LoadChild(branch)
	if child != nil {
		return child, nil
	}

	if !sm.backed || sm.family == nil {
		return nil, nil
	}

	if !hasBranch || isZeroHash(hash) {
		return nil, nil
	}

	data, err := sm.family.Fetch(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch child node %x: %w", hash[:8], err)
	}
	if data == nil {
		return nil, fmt.Errorf("child node %x not found in store", hash[:8])
	}

	// Fresh deserialised copy — not shared across SHAMap instances.
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize child node: %w", err)
	}

	// If another reader installed a child while we were fetching, return
	// theirs and let ours be GC'd.
	return inner.SetChildIfNil(branch, node), nil
}

// Type returns the map type
func (sm *SHAMap) Type() Type {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.mapType
}

// State returns the current state
func (sm *SHAMap) State() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

// SetImmutable sets the SHAMap state to immutable
func (sm *SHAMap) SetImmutable() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state == StateInvalid {
		return errors.New("cannot set invalid map to immutable")
	}

	sm.state = StateImmutable
	return nil
}

// SetFull marks the map as fully loaded
func (sm *SHAMap) SetFull() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.full = true
}

// SetLedgerSeq sets the ledger sequence number
func (sm *SHAMap) SetLedgerSeq(seq uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ledgerSeq = seq
}

// Hash returns the root hash of the SHAMap
func (sm *SHAMap) Hash() ([32]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state == StateInvalid {
		return [32]byte{}, errors.New("cannot get hash of invalid map")
	}

	return sm.root.Hash(), nil
}

// pathEntry represents an entry in the traversal path
type pathEntry struct {
	node   Node
	nodeID NodeID
}

// NodeStack holds the path from the root to a node during tree traversal
type NodeStack struct {
	entries []pathEntry
}

// NewNodeStack creates a new empty node stack
func NewNodeStack() *NodeStack {
	return &NodeStack{
		entries: make([]pathEntry, 0, MaxDepth), // Pre-allocate for efficiency
	}
}

// Push adds a node and its ID to the stack
func (s *NodeStack) Push(node Node, id NodeID) {
	s.entries = append(s.entries, pathEntry{node, id})
}

// Pop removes and returns the top node and ID from the stack
func (s *NodeStack) Pop() (Node, NodeID, bool) {
	if len(s.entries) == 0 {
		return nil, NodeID{}, false
	}

	idx := len(s.entries) - 1
	entry := s.entries[idx]
	s.entries = s.entries[:idx]

	return entry.node, entry.nodeID, true
}

// Top returns the top node and ID without removing them
func (s *NodeStack) Top() (Node, NodeID, bool) {
	if len(s.entries) == 0 {
		return nil, NodeID{}, false
	}

	entry := s.entries[len(s.entries)-1]
	return entry.node, entry.nodeID, true
}

// IsEmpty returns true if the stack is empty
func (s *NodeStack) IsEmpty() bool {
	return len(s.entries) == 0
}

// Clear removes all entries from the stack
func (s *NodeStack) Clear() {
	s.entries = s.entries[:0]
}

// Len returns the number of entries in the stack
func (s *NodeStack) Len() int {
	return len(s.entries)
}

// walkToKey traverses the tree toward a specific key.
func (sm *SHAMap) walkToKey(ctx context.Context, key [32]byte, stack *NodeStack) (Node, error) {
	if stack != nil && !stack.IsEmpty() {
		stack.Clear()
	}

	var node Node = sm.root
	nodeID := NewRootNodeID()

	for !node.IsLeaf() {
		if stack != nil {
			stack.Push(node, nodeID)
		}

		inner, ok := node.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		branch := SelectBranch(nodeID, key)
		if inner.IsEmptyBranch(int(branch)) {
			return nil, nil // Empty slot
		}

		child, err := sm.descendCtx(ctx, inner, int(branch))
		if err != nil {
			return nil, fmt.Errorf("failed to get child: %w", err)
		}
		if child == nil {
			return nil, nil // Empty slot
		}

		node = child
		childNodeID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get child node ID: %w", err)
		}
		nodeID = childNodeID
	}

	if stack != nil {
		stack.Push(node, nodeID)
	}

	return node, nil
}

// findItem returns the item with the specified key, or nil if not found.
func (sm *SHAMap) findItem(key [32]byte) (*Item, error) {
	node, err := sm.walkToKey(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}

	leafNode, ok := node.(LeafNode)
	if !ok {
		return nil, ErrInvalidType
	}

	item := leafNode.Item()
	itemKey := item.Key()
	if !bytes.Equal(itemKey[:], key[:]) {
		return nil, nil
	}

	return item, nil
}

// Has checks if an item with the given key exists
func (sm *SHAMap) Has(key [32]byte) (bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	item, err := sm.findItem(key)
	if err != nil {
		return false, err
	}
	return item != nil, nil
}

// Get returns the item associated with the key
func (sm *SHAMap) Get(key [32]byte) (*Item, bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	item, err := sm.findItem(key)
	if err != nil {
		return nil, false, err
	}
	return item, item != nil, nil
}

// Put adds or updates an item in the SHAMap
func (sm *SHAMap) Put(key [32]byte, data []byte) error {
	item := NewItem(key, data)
	return sm.PutItem(item)
}

// PutWithNodeType adds an item with a specific node type (for transaction+metadata)
func (sm *SHAMap) PutWithNodeType(key [32]byte, data []byte, nodeType NodeType) error {
	item := NewItem(key, data)
	return sm.PutItemWithNodeType(item, nodeType)
}

// PutItemWithNodeType adds an item with a specific node type
func (sm *SHAMap) PutItemWithNodeType(item *Item, nodeType NodeType) error {
	if item == nil {
		return ErrNilItem
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateModifying {
		return ErrImmutable
	}

	return sm.putItemWithNodeTypeUnsafe(item, nodeType)
}

// putItemWithNodeTypeUnsafe adds an item with specific node type without locking
func (sm *SHAMap) putItemWithNodeTypeUnsafe(item *Item, nodeType NodeType) error {
	key := item.Key()
	stack := NewNodeStack()

	// Walk towards the key, building stack of inner nodes (excluding leaf)
	node, err := sm.walkToKeyForDirty(key, stack)
	if err != nil {
		return fmt.Errorf("failed to walk to key: %w", err)
	}

	if node == nil {
		// Empty slot - create new leaf with specified node type
		newLeaf, err := sm.createTypedLeaf(nodeType, item)
		if err != nil {
			return fmt.Errorf("failed to create leaf: %w", err)
		}

		newRoot, err := sm.dirtyUp(stack, key, newLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	if !node.IsLeaf() {
		return ErrInvalidType
	}

	leafNode, ok := node.(LeafNode)
	if !ok {
		return ErrInvalidType
	}

	existingItem := leafNode.Item()
	existingKey := existingItem.Key()

	// Case 1: Same key - update existing item
	if bytes.Equal(key[:], existingKey[:]) {
		newLeaf, err := CreateLeafNode(nodeType, item)
		if err != nil {
			return err
		}

		newRoot, err := sm.dirtyUp(stack, key, newLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	// Case 2: Different key - need to split
	currentDepth := stack.Len()
	splitDepth := findSplitDepth(key, existingKey, currentDepth)

	// Create new leaf for the new item
	newLeaf, err := sm.createTypedLeaf(nodeType, item)
	if err != nil {
		return err
	}

	// Create inner nodes from current depth down to split depth.
	//
	// We build top-down and capture the chain so we can refresh hashes
	// bottom-up after the leaves are attached: each SetChild here records
	// the freshly-created child's hash (zero, because the child has no
	// children yet) into the parent's hashes[branch], and the parent's
	// own hash is computed from that zero. When the leaves are attached
	// to deepestInner below, deepestInner's hash is recomputed correctly,
	// but every ancestor in the chain still carries the stale zero hash.
	// That stale chain is what makes the in-memory SHAMap report a
	// CORRECT root via Hash() (updateHashUnsafe prefers child.Hash() over
	// the stale n.hashes[i]), while SerializeForWire used to write the
	// stale n.hashes[i] verbatim — producing wire bytes whose preimage
	// disagreed with the in-memory hash and breaking peer reconstruction
	// of the tx-set (#470 iter4 stall at seq 257). The refresh loop
	// below re-runs SetChild bottom-up so n.hashes[i] tracks the live
	// child.Hash(); serialization now also reads childPreimageHash (live
	// child preferred), so an unrefreshed cache can no longer diverge from
	// the in-memory hash on its own.
	innerNode := NewInnerNode()
	deepestInner := innerNode
	chain := []*InnerNode{innerNode}

	for d := currentDepth; d < splitDepth; d++ {
		branch := getBranchAtDepth(key, d)
		child := NewInnerNode()
		if err := deepestInner.SetChild(branch, child); err != nil {
			return err
		}
		deepestInner = child
		chain = append(chain, child)
	}

	// Place both leaves in the deepest inner node
	newBranch := getBranchAtDepth(key, splitDepth)
	existingBranch := getBranchAtDepth(existingKey, splitDepth)

	if err := deepestInner.SetChild(newBranch, newLeaf); err != nil {
		return err
	}
	if err := deepestInner.SetChild(existingBranch, leafNode); err != nil {
		return err
	}

	// Refresh stale chain hashes bottom-up. The chain has len(chain)
	// inner nodes at depths currentDepth..splitDepth; for each adjacent
	// (parent, child) pair walk SetChild again so the parent captures
	// child.Hash() (now valid after leaves were attached) and recomputes
	// its own hash. This is a no-op for chain[len-1] (deepestInner) —
	// already up-to-date from the SetChild leaf calls above.
	for i := len(chain) - 1; i > 0; i-- {
		branch := getBranchAtDepth(key, currentDepth+i-1)
		if err := chain[i-1].SetChild(branch, chain[i]); err != nil {
			return err
		}
	}

	// Dirty up from the top inner node
	newRoot, err := sm.dirtyUp(stack, key, innerNode)
	if err != nil {
		return fmt.Errorf("failed to dirty up: %w", err)
	}

	return sm.assignRoot(newRoot, key)
}

// PutItem adds or updates an item in the SHAMap
func (sm *SHAMap) PutItem(item *Item) error {
	if item == nil {
		return ErrNilItem
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateModifying {
		return ErrImmutable
	}

	return sm.putItemUnsafe(item)
}

// putItemUnsafe adds an item without locking (caller must hold lock)
func (sm *SHAMap) putItemUnsafe(item *Item) error {
	key := item.Key()
	stack := NewNodeStack()

	// Walk towards the key, building stack of inner nodes (excluding leaf)
	node, err := sm.walkToKeyForDirty(key, stack)
	if err != nil {
		return fmt.Errorf("failed to walk to key: %w", err)
	}

	if node == nil {
		// Empty slot - create new leaf
		nodeType, err := sm.getLeafNodeType()
		if err != nil {
			return err
		}

		newLeaf, err := sm.createTypedLeaf(nodeType, item)
		if err != nil {
			return fmt.Errorf("failed to create leaf: %w", err)
		}

		newRoot, err := sm.dirtyUp(stack, key, newLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	if !node.IsLeaf() {
		return ErrInvalidType
	}

	leafNode, ok := node.(LeafNode)
	if !ok {
		return ErrInvalidType
	}

	existingItem := leafNode.Item()
	existingKey := existingItem.Key()

	// Case 1: Same key - update existing item
	if bytes.Equal(key[:], existingKey[:]) {
		nodeType, err := sm.getLeafNodeType()
		if err != nil {
			return err
		}

		updatedLeaf, err := sm.createTypedLeaf(nodeType, item)
		if err != nil {
			return fmt.Errorf("failed to create updated leaf: %w", err)
		}

		newRoot, err := sm.dirtyUp(stack, key, updatedLeaf)
		if err != nil {
			return fmt.Errorf("failed to dirty up: %w", err)
		}

		return sm.assignRoot(newRoot, key)
	}

	// Case 2: Different key - need to split
	splitDepth := findSplitDepth(key, existingKey, stack.Len())
	newRoot, err := sm.createSplitStructure(key, existingKey, item, node, splitDepth, stack)
	if err != nil {
		return fmt.Errorf("failed to create split structure: %w", err)
	}

	return sm.assignRoot(newRoot, key)
}

// walkToKeyForDirty walks toward a key but doesn't include the final
// leaf in the stack.
func (sm *SHAMap) walkToKeyForDirty(key [32]byte, stack *NodeStack) (Node, error) {
	if stack != nil && !stack.IsEmpty() {
		stack.Clear()
	}

	var node Node = sm.root
	nodeID := NewRootNodeID()

	for !node.IsLeaf() {
		if stack != nil {
			stack.Push(node, nodeID)
		}

		inner, ok := node.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		branch := SelectBranch(nodeID, key)
		if inner.IsEmptyBranch(int(branch)) {
			return nil, nil
		}

		child, err := sm.descend(inner, int(branch))
		if err != nil {
			return nil, fmt.Errorf("failed to get child: %w", err)
		}
		if child == nil {
			return nil, nil
		}

		node = child
		childNodeID, err := nodeID.ChildNodeID(branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get child node ID: %w", err)
		}
		nodeID = childNodeID
	}

	// Don't push the final leaf node to the stack
	return node, nil
}

// dirtyUp updates the tree from leaf to root
func (sm *SHAMap) dirtyUp(stack *NodeStack, target [32]byte, child Node) (Node, error) {
	if sm.state == StateSyncing || sm.state == StateImmutable {
		return nil, ErrInvalidState
	}
	if child == nil {
		return nil, errors.New("dirtyUp called with nil child")
	}

	currentChild := child
	for !stack.IsEmpty() {
		node, nodeID, ok := stack.Pop()
		if !ok {
			return nil, errors.New("stack unexpectedly empty")
		}

		inner, ok := node.(*InnerNode)
		if !ok {
			return nil, errors.New("expected InnerNode on stack")
		}

		// Path-copy persistence: rebuild a fresh inner node along the
		// mutated path so any snapshot still referencing this subtree
		// keeps its original view. Untouched siblings stay shared via
		// the copied child pointers.
		cloned := inner.shallowClone()
		branch := SelectBranch(nodeID, target)
		if err := cloned.SetChild(int(branch), currentChild); err != nil {
			return nil, fmt.Errorf("failed to set child: %w", err)
		}

		currentChild = cloned
	}

	return currentChild, nil
}

// assignRoot safely assigns a new root
func (sm *SHAMap) assignRoot(newRoot Node, key [32]byte) error {
	if innerRoot, ok := newRoot.(*InnerNode); ok {
		sm.root = innerRoot
		return nil
	}

	// If newRoot is a leaf, wrap it in an inner node
	sm.root = NewInnerNode()
	rootNodeID := NewRootNodeID()
	branch := SelectBranch(rootNodeID, key)

	if err := sm.root.SetChild(int(branch), newRoot); err != nil {
		return fmt.Errorf("failed to set child in new root: %w", err)
	}

	return nil
}

// Delete removes the item associated with the given key from the SHAMap.
// It first locates and removes the corresponding leaf node, then reconstructs
// the tree from the leaf's parent up to the root, consolidating as needed.
func (sm *SHAMap) Delete(key [32]byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != StateModifying {
		return ErrImmutable
	}

	stack, _, err := sm.findAndRemoveLeaf(key)
	if err != nil {
		return err
	}

	newRoot, err := sm.consolidateAfterDelete(stack, key)
	if err != nil {
		return err
	}

	if rootInner, ok := newRoot.(*InnerNode); ok {
		sm.root = rootInner
	} else {
		return fmt.Errorf("expected root to be InnerNode, got %T", newRoot)
	}

	return nil
}

// findAndRemoveLeaf walks the SHAMap to locate the leaf node matching the key.
// It verifies the key, removes the leaf from the traversal stack, and returns
// the remaining stack for further processing.
func (sm *SHAMap) findAndRemoveLeaf(key [32]byte) (*NodeStack, LeafNode, error) {
	stack := NewNodeStack()
	_, err := sm.walkToKey(context.Background(), key, stack)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to walk to key: %w", err)
	}

	if stack.IsEmpty() {
		return nil, nil, ErrItemNotFound
	}

	leafNode, _, ok := stack.Pop()
	if !ok || !leafNode.IsLeaf() {
		return nil, nil, ErrItemNotFound
	}

	leaf, ok := leafNode.(LeafNode)
	if !ok {
		return nil, nil, ErrInvalidType
	}

	existingItem := leaf.Item()
	existingKey := existingItem.Key()
	if !bytes.Equal(key[:], existingKey[:]) {
		return nil, nil, ErrItemNotFound
	}

	return stack, leaf, nil
}

// consolidateAfterDelete reconstructs the SHAMap from a given node stack after
// a deletion. It applies bottom-up logic to restructure the tree and optimize
// it where possible (e.g., collapsing single-child inner nodes).
func (sm *SHAMap) consolidateAfterDelete(stack *NodeStack, key [32]byte) (Node, error) {
	var prevNode Node = nil

	for !stack.IsEmpty() {
		node, nodeID, ok := stack.Pop()
		if !ok {
			break
		}

		inner, ok := node.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		// Path-copy: shallow-clone so untouched siblings stay shared
		// with any snapshot that still references this subtree.
		clonedInner := inner.shallowClone()

		branch := SelectBranch(nodeID, key)
		if err := clonedInner.SetChild(int(branch), prevNode); err != nil {
			return nil, fmt.Errorf("failed to set child: %w", err)
		}

		if !nodeID.IsRoot() {
			switch clonedInner.BranchCount() {
			case 0:
				prevNode = nil
			case 1:
				onlyItem, err := sm.onlyBelow(clonedInner)
				if err != nil {
					return nil, fmt.Errorf("failed to check onlyBelow: %w", err)
				}

				if onlyItem != nil {
					nodeType, err := sm.getLeafNodeType()
					if err != nil {
						return nil, err
					}
					newLeaf, err := sm.createTypedLeaf(nodeType, onlyItem)
					if err != nil {
						return nil, fmt.Errorf("failed to create replacement leaf: %w", err)
					}
					prevNode = newLeaf
				} else {
					prevNode = clonedInner
				}
			default:
				prevNode = clonedInner
			}
		} else {
			// Always retain root
			prevNode = clonedInner
		}
	}

	if prevNode == nil {
		return nil, errors.New("unexpected nil root after deletion")
	}

	return prevNode, nil
}

// onlyBelow checks if there's exactly one item below the given node
// Returns the item if found, nil if there are 0 or multiple items
func (sm *SHAMap) onlyBelow(node Node) (*Item, error) {
	if node == nil {
		return nil, nil
	}

	current := node
	for !current.IsLeaf() {
		inner, ok := current.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		var nextNode Node = nil
		for i := 0; i < BranchFactor; i++ {
			child, err := sm.descend(inner, i)
			if err != nil {
				return nil, fmt.Errorf("failed to get child %d: %w", i, err)
			}

			if child != nil {
				if nextNode != nil {
					// Found second child - multiple items below
					return nil, nil
				}
				nextNode = child
			}
		}

		if nextNode == nil {
			// No children found
			return nil, nil
		}

		current = nextNode
	}

	// Found exactly one leaf
	leaf, ok := current.(LeafNode)
	if !ok {
		return nil, ErrInvalidType
	}

	return leaf.Item(), nil
}

// Snapshot returns a structurally-shared copy of the SHAMap in O(1).
// The source and the returned map share the same root pointer; mutation
// paths in either map are path-copy persistent (dirtyUp shallow-clones
// each touched inner node), so the snapshot's tree is never observed
// being mutated.
//
// For backed maps, dirty nodes present at entry are flushed to the store
// before the root is shared. FlushDirty and the subsequent RLock are
// separate critical sections, so a writer racing between the two can
// produce a snapshot whose root references dirty inner nodes that are
// not yet in the store; those will be flushed on the next FlushDirty.
// In other words, the on-disk image and the snapshot's in-memory image
// are consistent at the snapshot's quiescent moments, not unconditionally
// across concurrent writers.
func (sm *SHAMap) Snapshot(mutable bool) (*SHAMap, error) {
	if sm.backed && sm.family != nil {
		batch, err := sm.FlushDirty(false)
		if err != nil {
			return nil, fmt.Errorf("failed to flush dirty nodes: %w", err)
		}
		if len(batch.Entries) > 0 {
			if err := sm.family.StoreBatch(context.Background(), batch.Entries); err != nil {
				return nil, fmt.Errorf("failed to store flushed nodes: %w", err)
			}
		}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.state == StateInvalid {
		return nil, errors.New("cannot snapshot invalid map")
	}

	newState := StateImmutable
	if mutable {
		newState = StateModifying
	}

	out := &SHAMap{
		root:      sm.root,
		mapType:   sm.mapType,
		state:     newState,
		ledgerSeq: sm.ledgerSeq,
		full:      sm.full,
		backed:    sm.backed,
		family:    sm.family,
	}
	out.cachedSize.Store(-1)
	// Immutable→immutable snapshot observes the same leaf set; carry the
	// cached count across so the snapshot is O(1) on first Size() too.
	if !mutable && sm.state == StateImmutable {
		if n := sm.cachedSize.Load(); n >= 0 {
			out.cachedSize.Store(n)
		}
	}
	return out, nil
}

// Size returns the number of leaf items in the SHAMap.
// O(1) on immutable maps (memoised after the first call), O(n) on mutable.
func (sm *SHAMap) Size() int {
	if n := sm.cachedSize.Load(); n >= 0 {
		return int(n)
	}

	sm.mu.RLock()
	count := 0
	err := sm.forEachUnsafe(context.Background(), sm.root, func(*Item) bool {
		count++
		return true
	})
	isImmutable := sm.state == StateImmutable
	sm.mu.RUnlock()

	// Never cache a partial count: descend() can fail mid-walk on a backed
	// map, and a poisoned cache would persist for the map's lifetime.
	if isImmutable && err == nil {
		sm.cachedSize.Store(int64(count))
	}
	return count
}

// ForEach calls fn for every item in the tree.
// If fn returns false, iteration stops early.
// Equivalent to ForEachCtx(context.Background(), fn).
func (sm *SHAMap) ForEach(fn func(*Item) bool) error {
	return sm.ForEachCtx(context.Background(), fn)
}

// ForEachCtx is the context-aware variant of ForEach: iteration aborts
// with ctx.Err() whenever the context is cancelled. The check fires
// before each child descend so a long-running scan can be interrupted
// even when leaf callbacks return true.
func (sm *SHAMap) ForEachCtx(ctx context.Context, fn func(*Item) bool) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return sm.forEachUnsafe(ctx, sm.root, fn)
}

// forEachUnsafe recursively visits all items (caller must hold lock)
func (sm *SHAMap) forEachUnsafe(ctx context.Context, node Node, fn func(*Item) bool) error {
	if node == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if node.IsLeaf() {
		leafNode, ok := node.(LeafNode)
		if !ok {
			return ErrInvalidType
		}

		if !fn(leafNode.Item()) {
			return nil // Early termination requested
		}
		return nil
	}

	inner, ok := node.(*InnerNode)
	if !ok {
		return ErrInvalidType
	}

	for i := 0; i < BranchFactor; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := sm.descendCtx(ctx, inner, i)
		if err != nil {
			return fmt.Errorf("failed to get child %d: %w", i, err)
		}
		if child != nil {
			if err := sm.forEachUnsafe(ctx, child, fn); err != nil {
				return err
			}
		}
	}

	return nil
}

// Helper functions

// getLeafNodeType determines the appropriate leaf node type
func (sm *SHAMap) getLeafNodeType() (NodeType, error) {
	switch sm.mapType {
	case TypeTransaction:
		return NodeTypeTransactionNoMeta, nil
	case TypeState:
		return NodeTypeAccountState, nil
	default:
		return NodeType(0), fmt.Errorf("unknown map type: %v", sm.mapType)
	}
}

// createTypedLeaf creates a new leaf node with the specified type
func (sm *SHAMap) createTypedLeaf(nodeType NodeType, item *Item) (LeafNode, error) {
	return CreateLeafNode(nodeType, item)
}

// findSplitDepth finds the depth at which two keys first differ
func findSplitDepth(key1, key2 [32]byte, startDepth int) int {
	for depth := startDepth; depth < MaxDepth; depth++ {
		if getBranchAtDepth(key1, depth) != getBranchAtDepth(key2, depth) {
			return depth
		}
	}
	return MaxDepth - 1
}

// getBranchAtDepth gets the branch (0-15) for a key at a specific depth
func getBranchAtDepth(key [32]byte, depth int) int {
	if depth >= MaxDepth {
		return 0
	}

	byteIndex := depth / 2
	if byteIndex >= 32 {
		return 0
	}

	b := key[byteIndex]
	if depth%2 == 0 {
		return int(b >> 4) // Use upper 4 bits
	}
	return int(b & 0x0F) // Use lower 4 bits
}

// createSplitStructure creates the inner node structure needed to separate two keys
func (sm *SHAMap) createSplitStructure(newKey, existingKey [32]byte, newItem *Item, existingNode Node, splitDepth int, stack *NodeStack) (Node, error) {
	if splitDepth >= MaxDepth {
		return nil, ErrMaxDepthReached
	}

	// Create new leaf for the new item
	nodeType, err := sm.getLeafNodeType()
	if err != nil {
		return nil, err
	}

	newLeaf, err := sm.createTypedLeaf(nodeType, newItem)
	if err != nil {
		return nil, fmt.Errorf("failed to create new leaf: %w", err)
	}

	// Create inner node at split depth
	splitInner := NewInnerNode()

	// Get branches at split depth
	newBranch := getBranchAtDepth(newKey, splitDepth)
	existingBranch := getBranchAtDepth(existingKey, splitDepth)

	// Add both nodes to the split inner node
	if err := splitInner.SetChild(newBranch, newLeaf); err != nil {
		return nil, fmt.Errorf("failed to set new leaf: %w", err)
	}
	if err := splitInner.SetChild(existingBranch, existingNode); err != nil {
		return nil, fmt.Errorf("failed to set existing node: %w", err)
	}

	// Create intermediate inner nodes if needed
	currentNode := Node(splitInner)
	currentDepth := splitDepth - 1

	for currentDepth >= stack.Len() && currentDepth >= 0 {
		intermediateInner := NewInnerNode()
		branch := getBranchAtDepth(newKey, currentDepth)
		if err := intermediateInner.SetChild(branch, currentNode); err != nil {
			return nil, fmt.Errorf("failed to set intermediate node: %w", err)
		}
		currentNode = intermediateInner
		currentDepth--
	}

	// Use dirtyUp to propagate changes up the existing stack
	return sm.dirtyUp(stack, newKey, currentNode)
}

// IsBacked returns true if this SHAMap is backed by a NodeStore.
func (sm *SHAMap) IsBacked() bool {
	return sm.backed
}

// FlushDirty performs a post-order traversal of the tree, collecting all dirty nodes.
// Each dirty node is serialized and added to the returned NodeBatch.
// After serialization, nodes are marked clean (dirty=false).
// If releaseChildren is true, inner nodes release their child pointers after flush
// (retaining only hashes), allowing GC to reclaim memory. Children will be
// lazily reloaded from NodeStore on next access.
func (sm *SHAMap) FlushDirty(releaseChildren bool) (*NodeBatch, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.root == nil {
		return &NodeBatch{}, nil
	}

	batch := &NodeBatch{}

	if err := sm.flushNode(sm.root, releaseChildren, batch); err != nil {
		return nil, fmt.Errorf("failed to flush: %w", err)
	}

	return batch, nil
}

// flushNode recursively flushes a dirty node and its dirty children (post-order).
func (sm *SHAMap) flushNode(node Node, releaseChildren bool, batch *NodeBatch) error {
	if node == nil || !node.IsDirty() {
		return nil
	}

	// For inner nodes: flush children first (post-order)
	if inner, ok := node.(*InnerNode); ok {
		inner.mu.Lock()
		for i := 0; i < BranchFactor; i++ {
			child := inner.children[i]
			if child != nil && child.IsDirty() {
				// Flush child first (recursive)
				if err := sm.flushNode(child, releaseChildren, batch); err != nil {
					inner.mu.Unlock()
					return err
				}
			}
		}
		inner.mu.Unlock()

		// Synchronize the cached preimage with the just-flushed children
		// before serializing, so the flushed bytes hash to the in-memory
		// node hash even if some mutation path left a stale hashes[i].
		// Mirrors rippled's walkSubTree (SHAMap.cpp:1139).
		if err := inner.updateHashDeep(); err != nil {
			return fmt.Errorf("failed to update inner node hash: %w", err)
		}
	}

	// Serialize this node
	data, err := node.SerializeWithPrefix()
	if err != nil {
		return fmt.Errorf("failed to serialize node: %w", err)
	}

	hash := node.Hash()
	batch.Entries = append(batch.Entries, FlushEntry{
		Hash: hash,
		Data: data,
	})

	// Mark clean
	node.SetDirty(false)

	// Release children pointers for inner nodes (retain hashes for lazy reload).
	if releaseChildren {
		if inner, ok := node.(*InnerNode); ok {
			inner.ReleaseChildren()
		}
	}

	return nil
}

// Key is a type alias for a 32-byte key used in the SHAMap.
type Key = [32]byte

// FindDifference finds all keys that differ between this map and another.
// This is a convenience method that returns just the keys of items that differ,
// without the full DifferenceItem details.
//
// Parameters:
//   - other: the SHAMap to compare against
//
// Returns a slice of keys that are different (added, removed, or modified)
// between the two maps.
func (sm *SHAMap) FindDifference(other *SHAMap) ([]Key, error) {
	if other == nil {
		return nil, errors.New("cannot compare with nil map")
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	other.mu.RLock()
	defer other.mu.RUnlock()

	if sm.state == StateInvalid || other.state == StateInvalid {
		return nil, errors.New("cannot compare invalid maps")
	}

	// Quick check: if root hashes are identical, maps are identical
	ourHash := sm.root.Hash()
	otherHash := other.root.Hash()
	if ourHash == otherHash {
		return nil, nil
	}

	var keys []Key

	// Use a stack-based approach to compare trees
	type compareItem struct {
		ourNode   Node
		otherNode Node
	}

	stack := []compareItem{{ourNode: sm.root, otherNode: other.root}}

	for len(stack) > 0 {
		// Pop from stack
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if item.ourNode == nil && item.otherNode == nil {
			continue
		}

		// Handle case where one side is nil
		if item.ourNode == nil {
			// Everything in otherNode is added
			otherKeys, err := other.collectAllKeysUnsafe(item.otherNode)
			if err != nil {
				return nil, err
			}
			keys = append(keys, otherKeys...)
			continue
		}

		if item.otherNode == nil {
			// Everything in ourNode is removed
			ourKeys, err := sm.collectAllKeysUnsafe(item.ourNode)
			if err != nil {
				return nil, err
			}
			keys = append(keys, ourKeys...)
			continue
		}

		// Both nodes exist - compare hashes first
		ourNodeHash := item.ourNode.Hash()
		otherNodeHash := item.otherNode.Hash()
		if ourNodeHash == otherNodeHash {
			// Subtrees are identical
			continue
		}

		// Hashes differ - need to compare more deeply
		ourIsLeaf := item.ourNode.IsLeaf()
		otherIsLeaf := item.otherNode.IsLeaf()

		if ourIsLeaf && otherIsLeaf {
			// Both are leaves
			ourLeaf, ok := item.ourNode.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			otherLeaf, ok := item.otherNode.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}

			ourKey := ourLeaf.Item().Key()
			otherKey := otherLeaf.Item().Key()

			if ourKey == otherKey {
				// Same key, different content
				keys = append(keys, ourKey)
			} else {
				// Different keys - both are differences
				keys = append(keys, ourKey)
				keys = append(keys, otherKey)
			}
		} else if ourIsLeaf && !otherIsLeaf {
			// Our side is leaf, other side is inner
			ourLeaf, ok := item.ourNode.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			ourKey := ourLeaf.Item().Key()

			// Add our key as a difference
			keys = append(keys, ourKey)

			// Collect all keys from other inner node that don't match our key
			otherKeys, err := other.collectAllKeysExceptUnsafe(item.otherNode, ourKey)
			if err != nil {
				return nil, err
			}
			keys = append(keys, otherKeys...)
		} else if !ourIsLeaf && otherIsLeaf {
			// Our side is inner, other side is leaf
			otherLeaf, ok := item.otherNode.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			otherKey := otherLeaf.Item().Key()

			// Add other key as a difference
			keys = append(keys, otherKey)

			// Collect all keys from our inner node that don't match other key
			ourKeys, err := sm.collectAllKeysExceptUnsafe(item.ourNode, otherKey)
			if err != nil {
				return nil, err
			}
			keys = append(keys, ourKeys...)
		} else {
			// Both are inner nodes - compare children
			ourInner, ok := item.ourNode.(*InnerNode)
			if !ok {
				return nil, ErrInvalidType
			}
			otherInner, ok := item.otherNode.(*InnerNode)
			if !ok {
				return nil, ErrInvalidType
			}

			for branch := 0; branch < BranchFactor; branch++ {
				ourChild, err := ourInner.Child(branch)
				if err != nil {
					return nil, err
				}
				otherChild, err := otherInner.Child(branch)
				if err != nil {
					return nil, err
				}

				// Skip if both are nil
				if ourChild == nil && otherChild == nil {
					continue
				}

				// Skip if hashes match
				if ourChild != nil && otherChild != nil {
					if ourChild.Hash() == otherChild.Hash() {
						continue
					}
				}

				// Need to compare this branch
				stack = append(stack, compareItem{
					ourNode:   ourChild,
					otherNode: otherChild,
				})
			}
		}
	}

	return keys, nil
}

// collectAllKeysUnsafe collects all keys from a node and its descendants.
// Caller must hold the read lock.
func (sm *SHAMap) collectAllKeysUnsafe(node Node) ([]Key, error) {
	if node == nil {
		return nil, nil
	}

	var keys []Key

	stack := []Node{node}

	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if current == nil {
			continue
		}

		if current.IsLeaf() {
			leaf, ok := current.(LeafNode)
			if !ok {
				return nil, ErrInvalidType
			}
			keys = append(keys, leaf.Item().Key())
			continue
		}

		inner, ok := current.(*InnerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		for branch := 0; branch < BranchFactor; branch++ {
			child, err := sm.descend(inner, branch)
			if err != nil {
				return nil, err
			}
			if child != nil {
				stack = append(stack, child)
			}
		}
	}

	return keys, nil
}

// collectAllKeysExceptUnsafe collects all keys from a node except the given key.
// Caller must hold the read lock.
func (sm *SHAMap) collectAllKeysExceptUnsafe(node Node, exceptKey Key) ([]Key, error) {
	allKeys, err := sm.collectAllKeysUnsafe(node)
	if err != nil {
		return nil, err
	}

	var filteredKeys []Key
	for _, key := range allKeys {
		if key != exceptKey {
			filteredKeys = append(filteredKeys, key)
		}
	}

	return filteredKeys, nil
}

// WireNode is a node ready for wire transmission via TMLedgerData.
// NodeID is the SHAMap path-based identifier (33 bytes: 32 path + 1
// depth) used by the receiver to place the node in the partial tree.
// Data is the node's `SerializeForWire()` output.
type WireNode struct {
	NodeID []byte
	Data   []byte
}

// WalkWireNodes performs a pre-order traversal returning every node as
// wire data. Each NodeID is 33 bytes per SHAMapNodeID::getRawString.
func (sm *SHAMap) WalkWireNodes() ([]WireNode, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, nil
	}

	var out []WireNode
	if err := walkWireNodesRec(sm.root, [32]byte{}, 0, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func walkWireNodesRec(node Node, path [32]byte, depth int, out *[]WireNode) error {
	if node == nil {
		return nil
	}
	data, err := node.SerializeForWire()
	if err != nil {
		return err
	}
	nodeID := make([]byte, 33)
	copy(nodeID[:32], path[:])
	nodeID[32] = byte(depth)
	*out = append(*out, WireNode{NodeID: nodeID, Data: data})

	inner, ok := node.(*InnerNode)
	if !ok {
		return nil
	}
	inner.mu.RLock()
	defer inner.mu.RUnlock()
	for branch := 0; branch < BranchFactor; branch++ {
		child := inner.children[branch]
		if child == nil {
			continue
		}
		childPath := childPathForBranch(path, depth, branch)
		if err := walkWireNodesRec(child, childPath, depth+1, out); err != nil {
			return err
		}
	}
	return nil
}

// GetNodeFatByPath returns the SHAMap node at (wantedPath, wantedDepth)
// plus descendants out to `depth` levels, each as a (33-byte NodeID, wire
// blob) pair. Mirrors SHAMap::getNodeFat at SHAMapSync.cpp:434-525.
//
// Peers identify subtrees by SHAMapNodeID in TMGetLedger.nodeids, never
// by node hash. Single-child chains follow
// without spending budget. Leaves at the budget boundary are included
// only when fatLeaves is true (liTS_CANDIDATE callers pass false).
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
		inner, ok := node.(*InnerNode)
		if !ok {
			// Leaf reached before wantedDepth — not the requested node.
			return nil, nil
		}
		branch := selectBranchForPath(wantedPath, curDepth)
		inner.mu.RLock()
		child := inner.children[branch]
		inner.mu.RUnlock()
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
	if inner, ok := node.(*InnerNode); ok {
		inner.mu.RLock()
		empty := true
		for i := 0; i < BranchFactor; i++ {
			if inner.children[i] != nil {
				empty = false
				break
			}
		}
		inner.mu.RUnlock()
		if empty {
			return nil, nil
		}
	}

	// 2-3. Stack walk with the depth budget.
	type stackEntry struct {
		node  Node
		path  [32]byte
		depth int
		// budget is the remaining child-descent levels, mirroring
		// rippled's `depth` local variable inside the while loop.
		budget int
	}
	stack := []stackEntry{{node: node, path: curPath, depth: curDepth, budget: depth}}
	var out []WireNode

	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		data, err := e.node.SerializeForWire()
		if err != nil {
			return nil, err
		}
		nodeID := make([]byte, 33)
		copy(nodeID[:32], e.path[:])
		nodeID[32] = byte(e.depth)
		out = append(out, WireNode{NodeID: nodeID, Data: data})

		inner, ok := e.node.(*InnerNode)
		if !ok {
			continue
		}
		inner.mu.RLock()
		bc := 0
		for i := 0; i < BranchFactor; i++ {
			if inner.children[i] != nil {
				bc++
			}
		}
		// Descend if budget>0 or single-child chain.
		if e.budget == 0 && bc != 1 {
			inner.mu.RUnlock()
			continue
		}
		// Reverse iteration → ascending-branch pop order.
		for i := BranchFactor - 1; i >= 0; i-- {
			child := inner.children[i]
			if child == nil {
				continue
			}
			childPath := childPathForBranch(e.path, e.depth, i)
			childInner, isInner := child.(*InnerNode)
			if isInner && (e.budget > 1 || bc == 1) {
				// Push: budget-1 for multi-child, unchanged for chain.
				newBudget := e.budget - 1
				if bc == 1 {
					newBudget = e.budget
				}
				stack = append(stack, stackEntry{
					node:   childInner,
					path:   childPath,
					depth:  e.depth + 1,
					budget: newBudget,
				})
			} else if isInner || fatLeaves {
				// Include directly without descent.
				cdata, err := child.SerializeForWire()
				if err != nil {
					inner.mu.RUnlock()
					return nil, err
				}
				cNodeID := make([]byte, 33)
				copy(cNodeID[:32], childPath[:])
				cNodeID[32] = byte(e.depth + 1)
				out = append(out, WireNode{NodeID: cNodeID, Data: cdata})
			}
		}
		inner.mu.RUnlock()
	}
	return out, nil
}

// childPathForBranch returns the child path at depth+1. XRPL convention:
// nibble at index `depth` is in the high half of byte depth/2 when even,
// low half when odd.
func childPathForBranch(parentPath [32]byte, depth, branch int) [32]byte {
	out := parentPath
	bytePos := depth / 2
	if depth%2 == 0 {
		out[bytePos] = (out[bytePos] & 0x0F) | (byte(branch) << 4)
	} else {
		out[bytePos] = (out[bytePos] & 0xF0) | byte(branch)
	}
	return out
}

// selectBranchForPath returns the branch nibble at position `depth`
// of `path`. Inverse of childPathForBranch.
func selectBranchForPath(path [32]byte, depth int) int {
	bytePos := depth / 2
	if depth%2 == 0 {
		return int(path[bytePos] >> 4)
	}
	return int(path[bytePos] & 0x0F)
}

// pathPrefixEq compares the first `depth` nibbles of a and b.
func pathPrefixEq(a, b [32]byte, depth int) bool {
	for d := 0; d < depth; d++ {
		if selectBranchForPath(a, d) != selectBranchForPath(b, d) {
			return false
		}
	}
	return true
}
