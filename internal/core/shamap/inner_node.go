package shamap

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/protocol"
)

const BranchFactor = 16

var (
	ErrInvalidBranch = errors.New("invalid branch index")
	ErrEmptyNonRoot  = errors.New("non-root inner node cannot be empty")
)

// InnerNode represents an inner node in the SHAMap tree
type InnerNode struct {
	BaseNode
	mu       sync.RWMutex
	children [BranchFactor]Node
	hashes   [BranchFactor][32]byte
	isBranch uint16
}

// NewInnerNode creates a new empty inner node
func NewInnerNode() *InnerNode {
	return &InnerNode{
		BaseNode: BaseNode{dirty: true},
	}
}

// IsLeaf returns false - inner nodes are never leaves
func (n *InnerNode) IsLeaf() bool {
	return false
}

// IsInner returns true - this is an inner node
func (n *InnerNode) IsInner() bool {
	return true
}

// Type returns the node type
func (n *InnerNode) Type() NodeType {
	return NodeTypeInner
}

// IsEmpty returns true if the node has no active branches
func (n *InnerNode) IsEmpty() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.isBranch == 0
}

// IsEmptyBranch returns true if the given branch index is empty
func (n *InnerNode) IsEmptyBranch(index int) bool {
	if index < 0 || index >= BranchFactor {
		return true
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return (n.isBranch & (1 << index)) == 0
}

// BranchCount returns the number of active branches
func (n *InnerNode) BranchCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return bits.OnesCount16(n.isBranch)
}

// Child returns the child node at the given branch index
func (n *InnerNode) Child(index int) (Node, error) {
	if index < 0 || index >= BranchFactor {
		return nil, ErrInvalidBranch
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.children[index], nil
}

// ChildUnsafe returns the child without bounds checking or locking
// Use only when you're certain the index is valid and you hold the lock
func (n *InnerNode) ChildUnsafe(index int) Node {
	return n.children[index]
}

// SetChild sets the child node at the given branch index
func (n *InnerNode) SetChild(index int, child Node) error {
	if index < 0 || index >= BranchFactor {
		return ErrInvalidBranch
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.children[index] = child
	if child != nil {
		n.hashes[index] = child.Hash()
		n.isBranch |= 1 << index
	} else {
		n.hashes[index] = [32]byte{}
		n.isBranch &= ^(1 << index)
	}

	n.dirty = true
	return n.updateHashUnsafe()
}

// SetChildDirect sets the child pointer without updating hash or dirty flag.
// Used for attaching lazily-loaded nodes from the store.
// The caller must ensure the hash is already correct (set during deserialization).
func (n *InnerNode) SetChildDirect(index int, child Node) {
	if index < 0 || index >= BranchFactor {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.children[index] = child
}

// ChildHash returns the hash at a given branch index
func (n *InnerNode) ChildHash(index int) ([32]byte, error) {
	if index < 0 || index >= BranchFactor {
		return [32]byte{}, ErrInvalidBranch
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.hashes[index], nil
}

// ChildHashUnsafe returns the hash without bounds checking or locking
func (n *InnerNode) ChildHashUnsafe(index int) [32]byte {
	return n.hashes[index]
}

// UpdateHash recalculates the node's hash from its children
func (n *InnerNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

// updateHashUnsafe updates hash without locking (caller must hold lock)
func (n *InnerNode) updateHashUnsafe() error {
	if n.isBranch == 0 {
		// Empty node - hash is zero
		n.hash = [32]byte{}
		return nil
	}

	var data [][]byte

	// Add inner node prefix
	data = append(data, protocol.HashPrefixInnerNode[:])

	// Include ALL 16 child hashes in order
	// Empty branches contribute zero hash (32 zero bytes)
	zeroHash := make([]byte, 32)
	for i := 0; i < BranchFactor; i++ {
		if n.isBranch&(1<<i) != 0 {
			child := n.children[i]
			if child != nil {
				// Get hash from actual child node
				childHash := child.Hash()
				data = append(data, childHash[:])
			} else {
				// Child is nil but branch is set - use stored hash
				// This happens when node is deserialized from wire format
				data = append(data, n.hashes[i][:])
			}
		} else {
			// Empty branch: contribute 32 zero bytes
			data = append(data, zeroHash)
		}
	}

	err := n.setHash(data...)
	if err != nil {
		return err
	}
	return nil
}

func (n *InnerNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.isBranch == 0 {
		return nil, ErrEmptyNonRoot
	}

	var result []byte
	branchCount := n.BranchCount()

	if branchCount < 12 {
		// Compressed format: only serialize non-empty branches
		// Format: [Hash32][Position1][Hash32][Position1]...[WireType]
		for i := 0; i < BranchFactor; i++ {
			if n.isBranch&(1<<i) != 0 {
				// Add the 32-byte hash
				result = append(result, n.hashes[i][:]...)
				// Add the 1-byte position
				result = append(result, byte(i))
			}
		}
		// Add compressed inner wire type
		result = append(result, protocol.WireTypeCompressedInner)
	} else {
		// Full format: serialize all 16 hashes (including zero hashes)
		// Format: [Hash0][Hash1]...[Hash15][WireType]
		zeroHash := make([]byte, 32)
		for i := 0; i < BranchFactor; i++ {
			if n.isBranch&(1<<i) != 0 {
				// Non-empty branch: use the stored hash
				result = append(result, n.hashes[i][:]...)
			} else {
				// Empty branch: use zero hash
				result = append(result, zeroHash...)
			}
		}
		// Add full inner wire type
		result = append(result, protocol.WireTypeInner)
	}

	return result, nil
}

// SerializeWithPrefix serializes with type prefix for hashing and storage
func (n *InnerNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.isBranch == 0 {
		return nil, ErrEmptyNonRoot
	}

	var result []byte

	// Add the inner node prefix (4 bytes)
	result = append(result, protocol.HashPrefixInnerNode[:]...)

	// Add ALL 16 child hashes in order (even empty ones as zero hashes)
	zeroHash := make([]byte, 32)
	for i := 0; i < BranchFactor; i++ {
		if n.isBranch&(1<<i) != 0 {
			// Non-empty branch: use the stored hash
			result = append(result, n.hashes[i][:]...)
		} else {
			// Empty branch: use zero hash
			result = append(result, zeroHash...)
		}
	}

	return result, nil
}

// NewInnerNodeFromWire creates an InnerNode from wire format data
func NewInnerNodeFromWire(data []byte) (*InnerNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}

	wireType := data[len(data)-1]
	nodeData := data[:len(data)-1]

	switch wireType {
	case protocol.WireTypeInner:
		return parseFullInnerNode(nodeData)
	case protocol.WireTypeCompressedInner:
		return parseCompressedInnerNode(nodeData)
	default:
		return nil, fmt.Errorf("invalid wire type for inner node: %d", wireType)
	}
}

// parseFullInnerNode parses a full inner node (16 hashes of 32 bytes each = 512 bytes)
func parseFullInnerNode(data []byte) (*InnerNode, error) {
	expectedSize := BranchFactor * 32 // 16 * 32 = 512 bytes
	if len(data) != expectedSize {
		return nil, fmt.Errorf("invalid full inner node size: expected %d, got %d", expectedSize, len(data))
	}

	node := NewInnerNode()

	// Read 16 child hashes in order
	for i := 0; i < BranchFactor; i++ {
		start := i * 32
		end := start + 32

		var hash [32]byte
		copy(hash[:], data[start:end])

		// Set the hash and update isBranch if non-zero
		if !isZeroHash(hash) {
			node.hashes[i] = hash
			node.isBranch |= 1 << i
		}
	}

	// Update the node's own hash
	if err := node.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update inner node hash: %w", err)
	}

	node.dirty = false // loaded from wire, not modified
	return node, nil
}

// parseCompressedInnerNode parses compressed format: series of (32-byte hash + 1-byte position)
func parseCompressedInnerNode(data []byte) (*InnerNode, error) {
	const chunkSize = 33 // 32 bytes hash + 1 byte position

	if len(data)%chunkSize != 0 {
		return nil, fmt.Errorf("invalid compressed inner node size: %d not divisible by %d", len(data), chunkSize)
	}

	if len(data) > chunkSize*BranchFactor {
		return nil, fmt.Errorf("compressed inner node too large: %d > %d", len(data), chunkSize*BranchFactor)
	}

	node := NewInnerNode()

	// Parse each hash+position pair
	for i := 0; i < len(data); i += chunkSize {
		// Read 32-byte hash
		var hash [32]byte
		copy(hash[:], data[i:i+32])

		// Read 1-byte position
		position := data[i+32]
		if position >= BranchFactor {
			return nil, fmt.Errorf("invalid branch position: %d >= %d", position, BranchFactor)
		}

		// Set the hash at the specified position
		node.hashes[position] = hash
		node.isBranch |= 1 << position
	}

	// Update the node's own hash
	if err := node.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update inner node hash: %w", err)
	}

	node.dirty = false // loaded from wire, not modified
	return node, nil
}

// String returns a human-readable representation of the node
func (n *InnerNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("InnerNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))
	sb.WriteString("Branches:\n")

	for i := 0; i < BranchFactor; i++ {
		if n.isBranch&(1<<i) != 0 {
			sb.WriteString(fmt.Sprintf("  %d: %s\n", i, hex.EncodeToString(n.hashes[i][:])))
		}
	}

	return sb.String()
}

// Invariants performs internal consistency checks
func (n *InnerNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	count := 0
	for i := 0; i < BranchFactor; i++ {
		hasChild := n.children[i] != nil
		hasBit := (n.isBranch & (1 << i)) != 0
		hasHash := !isZeroHash(n.hashes[i])

		// Valid states:
		// 1. hasBit && hasChild && hasHash — expanded node
		// 2. hasBit && !hasChild && hasHash — hash-only (lazy, backed)
		// 3. !hasBit && !hasChild && !hasHash — empty branch
		if hasBit && !hasHash {
			return fmt.Errorf("branch %d: bit set but no hash", i)
		}
		if hasChild && !hasBit {
			return fmt.Errorf("branch %d: child present but bit not set", i)
		}
		if !hasBit && hasChild {
			return fmt.Errorf("branch %d: child present in empty branch", i)
		}

		if hasChild {
			count++
			// Verify child hash matches stored hash
			childHash := n.children[i].Hash()
			if childHash != n.hashes[i] {
				return fmt.Errorf("branch %d hash mismatch", i)
			}
		} else if hasBit {
			// Hash-only branch (lazy/backed) — count it
			count++
		}
	}

	if count == 0 && !isRoot {
		return ErrEmptyNonRoot
	}

	// Verify hash is correct
	if !n.IsZeroHash() {
		// Create a temporary copy to verify hash
		temp := &InnerNode{
			isBranch: n.isBranch,
			hashes:   n.hashes,
			children: n.children,
		}
		if err := temp.updateHashUnsafe(); err != nil {
			return fmt.Errorf("failed to verify hash: %w", err)
		}
		if temp.hash != n.hash {
			return fmt.Errorf("stored hash does not match computed hash")
		}
	}

	return nil
}

// Clone returns a deep copy of the node
func (n *InnerNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	clone := &InnerNode{
		BaseNode: BaseNode{hash: n.hash, dirty: true},
		isBranch: n.isBranch,
		hashes:   n.hashes, // Copy the array
	}

	// Deep clone children
	for i := 0; i < BranchFactor; i++ {
		if n.children[i] != nil {
			childClone, err := n.children[i].Clone()
			if err != nil {
				return nil, fmt.Errorf("failed to clone child at branch %d: %w", i, err)
			}
			clone.children[i] = childClone
		}
	}

	return clone, nil
}

// ForEachChild calls fn for each non-nil child with its branch index
// If fn returns false, iteration stops early
func (n *InnerNode) ForEachChild(fn func(index int, child Node) bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for i := 0; i < BranchFactor; i++ {
		if n.children[i] != nil {
			if !fn(i, n.children[i]) {
				break
			}
		}
	}
}

// HasChildren returns true if the node has any children
func (n *InnerNode) HasChildren() bool {
	return !n.IsEmpty()
}

// setChildHashForProof is used when deserializing an InnerNode for proof verification
func (n *InnerNode) setChildHashForProof(index int, hash [32]byte) error {
	if index < 0 || index >= BranchFactor {
		return ErrInvalidBranch
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.hashes[index] = hash
	n.children[index] = nil // No actual child, just hash
	if !isZeroHash(hash) {
		n.isBranch |= 1 << index
	} else {
		n.isBranch &= ^(1 << index)
	}

	return nil
}

func isZeroHash(hash [32]byte) bool {
	for _, b := range hash {
		if b != 0 {
			return false
		}
	}
	return true
}
