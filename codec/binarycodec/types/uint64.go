//revive:disable:var-naming
package types

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types/interfaces"
)

// maxJSONUInt is rippled's Json::Value::maxUInt (2^32-1): the largest integer
// rippled's JSON reader accepts as a bare number. A larger literal is rejected
// at parse time ("exceeds the allowable range") before it ever reaches the
// field parser, so large UInt64 values must be supplied as a string — goXRPL
// rejects bare numbers above this bound to match. rippled also stores any number
// written with a '.' or exponent as a double and rejects it for a UInt64 field
// as bad_type; Go's encoding/json collapses every JSON number to float64 and
// loses that literal distinction, so the integrality check below is the closest
// reachable approximation. See rippled json_reader.cpp:586-622, json_value.cpp:37,
// and STParsedJSON.cpp.
const maxJSONUInt = float64(math.MaxUint32)

// UInt64 represents a 64-bit unsigned integer.
type UInt64 struct{}

// ErrInvalidUInt64String is returned when a value is not a valid string representation of a UInt64.
var ErrInvalidUInt64String = errors.New("invalid UInt64 value")

// FromJSON converts a JSON value into a serialized byte slice representing a 64-bit unsigned integer.
// Accepts either a hex string (e.g. "2E4" for 740) or a numeric value (float64, int, int64, uint64).
// Numeric values are common when the RPC layer passes raw JSON where UInt64 fields appear as numbers
// (e.g. AssetPrice in OracleSet transactions).
// If the serialization fails, an error is returned.
func (u *UInt64) FromJSON(value any) ([]byte, error) {
	var n uint64
	switch v := value.(type) {
	case string:
		parsed, err := strconv.ParseUint(v, 16, 64)
		if err != nil {
			return nil, err
		}
		n = parsed
	case float64:
		if v < 0 || v > maxJSONUInt || v != math.Trunc(v) {
			return nil, ErrInvalidUInt64String
		}
		n = uint64(v)
	case int:
		n = uint64(v)
	case int64:
		n = uint64(v)
	case uint64:
		n = v
	case uint32:
		n = uint64(v)
	default:
		return nil, ErrInvalidUInt64String
	}

	var out [8]byte
	binary.BigEndian.PutUint64(out[:], n)
	return out[:], nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON string value. This method assumes the parser contains data representing
// a 64-bit unsigned integer. If the parsing fails, an error is returned.
// The output is a lowercase hex string without leading zeros, matching rippled's
// STUInt64::getJson (std::to_chars with base 16) — see rippled
// src/libxrpl/protocol/STInteger.cpp.
func (u *UInt64) ToJSON(p interfaces.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(8)
	if err != nil {
		return nil, err
	}
	return strconv.FormatUint(binary.BigEndian.Uint64(b), 16), nil
}
