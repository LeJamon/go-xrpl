package shamap

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
// AddKnownNodeFromPrefix keyed by Hash.
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
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.root == nil || maxNodes <= 0 {
		return nil, nil
	}
	// An empty map's root is an empty inner node with no serialized form;
	// there is nothing to pack. Production never walks an empty map (state
	// maps are non-empty and tx maps are skipped when the tx tree is empty),
	// but guard it so the walk is total.
	if !sm.root.HasChildren() {
		return nil, nil
	}
	out := make([]FetchPackNode, 0, maxNodes)
	if err := walkFetchPackRec(sm.root, maxNodes, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func walkFetchPackRec(node Node, maxNodes int, out *[]FetchPackNode) error {
	if node == nil || len(*out) >= maxNodes {
		return nil
	}
	data, err := node.SerializeWithPrefix()
	if err != nil {
		return err
	}
	*out = append(*out, FetchPackNode{Hash: node.Hash(), Data: data})

	inner, ok := node.(*innerNode)
	if !ok {
		return nil
	}
	inner.mu.RLock()
	defer inner.mu.RUnlock()
	for branch := 0; branch < BranchFactor; branch++ {
		if len(*out) >= maxNodes {
			break
		}
		child := inner.children[branch]
		if child == nil {
			continue
		}
		if err := walkFetchPackRec(child, maxNodes, out); err != nil {
			return err
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
	node, err := DeserializeFromPrefix(data)
	if err != nil {
		return false
	}
	if err := node.UpdateHash(); err != nil {
		return false
	}
	return node.Hash() == expected
}
