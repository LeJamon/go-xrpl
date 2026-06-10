//revive:disable:var-naming
package types

import (
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// Currency handles encoding and decoding of currency values in the binary codec.
type Currency struct{}

// FromJSON parses a JSON value into its binary currency representation.
func (c *Currency) FromJSON(json any) ([]byte, error) {
	str, ok := json.(string)
	if !ok {
		return nil, ErrInvalidCurrency
	}
	return encodeCurrencyCode(str)
}

// ToJSON serializes a binary currency value into a JSON-compatible format.
// A currency is always 160 bits on the wire.
func (c *Currency) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	currencyBytes, err := p.ReadBytes(20)
	if err != nil {
		return nil, err
	}
	return decodeCurrencyCode(currencyBytes)
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
