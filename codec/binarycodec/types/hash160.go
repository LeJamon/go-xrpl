//revive:disable:var-naming
package types

import "github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"

// Hash160 struct represents a 160-bit hash.
type Hash160 struct{}

// NewHash160 is a constructor for creating a new 160-bit hash.
func NewHash160() *Hash160 {
	return &Hash160{}
}

// FromJSON converts a hexadecimal string from JSON to its 20-byte representation.
func (h *Hash160) FromJSON(json any) ([]byte, error) {
	return hashFromJSON(json, 20)
}

// ToJSON reads 20 bytes from a BinaryParser and converts them into an
// uppercase hexadecimal string.
func (h *Hash160) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	return hashToJSON(p, 20)
}
