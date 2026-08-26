package tx

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// ParseJSON parses a JSON transaction into the appropriate transaction type.
// Uses the registry-based FromJSON for all registered types, with a fallback
// to BaseTx for unregistered types.
func ParseJSON(data []byte) (Transaction, error) {
	var header struct {
		TransactionType string `json:"TransactionType"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
	}
	tx, err := FromJSON(data)
	if err == ErrUnknownTransactionType {
		// Fallback: parse as generic BaseTx for unregistered types
		txType, knownType := TypeFromName(header.TransactionType)
		presentFields, err := jsonPresentFields(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse transaction fields: %w", err)
		}
		var values map[string]any
		if knownType {
			if err := json.Unmarshal(data, &values); err != nil {
				return nil, fmt.Errorf("failed to parse transaction fields: %w", err)
			}
			if err := checkTemplate(txType, presentFields, values); err != nil {
				return nil, fmt.Errorf("failed to validate transaction template: %w", err)
			}
		}
		var baseTx BaseTx
		if err := json.Unmarshal(data, &baseTx); err != nil {
			return nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
		baseTx.SetPresentFields(presentFields)
		baseTx.txType = txType
		if values == nil {
			if err := json.Unmarshal(data, &values); err != nil {
				return nil, fmt.Errorf("failed to parse transaction fields: %w", err)
			}
		}
		baseTx.setFallbackFields(values)
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

// ParseFromBinary parses a binary transaction blob and retains its canonical encoding.
func ParseFromBinary(blob []byte) (Transaction, error) {
	parsed, decoded, canonical, err := parseFromBinaryUnbound(blob)
	if err != nil {
		return nil, err
	}
	if err := bindCanonicalRawBytes(parsed, decoded, canonical); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseFromBinaryUnbound(blob []byte) (Transaction, map[string]any, []byte, error) {
	const (
		minTransactionBytes = 32
		maxTransactionBytes = 1 << 20
	)
	if len(blob) < minTransactionBytes || len(blob) > maxTransactionBytes {
		return nil, nil, nil, ter.Errorf(ter.TemMALFORMED, "transaction length invalid")
	}

	// Decode the canonical binary directly; the blob is already bytes, so
	// going through a hex string round-trip would only churn allocations on
	// this per-transaction hot path.
	jsonMap, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode binary transaction: %w", err)
	}
	canonical, err := binarycodec.EncodeBytes(jsonMap)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to canonicalize binary transaction: %w", err)
	}

	// Extract present fields from the decoded map
	// This is used to distinguish between absent fields and empty values
	presentFields := make(map[string]bool)
	for key := range jsonMap {
		presentFields[key] = true
	}

	typeName, _ := jsonMap["TransactionType"].(string)
	txType, knownType := TypeFromName(typeName)
	if _, hasTemplate := txTemplates[txType]; !knownType || !hasTemplate {
		return nil, nil, nil, ter.Errorf(ter.TemMALFORMED, "invalid transaction type %q", typeName)
	}
	if err := ValidateTemplateFields(txType, jsonMap); err != nil {
		return nil, nil, nil, ter.Errorf(ter.TemMALFORMED, "%s", err)
	}
	if txType == TypeBatch {
		if reason := batchMapConstructionChecksFailureReason(jsonMap); reason != "" {
			return nil, nil, nil, ter.Errorf(ter.TemMALFORMED, "%s", reason)
		}
	}

	// The TransactionType is already known from the decoded map, so build the
	// concrete transaction and unmarshal in one pass, skipping the redundant
	// TransactionType re-parse FromJSON performs. Unknown or unregistered
	// types fall back to the generic ParseJSON path (yielding a BaseTx).
	var parsed Transaction
	if knownType {
		if t, nerr := NewFromType(txType); nerr == nil {
			parsed = t
		}
	}
	jsonFields := jsonMap
	if parsed != nil {
		jsonFields = binaryTransactionJSONFields(parsed, jsonMap)
		if parsed.TxType() == TypeBatch {
			jsonFields = binaryBatchJSONFields(jsonFields)
		}
	}
	jsonBytes, err := json.Marshal(jsonFields)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal decoded transaction: %w", err)
	}
	if parsed != nil {
		if err := json.Unmarshal(jsonBytes, parsed); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
		restoreBinaryNoCurrency(parsed, jsonMap)
	}
	if parsed == nil {
		parsed, err = ParseJSON(jsonBytes)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	parsed.GetCommon().SetPresentFields(presentFields)

	return parsed, jsonMap, canonical, nil
}

const noCurrencyHex = "0000000000000000000000000000000000000001"

func binaryTransactionJSONFields(transaction Transaction, fields map[string]any) map[string]any {
	value := reflect.ValueOf(transaction)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	var adjusted map[string]any
	for _, field := range getFlattenInfo(value.Type()).fields {
		if field.isAmount {
			amount, ok := fields[field.name].(map[string]any)
			if !ok || amount["currency"] != "1" {
				continue
			}
			if adjusted == nil {
				adjusted = cloneFields(fields)
			}
			adjustedAmount := cloneFields(amount)
			adjustedAmount["currency"] = noCurrencyHex
			adjusted[field.name] = adjustedAmount
			continue
		}
		if field.boolint {
			encoded, ok := fields[field.name].(int)
			if !ok {
				continue
			}
			if adjusted == nil {
				adjusted = cloneFields(fields)
			}
			adjusted[field.name] = encoded != 0
			continue
		}

		fieldType := value.Type().Field(field.index).Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() != reflect.Uint64 {
			continue
		}
		encoded, ok := fields[field.name].(string)
		if !ok {
			continue
		}
		base := 16
		if field.baseTen {
			base = 10
		}
		decoded, err := strconv.ParseUint(encoded, base, 64)
		if err != nil {
			continue
		}
		if adjusted == nil {
			adjusted = cloneFields(fields)
		}
		adjusted[field.name] = decoded
	}
	if adjusted == nil {
		return fields
	}
	return adjusted
}

func cloneFields(fields map[string]any) map[string]any {
	cloned := make(map[string]any, len(fields))
	for name, value := range fields {
		cloned[name] = value
	}
	return cloned
}

func binaryBatchJSONFields(fields map[string]any) map[string]any {
	rawTransactions, ok := fields["RawTransactions"].([]any)
	if !ok {
		return fields
	}
	adjustedTransactions := make([]any, len(rawTransactions))
	copy(adjustedTransactions, rawTransactions)
	changed := false
	for i, raw := range rawTransactions {
		wrapper, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		innerFields, ok := wrapper["RawTransaction"].(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := innerFields["TransactionType"].(string)
		txType, ok := TypeFromName(typeName)
		if !ok {
			continue
		}
		inner, err := NewFromType(txType)
		if err != nil {
			continue
		}
		adjustedInner := binaryTransactionJSONFields(inner, innerFields)
		adjustedWrapper := make(map[string]any, len(wrapper))
		for name, value := range wrapper {
			adjustedWrapper[name] = value
		}
		adjustedWrapper["RawTransaction"] = adjustedInner
		adjustedTransactions[i] = adjustedWrapper
		changed = true
	}
	if !changed {
		return fields
	}
	adjusted := make(map[string]any, len(fields))
	for name, value := range fields {
		adjusted[name] = value
	}
	adjusted["RawTransactions"] = adjustedTransactions
	return adjusted
}

func restoreBinaryNoCurrency(transaction Transaction, fields map[string]any) {
	value := reflect.ValueOf(transaction)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	for _, field := range getFlattenInfo(value.Type()).fields {
		if !field.isAmount {
			continue
		}
		encoded, ok := fields[field.name].(map[string]any)
		if !ok || encoded["currency"] != "1" {
			continue
		}
		amountField := value.Field(field.index)
		if amountField.Kind() == reflect.Pointer {
			if !amountField.IsNil() {
				amountField.Interface().(*Amount).Currency = "1"
			}
			continue
		}
		amountField.Addr().Interface().(*Amount).Currency = "1"
	}
}
