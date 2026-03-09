package shamap

import (
	"errors"
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

	stack := NewNodeStack()
	leaf, err := sm.walkToKey(key, stack)
	if err != nil {
		return nil, err
	}

	// Verify we found the right leaf
	if leaf == nil {
		return &ProofPath{Key: key, Found: false}, nil
	}

	// Check if it's a leaf node
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
	// Validate path length
	if len(path) == 0 || len(path) > MaxDepth+1 {
		return false
	}

	currentHash := rootHash

	// Process path from root to leaf (reverse iteration since path is leaf-to-root)
	for i := len(path) - 1; i >= 0; i-- {
		nodeData := path[i]

		// Deserialize the node from wire format
		// This may fail if the data is malformed (e.g., from network)
		node, err := DeserializeNodeFromWire(nodeData)
		if err != nil {
			return false
		}

		// Update the node's hash and verify it matches expected
		if err := node.UpdateHash(); err != nil {
			return false
		}

		nodeHash := node.Hash()
		if nodeHash != currentHash {
			return false
		}

		// Calculate depth from root (0 = root, increases toward leaf)
		depth := len(path) - 1 - i

		if node.IsInner() {
			// This is an inner node, follow the branch toward our key
			innerNode, ok := node.(*InnerNode)
			if !ok {
				return false
			}

			// Create node ID at this depth to determine which branch to follow
			nodeID, err := CreateNodeID(uint8(depth), key)
			if err != nil {
				return false
			}

			// Calculate which branch to follow
			branch := SelectBranch(nodeID, key)

			// Get the hash of the child we should follow
			childHash, err := innerNode.ChildHash(int(branch))
			if err != nil {
				return false
			}

			// Check if branch is empty (zero hash means no child)
			if childHash == ([32]byte{}) {
				return false
			}

			currentHash = childHash
		} else if node.IsLeaf() {
			// This should be the final leaf node
			// Verify we've exhausted all blobs (leaf must be at position 0)
			if i != 0 {
				return false
			}

			// Verify this leaf contains our target key
			leafNode, ok := node.(LeafNode)
			if !ok {
				return false
			}

			item := leafNode.Item()
			if item == nil {
				return false
			}

			leafKey := item.Key()
			if leafKey != key {
				return false
			}

			// Successfully verified the proof - leaf key matches target
			return true
		} else {
			// Node is neither inner nor leaf - invalid
			return false
		}
	}

	// If we get here without finding a leaf, the proof is invalid
	return false
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

	// Process path from root to leaf
	for i := len(path) - 1; i >= 0; i-- {
		nodeData := path[i]

		node, err := DeserializeNodeFromWire(nodeData)
		if err != nil {
			return nil
		}

		if err := node.UpdateHash(); err != nil {
			return nil
		}

		nodeHash := node.Hash()
		if nodeHash != currentHash {
			return nil
		}

		depth := len(path) - 1 - i

		if node.IsInner() {
			innerNode, ok := node.(*InnerNode)
			if !ok {
				return nil
			}

			nodeID, err := CreateNodeID(uint8(depth), key)
			if err != nil {
				return nil
			}

			branch := SelectBranch(nodeID, key)

			childHash, err := innerNode.ChildHash(int(branch))
			if err != nil {
				return nil
			}

			if childHash == ([32]byte{}) {
				return nil
			}

			currentHash = childHash
		} else if node.IsLeaf() {
			if i != 0 {
				return nil
			}

			leafNode, ok := node.(LeafNode)
			if !ok {
				return nil
			}

			item := leafNode.Item()
			if item == nil {
				return nil
			}

			if item.Key() != key {
				return nil
			}

			// Return a copy of the data
			return item.Data()
		} else {
			return nil
		}
	}

	return nil
}

// ProofPathError represents an error that occurred during proof verification
// with additional context about where in the path the error occurred.
type ProofPathError struct {
	Position int
	Depth    int
	Message  string
	Err      error
}

func (e *ProofPathError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("proof error at position %d (depth %d): %s: %v",
			e.Position, e.Depth, e.Message, e.Err)
	}
	return fmt.Sprintf("proof error at position %d (depth %d): %s",
		e.Position, e.Depth, e.Message)
}

func (e *ProofPathError) Unwrap() error {
	return e.Err
}

// VerifyProofPathDetailed verifies a Merkle proof path with detailed error reporting.
// Unlike VerifyProofPath which returns a simple bool, this function returns
// a detailed error explaining why verification failed.
//
// Returns nil if the proof is valid, or a ProofPathError explaining the failure.
func VerifyProofPathDetailed(rootHash [32]byte, key [32]byte, path [][]byte) error {
	if len(path) == 0 {
		return &ProofPathError{Position: -1, Depth: -1, Message: "empty proof path"}
	}

	if len(path) > MaxDepth+1 {
		return &ProofPathError{
			Position: -1,
			Depth:    -1,
			Message:  fmt.Sprintf("proof path too long: %d > %d", len(path), MaxDepth+1),
		}
	}

	currentHash := rootHash

	for i := len(path) - 1; i >= 0; i-- {
		nodeData := path[i]
		depth := len(path) - 1 - i

		node, err := DeserializeNodeFromWire(nodeData)
		if err != nil {
			return &ProofPathError{
				Position: i,
				Depth:    depth,
				Message:  "failed to deserialize node",
				Err:      err,
			}
		}

		if err := node.UpdateHash(); err != nil {
			return &ProofPathError{
				Position: i,
				Depth:    depth,
				Message:  "failed to compute node hash",
				Err:      err,
			}
		}

		nodeHash := node.Hash()
		if nodeHash != currentHash {
			return &ProofPathError{
				Position: i,
				Depth:    depth,
				Message:  "hash mismatch",
			}
		}

		if node.IsInner() {
			innerNode, ok := node.(*InnerNode)
			if !ok {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "node claims to be inner but type assertion failed",
				}
			}

			nodeID, err := CreateNodeID(uint8(depth), key)
			if err != nil {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "failed to create node ID",
					Err:      err,
				}
			}

			branch := SelectBranch(nodeID, key)

			childHash, err := innerNode.ChildHash(int(branch))
			if err != nil {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  fmt.Sprintf("failed to get child hash for branch %d", branch),
					Err:      err,
				}
			}

			if childHash == ([32]byte{}) {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  fmt.Sprintf("required branch %d is empty", branch),
				}
			}

			currentHash = childHash
		} else if node.IsLeaf() {
			if i != 0 {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "leaf node found before end of path",
				}
			}

			leafNode, ok := node.(LeafNode)
			if !ok {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "node claims to be leaf but doesn't implement LeafNode interface",
				}
			}

			item := leafNode.Item()
			if item == nil {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "leaf node has nil item",
				}
			}

			if item.Key() != key {
				return &ProofPathError{
					Position: i,
					Depth:    depth,
					Message:  "leaf key doesn't match target key",
				}
			}

			// Success
			return nil
		} else {
			return &ProofPathError{
				Position: i,
				Depth:    depth,
				Message:  "node is neither inner nor leaf",
			}
		}
	}

	return errors.New("proof verification failed - did not reach leaf")
}
