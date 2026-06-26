package shamap

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
)

// FlushEntry holds a serialized node ready to be written to NodeStore.
type FlushEntry struct {
	Hash [32]byte // SHAMap node hash (used as key in NodeStore)
	Data []byte   // SerializeWithPrefix() output
}

// NodeBatch holds a batch of serialized nodes from FlushDirty().
type NodeBatch struct {
	Entries []FlushEntry
}

// DeserializeFromPrefix creates a SHAMap node from prefix-format data.
// The first 4 bytes are the hash prefix which identifies the node type.
// Inner nodes are created with hashes set but children nil (lazy loading).
// All deserialized nodes are marked as not dirty.
func DeserializeFromPrefix(data []byte) (Node, error) {
	return deserializeFromPrefix(data, nil)
}

// DeserializeFromPrefixWithHash is DeserializeFromPrefix for the common case
// where the node's hash is already known: the content-addressed key it was
// fetched by. Because the store is content-addressed, recomputing the hash from
// the bytes reproduces that key by construction — so the recompute (a
// SHA-512Half plus the serialization buffer it needs, per node) is pure waste
// on every lazy descent. Installing the known hash directly is the dominant
// per-fetch CPU/alloc saving on the replay hot path (issue #1084).
func DeserializeFromPrefixWithHash(data []byte, knownHash [32]byte) (Node, error) {
	return deserializeFromPrefix(data, &knownHash)
}

func deserializeFromPrefix(data []byte, knownHash *[32]byte) (Node, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("data too short for prefix: %d bytes", len(data))
	}

	var prefix [4]byte
	copy(prefix[:], data[:4])

	switch prefix {
	case protocol.HashPrefixInnerNode:
		return parseInnerNodeFromPrefix(data, knownHash)
	case protocol.HashPrefixLeafNode:
		return parseAccountStateLeafFromPrefix(data, knownHash)
	case protocol.HashPrefixTransactionID:
		return parseTransactionLeafFromPrefix(data, knownHash)
	case protocol.HashPrefixTxNode:
		return parseTransactionWithMetaLeafFromPrefix(data, knownHash)
	default:
		return nil, fmt.Errorf("unknown hash prefix: %x", prefix)
	}
}

// parseInnerNodeFromPrefix deserializes an inner node from prefix format.
// Format: [4-byte prefix][16 x 32-byte child hashes] = 516 bytes
// Children are hash-only (pointers nil) — they are loaded lazily.
func parseInnerNodeFromPrefix(data []byte, knownHash *[32]byte) (*innerNode, error) {
	const expectedSize = 4 + BranchFactor*32 // 4 + 512 = 516
	if len(data) != expectedSize {
		return nil, fmt.Errorf("invalid inner node prefix data size: expected %d, got %d", expectedSize, len(data))
	}

	node := &innerNode{} // dirty=false by default (zero value)

	// Skip 4-byte prefix, read 16 child hashes
	for i := range BranchFactor {
		start := 4 + i*32
		end := start + 32

		var hash [32]byte
		copy(hash[:], data[start:end])

		if !isZeroHash(hash) {
			node.hashes[i] = hash
			node.isBranch |= 1 << i
			// children[i] remains nil — lazy loaded on demand
		}
	}

	// Install the node's own hash. When it is already known (the fetch key),
	// use it directly; otherwise recompute it from the child hashes.
	if knownHash != nil {
		node.hash = *knownHash
	} else if err := node.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update inner node hash: %w", err)
	}

	return node, nil
}

// buildLeaf constructs a leaf from an already-parsed item. When the node hash
// is known (the fetch key) it is installed directly, skipping the per-descent
// re-hash; otherwise it is computed. Either way the leaf is marked clean, as it
// came from the store.
func buildLeaf(kind leafKind, item *Item, knownHash *[32]byte) (*leafNode, error) {
	if knownHash != nil {
		return newLeafNodeWithHash(kind, item, *knownHash)
	}
	node, err := newLeafNode(kind, item)
	if err != nil {
		return nil, err
	}
	node.SetDirty(false)
	return node, nil
}

// parseAccountStateLeafFromPrefix deserializes an account state leaf from prefix format.
// Format: [4-byte prefix][state_data][32-byte key]
func parseAccountStateLeafFromPrefix(data []byte, knownHash *[32]byte) (*leafNode, error) {
	if len(data) < 4+32 {
		return nil, fmt.Errorf("account state prefix data too short: %d bytes", len(data))
	}

	nodeData := data[4:]

	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	if isZeroHash(key) {
		return nil, fmt.Errorf("invalid account state: zero key")
	}

	stateData := nodeData[:keyStart]
	item := NewItem(key, stateData)

	return buildLeaf(leafAccountState, item, knownHash)
}

// parseTransactionLeafFromPrefix deserializes a transaction leaf from prefix format.
// Format: [4-byte prefix][tx_data]
func parseTransactionLeafFromPrefix(data []byte, knownHash *[32]byte) (*leafNode, error) {
	if len(data) <= 4 {
		return nil, fmt.Errorf("transaction prefix data too short: %d bytes", len(data))
	}

	txData := data[4:]

	// A tx-no-meta leaf's node hash equals its transaction id —
	// Sha512Half(prefix, txData) — which is also the item key, so reuse the
	// known hash for both when present and skip recomputing it.
	var key [32]byte
	if knownHash != nil {
		key = *knownHash
	} else {
		key = common.Sha512Half(protocol.HashPrefixTransactionID[:], txData)
	}
	item := NewItem(key, txData)

	return buildLeaf(leafTransaction, item, knownHash)
}

// parseTransactionWithMetaLeafFromPrefix deserializes a tx+meta leaf from prefix format.
// Format: [4-byte prefix][tx+meta_data][32-byte key]
func parseTransactionWithMetaLeafFromPrefix(data []byte, knownHash *[32]byte) (*leafNode, error) {
	if len(data) < 4+32 {
		return nil, fmt.Errorf("transaction+meta prefix data too short: %d bytes", len(data))
	}

	nodeData := data[4:]

	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	txData := nodeData[:keyStart]
	item := NewItem(key, txData)

	return buildLeaf(leafTransactionWithMeta, item, knownHash)
}
