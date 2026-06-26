package message

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Per-MessageType payload-size caps applied by ReadMessage BEFORE
// allocating. Without these, a peer can claim MaxPayloadSize for any
// type and force a 64MB allocation per claim — trivial OOM vector.
// Values are ~10× typical observed traffic per type; unknown types
// fall back to defaultPerTypeMax.
const (
	smallMsgMax       = 64 * 1024        // 64 KiB
	mediumMsgMax      = 1 * 1024 * 1024  // 1 MiB
	largeMsgMax       = 16 * 1024 * 1024 // 16 MiB
	defaultPerTypeMax = mediumMsgMax
)

// MaxPayloadSizeForType returns the largest payload a peer may claim
// for the given message type (post-decompress for compressed frames).
// Unknown types fall back to defaultPerTypeMax.
func MaxPayloadSizeForType(t MessageType) uint32 {
	switch t {
	case TypePing, TypeSquelch:
		return 2048
	case TypeEndpoints,
		TypeStatusChange,
		TypeProposeLedger,
		TypeValidation,
		TypeHaveSet,
		TypeHaveTransactions,
		TypeCluster:
		return smallMsgMax
	case TypeGetLedger,
		TypeProofPathReq,
		TypeReplayDeltaReq,
		TypeTransaction:
		return mediumMsgMax
	case TypeProofPathResponse,
		TypeReplayDeltaResponse:
		return largeMsgMax
	case TypeManifests,
		TypeValidatorList,
		TypeValidatorListCollection,
		TypeLedgerData,
		TypeGetObjects,
		TypeTransactions:
		// Bulk response/broadcast types can legitimately approach
		// rippled's single 64 MB protocol ceiling, which applies no
		// per-type cap of its own: a TMLedgerData reply fills up to
		// softMaxReplyNodes fat nodes, TMGetObjectByHash carries
		// fetch-pack data on the same type as its queries, a full
		// TMManifests batches every stored manifest unsplit, and a single
		// TMValidatorList / TMValidatorListCollection blob is bounded only
		// by the ceiling. A tighter local cap would tear down a peer
		// mid-sync, so these rely on the protocol ceiling (enforced in
		// ReadMessage) rather than a stricter limit.
		return MaxMessageSize
	default:
		return defaultPerTypeMax
	}
}

const (
	// HeaderSizeUncompressed is the size of an uncompressed message header.
	// Format: 4 bytes (6 bits flags + 26 bits size) + 2 bytes (type)
	HeaderSizeUncompressed = 6

	// HeaderSizeCompressed is the size of a compressed message header.
	// Format: 4 bytes (flags + size) + 2 bytes (type) + 4 bytes (uncompressed size)
	HeaderSizeCompressed = 10

	// MaxMessageSize is the hard protocol ceiling (rippled's single 64 MB
	// cap). ReadMessage rejects any message whose on-wire or uncompressed
	// claim exceeds it; the per-type caps above add stricter, type-aware
	// hardening on top.
	MaxMessageSize = 64 * 1024 * 1024

	// MaxPayloadSizeBits is the number of bits used for payload size (26 bits).
	MaxPayloadSizeBits = 26

	// MaxPayloadSize is the maximum payload size that can be encoded.
	MaxPayloadSize = (1 << MaxPayloadSizeBits) - 1

	// CompressionFlagMask isolates the first byte's algorithm nibble
	// (compression flag + 3 algorithm bits).
	CompressionFlagMask = 0xF0

	// CompressionReservedMask covers the two reserved bits of the first byte
	// that must be zero in a compressed frame (rippled rejects them).
	CompressionReservedMask = 0x0C

	// UncompressedFlagMask covers the six flag bits of the first byte that
	// must all be zero in an uncompressed frame.
	UncompressedFlagMask = 0xFC
)

var (
	// ErrMessageTooLarge is returned when a message exceeds the maximum size.
	ErrMessageTooLarge = errors.New("message too large")
	// ErrInvalidHeader is returned when the message header is invalid.
	ErrInvalidHeader = errors.New("invalid message header")
	// ErrUnknownCompression is returned for unknown compression algorithms.
	ErrUnknownCompression = errors.New("unknown compression algorithm")
	// ErrTruncatedMessage is returned when a message is truncated.
	ErrTruncatedMessage = errors.New("truncated message")
)

// Header represents a parsed message header.
type Header struct {
	// PayloadSize is the size of the payload in bytes.
	PayloadSize uint32
	// MessageType is the type of the message.
	MessageType MessageType
	// Compressed indicates if the message is compressed.
	Compressed bool
	// UncompressedSize is the original payload size before compression;
	// for an uncompressed frame it equals PayloadSize.
	UncompressedSize uint32
	// Algorithm is the compression algorithm used.
	Algorithm CompressionAlgorithm
}

// CompressionAlgorithm represents a compression algorithm.
type CompressionAlgorithm uint8

// Algorithm values are the first-byte nibble carried on the wire:
// None=0x00, LZ4=0x90 (the high bit is the compression flag). Keeping
// them identical to the wire byte lets the header pack/unpack the
// algorithm without a separate translation.
const (
	// AlgorithmNone means no compression.
	AlgorithmNone CompressionAlgorithm = 0x00
	// AlgorithmLZ4 means LZ4 compression.
	AlgorithmLZ4 CompressionAlgorithm = 0x90
)

// HeaderSize returns the size of the header based on compression.
func (h *Header) HeaderSize() int {
	if h.Compressed {
		return HeaderSizeCompressed
	}
	return HeaderSizeUncompressed
}

// TotalSize returns the total size of the message (header + payload).
func (h *Header) TotalSize() int {
	return h.HeaderSize() + int(h.PayloadSize)
}

// EncodeHeader encodes a message header into the provided buffer.
// For uncompressed messages, buf must be at least 6 bytes.
// For compressed messages, buf must be at least 10 bytes.
func EncodeHeader(buf []byte, payloadSize uint32, msgType MessageType, algorithm CompressionAlgorithm, uncompressedSize uint32) error {
	if payloadSize > MaxPayloadSize {
		return ErrMessageTooLarge
	}

	compressed := algorithm != AlgorithmNone
	requiredSize := HeaderSizeUncompressed
	if compressed {
		requiredSize = HeaderSizeCompressed
	}

	if len(buf) < requiredSize {
		return fmt.Errorf("buffer too small: need %d, got %d", requiredSize, len(buf))
	}

	// First 4 bytes: the top byte holds the algorithm nibble, the low 26 bits
	// hold the payload size. The algorithm value already carries the
	// compression flag in its high bit.
	sizeWithFlags := payloadSize
	if compressed {
		sizeWithFlags |= uint32(algorithm) << 24
	}

	buf[0] = byte((sizeWithFlags >> 24) & 0xFF)
	buf[1] = byte((sizeWithFlags >> 16) & 0xFF)
	buf[2] = byte((sizeWithFlags >> 8) & 0xFF)
	buf[3] = byte(sizeWithFlags & 0xFF)

	// Pack message type (2 bytes, big endian)
	buf[4] = byte((msgType >> 8) & 0xFF)
	buf[5] = byte(msgType & 0xFF)

	// For compressed messages, add uncompressed size
	if compressed {
		buf[6] = byte((uncompressedSize >> 24) & 0xFF)
		buf[7] = byte((uncompressedSize >> 16) & 0xFF)
		buf[8] = byte((uncompressedSize >> 8) & 0xFF)
		buf[9] = byte(uncompressedSize & 0xFF)
	}

	return nil
}

// DecodeHeader decodes a message header from the provided buffer.
// The buffer must contain at least 6 bytes. If the message is compressed,
// an additional 4 bytes will be read.
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSizeUncompressed {
		return nil, ErrTruncatedMessage
	}

	h := &Header{}

	// Parse first 4 bytes
	firstFour := binary.BigEndian.Uint32(buf[0:4])

	// Validate the framing marker.
	if buf[0]&0x80 != 0 {
		if buf[0]&CompressionReservedMask != 0 {
			return nil, ErrInvalidHeader
		}
		if buf[0]&CompressionFlagMask != byte(AlgorithmLZ4) {
			return nil, ErrUnknownCompression
		}
		h.Compressed = true
		h.Algorithm = AlgorithmLZ4
	} else if buf[0]&UncompressedFlagMask != 0 {
		return nil, ErrInvalidHeader
	}

	// Extract payload size (26 bits); the mask strips the flag/algorithm bits.
	h.PayloadSize = firstFour & MaxPayloadSize

	// Extract message type (2 bytes)
	h.MessageType = MessageType(binary.BigEndian.Uint16(buf[4:6]))

	// For compressed messages, read the uncompressed size from the wire;
	// for uncompressed frames the original size is the on-wire payload
	// size, mirroring rippled (ProtocolMessage.h:247) so the 64 MB
	// protocol-ceiling check sees the same value on both fields.
	if h.Compressed {
		if len(buf) < HeaderSizeCompressed {
			return nil, ErrTruncatedMessage
		}
		h.UncompressedSize = binary.BigEndian.Uint32(buf[6:10])
	} else {
		h.UncompressedSize = h.PayloadSize
	}

	return h, nil
}

// ReadMessage reads a complete message from the reader.
// Returns the header and the payload.
func ReadMessage(r io.Reader) (*Header, []byte, error) {
	// Read header (start with minimum size)
	headerBuf := make([]byte, HeaderSizeCompressed)
	if _, err := io.ReadFull(r, headerBuf[:HeaderSizeUncompressed]); err != nil {
		return nil, nil, fmt.Errorf("failed to read header: %w", err)
	}

	// Check if compressed
	if headerBuf[0]&0x80 != 0 {
		// Read additional 4 bytes for compressed header
		if _, err := io.ReadFull(r, headerBuf[HeaderSizeUncompressed:HeaderSizeCompressed]); err != nil {
			return nil, nil, fmt.Errorf("failed to read compressed header: %w", err)
		}
	}

	header, err := DecodeHeader(headerBuf)
	if err != nil {
		return nil, nil, err
	}

	// Hard protocol ceiling: rippled drops any message whose on-wire or
	// uncompressed claim exceeds a single 64 MB cap, on both fields
	// (ProtocolMessage.h:362-367). This is the absolute upper bound; the
	// per-type caps below add stricter, type-aware hardening.
	if header.PayloadSize > MaxMessageSize || header.UncompressedSize > MaxMessageSize {
		return nil, nil, fmt.Errorf("%w: exceeds protocol max %d bytes",
			ErrMessageTooLarge, MaxMessageSize)
	}

	// Cap both the on-wire and uncompressed claims per message type
	// BEFORE allocating so a tiny LZ4 frame cannot decompress into a
	// giant slice.
	maxSize := MaxPayloadSizeForType(header.MessageType)
	if header.PayloadSize > maxSize {
		return nil, nil, fmt.Errorf("%w: %d > %d for %s",
			ErrMessageTooLarge, header.PayloadSize, maxSize, header.MessageType)
	}
	if header.Compressed && header.UncompressedSize > maxSize {
		return nil, nil, fmt.Errorf("%w: uncompressed %d > %d for %s",
			ErrMessageTooLarge, header.UncompressedSize, maxSize, header.MessageType)
	}

	// Read payload
	payload := make([]byte, header.PayloadSize)
	if header.PayloadSize > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, nil, fmt.Errorf("failed to read payload: %w", err)
		}
	}

	return header, payload, nil
}

// WriteMessage writes a message with header to the writer.
func WriteMessage(w io.Writer, msgType MessageType, payload []byte) error {
	return WriteMessageCompressed(w, msgType, payload, AlgorithmNone, 0)
}

// WriteMessageCompressed writes a potentially compressed message.
func WriteMessageCompressed(w io.Writer, msgType MessageType, payload []byte, algorithm CompressionAlgorithm, uncompressedSize uint32) error {
	headerSize := HeaderSizeUncompressed
	if algorithm != AlgorithmNone {
		headerSize = HeaderSizeCompressed
	}

	buf := make([]byte, headerSize+len(payload))

	if err := EncodeHeader(buf, uint32(len(payload)), msgType, algorithm, uncompressedSize); err != nil {
		return err
	}

	copy(buf[headerSize:], payload)

	_, err := w.Write(buf)
	return err
}

// BuildWireMessage creates a complete wire-protocol message (header + payload) as bytes.
func BuildWireMessage(msgType MessageType, payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, msgType, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
