//revive:disable:var-naming
package types

import (
	"fmt"
	"maps"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// maxNestingDepth caps how deeply STObject/STArray containers may nest during
// decoding, mirroring rippled's limit (STVar.cpp:122, STObject.cpp:89). A field
// constructed at depth > maxNestingDepth is rejected.
const maxNestingDepth = 10

// STObject represents a map of serialized field instances, where each key is a field name
// and the associated value is the field's value. This structure allows us to represent nested
// and complex structures of the Ripple protocol.
type STObject struct {
	binarySerializer   *serdes.BinarySerializer
	skipJSONArrayLimit bool
}

// NewSTObject returns a new STObject with the given binary serializer.
func NewSTObject(bs *serdes.BinarySerializer) *STObject {
	return &STObject{binarySerializer: bs}
}

// NewTrustedSTObject returns an STObject for protocol objects constructed by
// trusted internal code. It preserves all structural validation while skipping
// the JSON-input-only array element limit.
func NewTrustedSTObject(bs *serdes.BinarySerializer) *STObject {
	return &STObject{binarySerializer: bs, skipJSONArrayLimit: true}
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

		st := SerializedTypeFor(v.Type)
		setSkipJSONArrayLimit(st, t.skipJSONArrayLimit)
		fieldValue := fimap[v]
		if v.Type == "STObject" && fieldValue == nil {
			fieldValue = map[string]any{}
		}
		if !t.skipJSONArrayLimit {
			if err := checkJSONArraySize(v.FieldName, v.Type, fieldValue); err != nil {
				return nil, err
			}
		}
		b, err := st.FromJSON(fieldValue)
		if err != nil {
			return nil, err
		}
		err = t.binarySerializer.WriteFieldAndValue(v, b)
		if err != nil {
			return nil, err
		}
	}
	return t.binarySerializer.Bytes(), nil
}

// checkJSONArraySize enforces MaxJSONArrayElements on a JSON array field before
// it is serialized, mirroring rippled's per-array-field maxSTParsedJSONArraySize
// cap. Only STArray/Vector256/PathSet field values are JSON arrays; the outer
// PathSet array is capped here, its inner paths in PathSet.FromJSON.
func checkJSONArraySize(fieldName, fieldType string, value any) error {
	switch fieldType {
	case "STArray", "Vector256", "PathSet":
		if n, ok := jsonArrayLen(value); ok && n > MaxJSONArrayElements {
			return &JSONArrayTooLargeError{Field: fieldName}
		}
	}
	return nil
}

// jsonArrayLen reports the length of a JSON array value in any of the shapes the
// codec accepts, or (0, false) if the value is not an array.
func jsonArrayLen(value any) (int, bool) {
	switch v := value.(type) {
	case []any:
		return len(v), true
	case []map[string]any:
		return len(v), true
	case []string:
		return len(v), true
	default:
		return 0, false
	}
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back to a JSON value. It continues parsing until it encounters an object end marker or runs
// out of data; an array end marker inside an object is rejected as malformed nesting. When
// decoded as a nested field, opts[0] carries the container's depth so the nesting cap is
// enforced across the whole tree (see toJSON).
func (t *STObject) ToJSON(p *serdes.BinaryParser, opts ...int) (any, error) {
	depth := 0
	if len(opts) > 0 {
		depth = opts[0]
	}
	m, _, _, err := t.toJSON(p, depth)
	return m, err
}

// ToJSONStrict decodes a top-level object and rejects a stray object end marker
// the way rippled's STTx rejects an "object terminator" (STTx.cpp:104-105).
// rippled enforces this only for transactions — its generic STObject(SerialIter&)
// constructor (STObject.cpp:85-92) discards the flag — but DecodeBytes is the one
// generic decode entrypoint, so the rule is applied to every top-level blob. No
// well-formed serialization carries a top-level terminator, so this never rejects
// valid input. Nested objects consume their own end marker through ToJSON.
func (t *STObject) ToJSONStrict(p *serdes.BinaryParser) (map[string]any, error) {
	m, _, sawEndMarker, err := t.toJSON(p, 0)
	if err != nil {
		return nil, err
	}
	if sawEndMarker {
		return nil, errStrayEndMarker
	}
	return m, nil
}

// toJSON parses fields until an object end marker or end of data. depth is this
// object's nesting level (0 at the top); each field it reads sits one level
// deeper. It reports whether parsing stopped on an object end marker so the
// top-level caller can reject one while nested containers treat it as the normal
// terminator. An array end marker inside an object is malformed
// (STObject.cpp:259-263) and errors.
func (t *STObject) toJSON(p *serdes.BinaryParser, depth int) (map[string]any, []string, bool, error) {
	m := make(map[string]any)
	fieldOrder := make([]string, 0)

	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return nil, nil, false, fmt.Errorf("ReadField error: %w", err)
		}

		if fi.FieldName == "ObjectEndMarker" {
			return m, fieldOrder, true, nil
		}
		if fi.FieldName == "ArrayEndMarker" {
			return nil, nil, false, errIllegalArrayEndMarker
		}

		// Each field is constructed one level deeper than its parent. rippled
		// rejects here, before parsing the field's value, when that level exceeds
		// the cap (STVar.cpp:122) — the guard that prevents unbounded recursion.
		childDepth := depth + 1
		if childDepth > maxNestingDepth {
			return nil, nil, false, errMaxNestingDepth
		}

		// rippled rejects an object that carries the same field twice — a key
		// serialization invariant (STObject.cpp:283-293). A map cannot represent
		// duplicates anyway, so detect it before the second value silently
		// overwrites the first and the re-encoding drops a field.
		if _, dup := m[fi.FieldName]; dup {
			return nil, nil, false, fmt.Errorf("duplicate field detected: %q", fi.FieldName)
		}

		st := SerializedTypeFor(fi.Type)
		if st == nil {
			return nil, nil, false, fmt.Errorf("unknown type %q for field %q", fi.Type, fi.FieldName)
		}

		var res any
		if fi.IsVLEncoded {
			vlen, err := p.ReadVariableLength()
			if err != nil {
				return nil, nil, false, fmt.Errorf("ReadVariableLength error for field %q: %w", fi.FieldName, err)
			}
			res, err = st.ToJSON(p, vlen)
			if err != nil {
				return nil, nil, false, fmt.Errorf("ToJSON error for VL field %q (type=%s, vlen=%d): %w", fi.FieldName, fi.Type, vlen, err)
			}
		} else {
			switch fi.Type {
			case "STObject":
				var childOrder []string
				res, childOrder, _, err = st.(*STObject).toJSON(p, childDepth)
				if err == nil {
					err = validateInnerObject(fi.FieldName, res.(map[string]any), childOrder)
				}
			case "STArray":
				res, err = st.ToJSON(p, childDepth)
			default:
				res, err = st.ToJSON(p)
			}
			if err != nil {
				return nil, nil, false, fmt.Errorf("ToJSON error for field %q (type=%s): %w", fi.FieldName, fi.Type, err)
			}
		}
		res, err = enumToStr(fi.FieldName, res)
		if err != nil {
			return nil, nil, false, err
		}

		res = coerceUInt64BaseTen(fi.Type, fi.FieldName, res)

		m[fi.FieldName] = res
		fieldOrder = append(fieldOrder, fi.FieldName)
	}
	// Running out of data before an object end marker is rippled-faithful:
	// STObject::set loops while the iterator has data and returns the fields it
	// parsed without requiring the 0xE1 terminator (STObject.cpp:243); the
	// nested-object constructor discards the end-of-object flag. sawEndMarker
	// stays false so the top level can still reject a stray terminator.
	return m, fieldOrder, false, nil
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

// createFieldInstanceMapFromJson creates a map of field instances from a JSON object.
// Each key-value pair in the JSON object is converted into a field instance, where the key
// represents the field name and the value is the field's value.
// Special handling for PermissionValue fields: converts string permission names to numeric values.
// Also handles X-addresses by extracting embedded tags.
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
			fi, err := defs.FieldInstanceByName(k)
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
	maps.Copy(processedJSON, json)
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
		fi, err := defs.FieldInstanceByName(k)
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
			if permissionValue, err := definitions.Get().DelegatablePermissionValue(strValue); err == nil {
				return uint32(permissionValue), nil
			}
			// A value with no registered name may be supplied in its decimal form
			// (the round-trip of an unknown sfPermissionValue). Parse it as a plain
			// UINT32 so it re-encodes rather than erroring.
			n, err := strconv.ParseUint(strValue, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("PermissionValue: unknown permission %q", strValue)
			}
			return uint32(n), nil
		}
	}

	// Resolve LedgerEntryType strings to their correct ledger entry type code.
	// UInt16.FromJSON tries transaction types first, which causes collisions for
	// names shared by both maps (e.g., "DepositPreauth" is tx type 19 but ledger
	// entry type 112). By resolving here we guarantee the ledger entry map wins.
	if k == "LedgerEntryType" {
		if strValue, ok := v.(string); ok {
			code, err := definitions.Get().LedgerEntryTypeCode(strValue)
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

// getSortedKeys is a helper function to sort the keys of a map of field instances based on
// their ordinal values. This is used to ensure that the fields are serialized in the
// correct order.
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
	case "TransactionType", "TransactionResult", "LedgerEntryType":
		code, ok := value.(int)
		if !ok {
			return nil, fmt.Errorf("%s: expected int code but got %T", fieldName, value)
		}
		switch fieldName {
		case "TransactionType":
			return definitions.Get().TransactionTypeName(int32(code))
		case "TransactionResult":
			return definitions.Get().TransactionResultName(int32(code))
		default:
			return definitions.Get().LedgerEntryTypeName(int32(code))
		}
	case "PermissionValue":
		code, ok := value.(uint32)
		if !ok {
			return nil, fmt.Errorf("PermissionValue: expected uint32 but got %T", value)
		}
		// Convert the permission value to its name when one is registered.
		if name, err := definitions.Get().DelegatablePermissionName(int32(code)); err == nil {
			return name, nil
		}
		// sfPermissionValue is a plain UINT32; a value with no registered name is
		// still valid on the wire. Emit its decimal form so it round-trips through
		// the string-typed struct field and reaches the delegatability check
		// rather than failing to decode.
		return strconv.FormatUint(uint64(code), 10), nil
	default:
		return value, nil
	}
}
