package serdes

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes/interfaces"
)

var (
	// ErrInvalidFieldIDLength is returned when the field ID length is invalid.
	ErrInvalidFieldIDLength = errors.New("invalid field ID length")
)

// FieldIDCodec is a struct that represents the field ID codec.
type FieldIDCodec struct {
	definitions interfaces.Definitions
}

// NewFieldIDCodec creates a new FieldIDCodec.
func NewFieldIDCodec(defs interfaces.Definitions) *FieldIDCodec {
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

// Decode returns the field name represented by the given field ID in hex string form.
func (f *FieldIDCodec) Decode(h string) (string, error) {
	b, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	if len(b) == 1 {
		return f.definitions.GetFieldNameByFieldHeader(f.definitions.CreateFieldHeader(int32(b[0]>>4), int32(b[0]&byte(15))))
	}
	if len(b) == 2 {
		firstByteHighBits := b[0] >> 4
		firstByteLowBits := b[0] & byte(15)
		if firstByteHighBits == 0 {
			return f.definitions.GetFieldNameByFieldHeader(f.definitions.CreateFieldHeader(int32(b[1]), int32(firstByteLowBits)))
		}
		return f.definitions.GetFieldNameByFieldHeader(f.definitions.CreateFieldHeader(int32(firstByteHighBits), int32(b[1])))
	}
	if len(b) == 3 {
		return f.definitions.GetFieldNameByFieldHeader(f.definitions.CreateFieldHeader(int32(b[1]), int32(b[2])))
	}
	return "", ErrInvalidFieldIDLength
}
