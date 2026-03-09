package shamap

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	// MaxDepth is the maximum depth of the SHAMap tree
	MaxDepth = 64
	// BranchMask is the mask for valid branch values (0-15)
	BranchMask = 0x0F
	// NodeIDSize is the size of a serialized NodeID in bytes
	NodeIDSize = 33
)

var (
	ErrInvalidNodeIDLength = errors.New("invalid NodeID length")
	ErrMaxDepthExceeded    = errors.New("maximum depth exceeded")
)

// NodeID represents a node's position in the SHAMap tree
type NodeID struct {
	depth uint8    // How many bits of the hash are relevant
	id    [32]byte // The key prefix from the leaf's hash
}

// NewNodeID New creates a new NodeID with the given depth and ID
func NewNodeID(depth uint8, id [32]byte) (NodeID, error) {
	if depth > MaxDepth {
		return NodeID{}, ErrMaxDepthExceeded
	}

	return NodeID{depth: depth, id: id}, nil
}

// NewRootNodeID creates a new root NodeID
func NewRootNodeID() NodeID {
	return NodeID{depth: 0, id: [32]byte{}}
}

// CreateNodeID creates a node ID for a given key and depth
func CreateNodeID(depth uint8, key [32]byte) (NodeID, error) {
	if depth > MaxDepth {
		return NodeID{}, ErrMaxDepthExceeded
	}

	// Apply depth mask to ensure only relevant bits are set
	var id [32]byte
	copy(id[:], key[:])

	// Mask out irrelevant bits beyond the depth
	if depth < MaxDepth {
		byteIndex := depth / 2
		if depth%2 == 1 {
			// Clear lower nibble of the byte at depth boundary
			if byteIndex < 32 {
				id[byteIndex] &= 0xF0
			}
		}
		// Clear all bytes beyond the depth boundary
		for i := (depth + 1) / 2; i < 32; i++ {
			id[i] = 0
		}
	}

	return NodeID{depth: depth, id: id}, nil
}

// Depth returns the depth of this node
func (n NodeID) Depth() uint8 {
	return n.depth
}

// ID returns the ID bytes
func (n NodeID) ID() [32]byte {
	return n.id
}

// IsRoot returns true if this node is the root
func (n NodeID) IsRoot() bool {
	return n.depth == 0
}

// MarshalBinary implements encoding.BinaryMarshaler
func (n NodeID) MarshalBinary() ([]byte, error) {
	data := make([]byte, NodeIDSize)
	copy(data[:32], n.id[:])
	data[32] = n.depth
	return data, nil
}

// UnmarshalBinary parses a NodeID from binary data and returns a new NodeID
func UnmarshalBinary(data []byte) (NodeID, error) {
	if len(data) != NodeIDSize {
		return NodeID{}, fmt.Errorf("%w: got %d, want %d", ErrInvalidNodeIDLength, len(data), NodeIDSize)
	}

	depth := data[32]
	if depth > MaxDepth {
		return NodeID{}, ErrMaxDepthExceeded
	}

	var id [32]byte
	copy(id[:], data[:32])

	return NodeID{depth: depth, id: id}, nil
}

// FromBytes parses a NodeID from raw bytes
func FromBytes(data []byte) (NodeID, error) {
	return UnmarshalBinary(data)
}

// Bytes returns the wire format: 32-byte ID + 1-byte depth
func (n NodeID) Bytes() []byte {
	data, _ := n.MarshalBinary() // Cannot fail for valid NodeID
	return data
}

// ChildNodeID returns the child node ID for the given branch (0-15)
func (n NodeID) ChildNodeID(branch uint8) (NodeID, error) {
	if branch > 15 {
		return NodeID{}, ErrInvalidBranch
	}

	if n.depth >= MaxDepth {
		return NodeID{}, ErrMaxDepthExceeded
	}

	newDepth := n.depth + 1
	newID := n.id // Copy the array

	byteIndex := n.depth / 2
	if byteIndex >= 32 {
		return NodeID{}, ErrMaxDepthExceeded
	}

	isHighNibble := n.depth%2 == 0

	if isHighNibble {
		newID[byteIndex] = (newID[byteIndex] & 0x0F) | (branch << 4)
	} else {
		newID[byteIndex] = (newID[byteIndex] & 0xF0) | branch
	}

	return NodeID{depth: newDepth, id: newID}, nil
}

// ParentNodeID returns the parent node ID
func (n NodeID) ParentNodeID() (NodeID, error) {
	if n.IsRoot() {
		return NodeID{}, errors.New("root node has no parent")
	}

	parentDepth := n.depth - 1
	parentID := n.id // Copy the array

	// Clear the nibble that was set by this child
	byteIndex := parentDepth / 2
	if byteIndex < 32 {
		isHighNibble := parentDepth%2 == 0
		if isHighNibble {
			parentID[byteIndex] &= 0x0F // Clear high nibble
		} else {
			parentID[byteIndex] &= 0xF0 // Clear low nibble
		}
	}

	return NodeID{depth: parentDepth, id: parentID}, nil
}

// SelectBranch returns which branch of a node would contain the given key
func SelectBranch(nodeID NodeID, key [32]byte) uint8 {
	depth := nodeID.depth
	if depth >= MaxDepth {
		return 0
	}

	byteIndex := depth / 2
	if byteIndex >= 32 {
		return 0
	}

	b := key[byteIndex]
	if depth%2 == 0 {
		return b >> 4 // Use upper 4 bits
	}
	return b & BranchMask // Use lower 4 bits
}

// String returns a human-readable representation of the node ID
func (n NodeID) String() string {
	if n.IsRoot() {
		return "NodeID(root)"
	}

	// Only show relevant bytes based on depth
	relevantBytes := (n.depth + 1) / 2
	if relevantBytes > 32 {
		relevantBytes = 32
	}

	return fmt.Sprintf("NodeID(depth=%d, id=%s)",
		n.depth,
		hex.EncodeToString(n.id[:relevantBytes]))
}

// Equal returns true if two NodeIDs are equal
func (n NodeID) Equal(other NodeID) bool {
	return n.depth == other.depth && n.id == other.id
}

// Compare compares two NodeIDs for ordering
// Returns -1 if n < other, 0 if equal, 1 if n > other
func (n NodeID) Compare(other NodeID) int {
	if n.depth < other.depth {
		return -1
	}
	if n.depth > other.depth {
		return 1
	}

	return bytes.Compare(n.id[:], other.id[:])
}

// IsDescendantOf returns true if this NodeID is a descendant of the other
func (n NodeID) IsDescendantOf(ancestor NodeID) bool {
	if n.depth <= ancestor.depth {
		return false
	}

	// Check if the ancestor's ID is a prefix of this ID
	ancestorBytes := (ancestor.depth + 1) / 2
	for i := 0; i < int(ancestorBytes); i++ {
		if i >= 32 {
			break
		}

		ancestorByte := ancestor.id[i]
		ourByte := n.id[i]

		// For the last relevant byte, we might need to mask
		if i == int(ancestorBytes)-1 && ancestor.depth%2 == 0 {
			// Only compare the high nibble
			if (ancestorByte & 0xF0) != (ourByte & 0xF0) {
				return false
			}
		} else {
			if ancestorByte != ourByte {
				return false
			}
		}
	}

	return true
}

// IsAncestorOf returns true if this NodeID is an ancestor of the other
func (n NodeID) IsAncestorOf(descendant NodeID) bool {
	return descendant.IsDescendantOf(n)
}

// Validate performs validation on the NodeID
func (n NodeID) Validate() error {
	if n.depth > MaxDepth {
		return ErrMaxDepthExceeded
	}

	// Check that bits beyond the depth are properly zeroed
	if n.depth < MaxDepth {
		// Check bytes that should be completely zero
		startByte := (n.depth + 1) / 2
		for i := startByte; i < 32; i++ {
			if n.id[i] != 0 {
				return fmt.Errorf("byte %d should be zero for depth %d", i, n.depth)
			}
		}

		// Check partial byte if depth is odd
		if n.depth%2 == 0 && startByte > 0 {
			byteIndex := startByte - 1
			if byteIndex < 32 && (n.id[byteIndex]&0x0F) != 0 {
				return fmt.Errorf("lower nibble of byte %d should be zero for depth %d", byteIndex, n.depth)
			}
		}
	}

	return nil
}
