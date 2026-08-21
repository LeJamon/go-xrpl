package shamap

import (
	"errors"
	"fmt"
)

const (
	// NodeIDSize is the size of a serialized NodeID in bytes
	NodeIDSize = 33
)

// Errors returned by NodeID parsing and tree traversal.
var (
	errInvalidNodeIDLength = errors.New("invalid NodeID length")
	errMaxDepthExceeded    = errors.New("maximum depth exceeded")
	errNonCanonicalNodeID  = errors.New("non-canonical NodeID")
)

// NodeID represents a node's position in the SHAMap tree
type NodeID struct {
	depth uint8    // How many bits of the hash are relevant
	id    [32]byte // The key prefix from the leaf's hash
}

func newRootNodeID() NodeID {
	return NodeID{depth: 0, id: [32]byte{}}
}

// createNodeID creates a node ID for a given key and depth
func createNodeID(depth uint8, key [32]byte) (NodeID, error) {
	if depth > maxDepth {
		return NodeID{}, errMaxDepthExceeded
	}

	// Apply depth mask to ensure only relevant bits are set
	var id [32]byte
	copy(id[:], key[:])

	// Mask out irrelevant bits beyond the depth
	if depth < maxDepth {
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

// IsRoot reports whether this node ID identifies the root.
func (n NodeID) IsRoot() bool {
	return n.depth == 0
}

// ParseNodeID parses a NodeID from its NodeIDSize-byte binary encoding.
func ParseNodeID(data []byte) (NodeID, error) {
	if len(data) != NodeIDSize {
		return NodeID{}, fmt.Errorf("%w: got %d, want %d", errInvalidNodeIDLength, len(data), NodeIDSize)
	}

	depth := data[32]
	if depth > maxDepth {
		return NodeID{}, errMaxDepthExceeded
	}

	var id [32]byte
	copy(id[:], data[:32])
	canonical, err := createNodeID(depth, id)
	if err != nil {
		return NodeID{}, err
	}
	if canonical.id != id {
		return NodeID{}, errNonCanonicalNodeID
	}

	return canonical, nil
}

// Bytes returns the wire format: 32-byte ID + 1-byte depth
func (n NodeID) Bytes() []byte {
	data := make([]byte, NodeIDSize)
	copy(data[:32], n.id[:])
	data[32] = n.depth
	return data
}

func (n NodeID) childNodeID(branch uint8) (NodeID, error) {
	if branch > 15 {
		return NodeID{}, errInvalidBranch
	}

	if n.depth >= maxDepth {
		return NodeID{}, errMaxDepthExceeded
	}

	newDepth := n.depth + 1
	newID := n.id // Copy the array

	byteIndex := n.depth / 2
	if byteIndex >= 32 {
		return NodeID{}, errMaxDepthExceeded
	}

	isHighNibble := n.depth%2 == 0

	if isHighNibble {
		newID[byteIndex] = (newID[byteIndex] & 0x0F) | (branch << 4) //nolint:gosec // G602: byteIndex < 32 guarded above
	} else {
		newID[byteIndex] = (newID[byteIndex] & 0xF0) | branch //nolint:gosec // G602: byteIndex < 32 guarded above
	}

	return NodeID{depth: newDepth, id: newID}, nil
}
