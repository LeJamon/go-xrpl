package peermanagement

import (
	"io"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// Re-export message types for consolidated API.
type (
	// MessageType represents the type of a peer protocol message.
	MessageType = message.MessageType

	// Message is the interface implemented by all protocol messages.
	Message = message.Message

	// Header represents a parsed message header.
	MessageHeader = message.Header

	// CompressionAlgorithm represents a compression algorithm.
	CompressionAlgorithm = message.CompressionAlgorithm
)

// Re-export message type constants.
const (
	TypeUnknown                 = message.TypeUnknown
	TypeManifests               = message.TypeManifests
	TypePing                    = message.TypePing
	TypeCluster                 = message.TypeCluster
	TypeEndpoints               = message.TypeEndpoints
	TypeTransaction             = message.TypeTransaction
	TypeGetLedger               = message.TypeGetLedger
	TypeLedgerData              = message.TypeLedgerData
	TypeProposeLedger           = message.TypeProposeLedger
	TypeStatusChange            = message.TypeStatusChange
	TypeHaveSet                 = message.TypeHaveSet
	TypeValidation              = message.TypeValidation
	TypeGetObjects              = message.TypeGetObjects
	TypeValidatorList           = message.TypeValidatorList
	TypeSquelch                 = message.TypeSquelch
	TypeValidatorListCollection = message.TypeValidatorListCollection
	TypeProofPathReq            = message.TypeProofPathReq
	TypeProofPathResponse       = message.TypeProofPathResponse
	TypeReplayDeltaReq          = message.TypeReplayDeltaReq
	TypeReplayDeltaResponse     = message.TypeReplayDeltaResponse
	TypeHaveTransactions        = message.TypeHaveTransactions
	TypeTransactions            = message.TypeTransactions
)

// Re-export compression algorithm constants.
const (
	AlgorithmNone = message.AlgorithmNone
	AlgorithmLZ4  = message.AlgorithmLZ4
)

// Re-export header size constants.
const (
	HeaderSizeUncompressed = message.HeaderSizeUncompressed
	HeaderSizeCompressed   = message.HeaderSizeCompressed
	MaxMessageSize         = message.MaxMessageSize
)

// MinCompressibleSize is the minimum message size worth compressing.
const MinCompressibleSize = message.MinCompressibleSize

// EncodeMessage encodes a message to bytes using protobuf.
func EncodeMessage(msg Message) ([]byte, error) {
	return message.Encode(msg)
}

// DecodeMessage decodes a message from bytes using protobuf.
func DecodeMessage(msgType MessageType, data []byte) (Message, error) {
	return message.Decode(msgType, data)
}

// EncodeMessageHeader encodes a message header into the provided buffer.
func EncodeMessageHeader(buf []byte, payloadSize uint32, msgType MessageType, algorithm CompressionAlgorithm, uncompressedSize uint32) error {
	return message.EncodeHeader(buf, payloadSize, msgType, algorithm, uncompressedSize)
}

// DecodeMessageHeader decodes a message header from the provided buffer.
func DecodeMessageHeader(buf []byte) (*MessageHeader, error) {
	return message.DecodeHeader(buf)
}

// ReadMessage reads a complete message from the reader.
func ReadMessage(r io.Reader) (*MessageHeader, []byte, error) {
	return message.ReadMessage(r)
}

// WriteMessage writes a message with header to the writer.
func WriteMessage(w io.Writer, msgType MessageType, payload []byte) error {
	return message.WriteMessage(w, msgType, payload)
}

// WriteMessageCompressed writes a potentially compressed message.
func WriteMessageCompressed(w io.Writer, msgType MessageType, payload []byte, algorithm CompressionAlgorithm, uncompressedSize uint32) error {
	return message.WriteMessageCompressed(w, msgType, payload, algorithm, uncompressedSize)
}

// CompressLZ4 compresses data using LZ4 (see message.CompressLZ4).
func CompressLZ4(data []byte) ([]byte, error) {
	return message.CompressLZ4(data)
}

// DecompressLZ4 decompresses LZ4-compressed data (see message.DecompressLZ4).
func DecompressLZ4(compressed []byte, uncompressedSize int) ([]byte, error) {
	return message.DecompressLZ4(compressed, uncompressedSize)
}

// ShouldCompress reports whether a message type is worth compressing.
func ShouldCompress(msgType uint16) bool {
	return message.ShouldCompress(msgType)
}

// CompressIfWorthwhile compresses data if it would be beneficial.
func CompressIfWorthwhile(msgType uint16, data []byte) ([]byte, bool) {
	return message.CompressIfWorthwhile(msgType, data)
}
