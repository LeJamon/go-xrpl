package message

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Per-MessageType payload-size caps applied during header validation before
// allocating. Without these, a peer can claim MaxPayloadSize for any
// type and force a 64MB allocation per claim — trivial OOM vector.
// Values are ~10× typical observed traffic per known type.
const (
	smallMsgMax  = 64 * 1024        // 64 KiB
	mediumMsgMax = 1 * 1024 * 1024  // 1 MiB
	largeMsgMax  = 16 * 1024 * 1024 // 16 MiB
)

// MaxPayloadSizeForType returns the largest payload a peer may claim
// for the given message type (post-decompress for compressed frames).
// Unknown types are bounded only by the universal protocol ceiling so a
// newer peer can introduce message types without breaking older peers.
func MaxPayloadSizeForType(t MessageType) uint32 {
	return payloadSizeLimit(t)
}

func IsKnownMessageType(t MessageType) bool {
	_, known := codecs[t]
	return known
}

func payloadSizeLimit(t MessageType) uint32 {
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
	case TypeProofPathResponse:
		return largeMsgMax
	case TypeReplayDeltaResponse:
		return MaxMessageSize
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
		// framing validation) rather than a stricter limit.
		return MaxMessageSize
	default:
		return MaxMessageSize
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
	// cap). Header validation rejects any message whose on-wire or uncompressed
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
func (h Header) HeaderSize() int {
	if h.Compressed {
		return HeaderSizeCompressed
	}
	return HeaderSizeUncompressed
}

// TotalSize returns the total size of the message (header + payload).
func (h Header) TotalSize() int {
	return h.HeaderSize() + int(h.PayloadSize)
}

// EncodeHeader encodes a message header into the provided buffer.
// For uncompressed messages, buf must be at least 6 bytes.
// For compressed messages, buf must be at least 10 bytes.
func EncodeHeader(buf []byte, payloadSize uint32, msgType MessageType, algorithm CompressionAlgorithm, uncompressedSize uint32) error {
	header, err := newHeader(payloadSize, msgType, algorithm, uncompressedSize)
	if err != nil {
		return err
	}

	if len(buf) < header.HeaderSize() {
		return fmt.Errorf("buffer too small: need %d, got %d", header.HeaderSize(), len(buf))
	}
	encodeHeader(buf, header)
	return nil
}

func encodeHeader(buf []byte, header Header) {
	sizeWithFlags := header.PayloadSize
	if header.Compressed {
		sizeWithFlags |= uint32(header.Algorithm) << 24
	}
	binary.BigEndian.PutUint32(buf[:4], sizeWithFlags)
	binary.BigEndian.PutUint16(buf[4:6], uint16(header.MessageType))
	if header.Compressed {
		binary.BigEndian.PutUint32(buf[6:10], header.UncompressedSize)
	}
}

// DecodeHeader decodes a message header from the provided buffer.
// The buffer must contain at least 6 bytes. If the message is compressed,
// an additional 4 bytes will be read.
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSizeUncompressed {
		return nil, ErrTruncatedMessage
	}

	firstFour := binary.BigEndian.Uint32(buf[0:4])
	algorithm := AlgorithmNone
	if buf[0]&0x80 != 0 {
		if buf[0]&CompressionReservedMask != 0 {
			return nil, ErrInvalidHeader
		}
		algorithm = CompressionAlgorithm(buf[0] & CompressionFlagMask)
	} else if buf[0]&UncompressedFlagMask != 0 {
		return nil, ErrInvalidHeader
	}

	payloadSize := firstFour & MaxPayloadSize
	msgType := MessageType(binary.BigEndian.Uint16(buf[4:6]))
	uncompressedSize := payloadSize
	if algorithm != AlgorithmNone {
		if len(buf) < HeaderSizeCompressed {
			return nil, ErrTruncatedMessage
		}
		uncompressedSize = binary.BigEndian.Uint32(buf[6:10])
	}
	header, err := newHeader(payloadSize, msgType, algorithm, uncompressedSize)
	if err != nil {
		return nil, err
	}
	return &header, nil
}

// ReadHeader reads and validates exactly one message header.
func ReadHeader(r io.Reader) (*Header, error) {
	headerBuf := make([]byte, HeaderSizeCompressed)
	if _, err := io.ReadFull(r, headerBuf[:HeaderSizeUncompressed]); err != nil {
		return nil, fmt.Errorf("failed to read header: %w: %w", ErrTruncatedMessage, err)
	}

	if headerBuf[0]&0x80 != 0 {
		if _, err := io.ReadFull(r, headerBuf[HeaderSizeUncompressed:HeaderSizeCompressed]); err != nil {
			return nil, fmt.Errorf("failed to read compressed header: %w: %w", ErrTruncatedMessage, err)
		}
	}

	header, err := DecodeHeader(headerBuf)
	if err != nil {
		return nil, err
	}
	return header, nil
}

// ReadPayload reads exactly the payload declared by a validated header.
func ReadPayload(r io.Reader, header Header) ([]byte, error) {
	if err := validateHeaderClaims(header); err != nil {
		return nil, err
	}
	payload := make([]byte, header.PayloadSize)
	if header.PayloadSize > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("failed to read payload: %w: %w", ErrTruncatedMessage, err)
		}
	}
	return payload, nil
}

func newHeader(
	payloadSize uint32,
	msgType MessageType,
	algorithm CompressionAlgorithm,
	uncompressedSize uint32,
) (Header, error) {
	header := Header{
		PayloadSize: payloadSize,
		MessageType: msgType,
		Algorithm:   algorithm,
	}
	switch algorithm {
	case AlgorithmNone:
		header.UncompressedSize = payloadSize
	case AlgorithmLZ4:
		header.Compressed = true
		header.UncompressedSize = uncompressedSize
	default:
		return Header{}, fmt.Errorf("%w: %#02x", ErrUnknownCompression, uint8(algorithm))
	}
	if err := validateHeaderClaims(header); err != nil {
		return Header{}, err
	}
	return header, nil
}

func validateHeaderClaims(header Header) error {
	switch header.Algorithm {
	case AlgorithmNone:
		if header.Compressed || header.UncompressedSize != header.PayloadSize {
			return ErrInvalidHeader
		}
	case AlgorithmLZ4:
		if !header.Compressed {
			return ErrInvalidHeader
		}
	default:
		return fmt.Errorf("%w: %#02x", ErrUnknownCompression, uint8(header.Algorithm))
	}
	if header.PayloadSize > MaxPayloadSize {
		return fmt.Errorf("%w: payload exceeds 26-bit wire limit %d bytes", ErrMessageTooLarge, MaxPayloadSize)
	}
	// Hard protocol ceiling: rippled drops any message whose on-wire or
	// uncompressed claim exceeds a single 64 MB cap, on both fields
	// (ProtocolMessage.h:362-367). This is the absolute upper bound; the
	// per-type caps below add stricter, type-aware hardening.
	if header.PayloadSize > MaxMessageSize || header.UncompressedSize > MaxMessageSize {
		return fmt.Errorf("%w: exceeds protocol max %d bytes",
			ErrMessageTooLarge, MaxMessageSize)
	}

	// Cap both the on-wire and uncompressed claims per message type
	// BEFORE allocating so a tiny LZ4 frame cannot decompress into a
	// giant slice.
	maxSize := MaxPayloadSizeForType(header.MessageType)
	if header.PayloadSize > maxSize {
		return fmt.Errorf("%w: %d > %d for %s",
			ErrMessageTooLarge, header.PayloadSize, maxSize, header.MessageType)
	}
	if header.Compressed && header.UncompressedSize > maxSize {
		return fmt.Errorf("%w: uncompressed %d > %d for %s",
			ErrMessageTooLarge, header.UncompressedSize, maxSize, header.MessageType)
	}
	return nil
}

// BuildWireMessage creates a complete wire-protocol message (header + payload) as bytes.
func BuildWireMessage(msgType MessageType, payload []byte) ([]byte, error) {
	if uint64(len(payload)) > uint64(MaxPayloadSize) {
		return nil, ErrMessageTooLarge
	}
	header, err := newHeader(uint32(len(payload)), msgType, AlgorithmNone, 0)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, header.TotalSize())
	encodeHeader(buf, header)
	copy(buf[HeaderSizeUncompressed:], payload)
	return buf, nil
}

// EncodeFrame serializes a message and wraps it in its wire-protocol header.
func EncodeFrame(msg Message) ([]byte, error) {
	msgType, err := typeOfMessage(msg)
	if err != nil {
		return nil, err
	}
	payload, err := encode(msg, msgType)
	if err != nil {
		return nil, err
	}
	return BuildWireMessage(msgType, payload)
}
