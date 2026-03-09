package shamap

import (
	"errors"
	"fmt"
)

// Wire protocol errors
var (
	ErrNodeNotLoaded     = errors.New("node not loaded")
	ErrInvalidDepth      = errors.New("invalid depth parameter")
	ErrSerializationFail = errors.New("serialization failed")
)

// NodeData represents a node's ID and serialized data for wire transmission.
// This is used when sending multiple nodes in a single response,
// such as when responding to a "fat" node request.
type NodeData struct {
	// Hash is the 32-byte hash identifying this node
	Hash [32]byte
	// Data is the serialized wire format of the node
	Data []byte
}

// String returns a string representation of the NodeData.
func (n *NodeData) String() string {
	return fmt.Sprintf("NodeData(hash=%x, size=%d)", n.Hash[:8], len(n.Data))
}

// Clone creates a deep copy of the NodeData.
func (n *NodeData) Clone() *NodeData {
	dataCopy := make([]byte, len(n.Data))
	copy(dataCopy, n.Data)
	return &NodeData{
		Hash: n.Hash,
		Data: dataCopy,
	}
}

// GetNodeFat retrieves a node and its descendants up to the specified depth.
// This is used to fetch multiple related nodes in a single request,
// reducing round-trips during synchronization.
//
// Parameters:
//   - nodeHash: the hash of the root node to fetch
//   - depth: how many levels of children to include (0 = just the node itself)
//
// Returns a slice of NodeData containing the node and its descendants.
// The first element is always the requested node, followed by its children
// in breadth-first order.
func (sm *SHAMap) GetNodeFat(nodeHash [32]byte, depth int) ([]NodeData, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if depth < 0 {
		return nil, ErrInvalidDepth
	}

	// Find the node with the given hash
	node := sm.findNodeByHash(nodeHash)
	if node == nil {
		return nil, ErrNodeNotFound
	}

	// Collect nodes up to the specified depth using BFS
	var result []NodeData

	type workItem struct {
		node  Node
		depth int
	}

	queue := []workItem{{node: node, depth: 0}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		// Serialize the node
		data, err := item.node.SerializeForWire()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSerializationFail, err)
		}

		result = append(result, NodeData{
			Hash: item.node.Hash(),
			Data: data,
		})

		// Add children if we haven't reached max depth
		if item.depth < depth && !item.node.IsLeaf() {
			inner, ok := item.node.(*InnerNode)
			if !ok {
				continue
			}

			for branch := 0; branch < BranchFactor; branch++ {
				child, err := inner.Child(branch)
				if err != nil || child == nil {
					continue
				}

				queue = append(queue, workItem{
					node:  child,
					depth: item.depth + 1,
				})
			}
		}
	}

	return result, nil
}

// findNodeByHash searches the tree for a node with the given hash.
// Caller must hold at least a read lock.
func (sm *SHAMap) findNodeByHash(targetHash [32]byte) Node {
	if sm.root == nil {
		return nil
	}

	// Check root first
	if sm.root.Hash() == targetHash {
		return sm.root
	}

	// BFS search for the node
	type workItem struct {
		node Node
	}

	queue := []workItem{{node: sm.root}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		if item.node.IsLeaf() {
			// Leaf nodes - check hash
			if item.node.Hash() == targetHash {
				return item.node
			}
			continue
		}

		inner, ok := item.node.(*InnerNode)
		if !ok {
			continue
		}

		for branch := 0; branch < BranchFactor; branch++ {
			if inner.IsEmptyBranch(branch) {
				continue
			}

			child, err := inner.Child(branch)
			if err != nil || child == nil {
				continue
			}

			if child.Hash() == targetHash {
				return child
			}

			queue = append(queue, workItem{node: child})
		}
	}

	return nil
}

// SerializeRoot serializes the root node for wire transmission.
// This is typically used when sending the tree's root to a peer
// to initiate synchronization.
//
// Returns the serialized wire format of the root node.
func (sm *SHAMap) SerializeRoot() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil {
		return nil, errors.New("no root node")
	}

	return sm.root.SerializeForWire()
}

// GetNodeByHash retrieves a single node by its hash.
// This is a simpler version of GetNodeFat with depth=0.
func (sm *SHAMap) GetNodeByHash(nodeHash [32]byte) (Node, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	node := sm.findNodeByHash(nodeHash)
	if node == nil {
		return nil, ErrNodeNotFound
	}

	return node, nil
}

// GetSerializedNode retrieves a node by hash and returns its wire-serialized form.
func (sm *SHAMap) GetSerializedNode(nodeHash [32]byte) ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	node := sm.findNodeByHash(nodeHash)
	if node == nil {
		return nil, ErrNodeNotFound
	}

	return node.SerializeForWire()
}

// GetChildHashes returns the hashes of all non-empty children of a node.
// This is useful for determining what nodes need to be fetched next during sync.
func (sm *SHAMap) GetChildHashes(nodeHash [32]byte) ([][32]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	node := sm.findNodeByHash(nodeHash)
	if node == nil {
		return nil, ErrNodeNotFound
	}

	if node.IsLeaf() {
		return nil, nil // Leaves have no children
	}

	inner, ok := node.(*InnerNode)
	if !ok {
		return nil, ErrInvalidType
	}

	var hashes [][32]byte
	for branch := 0; branch < BranchFactor; branch++ {
		if inner.IsEmptyBranch(branch) {
			continue
		}

		childHash, err := inner.ChildHash(branch)
		if err != nil {
			continue
		}

		hashes = append(hashes, childHash)
	}

	return hashes, nil
}

// BulkGetNodes retrieves multiple nodes by their hashes in a single call.
// This is more efficient than calling GetNodeByHash multiple times.
//
// Parameters:
//   - hashes: the hashes of nodes to retrieve
//
// Returns a map from hash to NodeData for all found nodes.
// Nodes that are not found are omitted from the result.
func (sm *SHAMap) BulkGetNodes(hashes [][32]byte) (map[[32]byte]NodeData, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[[32]byte]NodeData, len(hashes))

	// Build a set of requested hashes for O(1) lookup
	requested := make(map[[32]byte]struct{}, len(hashes))
	for _, h := range hashes {
		requested[h] = struct{}{}
	}

	if sm.root == nil {
		return result, nil
	}

	// BFS search collecting matching nodes
	type workItem struct {
		node Node
	}

	queue := []workItem{{node: sm.root}}

	for len(queue) > 0 && len(result) < len(hashes) {
		item := queue[0]
		queue = queue[1:]

		if item.node == nil {
			continue
		}

		nodeHash := item.node.Hash()

		// Check if this node was requested
		if _, wanted := requested[nodeHash]; wanted {
			data, err := item.node.SerializeForWire()
			if err == nil {
				result[nodeHash] = NodeData{
					Hash: nodeHash,
					Data: data,
				}
			}
		}

		// Continue traversal if not a leaf
		if !item.node.IsLeaf() {
			inner, ok := item.node.(*InnerNode)
			if !ok {
				continue
			}

			for branch := 0; branch < BranchFactor; branch++ {
				if inner.IsEmptyBranch(branch) {
					continue
				}

				child, err := inner.Child(branch)
				if err != nil || child == nil {
					continue
				}

				queue = append(queue, workItem{node: child})
			}
		}
	}

	return result, nil
}

// WireMessage represents a collection of nodes for wire transmission.
// This is used for batch node transfer during synchronization.
type WireMessage struct {
	Nodes   []NodeData
	MapType Type
	Seq     uint32
}

// CreateWireMessage creates a WireMessage from the SHAMap.
// This can be used to serialize a portion of the tree for transmission.
//
// Parameters:
//   - nodeHashes: specific nodes to include, or nil for all nodes
//   - maxNodes: maximum number of nodes to include (0 = no limit)
func (sm *SHAMap) CreateWireMessage(nodeHashes [][32]byte, maxNodes int) (*WireMessage, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	msg := &WireMessage{
		Nodes:   make([]NodeData, 0),
		MapType: sm.mapType,
		Seq:     sm.ledgerSeq,
	}

	if nodeHashes != nil {
		// Get specific nodes
		nodes, err := sm.BulkGetNodes(nodeHashes)
		if err != nil {
			return nil, err
		}

		for _, nd := range nodes {
			msg.Nodes = append(msg.Nodes, nd)
			if maxNodes > 0 && len(msg.Nodes) >= maxNodes {
				break
			}
		}
	} else {
		// Get all nodes via traversal
		if sm.root == nil {
			return msg, nil
		}

		type workItem struct {
			node Node
		}

		queue := []workItem{{node: sm.root}}

		for len(queue) > 0 {
			if maxNodes > 0 && len(msg.Nodes) >= maxNodes {
				break
			}

			item := queue[0]
			queue = queue[1:]

			if item.node == nil {
				continue
			}

			data, err := item.node.SerializeForWire()
			if err != nil {
				continue
			}

			msg.Nodes = append(msg.Nodes, NodeData{
				Hash: item.node.Hash(),
				Data: data,
			})

			if !item.node.IsLeaf() {
				inner, ok := item.node.(*InnerNode)
				if !ok {
					continue
				}

				for branch := 0; branch < BranchFactor; branch++ {
					child, err := inner.Child(branch)
					if err != nil || child == nil {
						continue
					}

					queue = append(queue, workItem{node: child})
				}
			}
		}
	}

	return msg, nil
}
