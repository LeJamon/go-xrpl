// Package types contains data structures for binary codec operations.
// revive:disable:var-naming
package types

import "github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"

// Hash256 struct represents a 256-bit hash.
type Hash256 struct{}

// NewHash256 is a constructor for creating a new 256-bit hash.
func NewHash256() *Hash256 {
	return &Hash256{}
}

// FromJSON converts a hexadecimal string from JSON to its 32-byte representation.
func (h *Hash256) FromJSON(json any) ([]byte, error) {
	return hashFromJSON(json, 32)
}

// ToJSON reads 32 bytes from a BinaryParser and converts them into an
// uppercase hexadecimal string.
func (h *Hash256) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	return hashToJSON(p, 32)
}
