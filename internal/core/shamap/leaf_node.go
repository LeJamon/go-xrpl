package shamap

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/protocol"
)

// LeafNode interface for all leaf node types
type LeafNode interface {
	Node
	Item() *Item
	SetItem(item *Item) (bool, error)
}

// -----------------------------------------------------------------------------
// AccountStateLeafNode

// AccountStateLeafNode represents a leaf node containing account state data
type AccountStateLeafNode struct {
	BaseNode
	mu   sync.RWMutex
	item *Item
}

// NewAccountStateLeafNode creates a new account state leaf node
func NewAccountStateLeafNode(item *Item) (*AccountStateLeafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}

	n := &AccountStateLeafNode{
		BaseNode: BaseNode{dirty: true},
		item:     item,
	}
	if err := n.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update hash: %w", err)
	}
	return n, nil
}

func (n *AccountStateLeafNode) IsLeaf() bool  { return true }
func (n *AccountStateLeafNode) IsInner() bool { return false }

// Item returns the item stored in this leaf node
func (n *AccountStateLeafNode) Item() *Item {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.item
}

// SetItem updates the item and returns true if the hash changed
func (n *AccountStateLeafNode) SetItem(item *Item) (bool, error) {
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

// UpdateHash recalculates the node's hash
func (n *AccountStateLeafNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

func (n *AccountStateLeafNode) updateHashUnsafe() error {
	if n.item == nil {
		return ErrNilItem
	}

	key := n.item.Key()
	err := n.setHash(protocol.HashPrefixLeafNode[:], n.item.Data(), key[:])
	if err != nil {
		return err
	}
	return nil
}

func (n *AccountStateLeafNode) Type() NodeType {
	return NodeTypeAccountState
}

func (n *AccountStateLeafNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}
	var result []byte
	// Add transaction + metadata data (no prefix for wire format)
	result = append(result, n.item.Data()...)
	key := n.item.Key()
	result = append(result, key[:]...)
	result = append(result, protocol.WireTypeAccountState)

	return result, nil
}

func (n *AccountStateLeafNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	var result []byte
	result = append(result, protocol.HashPrefixLeafNode[:]...)
	result = append(result, n.item.Data()...)
	key := n.item.Key()
	result = append(result, key[:]...)

	return result, nil
}

// NewAccountStateLeafFromWire creates an AccountStateLeafNode from wire format data
func NewAccountStateLeafFromWire(data []byte) (*AccountStateLeafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}

	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeAccountState {
		return nil, fmt.Errorf("invalid wire type for account state: %d", wireType)
	}

	nodeData := data[:len(data)-1]

	// Format: [state_data][32_byte_key]
	if len(nodeData) < 32 {
		return nil, fmt.Errorf("account state data too short")
	}

	// Extract key from last 32 bytes
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	// Verify key is not zero (as per rippled logic)
	if isZeroHash(key) {
		return nil, fmt.Errorf("invalid account state: zero key")
	}

	stateData := nodeData[:keyStart]
	item := NewItem(key, stateData)

	node, err := NewAccountStateLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from wire, not modified
	return node, nil
}

func (n *AccountStateLeafNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return fmt.Errorf("account state leaf has nil item")
	}

	if n.IsZeroHash() {
		return fmt.Errorf("account state leaf has zero hash")
	}

	return nil
}

func (n *AccountStateLeafNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("AccountStateLeafNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))

	if n.item != nil {
		key := n.item.Key()
		sb.WriteString(fmt.Sprintf("Key: %s\n", hex.EncodeToString(key[:])))
		sb.WriteString(fmt.Sprintf("Data Size: %d bytes\n", len(n.item.Data())))
	}

	return sb.String()
}

func (n *AccountStateLeafNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	clonedItem, err := n.item.Clone()
	if err != nil {
		return nil, fmt.Errorf("failed to clone item: %w", err)
	}

	return NewAccountStateLeafNode(clonedItem)
}

// -----------------------------------------------------------------------------
// TransactionLeafNode (transaction without metadata)

// TransactionLeafNode represents a leaf node containing transaction data without metadata
type TransactionLeafNode struct {
	BaseNode
	mu   sync.RWMutex
	item *Item
}

// NewTransactionLeafNode creates a new transaction leaf node
func NewTransactionLeafNode(item *Item) (*TransactionLeafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}

	n := &TransactionLeafNode{
		BaseNode: BaseNode{dirty: true},
		item:     item,
	}
	if err := n.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update hash: %w", err)
	}
	return n, nil
}

func (n *TransactionLeafNode) IsLeaf() bool  { return true }
func (n *TransactionLeafNode) IsInner() bool { return false }

func (n *TransactionLeafNode) Item() *Item {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.item
}

func (n *TransactionLeafNode) SetItem(item *Item) (bool, error) {
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

func (n *TransactionLeafNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

func (n *TransactionLeafNode) updateHashUnsafe() error {
	if n.item == nil {
		return ErrNilItem
	}

	err := n.setHash(protocol.HashPrefixTransactionID[:], n.item.Data())
	if err != nil {
		return err
	}
	return nil
}

func (n *TransactionLeafNode) Type() NodeType {
	return NodeTypeTransactionNoMeta
}

func (n *TransactionLeafNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}
	var result []byte
	// Add transaction + metadata data (no prefix for wire format)
	result = append(result, n.item.Data()...)
	key := n.item.Key()
	result = append(result, key[:]...)
	result = append(result, protocol.WireTypeTransaction)

	return result, nil
}

func (n *TransactionLeafNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	var result []byte
	result = append(result, protocol.HashPrefixTransactionID[:]...)
	result = append(result, n.item.Data()...)
	return result, nil
}

// NewTransactionLeafFromWire creates a TransactionLeafNode from wire format data
func NewTransactionLeafFromWire(data []byte) (*TransactionLeafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}

	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeTransaction {
		return nil, fmt.Errorf("invalid wire type for transaction: %d", wireType)
	}

	nodeData := data[:len(data)-1]

	// For transaction without metadata, the key is derived from hashing the data
	// As per rippled: sha512Half(HashPrefix::transactionID, data)
	key := crypto.Sha512Half(protocol.HashPrefixTransactionID[:], nodeData)

	item := NewItem(key, nodeData)
	node, err := NewTransactionLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from wire, not modified
	return node, nil
}

func (n *TransactionLeafNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return fmt.Errorf("transaction leaf has nil item")
	}

	if n.IsZeroHash() {
		return fmt.Errorf("transaction leaf has zero hash")
	}

	return nil
}

func (n *TransactionLeafNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TransactionLeafNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))

	if n.item != nil {
		key := n.item.Key()
		sb.WriteString(fmt.Sprintf("Key: %s\n", hex.EncodeToString(key[:])))
		sb.WriteString(fmt.Sprintf("Data Size: %d bytes\n", len(n.item.Data())))
	}

	return sb.String()
}

func (n *TransactionLeafNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	clonedItem, err := n.item.Clone()
	if err != nil {
		return nil, fmt.Errorf("failed to clone item: %w", err)
	}

	return NewTransactionLeafNode(clonedItem)
}

// -----------------------------------------------------------------------------
// TransactionWithMetaLeafNode (transaction with metadata)

// TransactionWithMetaLeafNode represents a leaf node containing transaction data with metadata
type TransactionWithMetaLeafNode struct {
	BaseNode
	mu   sync.RWMutex
	item *Item
}

// NewTransactionWithMetaLeafNode creates a new transaction+metadata leaf node
func NewTransactionWithMetaLeafNode(item *Item) (*TransactionWithMetaLeafNode, error) {
	if item == nil {
		return nil, ErrNilItem
	}

	n := &TransactionWithMetaLeafNode{
		BaseNode: BaseNode{dirty: true},
		item:     item,
	}
	if err := n.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update hash: %w", err)
	}
	return n, nil
}

func (n *TransactionWithMetaLeafNode) IsLeaf() bool  { return true }
func (n *TransactionWithMetaLeafNode) IsInner() bool { return false }

func (n *TransactionWithMetaLeafNode) Item() *Item {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.item
}

func (n *TransactionWithMetaLeafNode) SetItem(item *Item) (bool, error) {
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

func (n *TransactionWithMetaLeafNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

func (n *TransactionWithMetaLeafNode) updateHashUnsafe() error {
	if n.item == nil {
		return ErrNilItem
	}

	key := n.item.Key()
	err := n.setHash(protocol.HashPrefixTxNode[:], n.item.Data(), key[:])
	if err != nil {
		return err
	}
	return nil
}

func (n *TransactionWithMetaLeafNode) Type() NodeType {
	return NodeTypeTransactionWithMeta
}

// SerializeForWire - Used for network transmission and proof paths
func (n *TransactionWithMetaLeafNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}
	var result []byte
	// Add transaction + metadata data (no prefix for wire format)
	result = append(result, n.item.Data()...)
	key := n.item.Key()
	result = append(result, key[:]...)
	result = append(result, protocol.WireTypeTransactionWithMeta)

	return result, nil
}
func (n *TransactionWithMetaLeafNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	var result []byte

	result = append(result, protocol.HashPrefixTxNode[:]...)
	result = append(result, n.item.Data()...)
	key := n.item.Key()
	result = append(result, key[:]...)

	return result, nil
}

func NewTransactionWithMetaLeafFromWire(data []byte) (*TransactionWithMetaLeafNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}

	wireType := data[len(data)-1]
	if wireType != protocol.WireTypeTransactionWithMeta {
		return nil, fmt.Errorf("invalid wire type for transaction with meta: %d", wireType)
	}

	nodeData := data[:len(data)-1]

	// Format: [tx_data][32_byte_key]
	if len(nodeData) < 32 {
		return nil, fmt.Errorf("transaction with meta data too short")
	}

	// Extract key from last 32 bytes
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	txData := nodeData[:keyStart]
	item := NewItem(key, txData)

	node, err := NewTransactionWithMetaLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from wire, not modified
	return node, nil
}

func (n *TransactionWithMetaLeafNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return fmt.Errorf("transaction+meta leaf has nil item")
	}

	if n.IsZeroHash() {
		return fmt.Errorf("transaction+meta leaf has zero hash")
	}

	return nil
}

func (n *TransactionWithMetaLeafNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TransactionWithMetaLeafNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))

	if n.item != nil {
		key := n.item.Key()
		sb.WriteString(fmt.Sprintf("Key: %s\n", hex.EncodeToString(key[:])))
		sb.WriteString(fmt.Sprintf("Data Size: %d bytes\n", len(n.item.Data())))
	}

	return sb.String()
}

func (n *TransactionWithMetaLeafNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.item == nil {
		return nil, ErrNilItem
	}

	clonedItem, err := n.item.Clone()
	if err != nil {
		return nil, fmt.Errorf("failed to clone item: %w", err)
	}

	return NewTransactionWithMetaLeafNode(clonedItem)
}

// -----------------------------------------------------------------------------
// Helper Functions

// ItemFromLeafNode extracts the item from any leaf node type
func ItemFromLeafNode(node Node) *Item {
	if node == nil || !node.IsLeaf() {
		return nil
	}

	if leafNode, ok := node.(LeafNode); ok {
		return leafNode.Item()
	}

	return nil
}

// CreateLeafNode creates the appropriate leaf node type for the given node type
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
