//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"encoding/json"
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
// The input value may be a signed decimal string or an integral JSON number.
func (i *Int32) FromJSON(value any) ([]byte, error) {
	v, ok := int32Value(value)
	if !ok {
		return nil, ErrInvalidInt32
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(v))
	return buf, nil
}

func int32Value(value any) (int32, bool) {
	var n int64
	switch val := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			return 0, false
		}
		n = parsed
	case json.Number:
		parsed, err := strconv.ParseInt(string(val), 10, 32)
		if err != nil {
			return 0, false
		}
		n = parsed
	case int:
		n = int64(val)
	case int8:
		n = int64(val)
	case int16:
		n = int64(val)
	case int32:
		n = int64(val)
	case int64:
		n = val
	case uint:
		if uint64(val) > math.MaxInt32 {
			return 0, false
		}
		n = int64(val)
	case uint8:
		n = int64(val)
	case uint16:
		n = int64(val)
	case uint32:
		n = int64(val)
	case uint64:
		if val > math.MaxInt32 {
			return 0, false
		}
		n = int64(val)
	case float32:
		f := float64(val)
		if math.Trunc(f) != f || f < math.MinInt32 || f > math.MaxInt32 {
			return 0, false
		}
		n = int64(f)
	case float64:
		if math.Trunc(val) != val || val < math.MinInt32 || val > math.MaxInt32 {
			return 0, false
		}
		n = int64(val)
	default:
		return 0, false
	}
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, false
	}
	return int32(n), true
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
