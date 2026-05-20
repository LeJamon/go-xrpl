//revive:disable:var-naming
package types

import (
	"fmt"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
)

// STObject represents a map of serialized field instances, where each key is a field name
// and the associated value is the field's value. This structure allows us to represent nested
// and complex structures of the Ripple protocol.
type STObject struct {
	binarySerializer interfaces.BinarySerializer
}

// NewSTObject returns a new STObject with the given binary serializer.
func NewSTObject(bs interfaces.BinarySerializer) *STObject {
	return &STObject{binarySerializer: bs}
}

// FromJSON converts a JSON object into a serialized byte slice.
// It works by converting the JSON object into a map of field instances (which include the field definition
// and value), and then serializing each field instance.
// This method returns an error if the JSON input is not a valid object.
func (t *STObject) FromJSON(json any) ([]byte, error) {
	jsonMap, ok := json.(map[string]any)
	if !ok {
		return nil, errNotValidJSON
	}
	fimap, err := createFieldInstanceMapFromJson(jsonMap)

	if err != nil {
		return nil, err
	}

	sk := getSortedKeys(fimap)

	for _, v := range sk {
		if !v.IsSerialized {
			continue
		}

		st := GetSerializedType(v.Type)
		b, err := st.FromJSON(fimap[v])
		if err != nil {
			return nil, err
		}
		err = t.binarySerializer.WriteFieldAndValue(v, b)
		if err != nil {
			return nil, err
		}
	}
	return t.binarySerializer.GetSink(), nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back to a JSON value. It will continue parsing until it encounters an end marker for an object
// or an array, or until the parser has no more data.
func (t *STObject) ToJSON(p interfaces.BinaryParser, _ ...int) (any, error) {
	m := make(map[string]any)

	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return nil, fmt.Errorf("ReadField error: %w", err)
		}

		if fi.FieldName == "ObjectEndMarker" || fi.FieldName == "ArrayEndMarker" {
			break
		}

		st := GetSerializedType(fi.Type)
		if st == nil {
			return nil, fmt.Errorf("unknown type %q for field %q", fi.Type, fi.FieldName)
		}

		var res any
		if fi.IsVLEncoded {
			vlen, err := p.ReadVariableLength()
			if err != nil {
				return nil, fmt.Errorf("ReadVariableLength error for field %q: %w", fi.FieldName, err)
			}
			res, err = st.ToJSON(p, vlen)
			if err != nil {
				return nil, fmt.Errorf("ToJSON error for VL field %q (type=%s, vlen=%d): %w", fi.FieldName, fi.Type, vlen, err)
			}
		} else {
			res, err = st.ToJSON(p)
			if err != nil {
				return nil, fmt.Errorf("ToJSON error for field %q (type=%s): %w", fi.FieldName, fi.Type, err)
			}
		}
		res, err = enumToStr(fi.FieldName, res)
		if err != nil {
			return nil, err
		}

		res = coerceUInt64BaseTen(fi.Type, fi.FieldName, res)

		m[fi.FieldName] = res
	}
	return m, nil
}

// coerceUInt64BaseTen converts the lowercase-hex string produced by
// UInt64.ToJSON into the decimal string rippled emits for SFields flagged
// sMD_BaseTen — see rippled src/libxrpl/protocol/STInteger.cpp:246 and the
// rippled SField definitions in include/xrpl/protocol/detail/sfields.macro.
// A no-op for any other field/type combination.
func coerceUInt64BaseTen(fieldType, fieldName string, value any) any {
	if fieldType != "UInt64" {
		return value
	}
	if !definitions.IsBaseTenUInt64FieldName(fieldName) {
		return value
	}
	s, ok := value.(string)
	if !ok {
		return value
	}
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return value
	}
	return strconv.FormatUint(n, 10)
}

// nolint
// createFieldInstanceMapFromJson creates a map of field instances from a JSON object.
// Each key-value pair in the JSON object is converted into a field instance, where the key
// represents the field name and the value is the field's value.
// Special handling for PermissionValue fields: converts string permission names to numeric values.
// Also handles X-addresses by extracting embedded tags.
//
//lint:ignore U1000 // ignore this for now
func createFieldInstanceMapFromJson(json map[string]any) (map[definitions.FieldInstance]any, error) {
	// Fast path: no key holds an X-address — populate the field-instance map
	// directly from the caller's map without a defensive copy. Writes go only
	// to the fresh m; the caller's json is not mutated. Inner objects/arrays
	// are aliased into m by reference, but downstream callers (STObject /
	// STArray nested serialisation) do not mutate them either.
	hasX := false
	for _, v := range json {
		if s, ok := v.(string); ok && addresscodec.IsValidXAddress(s) {
			hasX = true
			break
		}
	}

	defs := definitions.Get()
	if !hasX {
		m := make(map[definitions.FieldInstance]any, len(json))
		for k, v := range json {
			fi, err := defs.GetFieldInstanceByFieldName(k)
			if err != nil {
				return nil, err
			}
			v, err = parseSpecialFields(k, v)
			if err != nil {
				return nil, err
			}
			m[*fi] = v
		}
		return m, nil
	}

	// Slow path: at least one X-address present. Copy, then resolve X-addresses
	// into classic addresses + tag siblings before building the field map.
	processedJSON := make(map[string]any, len(json))
	for k, v := range json {
		processedJSON[k] = v
	}
	for k, v := range json {
		strVal, ok := v.(string)
		if !ok || !addresscodec.IsValidXAddress(strVal) {
			continue
		}
		classicAddr, tag, _, err := addresscodec.XAddressToClassicAddress(strVal)
		if err != nil {
			return nil, fmt.Errorf("failed to decode X-address for field %s: %w", k, err)
		}
		processedJSON[k] = classicAddr
		if tag != 0 {
			var tagFieldName string
			switch k {
			case "Destination":
				tagFieldName = "DestinationTag"
			case "Account":
				tagFieldName = "SourceTag"
			default:
				return nil, fmt.Errorf("%s cannot have an associated tag", k)
			}
			if existingTag, exists := processedJSON[tagFieldName]; exists {
				if existingTag != tag {
					return nil, fmt.Errorf("duplicate %s: X-address tag (%d) does not match existing tag (%v)", tagFieldName, tag, existingTag)
				}
			}
			processedJSON[tagFieldName] = tag
		}
	}

	m := make(map[definitions.FieldInstance]any, len(processedJSON))
	for k, v := range processedJSON {
		fi, err := defs.GetFieldInstanceByFieldName(k)
		if err != nil {
			return nil, err
		}
		v, err = parseSpecialFields(k, v)
		if err != nil {
			return nil, err
		}
		m[*fi] = v
	}
	return m, nil
}

// parseSpecialFields is a helper function that handles special fields that need type parsing.
func parseSpecialFields(k string, v any) (any, error) {
	if k == "PermissionValue" {
		if strValue, ok := v.(string); ok {
			permissionValue, err := definitions.Get().GetDelegatablePermissionValueByName(strValue)
			if err != nil {
				return nil, err
			}
			//nolint:gosec // G115: Potential hardcoded credentials (gosec)
			return uint32(permissionValue), nil
		}
	}

	// Resolve LedgerEntryType strings to their correct ledger entry type code.
	// UInt16.FromJSON tries transaction types first, which causes collisions for
	// names shared by both maps (e.g., "DepositPreauth" is tx type 19 but ledger
	// entry type 112). By resolving here we guarantee the ledger entry map wins.
	if k == "LedgerEntryType" {
		if strValue, ok := v.(string); ok {
			code, err := definitions.Get().GetLedgerEntryTypeCodeByLedgerEntryTypeName(strValue)
			if err != nil {
				return nil, err
			}
			return int(code), nil
		}
	}

	// For UInt64 SFields rippled emits as decimal (sMD_BaseTen — sfMPTAmount,
	// sfMaximumAmount, sfOutstandingAmount, sfLockedAmount), accept the decimal
	// string and hand UInt64.FromJSON a numeric value so it skips its default
	// base-16 string parse. See rippled STParsedJSON.cpp:441-449.
	if definitions.IsBaseTenUInt64FieldName(k) {
		if strValue, ok := v.(string); ok {
			n, err := strconv.ParseUint(strValue, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			return n, nil
		}
	}

	return v, nil
}

// nolint
//
// getSortedKeys is a helper function to sort the keys of a map of field instances based on
// their ordinal values. This is used to ensure that the fields are serialized in the
// correct order.
//
//lint:ignore U1000 // ignore this for now
func getSortedKeys(m map[definitions.FieldInstance]any) []definitions.FieldInstance {
	keys := make([]definitions.FieldInstance, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}

	sort.SliceStable(keys, func(i, j int) bool {
		return keys[i].Ordinal < keys[j].Ordinal
	})
	return keys
}

// enumToStr is a helper function that takes a field name and its associated value,
// and returns a string representation of the value if the field is an enumerated type
// (i.e., TransactionType, TransactionResult, LedgerEntryType, PermissionValue).
// If the field is not an enumerated type, the original value is returned.
func enumToStr(fieldName string, value any) (any, error) {
	switch fieldName {
	case "TransactionType":
		// TODO: Check if this is still needed
		//nolint:gosec // G115: Potential hardcoded credentials (gosec)
		return definitions.Get().GetTransactionTypeNameByTransactionTypeCode(int32(value.(int)))
	case "TransactionResult":
		// TODO: Check if this is still needed
		//nolint:gosec // G115: Potential hardcoded credentials (gosec)
		return definitions.Get().GetTransactionResultNameByTransactionResultTypeCode(int32(value.(int)))
	case "LedgerEntryType":
		// TODO: Check if this is still needed
		//nolint:gosec // G115: Potential hardcoded credentials (gosec)
		return definitions.Get().GetLedgerEntryTypeNameByLedgerEntryTypeCode(int32(value.(int)))
	case "PermissionValue":
		// Convert permission value to permission name if available, otherwise return numeric value
		//nolint:gosec // G115: Potential hardcoded credentials (gosec)
		if name, err := definitions.Get().GetDelegatablePermissionNameByValue(int32(value.(uint32))); err == nil {
			return name, nil
		}
		return value, nil
	default:
		return value, nil
	}
}
