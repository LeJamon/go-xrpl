package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func serializedFieldParseMessage(value any, path string, defs *binarycodecdefs.Definitions) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fieldValue := object[name]
		field, err := defs.FieldInstanceByName(name)
		if err != nil {
			return fmt.Sprintf("Field '%s.%s' is unknown.", path, name)
		}

		fieldPath := path + "." + name
		switch field.Type {
		case "STObject":
			child, ok := parsedInnerObject(fieldValue)
			if !ok {
				return fmt.Sprintf("Field '%s' is not a JSON object.", fieldPath)
			}
			object[name] = child
			if message := serializedFieldParseMessage(child, fieldPath, defs); message != "" {
				return message
			}
			if !binarycodectypes.CanonicalizeInnerObjectTemplate(name, child) {
				return fmt.Sprintf("Object '%s' contents did not meet requirements for that type.", name)
			}
		case "STArray":
			if fieldValue == nil {
				continue
			}
			items, ok := fieldValue.([]any)
			if !ok {
				return fmt.Sprintf("Field '%s' is not a JSON array.", fieldPath)
			}
			for i, item := range items {
				itemObject, ok := item.(map[string]any)
				if !ok || len(itemObject) != 1 {
					return fmt.Sprintf(
						"Field '%s[%d]' must be an object with a single key/object value.",
						fieldPath, i)
				}
				var wrapperName string
				var wrapperValue any
				for wrapperName, wrapperValue = range itemObject {
				}
				wrapperField, err := defs.FieldInstanceByName(wrapperName)
				if err != nil {
					return fmt.Sprintf("Field '%s.%s' is unknown.", fieldPath, wrapperName)
				}
				wrapperObject, ok := parsedInnerObject(wrapperValue)
				if !ok {
					return fmt.Sprintf(
						"Field '%s[%d]' must be an object with a single key/object value.",
						fieldPath, i)
				}
				itemObject[wrapperName] = wrapperObject
				itemPath := fmt.Sprintf("%s.[%d].%s", fieldPath, i, wrapperName)
				if message := serializedFieldParseMessage(wrapperObject, itemPath, defs); message != "" {
					return fmt.Sprintf("Error at '%s'. %s", itemPath, message)
				}
				if wrapperField.Type != "STObject" {
					return fmt.Sprintf(
						"Item '%s' at index %d is not an object.  Arrays may only contain objects.",
						itemPath, i)
				}
				if !binarycodectypes.CanonicalizeInnerObjectTemplate(wrapperName, wrapperObject) {
					return fmt.Sprintf(
						"Error at '%s'. Object '%s' contents did not meet requirements for that type.",
						itemPath, wrapperName)
				}
			}
		default:
			normalized, message := validateSerializedLeaf(name, field.Type, fieldValue, fieldPath, defs)
			if message != "" {
				return message
			}
			object[name] = normalized
		}
	}

	return ""
}

func parsedInnerObject(value any) (map[string]any, bool) {
	if value == nil {
		return map[string]any{}, true
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func validateSerializedLeaf(
	name string,
	fieldType string,
	value any,
	path string,
	defs *binarycodecdefs.Definitions,
) (any, string) {
	switch fieldType {
	case "UInt8":
		return validateUInt8(name, value, path, defs)
	case "UInt16":
		return validateUInt16(name, value, path, defs)
	case "UInt32":
		return validateUInt32(name, value, path, defs)
	case "UInt64":
		return validateUInt64(name, value, path)
	case "Int32":
		return validateInt32(value, path)
	case "Amount":
		switch value.(type) {
		case string, json.Number:
			raw, err := json.Marshal(value)
			if err != nil {
				return value, fmt.Sprintf("Field '%s' has invalid data.", path)
			}
			amount, err := state.AmountFromJSON(raw)
			if err != nil {
				return value, fmt.Sprintf("Field '%s' has invalid data.", path)
			}
			switch {
			case amount.IsNative():
				value = amount.Value()
			case amount.IsMPT():
				value = map[string]any{
					"value":           amount.Value(),
					"mpt_issuance_id": amount.MPTIssuanceID(),
				}
			default:
				value = map[string]any{
					"value":    amount.Value(),
					"currency": amount.Currency,
					"issuer":   amount.Issuer,
				}
			}
		}
	case "Hash128":
		return validateHash(value, path, 16)
	case "Hash160":
		return validateHash(value, path, 20)
	case "Hash192":
		return validateHash(value, path, 24)
	case "Hash256":
		return validateHash(value, path, 32)
	case "Blob":
		return validateBlob(value, path)
	case "AccountID":
		return validateAccount(value, path)
	case "Vector256", "PathSet":
		if value == nil {
			return value, ""
		}
		items, ok := value.([]any)
		if !ok {
			return value, fmt.Sprintf("Field '%s' must be a JSON array.", path)
		}
		if len(items) == 0 {
			return value, ""
		}
	}

	serializedType := binarycodectypes.SerializedTypeFor(fieldType)
	if serializedType == nil {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if _, err := serializedType.FromJSON(value); err != nil {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateUInt8(name string, value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		if name == "TransactionResult" {
			if code, err := defs.TransactionResultCode(text); err == nil {
				return uint8(code), ""
			}
		}
		if !isUnsignedDecimal(text) {
			return value, fmt.Sprintf("Field '%s' has bad type.", path)
		}
		n, err := strconv.ParseUint(text, 10, 8)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint8(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint8 {
		return value, fmt.Sprintf("Field '%s' is out of range.", path)
	}
	return uint8(n), ""
}

func validateUInt16(name string, value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		switch name {
		case "TransactionType":
			if code, err := defs.TransactionTypeCode(text); err == nil {
				return uint16(code), ""
			}
		case "LedgerEntryType":
			if code, err := defs.LedgerEntryTypeCode(text); err == nil {
				return uint16(code), ""
			}
		}
		if text == "" || text[0] < '0' || text[0] > '9' {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		n, err := strconv.ParseUint(text, 10, 16)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint16(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint16 {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return uint16(n), ""
}

func validateUInt32(name string, value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		if name == "PermissionValue" {
			if code, err := defs.DelegatablePermissionValue(text); err == nil {
				return uint32(code), ""
			}
		}
		n, err := strconv.ParseUint(text, 10, 32)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint32(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint32 {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return uint32(n), ""
}

func validateUInt64(name string, value any, path string) (any, string) {
	if text, ok := value.(string); ok {
		base := 16
		if binarycodecdefs.IsBaseTenUInt64FieldName(name) {
			base = 10
		}
		n, err := strconv.ParseUint(text, base, 64)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return n, ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return n, ""
}

func validateInt32(value any, path string) (any, string) {
	if text, ok := value.(string); ok {
		n, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return int32(n), ""
	}

	negative, magnitude, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if (!negative && magnitude > math.MaxInt32) || (negative && magnitude > uint64(math.MaxInt32)+1) {
		return value, fmt.Sprintf("Field '%s' is out of range.", path)
	}
	n := int64(magnitude)
	if negative {
		n = -n
	}
	return int32(n), ""
}

func validateHash(value any, path string, size int) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if text == "" {
		return value, ""
	}
	decoded, err := hex.DecodeString(text)
	if err != nil || len(decoded) != size {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateBlob(value any, path string) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if _, err := hex.DecodeString(text); err != nil {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateAccount(value any, path string) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if len(text) == 40 {
		if decoded, err := hex.DecodeString(text); err == nil && len(decoded) == 20 {
			return value, ""
		}
	}
	if !types.IsValidClassicAddress(text) {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func integerNumericValue(value any) (negative bool, magnitude uint64, ok bool) {
	switch n := value.(type) {
	case json.Number:
		if strings.ContainsAny(n.String(), ".eE") {
			return false, 0, false
		}
		if strings.HasPrefix(n.String(), "-") {
			parsed, err := strconv.ParseInt(n.String(), 10, 32)
			if err != nil {
				return false, 0, false
			}
			return parsed < 0, uint64(-parsed), true
		}
		parsed, err := strconv.ParseUint(n.String(), 10, 32)
		return false, parsed, err == nil
	case int:
		return signedMagnitude(int64(n))
	case int8:
		return signedMagnitude(int64(n))
	case int16:
		return signedMagnitude(int64(n))
	case int32:
		return signedMagnitude(int64(n))
	case int64:
		return signedMagnitude(n)
	case uint:
		return false, uint64(n), true
	case uint8:
		return false, uint64(n), true
	case uint16:
		return false, uint64(n), true
	case uint32:
		return false, uint64(n), true
	case uint64:
		return false, n, true
	}
	return false, 0, false
}

func signedMagnitude(value int64) (negative bool, magnitude uint64, ok bool) {
	if value < 0 {
		return true, uint64(-(value + 1)) + 1, true
	}
	return false, uint64(value), true
}

func isUnsignedDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
