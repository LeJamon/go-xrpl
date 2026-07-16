package tx

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// CanonicalizeSerializedTransaction validates and returns the codec-decoded
// representation of a serialized transaction.
func CanonicalizeSerializedTransaction(fields map[string]any) (map[string]any, error) {
	encoded, err := serializedObjectBytes(fields)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseFromBinary(encoded)
	if err != nil {
		return nil, err
	}
	common := parsed.GetCommon()
	if common.TransactionType == "" {
		return nil, errors.New("transaction is missing TransactionType")
	}
	txType, ok := TypeFromName(common.TransactionType)
	if !ok {
		return nil, fmt.Errorf("unknown transaction type %q", common.TransactionType)
	}

	if err := checkRequiredFields(commonFields, common.PresentFields); err != nil {
		return nil, err
	}
	if err := checkRequiredFields(txTemplates[txType], common.PresentFields); err != nil {
		return nil, err
	}
	return binarycodec.DecodeBytes(encoded)
}

// CanonicalizeSerializedMetadata validates and returns the codec-decoded
// representation of transaction metadata.
func CanonicalizeSerializedMetadata(fields map[string]any) (map[string]any, error) {
	canonical, err := CanonicalizeSerializedObject(fields)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"TransactionResult", "TransactionIndex", "AffectedNodes"} {
		if _, ok := canonical[name]; !ok {
			return nil, fmt.Errorf("metadata is missing %s", name)
		}
	}
	return canonical, nil
}

// CanonicalizeSerializedObject validates codec field types and returns the
// decoded canonical object without applying a higher-level object template.
func CanonicalizeSerializedObject(fields map[string]any) (map[string]any, error) {
	encoded, err := serializedObjectBytes(fields)
	if err != nil {
		return nil, err
	}
	return binarycodec.DecodeBytes(encoded)
}

func serializedObjectBytes(fields map[string]any) ([]byte, error) {
	if err := validateSerializedJSONNumbers(fields); err != nil {
		return nil, err
	}
	return binarycodec.EncodeBytes(fields)
}

func validateSerializedJSONNumbers(value any) error {
	switch value := value.(type) {
	case map[string]any:
		defs := definitions.Get()
		for name, fieldValue := range value {
			if number, ok := fieldValue.(float64); ok {
				field, err := defs.FieldInstanceByName(name)
				if err == nil {
					var max float64
					switch field.Type {
					case "UInt8":
						max = 1<<8 - 1
					case "UInt16":
						max = 1<<16 - 1
					case "UInt32":
						max = 1<<32 - 1
					}
					if max != 0 && (number < 0 || number > max || math.Trunc(number) != number) {
						return fmt.Errorf("field %q is not a valid %s", name, field.Type)
					}
				}
			}
			if err := validateSerializedJSONNumbers(fieldValue); err != nil {
				return err
			}
		}
	case []any:
		for _, element := range value {
			if err := validateSerializedJSONNumbers(element); err != nil {
				return err
			}
		}
	case []map[string]any:
		for _, element := range value {
			if err := validateSerializedJSONNumbers(element); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRequiredFields(template []templateField, present map[string]bool) error {
	for _, field := range template {
		if field.style == soeREQUIRED && !present[field.name] {
			return fmt.Errorf("transaction is missing required field %q", field.name)
		}
	}
	return nil
}

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
	sortedNodes := make([]AffectedNode, len(meta.AffectedNodes))
	copy(sortedNodes, meta.AffectedNodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].LedgerIndex < sortedNodes[j].LedgerIndex
	})

	nodes := make([]map[string]any, len(sortedNodes))
	for i, node := range sortedNodes {
		nodes[i] = map[string]any{
			node.NodeType: buildAffectedNodeInner(node),
		}
	}
	result["AffectedNodes"] = nodes

	// DeliveredAmount (optional). The BINARY metadata field is sfDeliveredAmount
	// (codec/definitions name "DeliveredAmount", type Amount). The snake_case
	// "delivered_amount" is the RPC/JSON-only alias (see metadata.go ToMap) and
	// is NOT a binarycodec field — emitting it here makes binarycodec.Encode
	// fail with ErrUnknownField, which silently drops the tx from the consensus
	// build while its state mutation persists, yielding a ledger with advanced
	// state but an empty/short tx tree → transaction_hash fork.
	if meta.DeliveredAmount != nil {
		switch {
		case meta.DeliveredAmount.IsMPT():
			result["DeliveredAmount"] = map[string]any{
				"value":           meta.DeliveredAmount.Value(),
				"mpt_issuance_id": meta.DeliveredAmount.MPTIssuanceID(),
			}
		case meta.DeliveredAmount.Currency == "":
			result["DeliveredAmount"] = meta.DeliveredAmount.Value()
		default:
			result["DeliveredAmount"] = map[string]any{
				"value":    meta.DeliveredAmount.Value(),
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

	return binarycodec.EncodeBytesTrusted(metaMap)
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

// TransactionIndexFromTxWithMetaBlob returns sfTransactionIndex from a
// transaction-tree leaf. The JSON fallback is retained for transactions stored
// by the submit RPC before the open ledger is rebuilt.
func TransactionIndexFromTxWithMetaBlob(blob []byte) (uint32, bool) {
	if json.Valid(blob) {
		var stored struct {
			Meta map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(blob, &stored); err != nil {
			return 0, false
		}
		return transactionIndexFromMap(stored.Meta)
	}

	_, metaData, err := SplitTxWithMetaBlob(blob)
	if err != nil {
		return 0, false
	}
	return TransactionIndexFromMetadata(metaData)
}

// TransactionIndexFromMetadata returns sfTransactionIndex from serialized
// transaction metadata.
func TransactionIndexFromMetadata(metaData []byte) (uint32, bool) {
	if len(metaData) == 0 {
		return 0, false
	}
	meta, err := binarycodec.Decode(hex.EncodeToString(metaData))
	if err != nil {
		return 0, false
	}
	return transactionIndexFromMap(meta)
}

func transactionIndexFromMap(meta map[string]any) (uint32, bool) {
	raw, ok := meta["TransactionIndex"]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case uint32:
		return value, true
	case float64:
		if value < 0 || value > math.MaxUint32 || value != math.Trunc(value) {
			return 0, false
		}
		return uint32(value), true
	default:
		return 0, false
	}
}
