package shamap

import (
	"context"
	"fmt"
)

// ProofPath represents a Merkle proof path from a leaf to the root
type ProofPath struct {
	// Key is the key being proven
	Key [32]byte
	// Path contains serialized nodes from leaf to root
	Path [][]byte
	// Found indicates whether the key exists in the tree
	Found bool
}

// GetProofPath returns a proof path for the given key.
// The path consists of serialized nodes from leaf to root.
// Returns nil if the key does not exist in the map.
func (sm *SHAMap) GetProofPath(key [32]byte) (*ProofPath, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stack := newNodeStack()
	leaf, err := sm.walkToKey(context.Background(), key, stack, true)
	if err != nil {
		return nil, err
	}

	// Verify we found the right leaf
	if leaf == nil {
		return &ProofPath{Key: key, Found: false}, nil
	}

	leafNode, ok := leaf.(LeafNode)
	if !ok {
		return &ProofPath{Key: key, Found: false}, nil
	}

	// Verify this leaf contains the target key
	item := leafNode.Item()
	if item == nil || item.Key() != key {
		return &ProofPath{Key: key, Found: false}, nil
	}

	// Build proof path in leaf-to-root order
	// walkToKey pushes nodes in root-to-leaf order, including the leaf at the end
	// So stack has: [root, ..., parent_of_leaf, leaf]
	// We need to iterate in reverse to get: [leaf, parent_of_leaf, ..., root]
	path := make([][]byte, 0, stack.Len())

	for i := stack.Len() - 1; i >= 0; i-- {
		node := stack.entries[i].node

		// Serialize the node for wire transmission
		serialized, err := node.SerializeForWire()
		if err != nil {
			return nil, fmt.Errorf("failed to serialize node at depth %d: %w", i, err)
		}

		path = append(path, serialized)
	}

	return &ProofPath{
		Key:   key,
		Path:  path,
		Found: true,
	}, nil
}

// VerifyProofPath verifies a Merkle proof path.
// It checks that the path correctly proves the existence of a key
// with the given root hash.
//
// Parameters:
//   - rootHash: the expected root hash of the SHAMap
//   - key: the key being proven
//   - path: serialized nodes from leaf to root
//
// Returns true if the proof is valid, false otherwise.
func VerifyProofPath(rootHash [32]byte, key [32]byte, path [][]byte) bool {
	return VerifyProofPathWithValue(rootHash, key, path) != nil
}

// VerifyProofPathWithValue verifies a Merkle proof path and returns the value if valid.
// This is useful when you want to both verify the proof and extract the proven data.
//
// Parameters:
//   - rootHash: the expected root hash of the SHAMap
//   - key: the key being proven
//   - path: serialized nodes from leaf to root
//
// Returns the item data if proof is valid, nil otherwise.
func VerifyProofPathWithValue(rootHash [32]byte, key [32]byte, path [][]byte) []byte {
	// Validate path length
	if len(path) == 0 || len(path) > MaxDepth+1 {
		return nil
	}

	currentHash := rootHash

	// Process path from root to leaf (reverse iteration since path is leaf-to-root)
	for i := len(path) - 1; i >= 0; i-- {
		nodeData := path[i]

		// Deserialize the node from wire format
		// This may fail if the data is malformed (e.g., from network)
		node, err := DeserializeNodeFromWire(nodeData)
		if err != nil {
			return nil
		}

		// Update the node's hash and verify it matches expected
		if err := node.UpdateHash(); err != nil {
			return nil
		}

		nodeHash := node.Hash()
		if nodeHash != currentHash {
			return nil
		}

		// Calculate depth from root (0 = root, increases toward leaf)
		depth := len(path) - 1 - i

		switch typed := node.(type) {
		case *innerNode:
			// This is an inner node, follow the branch toward our key.
			// Create node ID at this depth to determine which branch to follow.
			nodeID, err := createNodeID(uint8(depth), key)
			if err != nil {
				return nil
			}

			branch := selectBranch(nodeID, key)

			childHash, err := typed.ChildHash(int(branch))
			if err != nil {
				return nil
			}

			// Check if branch is empty (zero hash means no child)
			if childHash == ([32]byte{}) {
				return nil
			}

			currentHash = childHash
		case LeafNode:
			// This should be the final leaf node.
			// Verify we've exhausted all blobs (leaf must be at position 0).
			if i != 0 {
				return nil
			}

			item := typed.Item()
			if item == nil {
				return nil
			}

			// Verify this leaf contains our target key
			if item.Key() != key {
				return nil
			}

			return item.Data()
		default:
			return nil
		}
	}

	// If we get here without finding a leaf, the proof is invalid
	return nil
}
