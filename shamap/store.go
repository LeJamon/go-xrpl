package shamap

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

// FlushEntry holds a serialized node ready to be written to NodeStore.
type FlushEntry struct {
	Hash      [32]byte // SHAMap node hash (used as key in NodeStore)
	Data      []byte   // SerializeWithPrefix() output
	LedgerSeq uint32
	MapType   Type
}

// DeserializeFromPrefix creates a SHAMap node from prefix-format data,
// returning a read-only NodeReader.
func DeserializeFromPrefix(data []byte) (NodeReader, error) {
	return deserializeFromPrefix(data)
}

// FlushEntryFromWire verifies a wire-format node and converts it to the
// prefix-format representation used by Family implementations.
func FlushEntryFromWire(data []byte, ledgerSeq uint32, mapType Type) (FlushEntry, error) {
	node, err := deserializeNodeFromWire(data)
	if err != nil {
		return FlushEntry{}, err
	}
	if err := node.UpdateHash(); err != nil {
		return FlushEntry{}, err
	}
	return flushEntryForNode(node, ledgerSeq, mapType)
}

func flushEntryForNode(node mapNode, ledgerSeq uint32, mapType Type) (FlushEntry, error) {
	prefixed, err := node.SerializeWithPrefix()
	if err != nil {
		return FlushEntry{}, err
	}
	return FlushEntry{
		Hash:      node.Hash(),
		Data:      prefixed,
		LedgerSeq: ledgerSeq,
		MapType:   mapType,
	}, nil
}

type storedNodeKind uint8

const (
	storedInner storedNodeKind = iota + 1
	storedAccountState
	storedTransaction
	storedTransactionWithMeta
)

type prefixNodeBody struct {
	kind    storedNodeKind
	payload []byte
	key     [32]byte
}

func deserializeFromPrefix(data []byte) (mapNode, error) {
	body, err := decodePrefixBody(data)
	if err != nil {
		return nil, err
	}
	return materializePrefixNode(body)
}

func decodePrefixBody(data []byte) (prefixNodeBody, error) {
	if len(data) < 4 {
		return prefixNodeBody{}, fmt.Errorf("data too short for prefix: %d bytes", len(data))
	}

	var prefix [4]byte
	copy(prefix[:], data[:4])

	switch prefix {
	case protocol.HashPrefixInnerNode():
		if len(data) != fullInnerSerializedSize {
			return prefixNodeBody{}, fmt.Errorf("invalid inner node prefix data size: expected %d, got %d", fullInnerSerializedSize, len(data))
		}
		return prefixNodeBody{kind: storedInner, payload: data[4:]}, nil
	case protocol.HashPrefixLeafNode():
		if len(data) < 4+32 {
			return prefixNodeBody{}, fmt.Errorf("account state prefix data too short: %d bytes", len(data))
		}
		body := prefixNodeBody{kind: storedAccountState}
		keyStart := len(data) - 32
		body.payload = data[4:keyStart]
		copy(body.key[:], data[keyStart:])
		if isZeroHash(body.key) {
			return prefixNodeBody{}, fmt.Errorf("invalid account state: zero key")
		}
		return body, nil
	case protocol.HashPrefixTransactionID():
		if len(data) <= 4 {
			return prefixNodeBody{}, fmt.Errorf("transaction prefix data too short: %d bytes", len(data))
		}
		return prefixNodeBody{kind: storedTransaction, payload: data[4:]}, nil
	case protocol.HashPrefixTxNode():
		if len(data) < 4+32 {
			return prefixNodeBody{}, fmt.Errorf("transaction+meta prefix data too short: %d bytes", len(data))
		}
		body := prefixNodeBody{kind: storedTransactionWithMeta}
		keyStart := len(data) - 32
		body.payload = data[4:keyStart]
		copy(body.key[:], data[keyStart:])
		return body, nil
	default:
		return prefixNodeBody{}, fmt.Errorf("unknown hash prefix: %x", prefix)
	}
}

func decodeInnerBranches(body prefixNodeBody, hashes *[BranchFactor][32]byte) (uint16, error) {
	if body.kind != storedInner || len(body.payload) != BranchFactor*32 {
		return 0, fmt.Errorf("invalid inner node body")
	}
	var branches uint16
	for branch := range BranchFactor {
		start := branch * 32
		copy(hashes[branch][:], body.payload[start:start+32])
		if !isZeroHash(hashes[branch]) {
			branches |= 1 << branch
		}
	}
	return branches, nil
}

func materializePrefixNode(body prefixNodeBody) (mapNode, error) {
	if body.kind == storedInner {
		node := &innerNode{}
		branches, err := decodeInnerBranches(body, &node.hashes)
		if err != nil {
			return nil, err
		}
		node.isBranch = branches
		if err := node.UpdateHash(); err != nil {
			return nil, fmt.Errorf("failed to update inner node hash: %w", err)
		}
		return node, nil
	}

	var (
		node *leafNode
		err  error
	)
	switch body.kind {
	case storedAccountState:
		node, err = newAccountStateLeafNode(NewItem(body.key, body.payload))
	case storedTransaction:
		key := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), body.payload)
		node, err = newTransactionLeafNode(NewItem(key, body.payload))
	case storedTransactionWithMeta:
		node, err = newTransactionWithMetaLeafNode(NewItem(body.key, body.payload))
	default:
		return nil, fmt.Errorf("unknown stored node kind: %d", body.kind)
	}
	if err != nil {
		return nil, err
	}
	node.SetDirty(false)
	return node, nil
}

func decodeAndVerifyPrefixNode(data []byte, expected [32]byte) (mapNode, error) {
	node, err := deserializeFromPrefix(data)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to deserialize child node: %v", ErrInvalidNodeData, err)
	}
	if actual := node.Hash(); actual != expected {
		return nil, fmt.Errorf("%w: child node hash mismatch: expected %x, got %x", ErrInvalidNodeData, expected[:8], actual[:8])
	}
	return node, nil
}
