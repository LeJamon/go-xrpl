package tx

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// ParseJSON parses a JSON transaction into the appropriate transaction type.
// Uses the registry-based FromJSON for all registered types, with a fallback
// to BaseTx for unregistered types.
func ParseJSON(data []byte) (Transaction, error) {
	tx, err := FromJSON(data)
	if err == ErrUnknownTransactionType {
		// Fallback: parse as generic BaseTx for unregistered types
		var header struct {
			TransactionType string `json:"TransactionType"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
		txType, _ := TypeFromName(header.TransactionType)
		var baseTx BaseTx
		if err := json.Unmarshal(data, &baseTx); err != nil {
			return nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
		baseTx.txType = txType
		return &baseTx, nil
	}
	return tx, err
}

// TypeFromString converts a transaction type string to a Type
func TypeFromString(s string) (Type, error) {
	t, ok := TypeFromName(s)
	if !ok {
		return 0, ErrInvalidTransactionType
	}
	return t, nil
}

// ParseFromBinary parses a binary transaction blob into a Transaction
func ParseFromBinary(blob []byte) (Transaction, error) {
	// Convert binary to hex string for the codec
	hexStr := hex.EncodeToString(blob)

	// Decode binary to JSON map
	jsonMap, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode binary transaction: %w", err)
	}

	// Extract present fields from the decoded map
	// This is used to distinguish between absent fields and empty values
	presentFields := make(map[string]bool)
	for key := range jsonMap {
		presentFields[key] = true
	}

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(jsonMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal decoded transaction: %w", err)
	}

	tx, err := ParseJSON(jsonBytes)
	if err != nil {
		return nil, err
	}

	tx.GetCommon().SetPresentFields(presentFields)

	// Preserve the original serialized bytes so that downstream consumers
	// (e.g. sortCanonicalSalted) can use them for hash/SHAMap computation
	// without re-encoding, ensuring byte-exact fidelity with the source.
	tx.SetRawBytes(blob)

	return tx, nil
}
