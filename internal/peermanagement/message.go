package peermanagement

import (
	"io"

	"github.com/pierrec/lz4"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// Re-export message types for consolidated API.
type (
	// MessageType represents the type of a peer protocol message.
	MessageType = message.MessageType

	// Message is the interface implemented by all protocol messages.
	Message = message.Message

	// MessageHeader represents a parsed message header.
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

// ReadMessage reads a complete message from the reader.
func ReadMessage(r io.Reader) (*MessageHeader, []byte, error) {
	return message.ReadMessage(r)
}

// WriteMessage writes a message with header to the writer.
func WriteMessage(w io.Writer, msgType MessageType, payload []byte) error {
	return message.WriteMessage(w, msgType, payload)
}

// DecompressLZ4 decompresses LZ4 compressed data.
func DecompressLZ4(compressed []byte, uncompressedSize int) ([]byte, error) {
	if uncompressedSize <= 0 {
		return nil, ErrDecompressFailed
	}

	decompressed := make([]byte, uncompressedSize)
	n, err := lz4.UncompressBlock(compressed, decompressed)
	if err != nil {
		return nil, err
	}

	if n != uncompressedSize {
		return nil, ErrDecompressFailed
	}

	return decompressed, nil
}
