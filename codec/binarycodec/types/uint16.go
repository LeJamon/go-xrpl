//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// UInt16 represents a 16-bit unsigned integer.
type UInt16 struct{}

// FromJSON converts a JSON value into a serialized byte slice representing a 16-bit unsigned integer.
// If the input value is a string, it's assumed to be a transaction type or ledger entry type name, and the
// method will attempt to convert it into a corresponding type code. If the conversion fails, an error is returned.
func (u *UInt16) FromJSON(value any) ([]byte, error) {
	if s, ok := value.(string); ok {
		tc, err := definitions.Get().TransactionTypeCode(s)
		if err != nil {
			tc, err = definitions.Get().LedgerEntryTypeCode(s)
			if err != nil {
				return nil, err
			}
		}
		value = int(tc)
	}

	var intValue uint64
	switch v := value.(type) {
	case int:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt16", v)
		}
		intValue = uint64(v)
	case int64:
		if v < 0 {
			return nil, fmt.Errorf("value %d out of range for UInt16", v)
		}
		intValue = uint64(v)
	case uint16:
		intValue = uint64(v)
	case uint32:
		intValue = uint64(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > math.MaxUint16 || math.Trunc(v) != v {
			return nil, fmt.Errorf("value %v out of range for UInt16", v)
		}
		intValue = uint64(v)
	default:
		return nil, fmt.Errorf("unsupported type %T for UInt16", value)
	}
	if intValue > math.MaxUint16 {
		return nil, fmt.Errorf("value %d out of range for UInt16", intValue)
	}
	var out [2]byte
	binary.BigEndian.PutUint16(out[:], uint16(intValue))
	return out[:], nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON integer value. This method assumes the parser contains data representing
// a 16-bit unsigned integer. If the parsing fails, an error is returned.
func (u *UInt16) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(2)
	if err != nil {
		return nil, err
	}
	return int(binary.BigEndian.Uint16(b)), nil
}
