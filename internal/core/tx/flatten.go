package tx

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// flattenField holds pre-computed metadata for a single struct field.
type flattenField struct {
	index     int
	name      string
	omitempty bool
	isAmount  bool // use flattenAmount converter
	isAsset   bool // use flattenAsset converter for Asset/Issue fields
	boolint   bool // convert bool to int (0 or 1)
}

// flattenInfo holds cached flatten metadata for a struct type.
type flattenInfo struct {
	fields []flattenField
}

// flattenCache stores pre-computed flattenInfo per type to avoid repeated reflection.
var flattenCache sync.Map // map[reflect.Type]*flattenInfo

// parseXRPLTag parses an xrpl struct tag.
// Format: "FieldName,opt1,opt2" where options are: omitempty, amount, asset, boolint
// Returns ("", false) for tags that should be skipped ("-").
func parseXRPLTag(tag string) (name string, omitempty bool, isAmount bool, isAsset bool, boolint bool, skip bool) {
	if tag == "" || tag == "-" {
		return "", false, false, false, false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty":
			omitempty = true
		case "amount":
			isAmount = true
		case "asset":
			isAsset = true
		case "boolint":
			boolint = true
		}
	}
	return name, omitempty, isAmount, isAsset, boolint, false
}

// getFlattenInfo returns the cached flattenInfo for a struct type.
// On first access, it computes and caches the info.
func getFlattenInfo(t reflect.Type) *flattenInfo {
	if cached, ok := flattenCache.Load(t); ok {
		return cached.(*flattenInfo)
	}

	info := &flattenInfo{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip embedded fields (like BaseTx)
		if field.Anonymous {
			continue
		}

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("xrpl")
		name, omitempty, isAmount, isAsset, boolint, skip := parseXRPLTag(tag)
		if skip {
			continue
		}

		info.fields = append(info.fields, flattenField{
			index:     i,
			name:      name,
			omitempty: omitempty,
			isAmount:  isAmount,
			isAsset:   isAsset,
			boolint:   boolint,
		})
	}

	flattenCache.Store(t, info)
	return info
}

// isEmptyValue returns true if the reflect.Value should be considered empty for omitempty.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.String:
		return v.String() == ""
	case reflect.Slice, reflect.Array:
		return v.Len() == 0
	case reflect.Map:
		return v.IsNil() || v.Len() == 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Struct:
		// For Amount structs, check if Value is empty
		if v.Type() == reflect.TypeOf(Amount{}) {
			valueField := v.FieldByName("Value")
			return valueField.String() == ""
		}
		return false
	default:
		return false
	}
}

// ReflectFlatten generates a flat map from a Transaction using struct tags.
// It starts with Common.ToMap() and adds type-specific fields based on xrpl tags.
func ReflectFlatten(tx Transaction) (map[string]any, error) {
	m := tx.GetCommon().ToMap()

	v := reflect.ValueOf(tx)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	info := getFlattenInfo(v.Type())

	for _, f := range info.fields {
		val := v.Field(f.index)

		if f.omitempty && isEmptyValue(val) {
			continue
		}

		if f.isAmount {
			// Handle Amount and *Amount
			var amt Amount
			if val.Kind() == reflect.Ptr {
				if val.IsNil() {
					continue
				}
				amt = val.Elem().Interface().(Amount)
			} else {
				amt = val.Interface().(Amount)
			}
			m[f.name] = flattenAmount(amt)
		} else if f.isAsset {
			// Handle Asset and *Asset (Issue objects for AMM)
			var asset Asset
			if val.Kind() == reflect.Ptr {
				if val.IsNil() {
					continue
				}
				asset = val.Elem().Interface().(Asset)
			} else {
				asset = val.Interface().(Asset)
			}
			m[f.name] = flattenAsset(asset)
		} else if f.boolint {
			// Convert bool to int (0 or 1) for XRPL protocol
			if val.Bool() {
				m[f.name] = 1
			} else {
				m[f.name] = 0
			}
		} else {
			// Default: dereference pointers and convert struct slices
			if val.Kind() == reflect.Ptr {
				if val.IsNil() {
					continue
				}
				val = val.Elem()
			}

			// Handle struct slices (like AuthAccounts) - convert to []map[string]any for STArray
			if val.Kind() == reflect.Slice && val.Len() > 0 {
				elemKind := val.Type().Elem().Kind()
				if elemKind == reflect.Struct || (elemKind == reflect.Ptr && val.Type().Elem().Elem().Kind() == reflect.Struct) {
					m[f.name] = flattenStructSlice(val)
					continue
				}
			}

			// UInt64 fields must be serialized as uppercase hex strings for the binary codec.
			// The XRPL binary codec's UInt64.FromJSON expects a hex string representation.
			if val.Kind() == reflect.Uint64 {
				m[f.name] = fmt.Sprintf("%X", val.Uint())
			} else {
				m[f.name] = val.Interface()
			}
		}
	}

	return m, nil
}

// flattenAmount converts an Amount to its serializable form.
// Native XRP amounts are returned as a string value.
// Issued currency amounts are returned as a map with value/currency/issuer.
// Note: The binary codec expects map[string]any, not map[string]string.
func flattenAmount(a Amount) any {
	if a.IsNative() {
		return a.Value()
	}
	return map[string]any{
		"value":    a.Value(),
		"currency": a.Currency,
		"issuer":   a.Issuer,
	}
}

// flattenAsset converts an Asset to its serializable Issue form.
// The binary codec expects map[string]any with "currency" and optionally "issuer" keys.
// XRP assets have only "currency": "XRP" (no issuer).
// IOU assets have "currency" and "issuer" keys.
// Reference: rippled Issue type serialization
func flattenAsset(a Asset) map[string]any {
	// XRP is represented by currency "XRP" with no issuer
	if a.Currency == "" || a.Currency == "XRP" {
		return map[string]any{
			"currency": "XRP",
		}
	}
	// IOU assets have both currency and issuer
	return map[string]any{
		"currency": a.Currency,
		"issuer":   a.Issuer,
	}
}

// flattenStructSlice converts a slice of structs to []map[string]any for STArray serialization.
// This is needed because the binary codec expects arrays to contain maps, not Go structs.
// It uses JSON tags to determine field names in the map.
func flattenStructSlice(v reflect.Value) []map[string]any {
	if v.Kind() != reflect.Slice {
		return nil
	}
	result := make([]map[string]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		elem := v.Index(i)
		result[i] = structToMap(elem)
	}
	return result
}

// structToMap converts a struct to map[string]any using JSON tags for field names.
func structToMap(v reflect.Value) map[string]any {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}

	result := make(map[string]any)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		// Get field name and options from JSON tag, fallback to struct field name
		jsonTag := field.Tag.Get("json")
		name := field.Name
		omitempty := false
		if jsonTag != "" && jsonTag != "-" {
			parts := strings.Split(jsonTag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					omitempty = true
				}
			}
		}

		fv := v.Field(i)

		// Skip empty values when omitempty is set
		if omitempty && isEmptyValue(fv) {
			continue
		}

		// Recursively convert nested structs
		if fv.Kind() == reflect.Struct {
			result[name] = structToMap(fv)
		} else if fv.Kind() == reflect.Ptr && !fv.IsNil() && fv.Elem().Kind() == reflect.Struct {
			result[name] = structToMap(fv.Elem())
		} else if fv.Kind() == reflect.Slice && fv.Len() > 0 && fv.Index(0).Kind() == reflect.Struct {
			result[name] = flattenStructSlice(fv)
		} else if fv.Kind() == reflect.Ptr && !fv.IsNil() {
			// Handle pointer types — dereference and apply type-specific conversions
			elem := fv.Elem()
			switch elem.Kind() {
			case reflect.Uint64:
				// UInt64 fields must be hex strings for the binary codec
				result[name] = fmt.Sprintf("%X", elem.Uint())
			default:
				result[name] = elem.Interface()
			}
		} else if fv.Kind() == reflect.Uint64 {
			// UInt64 fields must be hex strings for the binary codec
			result[name] = fmt.Sprintf("%X", fv.Uint())
		} else {
			result[name] = fv.Interface()
		}
	}
	return result
}
