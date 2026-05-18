package nodestore

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// nodeEncodingHeaderSize is the number of bytes in the encoding header.
// Format: [nodeType:1][ledgerSeq:4] = 5 bytes
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
		encodeBufPool.Put(p)
		return make([]byte, size)
	}
	*p = buf[:0]
	encodeBufPool.Put(p)
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

// decodeNodeData deserializes a node from kvstore data.
func decodeNodeData(hash Hash256, data []byte) (*Node, error) {
	if len(data) < nodeEncodingHeaderSize {
		return nil, fmt.Errorf("%w: data too short (%d bytes)", ErrDataCorrupt, len(data))
	}
	nodeType := NodeType(data[0])
	ledgerSeq := binary.BigEndian.Uint32(data[1:5])
	nodeData := make([]byte, len(data)-nodeEncodingHeaderSize)
	copy(nodeData, data[nodeEncodingHeaderSize:])
	return &Node{
		Type:      nodeType,
		Hash:      hash,
		Data:      nodeData,
		LedgerSeq: ledgerSeq,
	}, nil
}
