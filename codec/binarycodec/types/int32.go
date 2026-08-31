//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// Int32 represents a 32-bit signed integer.
type Int32 struct{}

// ErrInvalidInt32 is returned when a value cannot be converted to Int32.
var ErrInvalidInt32 = errors.New("invalid Int32 value")

// FromJSON converts a JSON value into a serialized byte slice representing a 32-bit signed integer.
// The input value can be an int, int32, int64, uint, uint32, uint64, float64, or decimal string.
func (i *Int32) FromJSON(value any) ([]byte, error) {
	var v int32

	switch val := value.(type) {
	case int:
		if val < math.MinInt32 || val > math.MaxInt32 {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case int32:
		v = val
	case int64:
		if val < math.MinInt32 || val > math.MaxInt32 {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case uint:
		if val > math.MaxInt32 {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case uint32:
		if val > math.MaxInt32 {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case uint64:
		if val > math.MaxInt32 {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case float64:
		if val < math.MinInt32 || val > math.MaxInt32 || val != math.Trunc(val) {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	case string:
		parsed, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return nil, ErrInvalidInt32
		}
		v = int32(parsed)
	default:
		return nil, ErrInvalidInt32
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(v))
	return buf, nil
}

// ToJSON takes a BinaryParser and converts the serialized byte data back to a JSON integer value.
func (i *Int32) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(4)
	if err != nil {
		return nil, err
	}

	v := int32(binary.BigEndian.Uint32(b))
	return int(v), nil
}
