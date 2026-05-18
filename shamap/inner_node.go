package shamap

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"

	"github.com/LeJamon/goXRPLd/protocol"
)

const BranchFactor = 16

// zeroHash is the package-wide zero [32]byte used to fill empty
// branches in the hash and serialise paths. Hoisted out of the hot
// path so updateHashUnsafe / SerializeWithPrefix don't reallocate
// `make([]byte, 32)` on every call.
var zeroHash [32]byte

// fullInnerSerializedSize is the wire/serialise size of a full inner
// node payload: HashPrefix(4) + 16 * 32 child hashes.
const fullInnerSerializedSize = 4 + BranchFactor*32

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

// LoadChild returns the child pointer, stored hash, and isBranch bit for
// the given branch under a single read-lock acquisition.
// Used by SHAMap.descend to avoid two separate locked reads on the hot
// traversal path while keeping access correctly synchronised.
func (n *InnerNode) LoadChild(index int) (Node, [32]byte, bool) {
	if index < 0 || index >= BranchFactor {
		return nil, [32]byte{}, false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.children[index], n.hashes[index], n.isBranch&(1<<index) != 0
}

// SetChildIfNil atomically attaches child at the given branch iff that
// slot is currently nil, and returns the resulting (possibly racing)
// child. This is the lock-correct primitive for descend()'s lazy-load
// path: concurrent readers under the SHAMap RLock can race to load the
// same backed child without losing work — the winner's deserialised
// node is installed, the loser observes it and returns it. Without this
// primitive a plain SetChildDirect under inner.mu still races with
// unlocked ChildUnsafe readers because the InnerNode mutex provides no
// guarantee to readers that bypass it.
func (n *InnerNode) SetChildIfNil(index int, child Node) Node {
	if index < 0 || index >= BranchFactor {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if existing := n.children[index]; existing != nil {
		return existing
	}
	n.children[index] = child
	return child
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

// updateHashUnsafe updates hash without locking (caller must hold lock).
// Streams directly into the SHA-512 hasher rather than accumulating a
// `[][]byte` slice — this is called on every Put/Delete for every
// inner on the path so the slice churn used to dominate the profile.
func (n *InnerNode) updateHashUnsafe() error {
	if n.isBranch == 0 {
		// Empty node - hash is zero.
		n.hash = [32]byte{}
		return nil
	}

	h := sha512.New()
	h.Write(protocol.HashPrefixInnerNode[:])
	for i := 0; i < BranchFactor; i++ {
		if n.isBranch&(1<<i) != 0 {
			if child := n.children[i]; child != nil {
				childHash := child.Hash()
				h.Write(childHash[:])
			} else {
				// Hash-only branch (lazy/backed) — use stored hash.
				h.Write(n.hashes[i][:])
			}
		} else {
			h.Write(zeroHash[:])
		}
	}
	sum := h.Sum(nil)
	copy(n.hash[:], sum[:32])
	return nil
}

func (n *InnerNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	// BranchCount takes the read-lock again — call the unlocked variant.
	branchCount := bits.OnesCount16(n.isBranch)
	if branchCount == 0 {
		return nil, ErrEmptyNonRoot
	}

	if branchCount < 12 {
		// Compressed format: only serialize non-empty branches.
		// Format: [Hash32][Position1] × N + [WireType].
		result := make([]byte, 0, branchCount*33+1)
		for i := 0; i < BranchFactor; i++ {
			if n.isBranch&(1<<i) != 0 {
				result = append(result, n.hashes[i][:]...)
				result = append(result, byte(i))
			}
		}
		result = append(result, protocol.WireTypeCompressedInner)
		return result, nil
	}

	// Full format: 16 × 32-byte hashes + WireType.
	result := make([]byte, BranchFactor*32+1)
	for i := 0; i < BranchFactor; i++ {
		off := i * 32
		if n.isBranch&(1<<i) != 0 {
			copy(result[off:off+32], n.hashes[i][:])
		}
		// Empty branch: leave the zero bytes in place.
	}
	result[BranchFactor*32] = protocol.WireTypeInner
	return result, nil
}

// SerializeWithPrefix serializes with type prefix for hashing and storage.
// Result is exactly fullInnerSerializedSize bytes (4 prefix + 16 × 32 hashes).
func (n *InnerNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.isBranch == 0 {
		return nil, ErrEmptyNonRoot
	}

	result := make([]byte, fullInnerSerializedSize)
	copy(result[:4], protocol.HashPrefixInnerNode[:])
	for i := 0; i < BranchFactor; i++ {
		if n.isBranch&(1<<i) != 0 {
			off := 4 + i*32
			copy(result[off:off+32], n.hashes[i][:])
		}
		// Empty branch: leave zero bytes in place.
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

// ReleaseChildren drops the in-memory child pointers, retaining only
// the per-branch hashes so the children can be lazy-reloaded from a
// NodeStore on next access. Used after FlushDirty to allow GC to
// reclaim the freshly-flushed subtree. This is intentionally a method
// rather than direct field access so callers cannot bypass the inner
// mutex or accidentally clear hashes alongside children.
func (n *InnerNode) ReleaseChildren() {
	n.mu.Lock()
	for i := 0; i < BranchFactor; i++ {
		n.children[i] = nil
	}
	n.mu.Unlock()
}

func isZeroHash(hash [32]byte) bool {
	for _, b := range hash {
		if b != 0 {
			return false
		}
	}
	return true
}
