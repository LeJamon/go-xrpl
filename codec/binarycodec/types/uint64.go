//revive:disable:var-naming
package types

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
)

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
	var buf = new(bytes.Buffer)

	var strVal string
	switch v := value.(type) {
	case string:
		strVal = v
	case float64:
		strVal = fmt.Sprintf("%X", uint64(v))
	case int:
		strVal = fmt.Sprintf("%X", uint64(v))
	case int64:
		strVal = fmt.Sprintf("%X", uint64(v))
	case uint64:
		strVal = fmt.Sprintf("%X", v)
	case uint32:
		strVal = fmt.Sprintf("%X", uint64(v))
	default:
		return nil, ErrInvalidUInt64String
	}

	// Pad the hex string to 16 characters (8 bytes)
	strVal = strings.Repeat("0", 16-len(strVal)) + strVal
	decoded, err := hex.DecodeString(strVal)
	if err != nil {
		return nil, err
	}
	buf.Write(decoded)

	return buf.Bytes(), nil
}

// ToJSON takes a BinaryParser and optional parameters, and converts the serialized byte data
// back into a JSON string value. This method assumes the parser contains data representing
// a 64-bit unsigned integer. If the parsing fails, an error is returned.
// The output is an uppercase hex string zero-padded to 16 characters (matching rippled behavior).
func (u *UInt64) ToJSON(p interfaces.BinaryParser, _ ...int) (any, error) {
	b, err := p.ReadBytes(8)
	if err != nil {
		return nil, err
	}
	// Convert to uppercase hex with zero padding to 16 characters
	hexStr := strings.ToUpper(hex.EncodeToString(b))
	return hexStr, nil
}
