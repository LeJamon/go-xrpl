package nodestore

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// nodeEncodingHeaderSize is the number of bytes in the encoding header.
// Format: [nodeType:1][ledgerSeq:4] = 5 bytes.
//
// This layout is go-xrpl-internal and intentionally diverges from rippled's
// on-disk blob format, which is [8 unused/zero bytes][nodeType:1][data:N] = 9
// bytes and carries no ledger sequence (rippled EncodedBlob.h:99-101,
// DecodedBlob.cpp:32-39). go-xrpl drops rippled's 8-byte pad and instead stores
// the ledger sequence inline. encodeNodeData/decodeNodeData below are the sole
// readers and writers of this format, so the two stay self-consistent.
//
// The divergence is safe because nodestore files are never shared with rippled:
// keys are content-addressed, so a node looked up by hash decodes identically
// regardless of header layout, and go-xrpl performs no cross-client on-disk
// import/export. Adopting rippled's 9-byte layout would only matter if such
// interop were ever required.
const nodeEncodingHeaderSize = 5

// encodeBufPool amortises the per-Store encoded-payload allocation.
// Backends are required to copy the value into their batch before Put
// returns, so callers may release the buffer immediately.
var encodeBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 1024)
		return &buf
	},
}

// encodeBufMaxRetainBytes bounds the buffer size retained in the pool so
// one pathological payload cannot pin oversized buffers indefinitely.
const encodeBufMaxRetainBytes = 64 << 10

func acquireEncodeBuf(size int) []byte {
	p := encodeBufPool.Get().(*[]byte)
	buf := *p
	if cap(buf) < size {
		// Pooled buffer too small: return the wrapper and allocate an
		// exact-fit buffer for the caller.
		*p = buf[:0]
		encodeBufPool.Put(p)
		return make([]byte, size)
	}
	// Hand the buffer to the caller; it is returned to the pool by
	// releaseEncodeBuf once the backend has copied the value. Putting it back
	// here would alias the same backing array to a concurrent acquirer — one
	// caller's encodeNodeData write would race the other's backend Put read.
	return buf[:size]
}

func releaseEncodeBuf(buf []byte) {
	if buf == nil || cap(buf) == 0 || cap(buf) > encodeBufMaxRetainBytes {
		return
	}
	b := buf[:0]
	encodeBufPool.Put(&b)
}

// encodeNodeData serializes a node for storage.
// Format: [nodeType:1][ledgerSeq:4][data:N].
// The returned buffer is borrowed from a sync.Pool; callers MUST call
// releaseEncodeBuf after the backend Set/Put returns.
func encodeNodeData(n *Node) []byte {
	buf := acquireEncodeBuf(nodeEncodingHeaderSize + len(n.Data))
	buf[0] = byte(n.Type)
	binary.BigEndian.PutUint32(buf[1:5], n.LedgerSeq)
	copy(buf[nodeEncodingHeaderSize:], n.Data)
	return buf
}

func validateNode(node *Node) error {
	if node == nil {
		return fmt.Errorf("%w: nil", ErrInvalidNode)
	}
	if !isSupportedNodeType(node.Type) {
		return fmt.Errorf("%w: unsupported type %d", ErrInvalidNode, node.Type)
	}
	if node.Hash == (Hash256{}) {
		return fmt.Errorf("%w: zero hash", ErrInvalidNode)
	}
	if len(node.Data) == 0 {
		return fmt.Errorf("%w: empty payload", ErrInvalidNode)
	}
	return nil
}

func isSupportedNodeType(nodeType NodeType) bool {
	switch nodeType {
	case NodeLedger, NodeAccount, NodeTransaction:
		return true
	default:
		return false
	}
}

// decodeNodeData deserializes a node and takes ownership of data. Key-value
// stores return caller-owned values, so the payload can back the immutable
// node directly instead of being copied again.
func decodeNodeData(hash Hash256, data []byte) (*Node, error) {
	if len(data) < nodeEncodingHeaderSize {
		return nil, fmt.Errorf("%w: data too short (%d bytes)", ErrDataCorrupt, len(data))
	}
	nodeType := NodeType(data[0])
	ledgerSeq := binary.BigEndian.Uint32(data[1:5])
	nodeData := data[nodeEncodingHeaderSize:len(data):len(data)]
	node := &Node{
		Type:      nodeType,
		Hash:      hash,
		Data:      nodeData,
		LedgerSeq: ledgerSeq,
	}
	if err := validateNode(node); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDataCorrupt, err)
	}
	return node, nil
}
