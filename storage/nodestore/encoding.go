package nodestore

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// nodeEncodingHeaderSize is the number of bytes in the encoding header.
// Format: [nodeType:1][ledgerSeq:4] = 5 bytes
const nodeEncodingHeaderSize = 5

// encodeBufPool amortises the per-Store / per-StoreBatch allocation of
// the encoded payload buffer. Each backend Set copies the slice
// internally, so callers may return the buffer immediately via
// releaseEncodeBuf.
var encodeBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 1024)
		return &buf
	},
}

// encodeBufMaxRetainBytes caps the size of buffers held in the pool so
// a single pathological large payload does not pin oversized buffers
// for the rest of the process lifetime.
const encodeBufMaxRetainBytes = 64 << 10

// acquireEncodeBuf returns a buffer sized to exactly size bytes, drawn
// from the pool when possible.
func acquireEncodeBuf(size int) []byte {
	p := encodeBufPool.Get().(*[]byte)
	buf := *p
	if cap(buf) < size {
		// Discard the under-sized pooled buffer; the pool will get a
		// fresh one when the caller releases.
		encodeBufPool.Put(p)
		return make([]byte, size)
	}
	*p = buf[:0]
	encodeBufPool.Put(p)
	return buf[:size]
}

// releaseEncodeBuf returns a buffer to the pool for reuse. Safe to
// call with a buffer that came from acquireEncodeBuf or a fresh make.
func releaseEncodeBuf(buf []byte) {
	if buf == nil || cap(buf) == 0 || cap(buf) > encodeBufMaxRetainBytes {
		return
	}
	b := buf[:0]
	encodeBufPool.Put(&b)
}

// encodeNodeData serializes a node for storage.
// Format: [nodeType:1][ledgerSeq:4][data:N]
// The returned buffer may come from a sync.Pool — callers MUST treat
// it as borrowed and call releaseEncodeBuf once the backend Set/Put
// returns (Set copies the value into the batch immediately).
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
