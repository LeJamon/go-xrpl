package shamap

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
)

// BranchFactor is the number of children of a SHAMap inner node: one per
// nibble (4 bits) of the key.
const BranchFactor = 16

const fullInnerSerializedSize = 4 + BranchFactor*32

// Errors returned by inner-node operations.
var (
	ErrInvalidBranch = errors.New("invalid branch index")
	ErrEmptyNonRoot  = errors.New("non-root inner node cannot be empty")
)

// innerNode represents an inner node in the SHAMap tree
type innerNode struct {
	baseNode
	mu       sync.RWMutex
	children [BranchFactor]Node
	hashes   [BranchFactor][32]byte
	isBranch uint16
}

// newInnerNode creates a new empty inner node
func newInnerNode() *innerNode {
	n := &innerNode{}
	n.SetDirty(true)
	return n
}

// Type returns the node type
func (n *innerNode) Type() NodeType {
	return NodeTypeInner
}

// Hash returns the node's hash under the node lock, so readers on a
// structurally-shared subtree never race a concurrent recompute.
func (n *innerNode) Hash() [32]byte {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.hash
}

// IsEmpty returns true if the node has no active branches
func (n *innerNode) IsEmpty() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.isBranch == 0
}

// IsEmptyBranch returns true if the given branch index is empty
func (n *innerNode) IsEmptyBranch(index int) bool {
	if index < 0 || index >= BranchFactor {
		return true
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return (n.isBranch & (1 << index)) == 0
}

// BranchCount returns the number of active branches
func (n *innerNode) BranchCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return bits.OnesCount16(n.isBranch)
}

// Child returns the child node at the given branch index
func (n *innerNode) Child(index int) (Node, error) {
	if index < 0 || index >= BranchFactor {
		return nil, ErrInvalidBranch
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.children[index], nil
}

// SetChild sets the child node at the given branch index
func (n *innerNode) SetChild(index int, child Node) error {
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

	n.SetDirty(true)
	return n.updateHashUnsafe()
}

// LoadChild returns the child pointer, stored hash, and isBranch bit for
// the given branch under a single read-lock acquisition.
// Index must be in [0, BranchFactor); out-of-range panics on slice deref.
// The returned [32]byte is the parent's stored hash for the branch, which
// may lag child.Hash() during a mutation cycle because dirtyUp clears
// parent hashes before they are recomputed. Callers that need the child's
// own current hash should call child.Hash() on the returned pointer.
func (n *innerNode) LoadChild(index int) (Node, [32]byte, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.children[index], n.hashes[index], n.isBranch&(1<<index) != 0
}

// SetChildIfNil attaches child at index iff that slot is currently nil and
// returns the resulting child. Concurrent readers racing to lazy-load the
// same backed child observe a single winning installation.
// Same index contract as LoadChild; out-of-range panics on slice deref.
//
// Preconditions (mirroring rippled canonicalizeChild,
// SHAMapInnerNode.cpp:397-412, enforced by construction at callers, not at
// runtime): branch must be a non-empty branch in isBranch, child must be
// non-nil, and child.Hash() must equal n.hashes[index].
func (n *innerNode) SetChildIfNil(index int, child Node) Node {
	n.mu.Lock()
	defer n.mu.Unlock()
	if existing := n.children[index]; existing != nil {
		return existing
	}
	n.children[index] = child
	return child
}

// ChildHash returns the hash at a given branch index
func (n *innerNode) ChildHash(index int) ([32]byte, error) {
	if index < 0 || index >= BranchFactor {
		return [32]byte{}, ErrInvalidBranch
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.hashes[index], nil
}

// UpdateHash recalculates the node's hash from its children
func (n *innerNode) UpdateHash() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updateHashUnsafe()
}

// updateHashUnsafe updates hash without locking (caller must hold lock).
func (n *innerNode) updateHashUnsafe() error {
	if n.isBranch == 0 {
		n.hash = [32]byte{}
		return nil
	}

	h := common.AcquireSHA512()
	defer common.ReleaseSHA512(h)
	h.Write(protocol.HashPrefixInnerNode[:])
	for i := range BranchFactor {
		if n.isBranch&(1<<i) != 0 {
			ch := n.childPreimageHash(i)
			h.Write(ch[:])
		} else {
			var zero [32]byte
			h.Write(zero[:])
		}
	}
	var buf [sha512.Size]byte
	sum := h.Sum(buf[:0])
	copy(n.hash[:], sum[:32])
	return nil
}

// childPreimageHash returns the hash branch i contributes to this node's
// preimage: the live child's current hash when the child is loaded, otherwise
// the cached hashes[i]. updateHashUnsafe and both serializers read this single
// source, so the in-memory node hash and the serialized preimage are computed
// identically and can never disagree — even mid-mutation, when dirtyUp clears
// parent hashes before they are recomputed and the split path leaves the chain
// transiently stale (see LoadChild and the split-chain note in put.go).
// Hash-only branches (children released after flush) fall back to hashes[i],
// which updateHashDeep keeps authoritative before ReleaseChildren runs.
// Caller must hold n.mu.
func (n *innerNode) childPreimageHash(i int) [32]byte {
	if child := n.children[i]; child != nil {
		return child.Hash()
	}
	return n.hashes[i]
}

// firstStalePreimage reports the first loaded branch whose cached hashes[i]
// disagrees with its live child's hash. ok is false when every loaded child
// matches its cached preimage — the invariant SetChild and updateHashDeep
// maintain. Caller must hold n.mu.
func (n *innerNode) firstStalePreimage() (branch int, cached, live [32]byte, ok bool) {
	for i := range BranchFactor {
		child := n.children[i]
		if child == nil {
			continue
		}
		if lh := child.Hash(); lh != n.hashes[i] {
			return i, n.hashes[i], lh, true
		}
	}
	return 0, [32]byte{}, [32]byte{}, false
}

// updateHashDeep resyncs the cached hashes[] preimage with every loaded child,
// then recomputes this node's own hash. flushNode calls it before releasing
// child pointers so the cache stays authoritative once the children are gone:
// after ReleaseChildren a branch serializes from hashes[i] (childPreimageHash
// falls back to it when the child is nil), so any value left stale by a
// mutation cycle must be reconciled here first. Mirrors rippled's
// SHAMapInnerNode::updateHashDeep, invoked from walkSubTree before each inner
// node is written.
func (n *innerNode) updateHashDeep() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range BranchFactor {
		if n.isBranch&(1<<i) != 0 {
			if child := n.children[i]; child != nil {
				n.hashes[i] = child.Hash()
			}
		}
	}
	return n.updateHashUnsafe()
}

// SerializeForWire emits the on-wire inner-node payload. Each branch
// contributes childPreimageHash(i) — the same live-child-preferred source
// updateHashUnsafe hashes — so the wire bytes always hash to this node's
// reported hash. rippled's SHAMapInnerNode::serializeForWire reads hashes_
// directly; go-xrpl prefers the live child because its mutation cycle can
// leave hashes[i] transiently lagging child.Hash() (see childPreimageHash).
// The bytes are identical once the cache is in sync.
func (n *innerNode) SerializeForWire() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	// Avoid BranchCount(): it would re-acquire the read lock.
	branchCount := bits.OnesCount16(n.isBranch)
	if branchCount == 0 {
		return nil, ErrEmptyNonRoot
	}

	if branchCount < 12 {
		// Compressed: [Hash32][Position1] × N + [WireType].
		result := make([]byte, 0, branchCount*33+1)
		for i := range BranchFactor {
			if n.isBranch&(1<<i) != 0 {
				ch := n.childPreimageHash(i)
				result = append(result, ch[:]...)
				result = append(result, byte(i))
			}
		}
		result = append(result, protocol.WireTypeCompressedInner)
		return result, nil
	}

	// Full: 16 × 32-byte hashes + WireType.
	result := make([]byte, BranchFactor*32+1)
	for i := range BranchFactor {
		off := i * 32
		if n.isBranch&(1<<i) != 0 {
			ch := n.childPreimageHash(i)
			copy(result[off:off+32], ch[:])
		}
	}
	result[BranchFactor*32] = protocol.WireTypeInner
	return result, nil
}

// SerializeWithPrefix serializes with the type prefix for hashing and storage.
// Like SerializeForWire, each branch contributes childPreimageHash(i) (live
// child preferred, cached hashes[i] as fallback) — the same source
// updateHashUnsafe hashes — so the preimage always hashes to this node's hash.
// rippled's SHAMapInnerNode::serializeWithPrefix reads hashes_ directly; the
// bytes match once the cache is in sync, which flushNode guarantees by calling
// updateHashDeep before releasing children.
func (n *innerNode) SerializeWithPrefix() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.isBranch == 0 {
		return nil, ErrEmptyNonRoot
	}

	result := make([]byte, fullInnerSerializedSize)
	copy(result[:4], protocol.HashPrefixInnerNode[:])
	for i := range BranchFactor {
		if n.isBranch&(1<<i) != 0 {
			off := 4 + i*32
			ch := n.childPreimageHash(i)
			copy(result[off:off+32], ch[:])
		}
	}
	return result, nil
}

// newInnerNodeFromWire creates an innerNode from wire format data
func newInnerNodeFromWire(data []byte) (*innerNode, error) {
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
func parseFullInnerNode(data []byte) (*innerNode, error) {
	expectedSize := BranchFactor * 32 // 16 * 32 = 512 bytes
	if len(data) != expectedSize {
		return nil, fmt.Errorf("invalid full inner node size: expected %d, got %d", expectedSize, len(data))
	}

	node := newInnerNode()

	// Read 16 child hashes in order
	for i := range BranchFactor {
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

	node.SetDirty(false) // loaded from wire, not modified
	return node, nil
}

// parseCompressedInnerNode parses compressed format: series of (32-byte hash + 1-byte position)
func parseCompressedInnerNode(data []byte) (*innerNode, error) {
	const chunkSize = 33 // 32 bytes hash + 1 byte position

	if len(data)%chunkSize != 0 {
		return nil, fmt.Errorf("invalid compressed inner node size: %d not divisible by %d", len(data), chunkSize)
	}

	if len(data) > chunkSize*BranchFactor {
		return nil, fmt.Errorf("compressed inner node too large: %d > %d", len(data), chunkSize*BranchFactor)
	}

	node := newInnerNode()

	// Parse each hash+position pair
	for i := 0; i < len(data); i += chunkSize {
		// Read 32-byte hash
		var hash [32]byte
		copy(hash[:], data[i:i+32])

		// Read 1-byte position
		position := data[i+32] //nolint:gosec // G602: len(data) is a multiple of chunkSize=33 (guarded above), so i+32 is in range
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

	node.SetDirty(false) // loaded from wire, not modified
	return node, nil
}

// String returns a human-readable representation of the node
func (n *innerNode) String(id NodeID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("innerNode ID: %s\n", id.String()))
	sb.WriteString(fmt.Sprintf("Hash: %s\n", hex.EncodeToString(n.hash[:])))
	sb.WriteString("Branches:\n")

	for i := range BranchFactor {
		if n.isBranch&(1<<i) != 0 {
			sb.WriteString(fmt.Sprintf("  %d: %s\n", i, hex.EncodeToString(n.hashes[i][:])))
		}
	}

	return sb.String()
}

// Invariants performs internal consistency checks
func (n *innerNode) Invariants(isRoot bool) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	count := 0
	for i := range BranchFactor {
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

		if hasChild || hasBit {
			count++
		}
	}

	if branch, _, _, stale := n.firstStalePreimage(); stale {
		return fmt.Errorf("branch %d hash mismatch", branch)
	}

	if count == 0 && !isRoot {
		return ErrEmptyNonRoot
	}

	// Verify hash is correct
	if !n.IsZeroHash() {
		// Create a temporary copy to verify hash
		temp := &innerNode{
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
func (n *innerNode) Clone() (Node, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	clone := &innerNode{
		isBranch: n.isBranch,
		hashes:   n.hashes, // Copy the array
	}
	clone.hash = n.hash
	clone.SetDirty(true)

	// Deep clone children
	for i := range BranchFactor {
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

// shallowClone returns a copy of n that shares every child pointer and
// branch hash with the source but has its own header (mutex, hash, dirty
// flag). It is the core primitive of the path-copy persistence used by
// mutation paths: rewriting a single leaf rebuilds only the chain of
// inner nodes from root down to that leaf, while every untouched subtree
// stays structurally shared with whichever snapshot or sibling map still
// references the source node.
func (n *innerNode) shallowClone() *innerNode {
	n.mu.RLock()
	defer n.mu.RUnlock()
	clone := &innerNode{
		isBranch: n.isBranch,
		hashes:   n.hashes,
		children: n.children,
	}
	clone.hash = n.hash
	clone.SetDirty(true)
	return clone
}

// HasChildren returns true if the node has any children
func (n *innerNode) HasChildren() bool {
	return !n.IsEmpty()
}

// ReleaseChildren drops in-memory child pointers while retaining per-branch
// hashes, allowing GC to reclaim a freshly-flushed subtree that will be
// lazy-reloaded from the NodeStore on next access.
func (n *innerNode) ReleaseChildren() {
	n.mu.Lock()
	for i := range BranchFactor {
		n.children[i] = nil
	}
	n.mu.Unlock()
}
