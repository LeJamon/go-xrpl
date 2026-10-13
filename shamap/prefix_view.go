package shamap

import (
	"crypto/sha512"
	"fmt"
)

// commitmentNodeView is the authenticated, read-only subset of a serialized
// node needed by the bounded commitment and root-reader paths. Leaf data is a
// view into the Family result and is copied only by callers that must retain
// it after the current read.
type commitmentNodeView struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	key      [32]byte
	data     []byte
	leafType NodeType
}

// decodeCommitmentNode verifies the canonical prefix bytes and extracts only
// the authenticated metadata needed by bounded readers. It intentionally does
// not materialize mutable SHAMap nodes or Items.
func decodeCommitmentNode(data []byte, expected [32]byte) (commitmentNodeView, error) {
	body, err := decodePrefixBody(data)
	if err != nil {
		return commitmentNodeView{}, fmt.Errorf("%w: failed to deserialize child node: %v", ErrInvalidNodeData, err)
	}
	view := commitmentNodeView{}
	switch body.kind {
	case storedInner:
		view.inner = true
		view.branches, err = decodeInnerBranches(body, &view.hashes)
		if err != nil {
			return commitmentNodeView{}, fmt.Errorf("%w: failed to deserialize child node: %v", ErrInvalidNodeData, err)
		}
	case storedAccountState:
		if len(body.payload) < 12 {
			return commitmentNodeView{}, fmt.Errorf("%w: account-state leaf too short: %d", ErrInvalidNodeData, len(data))
		}
		view.key, view.data, view.leafType = body.key, body.payload, NodeTypeAccountState
	case storedTransaction:
		if len(body.payload) < 12 {
			return commitmentNodeView{}, fmt.Errorf("%w: transaction leaf too short: %d", ErrInvalidNodeData, len(data))
		}
		// A no-metadata transaction leaf's key is the hash of its canonical
		// prefix bytes, which is the expected hash after verification.
		view.key, view.data, view.leafType = expected, body.payload, NodeTypeTransactionNoMeta
	case storedTransactionWithMeta:
		if len(body.payload) < 12 {
			return commitmentNodeView{}, fmt.Errorf("%w: transaction-with-metadata leaf too short: %d", ErrInvalidNodeData, len(data))
		}
		view.key, view.data, view.leafType = body.key, body.payload, NodeTypeTransactionWithMeta
	}

	actual := hashCommitmentPrefix(data, view.inner && view.branches == 0)
	if actual != expected {
		return commitmentNodeView{}, fmt.Errorf("%w: child node hash mismatch: expected %x, got %x", ErrInvalidNodeData, [8]byte(expected[:8]), [8]byte(actual[:8]))
	}
	return view, nil
}

// hashCommitmentPrefix matches mapNode.Hash for a stored prefix node. An
// empty inner node is represented by the zero hash in the in-memory SHAMap;
// all valid persisted inner nodes have at least one branch.
func hashCommitmentPrefix(data []byte, emptyInner bool) [32]byte {
	if emptyInner {
		return [32]byte{}
	}
	digest := sha512.Sum512(data)
	var hash [32]byte
	copy(hash[:], digest[:32])
	return hash
}
