//revive:disable:var-naming
package types

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// UInt8 represents an 8-bit unsigned integer.
type UInt8 struct{}

// FromJSON converts a JSON value into a serialized byte slice representing an 8-bit unsigned integer.
// If the input value is a string, it's assumed to be a transaction result name, and the method will
// attempt to convert it into a transaction result type code. If the conversion fails, an error is returned.
func (u *UInt8) FromJSON(value any) ([]byte, error) {
	if s, ok := value.(string); ok {
		tc, err := definitions.Get().TransactionResultCode(s)
		if err != nil {
			return nil, err
		}
		value = tc
	}

	var intValue uint64
	switch v := value.(type) {
	case int:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt8", v)
		}
		intValue = uint64(v)
	case int32:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt8", v)
		}
		intValue = uint64(v)
	case int64:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt8", v)
		}
		intValue = uint64(v)
	case uint8:
		intValue = uint64(v)
	case uint16:
		intValue = uint64(v)
	case uint32:
		intValue = uint64(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxUint8 || math.Trunc(v) != v {
			return nil, fmt.Errorf("value %v out of range for UInt8", v)
		}
		intValue = uint64(v)
	default:
		return nil, fmt.Errorf("unsupported type %T for UInt8", value)
	}
	if intValue > math.MaxUint8 {
		return nil, fmt.Errorf("value %d out of range for UInt8", intValue)
	}
	return []byte{byte(intValue)}, nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON integer value. This method assumes the parser contains data representing
// an 8-bit unsigned integer. If the parsing fails, an error is returned.
func (u *UInt8) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(1)
	if err != nil {
		return nil, err
	}
	return int(b[0]), nil
}
