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
	case TypeManifests,
		TypeValidatorList,
		TypeValidatorListCollection,
		TypeGetLedger,
		TypeGetObjects,
		TypeProofPathReq,
		TypeReplayDeltaReq,
		TypeTransaction:
		return mediumMsgMax
	case TypeLedgerData,
		TypeTransactions,
		TypeProofPathResponse,
		TypeReplayDeltaResponse:
		return largeMsgMax
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

	// MaxMessageSize is the maximum allowed message size (64 MB).
	MaxMessageSize = 64 * 1024 * 1024

	// MaxPayloadSizeBits is the number of bits used for payload size (26 bits).
	MaxPayloadSizeBits = 26

	// MaxPayloadSize is the maximum payload size that can be encoded.
	MaxPayloadSize = (1 << MaxPayloadSizeBits) - 1

	// CompressionFlagMask is the mask for compression flags in the first byte.
	CompressionFlagMask = 0xF0

	// CompressionNone indicates no compression.
	CompressionNone = 0x00

	// CompressionLZ4 is the first-byte algorithm nibble for LZ4 compression
	// (compression bit set + algorithm bits), matching rippled's
	// compression::Algorithm::LZ4.
	CompressionLZ4 = 0x90

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
	// UncompressedSize is the original size before compression (if compressed).
	UncompressedSize uint32
	// Algorithm is the compression algorithm used.
	Algorithm CompressionAlgorithm
}

// CompressionAlgorithm represents a compression algorithm.
type CompressionAlgorithm uint8

const (
	// AlgorithmNone means no compression.
	AlgorithmNone CompressionAlgorithm = 0
	// AlgorithmLZ4 means LZ4 compression.
	AlgorithmLZ4 CompressionAlgorithm = 1
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
// Reference: rippled Message.cpp setHeader()
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

	// Pack the first 4 bytes: compression flags (6 bits) + payload size (26 bits)
	// For uncompressed: first 6 bits are 0
	// For compressed: first bit is 1, next 3 bits are algorithm, next 2 bits reserved
	sizeWithFlags := payloadSize
	if compressed {
		// Set compression bit and algorithm
		// Bit layout: [1][alg][alg][alg][0][0][size...26 bits]
		// bit 7 = compression flag, bits 4-6 = algorithm
		sizeWithFlags |= uint32(0x80|(uint8(algorithm)<<4)) << 24
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
// Reference: rippled ProtocolMessage.h
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSizeUncompressed {
		return nil, ErrTruncatedMessage
	}

	h := &Header{}

	// Parse first 4 bytes
	firstFour := binary.BigEndian.Uint32(buf[0:4])

	// Validate the framing marker, mirroring rippled's parseMessageHeader.
	if buf[0]&0x80 != 0 {
		// Compressed frame: the two reserved bits must be clear and the
		// algorithm nibble must be exactly LZ4.
		if buf[0]&CompressionReservedMask != 0 {
			return nil, ErrInvalidHeader
		}
		if buf[0]&CompressionFlagMask != CompressionLZ4 {
			return nil, ErrUnknownCompression
		}
		h.Compressed = true
		h.Algorithm = AlgorithmLZ4
	} else if buf[0]&UncompressedFlagMask != 0 {
		// Uncompressed frame: the top six bits must all be zero.
		return nil, ErrInvalidHeader
	}

	// Extract payload size (26 bits); the mask strips the flag/algorithm bits.
	h.PayloadSize = firstFour & MaxPayloadSize

	// Extract message type (2 bytes)
	h.MessageType = MessageType(binary.BigEndian.Uint16(buf[4:6]))

	// For compressed messages, read uncompressed size
	if h.Compressed {
		if len(buf) < HeaderSizeCompressed {
			return nil, ErrTruncatedMessage
		}
		h.UncompressedSize = binary.BigEndian.Uint32(buf[6:10])
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

	// Cap both the on-wire and uncompressed claims BEFORE allocating
	// so a tiny LZ4 frame cannot decompress into a giant slice.
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

// PeekHeader reads and returns the header without consuming the payload.
// Useful for determining message type and size before full read.
func PeekHeader(buf []byte) (*Header, error) {
	return DecodeHeader(buf)
}
