// Package interfaces defines the BinaryParser interface for binary codec parsing operations.
//
//revive:disable:var-naming
package interfaces

import "github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"

// BinaryParser is an interface that defines the methods for a binary parser.
type BinaryParser interface {
	ReadByte() (byte, error)
	ReadField() (*definitions.FieldInstance, error)
	Peek() (byte, error)
	ReadBytes(n int) ([]byte, error)
	HasMore() bool
	ReadVariableLength() (int, error)
}
