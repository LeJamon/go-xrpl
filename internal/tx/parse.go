package tx

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

// ParseHash256NonZero decodes a 64-character hex string into a 32-byte hash,
// rejecting malformed input, wrong-length input, and the all-zero hash.
func ParseHash256NonZero(s string) ([32]byte, error) {
	var h [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return h, ter.Errorf(ter.TemMALFORMED, "invalid 256-bit hash")
	}
	copy(h[:], b)
	if h == [32]byte{} {
		return h, ter.Errorf(ter.TemMALFORMED, "256-bit hash must be non-zero")
	}
	return h, nil
}

// ParseFromBinary parses a binary transaction blob into a Transaction
func ParseFromBinary(blob []byte) (Transaction, error) {
	// Decode the canonical binary directly; the blob is already bytes, so
	// going through a hex string round-trip would only churn allocations on
	// this per-transaction hot path.
	jsonMap, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		return nil, fmt.Errorf("failed to decode binary transaction: %w", err)
	}

	// Extract present fields from the decoded map
	// This is used to distinguish between absent fields and empty values
	presentFields := make(map[string]bool)
	for key := range jsonMap {
		presentFields[key] = true
	}

	typeName, _ := jsonMap["TransactionType"].(string)
	txType, knownType := TypeFromName(typeName)

	// Reject any codec-known field that is not allowed for this transaction
	// type before the transaction can be applied, mirroring rippled's STTx
	// template application. Without this a tx carrying a field disallowed for
	// its type would be silently applied while rippled rejects it at
	// deserialization, forking the ledger.
	if knownType {
		if err := checkTemplate(txType, presentFields); err != nil {
			return nil, err
		}
	}

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(jsonMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal decoded transaction: %w", err)
	}

	// The TransactionType is already known from the decoded map, so build the
	// concrete transaction and unmarshal in one pass, skipping the redundant
	// TransactionType re-parse FromJSON performs. Unknown or unregistered
	// types fall back to the generic ParseJSON path (yielding a BaseTx).
	var parsed Transaction
	if knownType {
		if t, nerr := NewFromType(txType); nerr == nil {
			if err := json.Unmarshal(jsonBytes, t); err != nil {
				return nil, fmt.Errorf("failed to parse transaction: %w", err)
			}
			parsed = t
		}
	}
	if parsed == nil {
		parsed, err = ParseJSON(jsonBytes)
		if err != nil {
			return nil, err
		}
	}

	parsed.GetCommon().SetPresentFields(presentFields)

	// Preserve the original serialized bytes so that downstream consumers
	// (e.g. sortCanonicalSalted) can use them for hash/SHAMap computation
	// without re-encoding, ensuring byte-exact fidelity with the source.
	parsed.SetRawBytes(blob)

	return parsed, nil
}
