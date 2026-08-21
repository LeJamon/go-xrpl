package shamap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// AddKnownNode adds a node received from an external source.
// This is used during synchronization to populate the tree with data from peers.
//
// Parameters:
//   - nodeHash: the expected hash of the node
//   - data: the serialized wire format of the node
//
// Returns an error if the node data is invalid or doesn't match the expected hash.
func (sm *SHAMap) AddKnownNode(nodeHash [32]byte, data []byte) error {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.acquisition.attachmentMu.Lock()
	defer sm.acquisition.attachmentMu.Unlock()

	if sm.tree.state != stateSyncing {
		return ErrSyncNotInProgress
	}

	if len(data) == 0 {
		return ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
	}

	// Verify the hash matches
	if err := node.UpdateHash(); err != nil {
		return fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := node.Hash()
	if !bytes.Equal(computedHash[:], nodeHash[:]) {
		return ErrNodeHashMismatch
	}

	// Find the location in the tree where this node belongs
	return sm.insertKnownNode(nodeHash, node)
}

// AddKnownNodeFromPrefixWithEntry inserts and verifies a prefix-format node,
// returning the clean node's NodeStore entry for the caller to persist.
func (sm *SHAMap) AddKnownNodeFromPrefixWithEntry(nodeID NodeID, data []byte) (AddNodeResult, FlushEntry, error) {
	return sm.addKnownNodeFromPrefix(context.Background(), nodeID, data, true)
}

func (sm *SHAMap) addKnownNodeFromPrefix(ctx context.Context, nodeID NodeID, data []byte, withEntry bool) (AddNodeResult, FlushEntry, error) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	sm.acquisition.attachmentMu.Lock()
	defer sm.acquisition.attachmentMu.Unlock()

	if sm.tree.state != stateSyncing {
		return NodeInvalid, FlushEntry{}, ErrSyncNotInProgress
	}
	if nodeID.IsRoot() {
		return NodeInvalid, FlushEntry{}, ErrUnexpectedNode
	}
	if len(data) == 0 {
		return NodeInvalid, FlushEntry{}, ErrInvalidNodeData
	}

	var verified mapNode
	result, err := sm.attachKnownNodeAt(ctx, access, nodeID, !withEntry, func() (mapNode, error) {
		var derr error
		verified, derr = deserializeFromPrefix(data)
		return verified, derr
	})
	if err != nil || result != NodeUseful || !withEntry {
		return result, FlushEntry{}, err
	}
	entry, err := flushEntryForNode(verified, sm.tree.ledgerSeq, sm.tree.mapType)
	if err != nil {
		return result, FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return result, entry, err
}

// AddKnownNodeByID inserts a node from wire data at the position specified
// by the peer-supplied SHAMap NodeID (path + depth). The node's computed
// hash must match the parent's stored child hash at the target branch.
//
// Descent through the partial tree is driven by the NodeID, not by
// hash-searching.
//
// Returns an AddNodeResult and, only for NodeInvalid, the underlying error:
//   - NodeUseful, nil on a fresh attach
//   - NodeDuplicate, nil when the slot is already populated or the path
//     consolidated into a mid-path leaf
//   - NodeReRequest, nil when an ancestor on the path is still a hash-only
//     stub: not a reject, re-requested by the next getMissingNodes walk
//   - NodeInvalid with ErrEmptyBranchOnPath (path into a branch this map does
//     not reference), ErrNodeHashMismatch, ErrUnexpectedNode (inner where only
//     a leaf may live), or ErrSyncNotInProgress / ErrInvalidNodeData on misuse
func (sm *SHAMap) AddKnownNodeByID(nodeID NodeID, data []byte) (AddNodeResult, error) {
	result, _, err := sm.addKnownNodeByID(context.Background(), nodeID, data, false)
	return result, err
}

// AddKnownNodeByIDWithEntryContext inserts and verifies a wire node, returning
// its clean NodeStore entry and honoring cancellation during backing-store reads.
func (sm *SHAMap) AddKnownNodeByIDWithEntryContext(ctx context.Context, nodeID NodeID, data []byte) (AddNodeResult, FlushEntry, error) {
	return sm.addKnownNodeByID(ctx, nodeID, data, true)
}

func (sm *SHAMap) addKnownNodeByID(ctx context.Context, nodeID NodeID, data []byte, withEntry bool) (AddNodeResult, FlushEntry, error) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	sm.acquisition.attachmentMu.Lock()
	defer sm.acquisition.attachmentMu.Unlock()

	if sm.tree.state != stateSyncing {
		return NodeInvalid, FlushEntry{}, ErrSyncNotInProgress
	}
	if nodeID.IsRoot() {
		return NodeInvalid, FlushEntry{}, ErrUnexpectedNode
	}
	if len(data) == 0 {
		return NodeInvalid, FlushEntry{}, ErrInvalidNodeData
	}

	var verified mapNode
	result, err := sm.attachKnownNodeAt(ctx, access, nodeID, !withEntry, func() (mapNode, error) {
		var derr error
		verified, derr = deserializeNodeFromWire(data)
		return verified, derr
	})
	if err != nil || result != NodeUseful || !withEntry {
		return result, FlushEntry{}, err
	}
	entry, err := flushEntryForNode(verified, sm.tree.ledgerSeq, sm.tree.mapType)
	if err != nil {
		return result, FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return result, entry, err
}

// attachKnownNodeAt descends along nodeID's path and attaches the node
// produced by deserialize at the first hash-only slot, after verifying its
// hash against the parent's stored child hash. deserialize runs only once the
// target slot is known to be empty, so a duplicate (slot already populated, or
// a consolidated leaf mid-path) short-circuits without parsing the peer's
// data. Reaching a hash-only ancestor before the target depth is NodeReRequest,
// not an error, so off-frontier fat-reply nodes converge over re-requests
// instead of flooding the reject log. Shared by the wire- and prefix-format
// acquisition paths. Caller must hold the write lock and have validated
// state and nodeID. markDirty is false when the caller receives and persists a
// FlushEntry; attaching verified immutable data does not modify the tree hash.
func (sm *SHAMap) attachKnownNodeAt(ctx context.Context, access *familyAccess, nodeID NodeID, markDirty bool, deserialize func() (mapNode, error)) (AddNodeResult, error) {
	if sm.tree.root == nil {
		return NodeInvalid, ErrParentNotInTree
	}

	targetDepth := int(nodeID.Depth())
	targetPath := nodeID.ID()

	parent := sm.tree.root
	ancestors := make([]*innerNode, 0, targetDepth)

	for curDepth := range targetDepth {
		ancestors = append(ancestors, parent)
		branch := selectBranchForPath(targetPath, curDepth)

		child, childHash, isSet := parent.LoadChild(branch)
		if !isSet {
			// A materialized ancestor has no child here: the offered node is
			// not part of this map (a node from another tree).
			return NodeInvalid, ErrEmptyBranchOnPath
		}

		if curDepth+1 == targetDepth {
			if child != nil {
				return NodeDuplicate, nil
			}
			newNode, err := deserialize()
			if err != nil {
				return NodeInvalid, fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
			}
			// At leaf depth, an inner node is provably invalid — mark the
			// map and bail.
			if _, isInner := newNode.(*innerNode); isInner && targetDepth == MaxDepth {
				sm.tree.state = stateInvalid
				return NodeInvalid, ErrUnexpectedNode
			}
			if err := newNode.UpdateHash(); err != nil {
				return NodeInvalid, fmt.Errorf("failed to compute node hash: %w", err)
			}
			if newNode.Hash() != childHash {
				return NodeInvalid, ErrNodeHashMismatch
			}
			if parent.SetChildIfNil(branch, newNode) != newNode {
				return NodeDuplicate, nil
			}
			if markDirty {
				newNode.SetDirty(true)
				for _, ancestor := range ancestors {
					ancestor.SetDirty(true)
				}
			}
			return NodeUseful, nil
		}

		if child == nil {
			loaded, loadErr := fetchFromStoreForNodePlacementContext(ctx, sm, access, parent, branch)
			if loadErr != nil {
				if errors.Is(loadErr, context.Canceled) || errors.Is(loadErr, context.DeadlineExceeded) {
					return NodeInvalid, loadErr
				}
				return NodeReRequest, nil
			}
			if loaded == nil {
				return NodeReRequest, nil
			}
			child = parent.SetChildIfNil(branch, loaded)
		}
		nextInner, ok := child.(*innerNode)
		if !ok {
			// A leaf encountered mid-path is the canonical content at this
			// slot (SHAMap consolidates lone leaves above leafDepth).
			return NodeDuplicate, nil
		}
		parent = nextInner
	}

	return NodeInvalid, ErrUnexpectedNode
}

// insertKnownNode inserts a node at the correct location in the tree.
// The caller must hold the write lock.
func (sm *SHAMap) insertKnownNode(nodeHash [32]byte, node mapNode) error {
	if sm.tree.root == nil {
		return ErrUnexpectedNode
	}

	// Find the parent that references this hash
	return sm.insertNodeRecursive(sm.tree.root, nodeHash, node, 0)
}

// insertNodeRecursive recursively finds and inserts a node at the correct location.
func (sm *SHAMap) insertNodeRecursive(current mapNode, targetHash [32]byte, newNode mapNode, depth int) error {
	if current == nil {
		return ErrUnexpectedNode
	}

	if depth > MaxDepth {
		return ErrMaxDepthExceeded
	}

	inner, ok := current.(*innerNode)
	if !ok {
		return ErrUnexpectedNode
	}

	for branch := range BranchFactor {
		child, childHash, isSet := inner.LoadChild(branch)
		if !isSet {
			continue
		}

		if bytes.Equal(childHash[:], targetHash[:]) {
			// Found the branch - insert the node here
			newNode.SetDirty(true)
			return inner.SetChild(branch, newNode)
		}

		if _, isInner := child.(*innerNode); isInner {
			// Recurse into this inner node
			err := sm.insertNodeRecursive(child, targetHash, newNode, depth+1)
			if err == nil {
				inner.SetDirty(true)
				return nil // Successfully inserted
			}
			// Continue searching other branches if not found
		}
	}

	return ErrUnexpectedNode
}

// AddRootNode sets the root from external data.
// This is used to initialize a SHAMap during synchronization when receiving
// the root hash/data from a peer.
//
// Parameters:
//   - hash: the expected hash of the root node
//   - data: the serialized wire format of the root node
//
// Returns an error if the root is already set, the data is invalid,
// or the hash doesn't match.
func (sm *SHAMap) AddRootNode(hash [32]byte, data []byte) error {
	_, err := sm.addRootNode(hash, data, false)
	return err
}

// AddRootNodeWithEntry sets and verifies a clean root and returns its NodeStore
// entry for the caller to persist without deserializing it a second time.
func (sm *SHAMap) AddRootNodeWithEntry(hash [32]byte, data []byte) (FlushEntry, error) {
	return sm.addRootNode(hash, data, true)
}

func (sm *SHAMap) addRootNode(hash [32]byte, data []byte, withEntry bool) (FlushEntry, error) {
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.acquisition.attachmentMu.Lock()
	defer sm.acquisition.attachmentMu.Unlock()

	if sm.tree.root != nil && sm.tree.root.HasChildren() {
		return FlushEntry{}, ErrRootAlreadySet
	}

	if len(data) == 0 {
		return FlushEntry{}, ErrInvalidNodeData
	}

	// Deserialize the node from wire format
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		return FlushEntry{}, fmt.Errorf("%w: %w", ErrInvalidNodeData, err)
	}

	// Must be an inner node for root
	root, ok := node.(*innerNode)
	if !ok {
		return FlushEntry{}, fmt.Errorf("root must be an inner node, got %T", node)
	}

	if err := root.UpdateHash(); err != nil {
		return FlushEntry{}, fmt.Errorf("failed to compute node hash: %w", err)
	}

	computedHash := root.Hash()
	if !bytes.Equal(computedHash[:], hash[:]) {
		return FlushEntry{}, ErrNodeHashMismatch
	}

	sm.tree.root = root
	root.SetDirty(!withEntry)
	sm.tree.state = stateSyncing

	if !withEntry {
		return FlushEntry{}, nil
	}
	entry, err := flushEntryForNode(root, sm.tree.ledgerSeq, sm.tree.mapType)
	if err != nil {
		return FlushEntry{}, fmt.Errorf("%w: %v", ErrNodeSerialization, err)
	}
	return entry, nil
}
