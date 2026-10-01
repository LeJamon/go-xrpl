package entry

import (
	"fmt"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	codecTypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
)

const (
	innerRequired = schema.InnerRequired
	innerOptional = schema.InnerOptional
	innerDefault  = schema.InnerDefault
)

type innerValueKind = schema.InnerValueKind

const (
	innerAny             = schema.InnerAny
	innerString          = schema.InnerString
	innerUInt8           = schema.InnerUInt8
	innerUInt16          = schema.InnerUInt16
	innerUInt32          = schema.InnerUInt32
	innerUInt64          = schema.InnerUInt64
	innerPermissionValue = schema.InnerPermissionValue
	innerArray           = schema.InnerArray
)

func validateDecodedSTArray(fieldName string, value []any) error {
	return validateTypedSTArray(fieldName, value, false)
}

func validateSTArrayForEncode(fieldName string, value []any) error {
	return validateTypedSTArray(fieldName, value, true)
}

func validateTypedSTArray(fieldName string, value []any, forEncode bool) error {
	wrapper, ok := schema.ArrayElementTemplate(fieldName)
	if !ok {
		return fmt.Errorf("ledgerfields: STArray field %s has no inner-object template", fieldName)
	}
	for i, item := range value {
		element, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("ledgerfields: %s[%d]: expected wrapped STObject, got %T", fieldName, i, item)
		}
		if len(element) != 1 {
			return fmt.Errorf("ledgerfields: %s[%d]: expected exactly one %s wrapper", fieldName, i, wrapper)
		}
		raw, ok := element[wrapper]
		if !ok {
			return fmt.Errorf("ledgerfields: %s[%d]: wrong wrapper, expected %s", fieldName, i, wrapper)
		}
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("ledgerfields: %s[%d].%s: expected STObject, got %T", fieldName, i, wrapper, raw)
		}
		if err := validateInnerObject(wrapper, object, forEncode); err != nil {
			return fmt.Errorf("ledgerfields: %s[%d].%s: %w", fieldName, i, wrapper, err)
		}
	}
	return nil
}

func validateDecodedInnerObject(name string, object map[string]any) error {
	return validateInnerObject(name, object, false)
}

func validateInnerObjectForEncode(name string, object map[string]any) error {
	return validateInnerObject(name, object, true)
}

func validateInnerObject(name string, object map[string]any, forEncode bool) error {
	template, ok := schema.InnerObjectTemplateByName(name)
	if !ok {
		return fmt.Errorf("no template for inner object %s", name)
	}
	for fieldName, value := range object {
		field, ok := template.Fields[fieldName]
		if !ok {
			if forEncode {
				discardable, err := validateDiscardableInnerField(fieldName, value)
				if err != nil {
					return err
				}
				if discardable {
					continue
				}
			}
			return fmt.Errorf("field %s is not allowed", fieldName)
		}
		if !validInnerValue(field.Kind, value, forEncode) {
			return fmt.Errorf("field %s has type %T, want %s", fieldName, value, field.Kind)
		}
		if field.Style == innerDefault && innerValueIsZero(field.Kind, value) {
			return fmt.Errorf("default field %s is explicitly set", fieldName)
		}
	}
	for fieldName, field := range template.Fields {
		if field.Style == innerRequired {
			if _, ok := object[fieldName]; !ok {
				return fmt.Errorf("required field %s is missing", fieldName)
			}
		}
	}
	if authAccounts, ok := object["AuthAccounts"]; ok {
		normalized, ok := normalizeInnerArray(authAccounts)
		if !ok {
			return fmt.Errorf("field AuthAccounts has type %T, want array", authAccounts)
		}
		if err := validateTypedSTArray("AuthAccounts", normalized, forEncode); err != nil {
			return err
		}
	}
	return nil
}

func normalizeInnerArray(value any) ([]any, bool) {
	switch array := value.(type) {
	case []any:
		return array, true
	case []map[string]any:
		normalized := make([]any, len(array))
		for i := range array {
			normalized[i] = array[i]
		}
		return normalized, true
	default:
		return nil, false
	}
}

func validInnerValue(kind innerValueKind, value any, forEncode bool) bool {
	if value == nil {
		return false
	}
	switch kind {
	case innerAny:
		return true
	case innerString:
		_, ok := value.(string)
		return ok
	case innerArray:
		_, ok := normalizeInnerArray(value)
		return ok
	case innerUInt8:
		if !forEncode {
			_, ok := value.(int)
			return ok
		}
		n, ok := encoderUnsigned(value)
		return ok && n <= 1<<8-1
	case innerUInt16:
		if !forEncode {
			_, ok := value.(int)
			return ok
		}
		n, ok := encoderUnsigned(value)
		return ok && n <= 1<<16-1
	case innerUInt32:
		if !forEncode {
			_, ok := value.(uint32)
			return ok
		}
		n, ok := encoderUnsigned(value)
		return ok && n <= 1<<32-1
	case innerUInt64:
		if !forEncode {
			_, ok := value.(string)
			return ok
		}
		return validEncoderUInt64(value)
	case innerPermissionValue:
		if !forEncode {
			_, ok := value.(string)
			return ok
		}
		if name, ok := value.(string); ok {
			if _, err := definitions.Get().DelegatablePermissionValue(name); err == nil {
				return true
			}
			_, err := strconv.ParseUint(name, 10, 32)
			return err == nil
		}
		n, ok := encoderUnsigned(value)
		return ok && n <= math.MaxUint32
	default:
		return false
	}
}

func encoderUnsigned(value any) (uint64, bool) {
	switch n := value.(type) {
	case int:
		return uint64(n), n >= 0
	case int32:
		return uint64(n), n >= 0
	case int64:
		return uint64(n), n >= 0
	case uint8:
		return uint64(n), true
	case uint16:
		return uint64(n), true
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	case float64:
		if n < 0 || n > math.MaxUint32 || n != math.Trunc(n) {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func validEncoderUInt64(value any) bool {
	switch n := value.(type) {
	case string:
		_, err := strconv.ParseUint(n, 16, 64)
		return err == nil
	case float64:
		return n >= 0 && n <= math.MaxUint32 && n == math.Trunc(n)
	case int:
		return n >= 0
	case int64:
		return n >= 0
	case uint32, uint64:
		return true
	default:
		return false
	}
}

func numericUInt64IsZero(value any) bool {
	switch n := value.(type) {
	case float64:
		return n == 0
	case int:
		return n == 0
	case int64:
		return n == 0
	case uint32:
		return n == 0
	case uint64:
		return n == 0
	default:
		return false
	}
}

func innerValueIsZero(kind innerValueKind, value any) bool {
	switch kind {
	case innerUInt8, innerUInt16, innerUInt32, innerPermissionValue:
		n, ok := encoderUnsigned(value)
		return ok && n == 0
	case innerUInt64:
		switch n := value.(type) {
		case string:
			parsed, err := strconv.ParseUint(n, 16, 64)
			return err == nil && parsed == 0
		default:
			return validEncoderUInt64(value) && numericUInt64IsZero(value)
		}
	case innerString:
		s, ok := value.(string)
		return ok && s == ""
	case innerArray:
		array, ok := normalizeInnerArray(value)
		return ok && len(array) == 0
	default:
		return false
	}
}

func validateDiscardableInnerField(name string, value any) (bool, error) {
	field, err := definitions.Get().FieldInstanceByName(name)
	if err != nil || field.IsSerialized || field.Nth <= 256 {
		return false, nil
	}

	fieldType := field.Type
	switch fieldType {
	case "Transaction", "LedgerEntry", "Validation", "Metadata":
		fieldType = "STObject"
	}
	serializedType := codecTypes.SerializedTypeFor(fieldType)
	if serializedType == nil {
		return true, fmt.Errorf("discardable field %s has unsupported type %s", name, field.Type)
	}
	if _, err := serializedType.FromJSON(value); err != nil {
		return true, fmt.Errorf("discardable field %s: %w", name, err)
	}
	return true, nil
}
