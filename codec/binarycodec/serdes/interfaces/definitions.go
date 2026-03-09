// Package interfaces defines interfaces for binary serialization and deserialization of XRPL fields.
//
//revive:disable:var-naming
package interfaces

import "github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"

// Definitions provides methods to look up and create XRPL field definitions by name or header.
type Definitions interface {
	GetFieldNameByFieldHeader(fh definitions.FieldHeader) (string, error)
	GetFieldInstanceByFieldName(fieldName string) (*definitions.FieldInstance, error)
	GetFieldHeaderByFieldName(fieldName string) (*definitions.FieldHeader, error)
	CreateFieldHeader(typecode, fieldcode int32) definitions.FieldHeader
}
