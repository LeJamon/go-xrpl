//revive:disable:var-naming
package types

import "github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"

// Hash192 struct represents a 192-bit hash.
type Hash192 struct{}

// NewHash192 is a constructor for creating a new 192-bit hash.
func NewHash192() *Hash192 {
	return &Hash192{}
}

// FromJSON converts a hexadecimal string from JSON to its 24-byte representation.
func (h *Hash192) FromJSON(json any) ([]byte, error) {
	return hashFromJSON(json, 24)
}

// ToJSON reads 24 bytes from a BinaryParser and converts them into an
// uppercase hexadecimal string.
func (h *Hash192) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	return hashToJSON(p, 24)
}
