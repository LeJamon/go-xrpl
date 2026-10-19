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
	parsed, canonical, err := parseFromBinaryUnbound(blob)
	if err != nil {
		return nil, err
	}
	if err := bindCanonicalRawBytes(parsed, canonical); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseFromBinaryUnbound(blob []byte) (Transaction, []byte, error) {
	if err := checkTransactionLength(blob); err != nil {
		return nil, nil, err
	}

	// Decode the canonical binary directly; the blob is already bytes, so
	// going through a hex string round-trip would only churn allocations on
	// this per-transaction hot path.
	jsonMap, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode binary transaction: %w", err)
	}
	canonical, err := binarycodec.EncodeBytes(jsonMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to canonicalize binary transaction: %w", err)
	}

	parsed, err := transactionFromFields(jsonMap)
	return parsed, canonical, err
}

// ParseFromDecodedFields constructs an owned transaction from fields returned by
// the binary decoder, retaining their canonical encoding and field identity.
func ParseFromDecodedFields(fields map[string]any) (Transaction, error) {
	canonical, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize binary transaction: %w", err)
	}
	if err := checkTransactionLength(canonical); err != nil {
		return nil, err
	}
	parsed, err := transactionFromFields(fields)
	if err != nil {
		return nil, err
	}
	if err := bindCanonicalRawBytes(parsed, canonical); err != nil {
		return nil, err
	}
	return parsed, nil
}

func checkTransactionLength(blob []byte) error {
	if len(blob) < 32 || len(blob) > 1<<20 {
		return ter.Errorf(ter.TemMALFORMED, "transaction length invalid")
	}
	return nil
}

func validateTransactionFields(fields map[string]any) error {
	typeName, _ := fields["TransactionType"].(string)
	txType, knownType := TypeFromName(typeName)
	if _, hasTemplate := txTemplates[txType]; !knownType || !hasTemplate {
		return ter.Errorf(ter.TemMALFORMED, "invalid transaction type %q", typeName)
	}
	if err := ValidateTemplateFields(txType, fields); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "%s", err)
	}
	return nil
}

type binaryFieldsUnmarshaler interface {
	UnmarshalBinaryFields(map[string]any) error
}

func transactionFromFields(fields map[string]any) (Transaction, error) {
	if err := validateTransactionFields(fields); err != nil {
		return nil, err
	}
	typeName, _ := fields["TransactionType"].(string)
	txType, _ := TypeFromName(typeName)
	if txType == TypeBatch {
		// Structural checks normalize inner type names. Keep caller maps owned
		// by their caller, including when construction fails.
		fields = cloneTransactionFieldValue(fields).(map[string]any)
		if reason := batchMapConstructionChecksFailureReason(fields); reason != "" {
			return nil, ter.Errorf(ter.TemMALFORMED, "%s", reason)
		}
	}

	parsed, _ := NewFromType(txType)
	jsonFields := fields
	if parsed != nil {
		jsonFields = binaryTransactionJSONFields(parsed, fields)
	}
	if decoder, ok := parsed.(binaryFieldsUnmarshaler); ok {
		if err := decoder.UnmarshalBinaryFields(jsonFields); err != nil {
			return nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
	} else {
		jsonBytes, err := json.Marshal(jsonFields)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal decoded transaction: %w", err)
		}
		if parsed == nil {
			parsed, err = ParseJSON(jsonBytes)
		} else {
			err = json.Unmarshal(jsonBytes, parsed)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse transaction: %w", err)
		}
	}
	restoreBinaryNoCurrency(parsed, fields)
	presentFields := make(map[string]bool, len(fields))
	for name := range fields {
		presentFields[name] = true
	}
	parsed.GetCommon().SetPresentFields(presentFields)
	return parsed, nil
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

func batchInnerObject(wrapper map[string]any) (string, map[string]any) {
	if len(wrapper) == 1 {
		for name, value := range wrapper {
			inner, _ := value.(map[string]any)
			return name, inner
		}
	}
	return "", nil
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
