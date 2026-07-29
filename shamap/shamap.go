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
	ErrNilFamily    = errors.New("family is required for backed SHAMap")
)

type mapState int

const (
	stateModifying mapState = iota
	stateImmutable
	stateSyncing
	stateInvalid
)

type treeState struct {
	mu         sync.RWMutex
	root       *innerNode
	mapType    Type
	state      mapState
	ledgerSeq  uint32
	full       bool
	cachedSize atomic.Int64
}

type backingState struct {
	mu        sync.RWMutex
	access    *familyAccess
	fullBelow *FullBelowCache
	loads     atomic.Uint64
}

type acquisitionState struct {
	walkMu       sync.Mutex
	attachmentMu sync.Mutex
	cursor       *backedWalkCursor
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

// SHAMap is the main structure representing the tree
type SHAMap struct {
	tree        treeState
	backing     backingState
	acquisition acquisitionState
}

// New creates a new empty SHAMap with the specified type
func New(mapType Type) *SHAMap {
	sm := &SHAMap{
		tree: treeState{
			root:    newInnerNode(),
			mapType: mapType,
			state:   stateModifying,
			full:    true,
		},
		backing: backingState{fullBelow: NewFullBelowCache()},
	}
	sm.tree.cachedSize.Store(-1)
	return sm
}

// NewBacked creates a new empty backed SHAMap with the specified type and Family.
// Unlike New(), this map will flush dirty nodes to the Family and support lazy loading.
func NewBacked(mapType Type, family Family) (*SHAMap, error) {
	if family == nil {
		return nil, ErrNilFamily
	}
	access := bindFamily(family)
	sm := &SHAMap{
		tree: treeState{
			root:    newInnerNode(),
			mapType: mapType,
			state:   stateModifying,
			full:    true,
		},
		backing: backingState{
			access:    access,
			fullBelow: familyFullBelowCache(family),
		},
	}
	sm.tree.cachedSize.Store(-1)
	return sm, nil
}

// SetFamily sets the Family on an existing SHAMap, enabling backed mode.
// This allows converting an unbacked map to a backed map.
func (sm *SHAMap) SetFamily(family Family) {
	access := bindFamily(family)
	var fullBelow *FullBelowCache
	if family != nil {
		fullBelow = familyFullBelowCache(family)
	}

	sm.acquisition.walkMu.Lock()
	sm.tree.mu.Lock()
	sm.backing.mu.Lock()
	sm.backing.access = access
	sm.acquisition.cursor = nil
	if family != nil {
		sm.backing.fullBelow = fullBelow
	}
	sm.backing.mu.Unlock()
	sm.tree.mu.Unlock()
	sm.acquisition.walkMu.Unlock()
}

// NewFromRootHash creates a backed SHAMap from a root hash and a Family.
// The root inner node is fetched from the store with child pointers nil (hash-only).
// Children are loaded lazily on demand via descend().
func NewFromRootHash(mapType Type, rootHash [32]byte, family Family) (*SHAMap, error) {
	return NewFromRootHashContext(context.Background(), mapType, rootHash, family)
}

// NewFromRootHashContext creates a backed SHAMap while forwarding ctx to the
// root-node fetch. Descendants remain lazily loaded.
func NewFromRootHashContext(ctx context.Context, mapType Type, rootHash [32]byte, family Family) (*SHAMap, error) {
	if family == nil {
		return nil, ErrNilFamily
	}
	access := bindFamily(family)

	// Fetch root node from store
	data, err := access.fetch(ctx, rootHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch root node: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("root node %x: %w", rootHash[:8], ErrNodeNotInStore)
	}

	// Deserialize — creates innerNode with hashes set, children nil.
	node, err := decodeAndVerifyPrefixNode(data, rootHash)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to deserialize root node: %v", ErrInvalidNodeData, err)
	}

	root, ok := node.(*innerNode)
	if !ok {
		return nil, fmt.Errorf("%w: root node is not an inner node, got %T", ErrInvalidNodeData, node)
	}

	sm := &SHAMap{
		tree: treeState{
			root:    root,
			mapType: mapType,
			state:   stateModifying,
			full:    true,
		},
		backing: backingState{
			access:    access,
			fullBelow: familyFullBelowCache(family),
		},
	}
	sm.tree.cachedSize.Store(-1)
	return sm, nil
}

func (sm *SHAMap) descend(inner *innerNode, branch int) (mapNode, error) {
	return sm.descendCtx(context.Background(), inner, branch)
}

// descendCtx returns the child node at the given branch of an inner node.
// For backed maps, if the child pointer is nil but the hash is set, the
// node is fetched from the Family and deserialized.
//
// Safe to call while holding only the tree read lock: all child/hash access
// goes through innerNode.LoadChild and the lazy attach uses SetChildIfNil,
// so concurrent readers racing on the same branch all return the same
// installed child. Each SHAMap retains its own deserialised subtree.
func (sm *SHAMap) descendCtx(ctx context.Context, inner *innerNode, branch int) (mapNode, error) {
	child, hash, hasBranch := inner.LoadChild(branch)
	if child != nil {
		return child, nil
	}

	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	if !access.available() {
		return nil, nil
	}

	if !hasBranch || isZeroHash(hash) {
		return nil, nil
	}

	data, err := access.fetch(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch child node %x: %w", hash[:8], err)
	}
	if data == nil {
		return nil, fmt.Errorf("child node %x: %w", hash[:8], ErrNodeNotInStore)
	}

	// Fresh deserialised copy — not shared across SHAMap instances.
	node, err := decodeAndVerifyPrefixNode(data, hash)
	if err != nil {
		return nil, err
	}

	// If another reader installed a child while we were fetching, return
	// theirs and let ours be GC'd.
	installed := inner.SetChildIfNil(branch, node)
	if installed == node {
		sm.backing.loads.Add(1)
	}
	return installed, nil
}

// FamilyLoadCount returns how many nodes this map has loaded and verified from
// its backing Family. The count includes temporary traversal nodes released
// after their subtrees are proven complete.
func (sm *SHAMap) FamilyLoadCount() uint64 {
	return sm.backing.loads.Load()
}

// Type returns the map type
func (sm *SHAMap) Type() Type {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	return sm.tree.mapType
}

// SetImmutable sets the SHAMap state to immutable
func (sm *SHAMap) SetImmutable() error {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()

	if sm.tree.state == stateInvalid {
		return fmt.Errorf("%w: cannot set invalid map to immutable", ErrInvalidState)
	}

	sm.tree.state = stateImmutable
	return nil
}

// SetLedgerSeq sets the ledger sequence number
func (sm *SHAMap) SetLedgerSeq(seq uint32) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.tree.ledgerSeq = seq
}

// Hash returns the root hash of the SHAMap
func (sm *SHAMap) Hash() ([32]byte, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.state == stateInvalid {
		return [32]byte{}, fmt.Errorf("%w: cannot get hash of invalid map", ErrInvalidState)
	}

	return sm.tree.root.Hash(), nil
}

// findItem returns the item with the specified key, or nil if not found.
func (sm *SHAMap) findItem(ctx context.Context, key [32]byte) (*Item, error) {
	node, err := sm.walkToKey(ctx, key, nil, false)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}

	leaf, ok := node.(mapLeaf)
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
	return sm.HasContext(context.Background(), key)
}

// HasContext checks whether key exists while forwarding ctx to lazy fetches.
func (sm *SHAMap) HasContext(ctx context.Context, key [32]byte) (bool, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	item, err := sm.findItem(ctx, key)
	if err != nil {
		return false, err
	}
	return item != nil, nil
}

// Get returns the item associated with the key
func (sm *SHAMap) Get(key [32]byte) (*Item, bool, error) {
	return sm.GetContext(context.Background(), key)
}

// GetContext returns key's item while forwarding ctx to lazy fetches.
func (sm *SHAMap) GetContext(ctx context.Context, key [32]byte) (*Item, bool, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	item, err := sm.findItem(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return item, item != nil, nil
}

// getLeafNodeType determines the appropriate leaf node type
func (sm *SHAMap) getLeafNodeType() (NodeType, error) {
	switch sm.tree.mapType {
	case TypeTransaction:
		return NodeTypeTransactionNoMeta, nil
	case TypeState:
		return NodeTypeAccountState, nil
	default:
		return NodeType(0), fmt.Errorf("unknown map type: %v", sm.tree.mapType)
	}
}

// createTypedLeaf creates a new leaf node with the specified type
func (sm *SHAMap) createTypedLeaf(nodeType NodeType, item *Item) (mapLeaf, error) {
	return createLeafNode(nodeType, item)
}

// IsBacked returns true if this SHAMap is backed by a NodeStore.
func (sm *SHAMap) IsBacked() bool {
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	return sm.backing.access.available()
}

// FullBelowCache returns the map's full-below cache. Snapshots share the
// source's cache, so a snapshot and its source agree on which subtrees are
// proven complete.
func (sm *SHAMap) FullBelowCache() *FullBelowCache {
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	return sm.backing.fullBelow
}
