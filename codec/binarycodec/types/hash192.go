// Package types contains data structures for binary codec operations.
//
// revive:disable:var-naming
package types

// Hash192 struct represents a 192-bit hash.
type Hash192 struct {
	hashI
}

// NewHash192 is a constructor for creating a new 192-bit hash.
func NewHash192() *Hash192 {
	return &Hash192{newHash(24)}
}
