package message

import (
	"errors"
	"fmt"

	"github.com/pierrec/lz4"
)

// MinCompressibleSize is the compression size threshold: payloads at or
// below this many bytes are left uncompressed, matching rippled's
// `messageBytes <= 70` check (Message.cpp).
const MinCompressibleSize = 70

// ErrDecompressFailed is returned when LZ4 decompression fails or the
// decompressed length does not match the claimed uncompressed size.
var ErrDecompressFailed = errors.New("failed to decompress message")

// CompressLZ4 compresses data using LZ4. It returns nil (and no error)
// when the input is at or below MinCompressibleSize or compression would
// not save space.
func CompressLZ4(data []byte) ([]byte, error) {
	if len(data) <= MinCompressibleSize {
		return nil, nil
	}

	maxSize := lz4.CompressBlockBound(len(data))
	compressed := make([]byte, maxSize)

	n, err := lz4.CompressBlock(data, compressed, nil)
	if err != nil {
		return nil, err
	}

	if n == 0 || n >= len(data) {
		return nil, nil
	}

	return compressed[:n], nil
}

// DecompressLZ4 decompresses an LZ4 block whose original length is
// uncompressedSize. It fails if the size is non-positive or the
// decompressed length does not match the claim.
func DecompressLZ4(compressed []byte, uncompressedSize int) ([]byte, error) {
	if uncompressedSize <= 0 {
		return nil, fmt.Errorf("%w: invalid uncompressed size %d", ErrDecompressFailed, uncompressedSize)
	}
	if uint64(uncompressedSize) > uint64(MaxMessageSize) {
		return nil, fmt.Errorf("%w: uncompressed size %d exceeds protocol max %d", ErrMessageTooLarge, uncompressedSize, MaxMessageSize)
	}

	decompressed := make([]byte, uncompressedSize)
	n, err := lz4.UncompressBlock(compressed, decompressed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecompressFailed, err)
	}

	if n != uncompressedSize {
		return nil, fmt.Errorf("%w: decompressed length %d does not match claim %d", ErrDecompressFailed, n, uncompressedSize)
	}

	return decompressed, nil
}

// ShouldCompress reports whether a message type is worth compressing,
// matching the set rippled compresses.
func ShouldCompress(msgType MessageType) bool {
	switch msgType {
	case TypeManifests,
		TypeEndpoints,
		TypeTransaction,
		TypeGetLedger,
		TypeLedgerData,
		TypeGetObjects,
		TypeValidatorList,
		TypeValidatorListCollection,
		TypeReplayDeltaResponse,
		TypeTransactions:
		return true
	default:
		return false
	}
}

// CompressIfWorthwhile compresses data when the message type is
// compressible and the payload is large enough; otherwise it returns the
// input unchanged with false.
func CompressIfWorthwhile(msgType MessageType, data []byte) ([]byte, bool) {
	if !ShouldCompress(msgType) || len(data) <= MinCompressibleSize {
		return data, false
	}

	compressed, err := CompressLZ4(data)
	if err != nil || compressed == nil {
		return data, false
	}
	if len(compressed)+HeaderSizeCompressed >= len(data)+HeaderSizeUncompressed {
		return data, false
	}

	return compressed, true
}

// CompressFrameIfWorthwhile returns an LZ4 wire representation when frame is
// one complete uncompressed message and compression reduces its total size.
// The input is never modified.
func CompressFrameIfWorthwhile(frame []byte) ([]byte, bool) {
	header, err := DecodeHeader(frame)
	if err != nil || header.Compressed {
		return frame, false
	}

	payloadSize := int(header.PayloadSize)
	if payloadSize != len(frame)-HeaderSizeUncompressed {
		return frame, false
	}

	payload, compressed := CompressIfWorthwhile(
		header.MessageType,
		frame[HeaderSizeUncompressed:],
	)
	if !compressed {
		return frame, false
	}

	wire := make([]byte, HeaderSizeCompressed+len(payload))
	if err := EncodeHeader(
		wire,
		uint32(len(payload)),
		header.MessageType,
		AlgorithmLZ4,
		header.PayloadSize,
	); err != nil {
		return frame, false
	}
	copy(wire[HeaderSizeCompressed:], payload)
	return wire, true
}
