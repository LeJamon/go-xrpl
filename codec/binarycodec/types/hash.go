//revive:disable:var-naming
package types

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// InvalidHashLengthError struct is used when the hash length does not meet the expected value.
type InvalidHashLengthError struct {
	Expected int
}

// InvalidHashTypeError indicates the provided JSON value is not a valid hash type.
type InvalidHashTypeError struct{}

// InvalidHexStringError indicates an error occurred decoding a hex string.
type InvalidHexStringError struct {
	Err error
}

func (e *InvalidHashLengthError) Error() string {
	return fmt.Sprintf("invalid hash length expected length %v", e.Expected)
}

func (e *InvalidHashTypeError) Error() string {
	return "invalid hash type"
}

func (e *InvalidHexStringError) Error() string {
	return "error decoding hex string: " + e.Err.Error()
}

// hashFromJSON converts a hexadecimal string from JSON to a byte array of the
// given length. It returns an error if the conversion fails or the length of
// the decoded byte array is not as expected.
func hashFromJSON(json any, length int) ([]byte, error) {
	v, ok := json.(string)
	if !ok {
		return nil, &InvalidHashTypeError{}
	}
	if v == "0" {
		return make([]byte, length), nil
	}
	decoded, err := hex.DecodeString(v)
	if err != nil {
		return nil, &InvalidHexStringError{Err: err}
	}
	if length != len(decoded) {
		return nil, &InvalidHashLengthError{Expected: length}
	}
	return decoded, nil
}

// hashToJSON reads length bytes from a BinaryParser and converts them into an
// uppercase hexadecimal string. It returns an error if the read fails.
func hashToJSON(p *serdes.BinaryParser, length int) (any, error) {
	b, err := p.ReadBytes(length)
	if err != nil {
		return nil, err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}
