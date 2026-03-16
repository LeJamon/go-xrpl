package tx

import (
	"encoding/hex"
	"errors"
	"sort"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
)

var (
	// ErrLengthPrefixTooLong is returned when the length exceeds 918744 bytes
	ErrLengthPrefixTooLong = errors.New("length of value must not exceed 918744 bytes")
)

// EncodeVL encodes a variable-length prefix for the given data length.
// This matches the XRPL VL encoding format:
// - 0-192 bytes: 1 byte prefix (0x00-0xC0)
// - 193-12480 bytes: 2 byte prefix
// - 12481-918744 bytes: 3 byte prefix
func EncodeVL(length int) ([]byte, error) {
	if length <= 192 {
		return []byte{byte(length)}, nil
	}
	if length <= 12480 {
		length -= 193
		b1 := byte((length >> 8) + 193)
		b2 := byte(length & 0xFF)
		return []byte{b1, b2}, nil
	}
	if length <= 918744 {
		length -= 12481
		b1 := byte((length >> 16) + 241)
		b2 := byte((length >> 8) & 0xFF)
		b3 := byte(length & 0xFF)
		return []byte{b1, b2, b3}, nil
	}
	return nil, ErrLengthPrefixTooLong
}

// EncodeWithVL encodes data with a VL prefix
func EncodeWithVL(data []byte) ([]byte, error) {
	vlPrefix, err := EncodeVL(len(data))
	if err != nil {
		return nil, err
	}
	result := make([]byte, len(vlPrefix)+len(data))
	copy(result, vlPrefix)
	copy(result[len(vlPrefix):], data)
	return result, nil
}

// MetadataToMap converts a Metadata struct to a map[string]any for binary encoding
func MetadataToMap(meta *Metadata) map[string]any {
	if meta == nil {
		return nil
	}

	result := make(map[string]any)

	// TransactionResult - convert Result enum to string
	result["TransactionResult"] = meta.TransactionResult.String()

	// TransactionIndex
	result["TransactionIndex"] = meta.TransactionIndex

	// AffectedNodes - sort by LedgerIndex to match rippled's ordering
	if len(meta.AffectedNodes) > 0 {
		// Sort nodes by LedgerIndex (ascending)
		sortedNodes := make([]AffectedNode, len(meta.AffectedNodes))
		copy(sortedNodes, meta.AffectedNodes)
		sort.Slice(sortedNodes, func(i, j int) bool {
			return sortedNodes[i].LedgerIndex < sortedNodes[j].LedgerIndex
		})

		nodes := make([]map[string]any, len(sortedNodes))
		for i, node := range sortedNodes {
			nodeMap := make(map[string]any)

			// Create the inner node content
			innerNode := make(map[string]any)
			innerNode["LedgerEntryType"] = node.LedgerEntryType
			innerNode["LedgerIndex"] = node.LedgerIndex

			// Add PreviousTxnLgrSeq and PreviousTxnID for ModifiedNode only
			// For DeletedNode, these fields appear inside FinalFields (via sMD_DeleteFinal)
			// but NOT at the node level in the metadata structure
			if node.NodeType == "ModifiedNode" && node.PreviousTxnLgrSeq != 0 {
				innerNode["PreviousTxnLgrSeq"] = node.PreviousTxnLgrSeq
			}
			if node.NodeType == "ModifiedNode" && node.PreviousTxnID != "" {
				innerNode["PreviousTxnID"] = node.PreviousTxnID
			}

			if node.FinalFields != nil && len(node.FinalFields) > 0 {
				innerNode["FinalFields"] = node.FinalFields
			}
			if node.PreviousFields != nil && len(node.PreviousFields) > 0 {
				innerNode["PreviousFields"] = node.PreviousFields
			}
			if node.NewFields != nil && len(node.NewFields) > 0 {
				innerNode["NewFields"] = node.NewFields
			}

			// Wrap in the node type
			nodeMap[node.NodeType] = innerNode
			nodes[i] = nodeMap
		}
		result["AffectedNodes"] = nodes
	}

	// DeliveredAmount (optional)
	if meta.DeliveredAmount != nil {
		// Check if it's an XRP amount (no Currency field)
		if meta.DeliveredAmount.Currency == "" {
			result["delivered_amount"] = meta.DeliveredAmount.Value
		} else {
			result["delivered_amount"] = map[string]any{
				"value":    meta.DeliveredAmount.Value,
				"currency": meta.DeliveredAmount.Currency,
				"issuer":   meta.DeliveredAmount.Issuer,
			}
		}
	}

	return result
}

// SerializeMetadata serializes metadata to binary format
func SerializeMetadata(meta *Metadata) ([]byte, error) {
	if meta == nil {
		return nil, nil
	}

	metaMap := MetadataToMap(meta)
	if metaMap == nil {
		return nil, nil
	}

	hexStr, err := binarycodec.Encode(metaMap)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// CreateTxWithMetaBlob creates the combined VL-encoded transaction + VL-encoded metadata blob
// This is the format expected by the transaction tree in the XRPL:
// [VL-length][tx_data][VL-length][metadata_data]
func CreateTxWithMetaBlob(txBlob []byte, meta *Metadata) ([]byte, error) {
	// Encode transaction with VL prefix
	vlTx, err := EncodeWithVL(txBlob)
	if err != nil {
		return nil, err
	}

	// Serialize and encode metadata with VL prefix
	metaBlob, err := SerializeMetadata(meta)
	if err != nil {
		return nil, err
	}

	vlMeta, err := EncodeWithVL(metaBlob)
	if err != nil {
		return nil, err
	}

	// Combine: VL-tx + VL-metadata
	result := make([]byte, len(vlTx)+len(vlMeta))
	copy(result, vlTx)
	copy(result[len(vlTx):], vlMeta)

	return result, nil
}

// SplitTxWithMetaBlob splits a combined VL-encoded blob into separate
// transaction bytes and metadata bytes. This is the inverse of CreateTxWithMetaBlob.
// The input format is: [VL-length][tx_data][VL-length][metadata_data]
// Uses the existing BinaryParser.ReadVariableLength from the codec package.
func SplitTxWithMetaBlob(blob []byte) (txData []byte, metaData []byte, err error) {
	if len(blob) == 0 {
		return nil, nil, errors.New("empty blob")
	}

	parser := serdes.NewBinaryParser(blob, nil)

	// Read tx: VL prefix then data
	txLen, err := parser.ReadVariableLength()
	if err != nil {
		return nil, nil, err
	}
	txData, err = parser.ReadBytes(txLen)
	if err != nil {
		return nil, nil, err
	}

	// Read meta: VL prefix then data (optional)
	if !parser.HasMore() {
		return txData, nil, nil
	}
	metaLen, err := parser.ReadVariableLength()
	if err != nil {
		return nil, nil, err
	}
	metaData, err = parser.ReadBytes(metaLen)
	if err != nil {
		return nil, nil, err
	}

	return txData, metaData, nil
}
