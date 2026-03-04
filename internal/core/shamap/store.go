package shamap

import (
	"fmt"

	crypto "github.com/LeJamon/goXRPLd/internal/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/protocol"
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
	if len(data) < 4 {
		return nil, fmt.Errorf("data too short for prefix: %d bytes", len(data))
	}

	var prefix [4]byte
	copy(prefix[:], data[:4])

	switch prefix {
	case protocol.HashPrefixInnerNode:
		return parseInnerNodeFromPrefix(data)
	case protocol.HashPrefixLeafNode:
		return parseAccountStateLeafFromPrefix(data)
	case protocol.HashPrefixTransactionID:
		return parseTransactionLeafFromPrefix(data)
	case protocol.HashPrefixTxNode:
		return parseTransactionWithMetaLeafFromPrefix(data)
	default:
		return nil, fmt.Errorf("unknown hash prefix: %x", prefix)
	}
}

// parseInnerNodeFromPrefix deserializes an inner node from prefix format.
// Format: [4-byte prefix][16 x 32-byte child hashes] = 516 bytes
// Children are hash-only (pointers nil) — they are loaded lazily.
func parseInnerNodeFromPrefix(data []byte) (*InnerNode, error) {
	const expectedSize = 4 + BranchFactor*32 // 4 + 512 = 516
	if len(data) != expectedSize {
		return nil, fmt.Errorf("invalid inner node prefix data size: expected %d, got %d", expectedSize, len(data))
	}

	node := &InnerNode{} // dirty=false by default (zero value)

	// Skip 4-byte prefix, read 16 child hashes
	for i := 0; i < BranchFactor; i++ {
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

	// Compute the node's own hash
	if err := node.UpdateHash(); err != nil {
		return nil, fmt.Errorf("failed to update inner node hash: %w", err)
	}

	return node, nil
}

// parseAccountStateLeafFromPrefix deserializes an account state leaf from prefix format.
// Format: [4-byte prefix][state_data][32-byte key]
func parseAccountStateLeafFromPrefix(data []byte) (*AccountStateLeafNode, error) {
	if len(data) < 4+32 {
		return nil, fmt.Errorf("account state prefix data too short: %d bytes", len(data))
	}

	// Skip 4-byte prefix
	nodeData := data[4:]

	// Extract key from last 32 bytes
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	if isZeroHash(key) {
		return nil, fmt.Errorf("invalid account state: zero key")
	}

	stateData := nodeData[:keyStart]
	item := NewItem(key, stateData)

	node, err := NewAccountStateLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from store
	return node, nil
}

// parseTransactionLeafFromPrefix deserializes a transaction leaf from prefix format.
// Format: [4-byte prefix][tx_data]
func parseTransactionLeafFromPrefix(data []byte) (*TransactionLeafNode, error) {
	if len(data) <= 4 {
		return nil, fmt.Errorf("transaction prefix data too short: %d bytes", len(data))
	}

	// Skip 4-byte prefix
	txData := data[4:]

	// Key is derived from hashing the data (same as wire format)
	key := crypto.Sha512Half(protocol.HashPrefixTransactionID[:], txData)
	item := NewItem(key, txData)

	node, err := NewTransactionLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from store
	return node, nil
}

// parseTransactionWithMetaLeafFromPrefix deserializes a tx+meta leaf from prefix format.
// Format: [4-byte prefix][tx+meta_data][32-byte key]
func parseTransactionWithMetaLeafFromPrefix(data []byte) (*TransactionWithMetaLeafNode, error) {
	if len(data) < 4+32 {
		return nil, fmt.Errorf("transaction+meta prefix data too short: %d bytes", len(data))
	}

	// Skip 4-byte prefix
	nodeData := data[4:]

	// Extract key from last 32 bytes
	keyStart := len(nodeData) - 32
	var key [32]byte
	copy(key[:], nodeData[keyStart:])

	txData := nodeData[:keyStart]
	item := NewItem(key, txData)

	node, err := NewTransactionWithMetaLeafNode(item)
	if err != nil {
		return nil, err
	}
	node.dirty = false // loaded from store
	return node, nil
}
