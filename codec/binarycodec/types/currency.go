//revive:disable:var-naming
package types

import (
	"bytes"
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
)

var (
	// ErrMissingCurrencyLengthOption is returned when no length option is
	// provided to Currency.ToJSON.
	ErrMissingCurrencyLengthOption = errors.New("missing length option for Currency.ToJSON")
)

// Currency handles encoding and decoding of currency values in the binary codec.
type Currency struct{}

// FromJSON parses a JSON value into its binary currency representation.
func (c *Currency) FromJSON(json any) ([]byte, error) {
	if str, ok := json.(string); ok {
		return c.fromString(str)
	}
	return nil, ErrInvalidCurrency
}

// ToJSON serializes a binary currency value into a JSON-compatible format.
// It requires a length option specifying the byte length to read.
func (c *Currency) ToJSON(p interfaces.BinaryParser, opts ...int) (any, error) {
	// default to 20 bytes, https://xrpl.org/docs/references/protocol/ledger-data/ledger-entry-types/oracle#currency-internal-format
	length := 20
	if len(opts) > 0 && opts[0] > 0 {
		length = opts[0]
	}

	currencyBytes, err := p.ReadBytes(length)
	if err != nil {
		return nil, err
	}

	if bytes.Equal(currencyBytes, XRPBytes) {
		return "XRP", nil
	}

	// Standard 3-char ISO code: bytes 0-11 and 15-19 must be zero,
	// bytes 12-14 must each be non-zero printable.
	if len(currencyBytes) == 20 &&
		isAllZero(currencyBytes[:12]) && isAllZero(currencyBytes[15:]) &&
		currencyBytes[12] != 0 && currencyBytes[13] != 0 && currencyBytes[14] != 0 {
		return string(currencyBytes[12:15]), nil
	}
	return hex.EncodeToString(currencyBytes), nil
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func (c *Currency) fromString(str string) ([]byte, error) {
	if len(str) == 3 {
		var bytes [20]byte
		if str != "XRP" {
			isoBytes := []byte(str)
			copy(bytes[12:], isoBytes)
		}
		return bytes[:], nil
	}

	bytes, err := hex.DecodeString(str)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}
