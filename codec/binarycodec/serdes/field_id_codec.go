package serdes

import (
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// FieldIDCodec is a struct that represents the field ID codec.
type FieldIDCodec struct {
	definitions *definitions.Definitions
}

// NewFieldIDCodec creates a new FieldIDCodec.
func NewFieldIDCodec(defs *definitions.Definitions) *FieldIDCodec {
	return &FieldIDCodec{definitions: defs}
}

// defaultFieldIDCodec is the canonical, stateless codec bound to the global
// definitions singleton. The codec only reads from its definitions field, so
// sharing one instance across goroutines is safe and avoids per-call allocs
// on the hot serialization path.
var defaultFieldIDCodec = &FieldIDCodec{definitions: definitions.Get()}

// DefaultFieldIDCodec returns a shared FieldIDCodec bound to definitions.Get().
// Call this instead of NewFieldIDCodec(definitions.Get()) on hot paths.
func DefaultFieldIDCodec() *FieldIDCodec {
	return defaultFieldIDCodec
}

// Encode returns the unique field ID for a given field name.
// This field ID consists of the type code and field code, in 1 to 3 bytes
// depending on whether those values are "common" (<16) or "uncommon" (>16).
func (f *FieldIDCodec) Encode(fieldName string) ([]byte, error) {
	fh, err := f.definitions.GetFieldHeaderByFieldName(fieldName)
	if err != nil {
		return nil, err
	}
	var b []byte
	if fh.TypeCode < 16 && fh.FieldCode < 16 {
		return append(b, (byte(fh.TypeCode<<4))|byte(fh.FieldCode)), nil
	}
	if fh.TypeCode >= 16 && fh.FieldCode < 16 {
		return append(b, (byte(fh.FieldCode)), byte(fh.TypeCode)), nil
	}
	if fh.TypeCode < 16 && fh.FieldCode >= 16 {
		return append(b, byte(fh.TypeCode<<4), byte(fh.FieldCode)), nil
	}
	return append(b, 0, byte(fh.TypeCode), byte(fh.FieldCode)), nil
}
