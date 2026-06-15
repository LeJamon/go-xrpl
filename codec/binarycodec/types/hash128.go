//revive:disable:var-naming
package types

import "github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"

// Hash128 struct represents a 128-bit hash.
type Hash128 struct{}

// NewHash128 is a constructor for creating a new 128-bit hash.
func NewHash128() *Hash128 {
	return &Hash128{}
}

// FromJSON converts a hexadecimal string from JSON to its 16-byte representation.
func (h *Hash128) FromJSON(json any) ([]byte, error) {
	return hashFromJSON(json, 16)
}

// ToJSON reads 16 bytes from a BinaryParser and converts them into an
// uppercase hexadecimal string.
func (h *Hash128) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	return hashToJSON(p, 16)
}
