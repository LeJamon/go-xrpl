// Package interfaces defines the BinarySerializer interface for binary codec serialization operations.
//
//revive:disable:var-naming
package interfaces

import "github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"

// BinarySerializer is an interface that defines the methods for a binary serializer.
type BinarySerializer interface {
	WriteFieldAndValue(fieldInstance definitions.FieldInstance, value []byte) error
	GetSink() []byte
}
