package shamap

import "context"

// FetchPackNode is a single SHAMap tree node packaged for a fetch-pack: its
// node hash (the TMIndexedObject.hash carried on the wire) and its prefix
// serialization (SerializeWithPrefix — the [HashPrefix][body] blob whose
// sha512Half is the node hash, matching rippled's fetch-pack node format).
type FetchPackNode struct {
	Hash [32]byte
	Data []byte
}

// WalkFetchPackNodes returns up to maxNodes SHAMap tree nodes (inner and
// leaf) in pre-order, each paired with its node hash and prefix serialization.
//
// This is the serve-side building block for a fetch-pack. Rippled's
// LedgerMaster::populateFetchPack (LedgerMaster.cpp:2063-2093) walks
// want->stateMap() emitting each node's serializeWithPrefix() bytes — the
// [HashPrefix][body] form whose sha512Half is the node hash. go-xrpl emits the
// identical SerializeWithPrefix() bytes so a rippled peer's consume check
// (hash == sha512Half(data), LedgerMaster.cpp:2019) accepts every node, and a
// go-xrpl receiver verifies and reconstructs the tree by feeding each blob to
// its prefix-format acquisition path keyed by Hash.
//
// Pre-order guarantees the root precedes its descendants, so a result
// truncated at maxNodes is always a connected prefix of the tree the receiver
// can use. Unlike rippled, the walk does NOT diff against a "have" ledger:
// rippled sends only want-vs-have differences because the receiver's node DB
// supplies the unchanged nodes, but a go-xrpl acquisition fills an in-memory
// SHAMap with no node-hash-keyed backing store to supply un-sent shared nodes
// — so a diff would leave it unable to complete. Sending want's full (capped)
// tree is correct for any receiver: a node it already holds is simply ignored.
func (sm *SHAMap) WalkFetchPackNodes(maxNodes int) ([]FetchPackNode, error) {
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()

	if sm.tree.root == nil || maxNodes <= 0 {
		return nil, nil
	}
	// An empty map's root is an empty inner node with no serialized form;
	// there is nothing to pack. Production never walks an empty map (state
	// maps are non-empty and tx maps are skipped when the tx tree is empty),
	// but guard it so the walk is total.
	if !sm.tree.root.HasChildren() {
		return nil, nil
	}
	out := make([]FetchPackNode, 0, maxNodes)
	if err := walkFetchPackDifferences(context.Background(), sm, nil, sm.tree.root, nil, maxNodes, 0, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WalkFetchPackNodesContext is the cancellation-aware form of
// WalkFetchPackNodes used by bounded peer serving.
func (sm *SHAMap) WalkFetchPackNodesContext(ctx context.Context, maxNodes int) ([]FetchPackNode, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sm == nil || maxNodes <= 0 {
		return nil, nil
	}
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if sm.tree.root == nil || !sm.tree.root.HasChildren() {
		return nil, nil
	}
	out := make([]FetchPackNode, 0, maxNodes)
	if err := walkFetchPackDifferences(ctx, sm, nil, sm.tree.root, nil, maxNodes, 0, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WalkFetchPackDifferences returns the nodes present in sm but not already
// represented by have. Shared subtrees are skipped by hash, while changed
// inner nodes and their descendants are serialized with the same prefix used
// by fetch-pack wire objects. Traversal is bounded and cancellation-aware.
func (sm *SHAMap) WalkFetchPackDifferences(ctx context.Context, have *SHAMap, maxNodes int) ([]FetchPackNode, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxNodes <= 0 || sm == nil {
		return nil, nil
	}
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if sm.tree.root == nil || !sm.tree.root.HasChildren() {
		return nil, nil
	}
	var haveRoot mapNode
	sharedTree := have != nil && sm == have
	if have != nil {
		if !sharedTree {
			have.tree.mu.RLock()
			defer have.tree.mu.RUnlock()
		}
		haveRoot = have.tree.root
	}
	out := make([]FetchPackNode, 0, maxNodes)
	if err := walkFetchPackDifferences(ctx, sm, have, sm.tree.root, haveRoot, maxNodes, 0, nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WalkFetchPackNodesContextBounded is WalkFetchPackNodesContext with an
// aggregate serialization budget. complete is false when the byte limit stops
// traversal before the node-count limit does.
func (sm *SHAMap) WalkFetchPackNodesContextBounded(ctx context.Context, maxNodes int, maxBytes int64) ([]FetchPackNode, bool, error) {
	return sm.walkFetchPackDifferencesBounded(ctx, nil, maxNodes, maxBytes)
}

// WalkFetchPackDifferencesBounded is WalkFetchPackDifferences with an aggregate
// serialization budget. complete is false when another differing node exists
// but cannot fit.
func (sm *SHAMap) WalkFetchPackDifferencesBounded(ctx context.Context, have *SHAMap, maxNodes int, maxBytes int64) ([]FetchPackNode, bool, error) {
	return sm.walkFetchPackDifferencesBounded(ctx, have, maxNodes, maxBytes)
}

func (sm *SHAMap) walkFetchPackDifferencesBounded(ctx context.Context, have *SHAMap, maxNodes int, maxBytes int64) ([]FetchPackNode, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sm == nil || maxNodes <= 0 || maxBytes <= 0 {
		return nil, false, nil
	}
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	if sm.tree.root == nil || !sm.tree.root.HasChildren() {
		return nil, true, nil
	}
	var haveRoot mapNode
	sharedTree := have != nil && sm == have
	if have != nil {
		if !sharedTree {
			have.tree.mu.RLock()
			defer have.tree.mu.RUnlock()
		}
		haveRoot = have.tree.root
	}
	out := make([]FetchPackNode, 0, maxNodes)
	used := int64(0)
	complete := true
	if err := walkFetchPackDifferences(ctx, sm, have, sm.tree.root, haveRoot, maxNodes, maxBytes, &used, &complete, &out); err != nil {
		return nil, false, err
	}
	return out, complete, nil
}

func walkFetchPackDifferences(
	ctx context.Context,
	wantMap, haveMap *SHAMap,
	want, have mapNode,
	maxNodes int,
	maxBytes int64,
	used *int64,
	complete *bool,
	out *[]FetchPackNode,
) error {
	if want == nil || len(*out) >= maxNodes || complete != nil && !*complete {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if have != nil && want.Hash() == have.Hash() {
		return nil
	}
	data, err := want.SerializeWithPrefix()
	if err != nil {
		return err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes-*used {
		*complete = false
		return nil
	}
	*out = append(*out, FetchPackNode{Hash: want.Hash(), Data: data})
	if used != nil {
		*used += int64(len(data))
	}
	if len(*out) >= maxNodes {
		return nil
	}
	wantInner, ok := want.(*innerNode)
	if !ok {
		return nil
	}
	var haveInner *innerNode
	if candidate, ok := have.(*innerNode); ok {
		haveInner = candidate
	}
	for branch := range BranchFactor {
		child, wantHash, present := wantInner.LoadChild(branch)
		if !present {
			continue
		}
		var haveChild mapNode
		if haveInner != nil {
			var haveHash [32]byte
			var havePresent bool
			haveChild, haveHash, havePresent = haveInner.LoadChild(branch)
			if child != nil {
				wantHash = child.Hash()
			}
			if haveChild != nil {
				haveHash = haveChild.Hash()
			}
			if havePresent && haveHash == wantHash {
				continue
			}
			if havePresent && haveChild == nil {
				var err error
				haveChild, err = haveMap.descendCtx(ctx, haveInner, branch)
				if err != nil {
					return err
				}
			}
		}
		if child == nil {
			var err error
			child, err = wantMap.descendCtx(ctx, wantInner, branch)
			if err != nil {
				return err
			}
		}
		if err := walkFetchPackDifferences(ctx, wantMap, haveMap, child, haveChild, maxNodes, maxBytes, used, complete, out); err != nil {
			return err
		}
		if len(*out) >= maxNodes || complete != nil && !*complete {
			return nil
		}
	}
	return nil
}

// VerifyFetchPackNode reports whether data is the prefix (serializeWithPrefix)
// serialization of a SHAMap node whose computed hash equals expected. The
// fetch-pack consume path uses it to reject poisoned (hash != data) nodes
// before caching them, mirroring rippled's LedgerMaster::getFetchPack
// sha512Half(data) == hash check (LedgerMaster.cpp:2019). The leading
// ledger-header object of a pack carries the ledgerMaster prefix, not a SHAMap
// node prefix, so it fails here and is dropped; only SHAMap tree nodes are
// needed to complete an acquisition.
func VerifyFetchPackNode(expected [32]byte, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	node, err := deserializeFromPrefix(data)
	if err != nil {
		return false
	}
	if err := node.UpdateHash(); err != nil {
		return false
	}
	return node.Hash() == expected
}
