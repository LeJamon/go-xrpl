//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types/interfaces"
)

// Int32 represents a 32-bit signed integer.
type Int32 struct{}

// ErrInvalidInt32 is returned when a value cannot be converted to Int32.
var ErrInvalidInt32 = errors.New("invalid Int32 value")

// FromJSON serializes a JSON value as a 32-bit signed integer (big-endian). The
// input may be an int, int32, int64, or float64 — encoding/json decodes every
// JSON number as float64. Out-of-range and non-integral values are rejected
// rather than silently truncated.
//
// rippled parses STI_INT32 from a JSON int or an in-range JSON string, bounds-
// checks a uint against the int32 max, and rejects every JSON real with bad_type,
// since Json::Value tags ints and reals distinctly (STParsedJSON.cpp STI_INT32
// case). encoding/json erases that tag — both 740 and 740.0 arrive as
// float64(740) — so this path cannot reproduce rippled's blanket real-rejection;
// it instead rejects the out-of-range and fractional reals rippled also rejects.
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
	case float64:
		if val < math.MinInt32 || val > math.MaxInt32 || val != math.Trunc(val) {
			return nil, ErrInvalidInt32
		}
		v = int32(val)
	default:
		return nil, ErrInvalidInt32
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(v))
	return buf, nil
}

// ToJSON takes a BinaryParser and converts the serialized byte data back to a JSON integer value.
func (i *Int32) ToJSON(p interfaces.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(4)
	if err != nil {
		return nil, err
	}

	v := int32(binary.BigEndian.Uint32(b))
	return int(v), nil
}
