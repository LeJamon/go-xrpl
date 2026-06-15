//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// UInt32 represents a 32-bit unsigned integer.
type UInt32 struct{}

// FromJSON converts a JSON value into a serialized byte slice representing a 32-bit unsigned integer.
// The input value is assumed to be an integer. If the serialization fails, an error is returned.
func (u *UInt32) FromJSON(value any) ([]byte, error) {
	var val uint32
	switch v := value.(type) {
	case uint32:
		val = v
	case int:
		val = uint32(v)
	case int64:
		val = uint32(v)
	case uint64:
		val = uint32(v)
	case float64:
		val = uint32(v)
	default:
		return nil, fmt.Errorf("unsupported type %T for UInt32", value)
	}

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], val)
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
