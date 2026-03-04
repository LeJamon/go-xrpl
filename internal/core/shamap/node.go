package shamap

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/LeJamon/goXRPLd/internal/protocol"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/crypto/common"
)

// NodeType defines the type of SHAMap node
type NodeType int

const (
	NodeTypeInner NodeType = iota + 1
	NodeTypeTransactionNoMeta
	NodeTypeTransactionWithMeta
	NodeTypeAccountState
)

// String returns a string representation of the node type
func (nt NodeType) String() string {
	switch nt {
	case NodeTypeInner:
		return "inner"
	case NodeTypeTransactionNoMeta:
		return "transaction"
	case NodeTypeTransactionWithMeta:
		return "transaction+meta"
	case NodeTypeAccountState:
		return "account_state"
	default:
		return fmt.Sprintf("unknown(%d)", int(nt))
	}
}

// Node defines the interface all tree nodes must implement
type Node interface {
	IsLeaf() bool
	IsInner() bool
	Hash() [32]byte
	Type() NodeType
	UpdateHash() error
	SerializeForWire() ([]byte, error)
	SerializeWithPrefix() ([]byte, error)
	String(nodeID NodeID) string
	Invariants(isRoot bool) error
	Clone() (Node, error)
	IsDirty() bool
	SetDirty(bool)
}

// BaseNode provides common functionality for all node types
type BaseNode struct {
	hash  [32]byte
	dirty bool
}

// IsDirty returns true if the node has been created or modified since last flush.
func (b *BaseNode) IsDirty() bool { return b.dirty }

// SetDirty marks the node as dirty (modified) or clean (flushed/loaded).
func (b *BaseNode) SetDirty(d bool) { b.dirty = d }

// Hash returns the hash of the node
func (b *BaseNode) Hash() [32]byte {
	return b.hash
}

// setHash computes and sets the hash from the provided data
func (b *BaseNode) setHash(data ...[]byte) error {
	if len(data) == 0 {
		return fmt.Errorf("no data provided for hash calculation")
	}

	hash := crypto.Sha512Half(data...)
	b.hash = hash
	return nil
}

// String returns a string representation of the base node
func (b *BaseNode) String(id NodeID) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("NodeID: %s", id.String()))
	sb.WriteString(fmt.Sprintf(", Hash: %s", hex.EncodeToString(b.hash[:])))
	return sb.String()
}

// IsZeroHash returns true if the hash is zero (uninitialized)
func (b *BaseNode) IsZeroHash() bool {
	return b.hash == [32]byte{}
}

func DeserializeNodeFromWire(data []byte) (Node, error) {
	if len(data) == 0 {
		return nil, errors.New("empty wire data")
	}

	wireType := data[len(data)-1]

	switch wireType {
	case protocol.WireTypeInner:
		return NewInnerNodeFromWire(data)
	case protocol.WireTypeCompressedInner:
		return NewInnerNodeFromWire(data)
	case protocol.WireTypeAccountState:
		return NewAccountStateLeafFromWire(data)
	case protocol.WireTypeTransaction:
		return NewTransactionLeafFromWire(data)
	case protocol.WireTypeTransactionWithMeta:
		return NewTransactionWithMetaLeafFromWire(data)
	default:
		return nil, fmt.Errorf("unknown wire type: %d", wireType)
	}
}
