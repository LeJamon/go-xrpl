package shamap

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
)

// leafKind discriminates the three flavours of SHAMap leaf node.
type leafKind uint8

const (
	leafAccountState        leafKind = iota + 1
	leafTransaction                  // without metadata
	leafTransactionWithMeta          // with metadata
)

// leafKindInfo holds the protocol attributes that distinguish the leaf
// flavours. leafKinds is indexed by leafKind value (1..3); index 0 is
// unused, so an out-of-range kind panics uniformly across all accessors.
type leafKindInfo struct {
	nodeType   NodeType
	hashPrefix []byte
	wireType   byte
	keyOnWire  bool
	label      string
}

var leafKinds = [...]leafKindInfo{
	leafAccountState: {
		nodeType:   NodeTypeAccountState,
		hashPrefix: protocol.HashPrefixLeafNode[:],
		wireType:   protocol.WireTypeAccountState,
		keyOnWire:  true,
		label:      "AccountStateLeafNode",
	},
	leafTransaction: {
		nodeType:   NodeTypeTransactionNoMeta,
		hashPrefix: protocol.HashPrefixTransactionID[:],
		wireType:   protocol.WireTypeTransaction,
		keyOnWire:  false,
		label:      "TransactionLeafNode",
	},
	leafTransactionWithMeta: {
		nodeType:   NodeTypeTransactionWithMeta,
		hashPrefix: protocol.HashPrefixTxNode[:],
		wireType:   protocol.WireTypeTransactionWithMeta,
		keyOnWire:  true,
		label:      "TransactionWithMetaLeafNode",
	},
}

// LeafNode interface extends Node with item-level access.
type LeafNode interface {
	Node
	Item() *Item
	SetItem(item *Item) (bool, error)
}

// leafNode is the single implementation of LeafNode for all three leaf
// flavours.  Which hash prefix, wire format, and node type to use is
// dispatched on kind.
type leafNode struct {
	baseNode
	mu   sync.RWMutex
	item *Item
	kind leafKind
}

// newLeafNode is the common constructor.
func newLeafNode(kind leafKind, item *Item) (*leafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}
	if len(item.Data()) < 12 {
		return nil, ErrItemTooSmall
	}
	n := &leafNode{
		item: item,
		kind: kind,
	}
	n.SetDirty(true)
	if err := n.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update hash: %w", err)
	}
	return n, nil
}

// newLeafNodeWithHash builds a leaf whose hash is already known — the
// content-addressed key it was fetched by — and installs it directly instead
// of re-hashing the item (a SHA-512Half over the full, possibly large, leaf
// data on every lazy descent). The node is marked clean: it came from the store.
func newLeafNodeWithHash(kind leafKind, item *Item, h [32]byte) (*leafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}
	if len(item.Data()) < 12 {
		return nil, ErrItemTooSmall
	}
	n := &leafNode{
		item: item,
		kind: kind,
	}
	n.hash = h
	n.SetDirty(false)
	return n, nil
}

// newAccountStateLeafNode creates a new account state leaf node.
func newAccountStateLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafAccountState, item)
}

// newTransactionLeafNode creates a new transaction leaf node (without metadata).
func newTransactionLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafTransaction, item)
}

// newTransactionWithMetaLeafNode creates a new transaction+metadata leaf node.
func newTransactionWithMetaLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafTransactionWithMeta, item)
}

// Hash returns the node's hash under the node lock, so readers on a
// structurally-shared subtree never race a concurrent recompute.
func (n *leafNode) Hash() [32]byte {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.hash
}

// Item returns the item stored in this leaf node.
func (n *leafNode) Item() *Item {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.item
}

// SetItem replaces the stored item and reports whether the node hash changed.
func (n *leafNode) SetItem(item *Item) (bool, error) {
	if item == nil {
		return false, ErrNilItem
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	oldHash := n.hash
	n.item = item
	n.SetDirty(true)
	if err := n.updateHashUnsafe(); err != nil {
		return false, fmt.Errorf("failed to update hash: %w", err)
	}
	return n.hash != oldHash, nil
}

// UpdateHash recomputes the node's hash from its item.
func (n *leafNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

func (n *leafNode) updateHashUnsafe() error {
	if n.item == nil {
		return ErrNilItem
	}
	if n.keyOnWire() {
		key := n.item.Key()
		return n.setHash(n.hashPrefix(), n.item.Data(), key[:])
	}
	return n.setHash(n.hashPrefix(), n.item.Data())
}

// Type returns the SHAMap node type.
func (n *leafNode) Type() NodeType {
	return leafKinds[n.kind].nodeType
}

// hashPrefix returns the 4-byte hash prefix used for SerializeWithPrefix.
func (n *leafNode) hashPrefix() []byte {
	return leafKinds[n.kind].hashPrefix
}

// wireType returns the trailing wire-type byte.
func (n *leafNode) wireType() byte {
	return leafKinds[n.kind].wireType
}

// keyOnWire reports whether the wire/prefix format includes the key.
func (n *leafNode) keyOnWire() bool {
	return leafKinds[n.kind].keyOnWire
}

// SerializeForWire returns the leaf's wire-format encoding.
func (n *leafNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return nil, ErrNilItem
	}
	data := n.item.Data()
	if n.keyOnWire() {
		key := n.item.Key()
		result := make([]byte, 0, len(data)+33)
		result = append(result, data...)
		result = append(result, key[:]...)
		result = append(result, n.wireType())
		return result, nil
	}
	result := make([]byte, 0, len(data)+1)
	result = append(result, data...)
	result = append(result, n.wireType())
	return result, nil
}

// SerializeWithPrefix returns the leaf prefixed with its hash prefix, in the
// form hashed to produce the node hash.
func (n *leafNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return nil, ErrNilItem
	}
	data := n.item.Data()
	if n.keyOnWire() {
		key := n.item.Key()
		result := make([]byte, 0, 4+len(data)+32)
		result = append(result, n.hashPrefix()...)
		result = append(result, data...)
		result = append(result, key[:]...)
		return result, nil
	}
	result := make([]byte, 0, 4+len(data))
	result = append(result, n.hashPrefix()...)
	result = append(result, data...)
	return result, nil
}

// newAccountStateLeafFromWire creates a leafNode from wire format data
// for an account-state leaf.
func newAccountStateLeafFromWire(data []byte) (*leafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}
	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeAccountState {
		return nil, fmt.Errorf("invalid wire type for account state: %d", wireType)
	}
	nodeData := data[:len(data)-1]
	if len(nodeData) < 32 {
		return nil, fmt.Errorf("account state data too short")
	}
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])
	if isZeroHash(key) {
		return nil, fmt.Errorf("invalid account state: zero key")
	}
	stateData := nodeData[:keyStart]
	item := NewItem(key, stateData)
	node, err := newLeafNode(leafAccountState, item)
	if err != nil {
		return nil, err
	}
	node.SetDirty(false)
	return node, nil
}

// NewTransactionLeafFromWire creates a transaction leaf (without metadata)
// from wire format data. The key is derived by hashing the data.
func NewTransactionLeafFromWire(data []byte) (LeafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}
	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeTransaction {
		return nil, fmt.Errorf("invalid wire type for transaction: %d", wireType)
	}
	nodeData := data[:len(data)-1]
	key := common.Sha512Half(protocol.HashPrefixTransactionID[:], nodeData)
	item := NewItem(key, nodeData)
	node, err := newLeafNode(leafTransaction, item)
	if err != nil {
		return nil, err
	}
	node.SetDirty(false)
	return node, nil
}

// newTransactionWithMetaLeafFromWire creates a leafNode from wire format
// data for a transaction+metadata leaf.
func newTransactionWithMetaLeafFromWire(data []byte) (*leafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}
	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeTransactionWithMeta {
		return nil, fmt.Errorf("invalid wire type for transaction with meta: %d", wireType)
	}
	nodeData := data[:len(data)-1]
	if len(nodeData) < 32 {
		return nil, fmt.Errorf("transaction with meta data too short")
	}
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])
	txData := nodeData[:keyStart]
	item := NewItem(key, txData)
	node, err := newLeafNode(leafTransactionWithMeta, item)
	if err != nil {
		return nil, err
	}
	node.SetDirty(false)
	return node, nil
}

// Invariants checks the leaf's structural invariants.
func (n *leafNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return fmt.Errorf("leaf has nil item")
	}
	if n.IsZeroHash() {
		return fmt.Errorf("leaf has zero hash")
	}
	return nil
}

func (n *leafNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s ID: %s\n", leafKinds[n.kind].label, id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))
	if n.item != nil {
		key := n.item.Key()
		sb.WriteString(fmt.Sprintf("Key: %s\n", hex.EncodeToString(key[:])))
		sb.WriteString(fmt.Sprintf("Data Size: %d bytes\n", len(n.item.Data())))
	}
	return sb.String()
}

// Clone returns a deep copy of the leaf node.
func (n *leafNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.item == nil {
		return nil, ErrNilItem
	}
	clonedItem, err := n.item.Clone()
	if err != nil {
		return nil, fmt.Errorf("failed to clone item: %w", err)
	}
	return newLeafNode(n.kind, clonedItem)
}

// createLeafNode creates the appropriate leaf node type for the given node type.
func createLeafNode(nodeType NodeType, item *Item) (LeafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}
	switch nodeType {
	case NodeTypeAccountState:
		return newAccountStateLeafNode(item)
	case NodeTypeTransactionNoMeta:
		return newTransactionLeafNode(item)
	case NodeTypeTransactionWithMeta:
		return newTransactionWithMetaLeafNode(item)
	default:
		return nil, fmt.Errorf("invalid node type for leaf: %v", nodeType)
	}
}
