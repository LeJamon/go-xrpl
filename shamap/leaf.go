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
	BaseNode
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
		BaseNode: BaseNode{dirty: true},
		item:     item,
		kind:     kind,
	}
	if err := n.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update hash: %w", err)
	}
	return n, nil
}

// NewAccountStateLeafNode creates a new account state leaf node.
func NewAccountStateLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafAccountState, item)
}

// NewTransactionLeafNode creates a new transaction leaf node (without metadata).
func NewTransactionLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafTransaction, item)
}

// NewTransactionWithMetaLeafNode creates a new transaction+metadata leaf node.
func NewTransactionWithMetaLeafNode(item *Item) (*leafNode, error) {
	return newLeafNode(leafTransactionWithMeta, item)
}

// IsLeaf reports that this node is a leaf (always true).
func (n *leafNode) IsLeaf() bool { return true }

// IsInner reports whether this is an inner node (always false for a leaf).
func (n *leafNode) IsInner() bool { return false }

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
	n.dirty = true
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
	key := n.item.Key()
	switch n.kind {
	case leafAccountState:
		return n.setHash(protocol.HashPrefixLeafNode[:], n.item.Data(), key[:])
	case leafTransaction:
		return n.setHash(protocol.HashPrefixTransactionID[:], n.item.Data())
	case leafTransactionWithMeta:
		return n.setHash(protocol.HashPrefixTxNode[:], n.item.Data(), key[:])
	default:
		return fmt.Errorf("unknown leaf kind: %d", n.kind)
	}
}

// Type returns the SHAMap node type.
func (n *leafNode) Type() NodeType {
	switch n.kind {
	case leafAccountState:
		return NodeTypeAccountState
	case leafTransaction:
		return NodeTypeTransactionNoMeta
	case leafTransactionWithMeta:
		return NodeTypeTransactionWithMeta
	default:
		return 0
	}
}

// hashPrefix returns the 4-byte hash prefix used for SerializeWithPrefix.
func (n *leafNode) hashPrefix() []byte {
	switch n.kind {
	case leafAccountState:
		return protocol.HashPrefixLeafNode[:]
	case leafTransaction:
		return protocol.HashPrefixTransactionID[:]
	case leafTransactionWithMeta:
		return protocol.HashPrefixTxNode[:]
	default:
		panic("unknown leaf kind")
	}
}

// wireType returns the trailing wire-type byte.
func (n *leafNode) wireType() byte {
	switch n.kind {
	case leafAccountState:
		return protocol.WireTypeAccountState
	case leafTransaction:
		return protocol.WireTypeTransaction
	case leafTransactionWithMeta:
		return protocol.WireTypeTransactionWithMeta
	default:
		panic("unknown leaf kind")
	}
}

// keyOnWire reports whether the wire/prefix format includes the key.
func (n *leafNode) keyOnWire() bool {
	return n.kind != leafTransaction
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

// NewAccountStateLeafFromWire creates a leafNode from wire format data
// for an account-state leaf.
func NewAccountStateLeafFromWire(data []byte) (*leafNode, error) {
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
	node.dirty = false
	return node, nil
}

// NewTransactionLeafFromWire creates a leafNode from wire format data
// for a transaction leaf (without metadata). The key is derived by
// hashing the data.
func NewTransactionLeafFromWire(data []byte) (*leafNode, error) {
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
	node.dirty = false
	return node, nil
}

// NewTransactionWithMetaLeafFromWire creates a leafNode from wire format
// data for a transaction+metadata leaf.
func NewTransactionWithMetaLeafFromWire(data []byte) (*leafNode, error) {
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
	node.dirty = false
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
	switch n.kind {
	case leafAccountState:
		sb.WriteString(fmt.Sprintf("AccountStateLeafNode ID: %s\n", id.String()))
	case leafTransaction:
		sb.WriteString(fmt.Sprintf("TransactionLeafNode ID: %s\n", id.String()))
	case leafTransactionWithMeta:
		sb.WriteString(fmt.Sprintf("TransactionWithMetaLeafNode ID: %s\n", id.String()))
	}
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

// CreateLeafNode creates the appropriate leaf node type for the given node type.
func CreateLeafNode(nodeType NodeType, item *Item) (LeafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}
	switch nodeType {
	case NodeTypeAccountState:
		return NewAccountStateLeafNode(item)
	case NodeTypeTransactionNoMeta:
		return NewTransactionLeafNode(item)
	case NodeTypeTransactionWithMeta:
		return NewTransactionWithMetaLeafNode(item)
	default:
		return nil, fmt.Errorf("invalid node type for leaf: %v", nodeType)
	}
}
