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
	ErrImmutable    = errors.New("cannot modify immutable SHAMap")
	ErrNilItem      = errors.New("cannot add nil item")
	ErrItemNotFound = errors.New("item not found")
	ErrInvalidType  = errors.New("invalid node type")
	ErrInvalidState = errors.New("invalid state for operation")
	ErrItemTooSmall = errors.New("item data too small (minimum 12 bytes)")
)

// State defines the state of the SHAMap
type State int

// SHAMap lifecycle states.
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

// SHAMap tree types: a transaction tree or an account-state tree.
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

// Key is a type alias for a 32-byte key used in the SHAMap.
type Key = [32]byte

// SHAMap is the main structure representing the tree
type SHAMap struct {
	mu        sync.RWMutex
	root      *innerNode
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
func New(mapType Type) *SHAMap {
	sm := &SHAMap{
		root:    newInnerNode(),
		mapType: mapType,
		state:   StateModifying,
		full:    true,
		backed:  false,
	}
	sm.cachedSize.Store(-1)
	return sm
}

// NewBacked creates a new empty backed SHAMap with the specified type and Family.
// Unlike New(), this map will flush dirty nodes to the Family and support lazy loading.
func NewBacked(mapType Type, family Family) (*SHAMap, error) {
	if family == nil {
		return nil, errors.New("family is required for backed SHAMap")
	}
	sm := &SHAMap{
		root:    newInnerNode(),
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

	// Deserialize — creates innerNode with hashes set, children nil
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize root node: %w", err)
	}

	root, ok := node.(*innerNode)
	if !ok {
		return nil, fmt.Errorf("root node is not an inner node, got %T", node)
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

func (sm *SHAMap) descend(inner *innerNode, branch int) (Node, error) {
	return sm.descendCtx(context.Background(), inner, branch)
}

// descendCtx returns the child node at the given branch of an inner node.
// For backed maps, if the child pointer is nil but the hash is set, the
// node is fetched from the Family and deserialized.
//
// Safe to call while holding only the SHAMap RLock: all child/hash access
// goes through innerNode.LoadChild and the lazy attach uses SetChildIfNil,
// so concurrent readers racing on the same branch all return the same
// installed child. Each SHAMap retains its own deserialised subtree.
func (sm *SHAMap) descendCtx(ctx context.Context, inner *innerNode, branch int) (Node, error) {
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
		return fmt.Errorf("%w: cannot set invalid map to immutable", ErrInvalidState)
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
		return [32]byte{}, fmt.Errorf("%w: cannot get hash of invalid map", ErrInvalidState)
	}

	return sm.root.Hash(), nil
}

// findItem returns the item with the specified key, or nil if not found.
func (sm *SHAMap) findItem(key [32]byte) (*Item, error) {
	node, err := sm.walkToKey(context.Background(), key, nil, false)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}

	leaf, ok := node.(LeafNode)
	if !ok {
		return nil, ErrInvalidType
	}

	item := leaf.Item()
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
	stack := newNodeStack()

	node, err := sm.walkToKey(context.Background(), key, stack, false)
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

	leaf, ok := node.(LeafNode)
	if !ok {
		return ErrInvalidType
	}

	existingItem := leaf.Item()
	existingKey := existingItem.Key()

	// Case 1: Same key - update existing item
	if bytes.Equal(key[:], existingKey[:]) {
		newLeaf, err := sm.createTypedLeaf(nodeType, item)
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

	// Build the chain of inner nodes from currentDepth down to splitDepth,
	// then attach both leaves to the deepest node. Each SetChild during the
	// top-down build records the (then-empty) child's zero hash, so once
	// the leaves are attached the ancestors carry stale branch hashes; the
	// bottom-up refresh loop below re-runs SetChild so every cached hash
	// tracks the live child. Serialization also prefers the live child's
	// hash (childPreimageHash), so wire bytes can never diverge from the
	// in-memory root hash even if a cache entry lags.
	topInner := newInnerNode()
	deepestInner := topInner
	chain := []*innerNode{topInner}

	for d := currentDepth; d < splitDepth; d++ {
		branch := getBranchAtDepth(key, d)
		child := newInnerNode()
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
	if err := deepestInner.SetChild(existingBranch, leaf); err != nil {
		return err
	}

	// Refresh stale chain hashes bottom-up (no-op for deepestInner, which
	// is already current from the leaf attachments above).
	for i := len(chain) - 1; i > 0; i-- {
		branch := getBranchAtDepth(key, currentDepth+i-1)
		if err := chain[i-1].SetChild(branch, chain[i]); err != nil {
			return err
		}
	}

	// Dirty up from the top inner node
	newRoot, err := sm.dirtyUp(stack, key, topInner)
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

// putItemUnsafe adds an item without locking (caller must hold lock).
// It delegates to putItemWithNodeTypeUnsafe using the default node type for the map.
func (sm *SHAMap) putItemUnsafe(item *Item) error {
	nodeType, err := sm.getLeafNodeType()
	if err != nil {
		return err
	}
	return sm.putItemWithNodeTypeUnsafe(item, nodeType)
}

// dirtyUp updates the tree from leaf to root
func (sm *SHAMap) dirtyUp(stack *nodeStack, target [32]byte, child Node) (Node, error) {
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

		inner, ok := node.(*innerNode)
		if !ok {
			return nil, errors.New("expected inner node on stack")
		}

		// Path-copy persistence: rebuild a fresh inner node along the
		// mutated path so any snapshot still referencing this subtree
		// keeps its original view. Untouched siblings stay shared via
		// the copied child pointers.
		cloned := inner.shallowClone()
		branch := selectBranch(nodeID, target)
		if err := cloned.SetChild(int(branch), currentChild); err != nil {
			return nil, fmt.Errorf("failed to set child: %w", err)
		}

		currentChild = cloned
	}

	return currentChild, nil
}

// assignRoot safely assigns a new root
func (sm *SHAMap) assignRoot(newRoot Node, key [32]byte) error {
	if innerRoot, ok := newRoot.(*innerNode); ok {
		sm.root = innerRoot
		return nil
	}

	// If newRoot is a leaf, wrap it in an inner node
	sm.root = newInnerNode()
	rootNodeID := NewRootNodeID()
	branch := selectBranch(rootNodeID, key)

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

	if rootInner, ok := newRoot.(*innerNode); ok {
		sm.root = rootInner
	} else {
		return fmt.Errorf("expected root to be an inner node, got %T", newRoot)
	}

	return nil
}

// findAndRemoveLeaf walks the SHAMap to locate the leaf node matching the key.
// It verifies the key, removes the leaf from the traversal stack, and returns
// the remaining stack for further processing.
func (sm *SHAMap) findAndRemoveLeaf(key [32]byte) (*nodeStack, LeafNode, error) {
	stack := newNodeStack()
	_, err := sm.walkToKey(context.Background(), key, stack, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to walk to key: %w", err)
	}

	if stack.IsEmpty() {
		return nil, nil, ErrItemNotFound
	}

	node, _, ok := stack.Pop()
	if !ok {
		return nil, nil, ErrItemNotFound
	}

	leaf, ok := node.(LeafNode)
	if !ok {
		return nil, nil, ErrItemNotFound
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
func (sm *SHAMap) consolidateAfterDelete(stack *nodeStack, key [32]byte) (Node, error) {
	var prevNode Node = nil

	for !stack.IsEmpty() {
		node, nodeID, ok := stack.Pop()
		if !ok {
			break
		}

		inner, ok := node.(*innerNode)
		if !ok {
			return nil, ErrInvalidType
		}

		// Path-copy: shallow-clone so untouched siblings stay shared
		// with any snapshot that still references this subtree.
		clonedInner := inner.shallowClone()

		branch := selectBranch(nodeID, key)
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
// Flushing a structurally-shared subtree from either map is safe: the
// dirty flag is atomic and node hashes are read and written under each
// node's own lock.
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
		return nil, fmt.Errorf("%w: cannot snapshot invalid map", ErrInvalidState)
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
	err := sm.walkLeavesUnsafe(context.Background(), sm.root, func(*Item) bool {
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

	return sm.walkLeavesUnsafe(ctx, sm.root, fn)
}

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
	return createLeafNode(nodeType, item)
}

// IsBacked returns true if this SHAMap is backed by a NodeStore.
func (sm *SHAMap) IsBacked() bool {
	return sm.backed
}
