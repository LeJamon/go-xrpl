package types

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
)

var (
	// ErrInvalidJSONNumber is returned when a json.Number cannot be parsed to an integer.
	ErrInvalidJSONNumber = errors.New("invalid json.Number")
	// ErrUnsupportedPermissionType is returned when the JSON type is unsupported for PermissionValue.
	ErrUnsupportedPermissionType = errors.New("unsupported JSON type for PermissionValue")
	// ErrPermissionValueOutOfRange is returned when the permission value is outside the uint32 range.
	ErrPermissionValueOutOfRange = errors.New("permission value out of uint32 range")
)

// PermissionValue represents a 32-bit unsigned integer permission value.
type PermissionValue struct{}

// FromJSON converts a JSON value into a serialized byte slice representing a 32-bit unsigned integer permission value.
// If the input value is a string, it's assumed to be a permission name, and the method will
// attempt to convert it into a corresponding permission value. If the conversion fails, an error is returned.
func (p *PermissionValue) FromJSON(value any) ([]byte, error) {
	if s, ok := value.(string); ok {
		pv, err := definitions.Get().GetDelegatablePermissionValueByName(s)
		if err != nil {
			return nil, err
		}
		value = pv
	}

	var ui64 uint64
	switch v := value.(type) {
	case int:
		if v < 0 {
			return nil, ErrPermissionValueOutOfRange
		}
		ui64 = uint64(v)
	case int32:
		if v < 0 {
			return nil, ErrPermissionValueOutOfRange
		}
		ui64 = uint64(v)
	case int64:
		if v < 0 {
			return nil, ErrPermissionValueOutOfRange
		}
		ui64 = uint64(v)
	case uint32:
		ui64 = uint64(v)
	case float64:
		if v < 0 || v > float64(math.MaxUint32) {
			return nil, ErrPermissionValueOutOfRange
		}
		ui64 = uint64(v)
	case json.Number:
		num, err := v.Int64()
		if err != nil || num < 0 {
			return nil, ErrInvalidJSONNumber
		}
		ui64 = uint64(num)
	default:
		return nil, ErrUnsupportedPermissionType
	}

	if ui64 > math.MaxUint32 {
		return nil, ErrPermissionValueOutOfRange
	}

	// Now safe to cast
	ui32 := uint32(ui64)
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, ui32)
	return buf, nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON value. If a permission name is found for the value, it returns the name;
// otherwise, it returns the numeric value. If the parsing fails, an error is returned.
func (p *PermissionValue) ToJSON(parser interfaces.BinaryParser, _ ...int) (any, error) {
	b, err := parser.ReadBytes(4)
	if err != nil {
		return nil, err
	}

	permissionValue := binary.BigEndian.Uint32(b)

	// #nosec G115
	if name, err := definitions.Get().GetDelegatablePermissionNameByValue(int32(permissionValue)); err == nil {
		return name, nil
	}

	return permissionValue, nil
}
