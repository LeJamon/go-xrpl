package shamap

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/protocol"

	"github.com/LeJamon/go-xrpl/crypto/common"
)

// NodeType defines the type of SHAMap node
type NodeType int

// NodeType values identify each kind of SHAMap node.
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

// Node defines the interface all tree nodes must implement.
// Concrete nodes are either *innerNode or a LeafNode; callers
// discriminate with a type switch.
type Node interface {
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

// baseNode provides common functionality for all node types.
// The dirty flag is atomic because Snapshot shares node pointers between
// maps with independent mutexes: concurrent flushes on structurally-shared
// subtrees read and clear the flag without a common lock.
type baseNode struct {
	hash  [32]byte
	dirty atomic.Bool
}

// IsDirty returns true if the node has been created or modified since last flush.
func (b *baseNode) IsDirty() bool { return b.dirty.Load() }

// SetDirty marks the node as dirty (modified) or clean (flushed/loaded).
func (b *baseNode) SetDirty(d bool) { b.dirty.Store(d) }

// Hash returns the hash of the node. Concrete node types shadow this with
// a mutex-guarded variant; this unguarded read is only used while the
// node's own lock is already held.
func (b *baseNode) Hash() [32]byte {
	return b.hash
}

// setHash computes and sets the hash from the provided data
func (b *baseNode) setHash(data ...[]byte) error {
	if len(data) == 0 {
		return fmt.Errorf("no data provided for hash calculation")
	}

	hash := common.Sha512Half(data...)
	b.hash = hash
	return nil
}

// String returns a string representation of the base node
func (b *baseNode) String(id NodeID) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("NodeID: %s", id.String()))
	sb.WriteString(fmt.Sprintf(", Hash: %s", hex.EncodeToString(b.hash[:])))
	return sb.String()
}

// IsZeroHash returns true if the hash is zero (uninitialized)
func (b *baseNode) IsZeroHash() bool {
	return b.hash == [32]byte{}
}

// DeserializeNodeFromWire reconstructs a Node from its wire-format encoding,
// dispatching on the trailing wire-type byte.
func DeserializeNodeFromWire(data []byte) (Node, error) {
	if len(data) == 0 {
		return nil, errors.New("empty wire data")
	}

	wireType := data[len(data)-1]

	switch wireType {
	case protocol.WireTypeInner:
		return newInnerNodeFromWire(data)
	case protocol.WireTypeCompressedInner:
		return newInnerNodeFromWire(data)
	case protocol.WireTypeAccountState:
		return newAccountStateLeafFromWire(data)
	case protocol.WireTypeTransaction:
		return NewTransactionLeafFromWire(data)
	case protocol.WireTypeTransactionWithMeta:
		return newTransactionWithMetaLeafFromWire(data)
	default:
		return nil, fmt.Errorf("unknown wire type: %d", wireType)
	}
}
