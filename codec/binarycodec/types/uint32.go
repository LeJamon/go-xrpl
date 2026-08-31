//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// UInt32 represents a 32-bit unsigned integer.
type UInt32 struct{}

// FromJSON converts a JSON value into a serialized byte slice representing a 32-bit unsigned integer.
// If the value cannot be represented exactly as a UInt32, an error is returned.
func (u *UInt32) FromJSON(value any) ([]byte, error) {
	var val uint64
	switch v := value.(type) {
	case uint32:
		val = uint64(v)
	case int:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt32", v)
		}
		val = uint64(v)
	case int64:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt32", v)
		}
		val = uint64(v)
	case uint64:
		val = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxUint32 || math.Trunc(v) != v {
			return nil, fmt.Errorf("value %v out of range for UInt32", v)
		}
		val = uint64(v)
	default:
		return nil, fmt.Errorf("unsupported type %T for UInt32", value)
	}
	if val > math.MaxUint32 {
		return nil, fmt.Errorf("value %d out of range for UInt32", val)
	}

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(val))
	return out[:], nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON integer value. This method assumes the parser contains data representing
// a 32-bit unsigned integer. If the parsing fails, an error is returned.
func (u *UInt32) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(4)
	if err != nil {
		return nil, err
	}
	return binary.BigEndian.Uint32(b), nil
}
