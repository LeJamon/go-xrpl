package host

import (
	"reflect"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// XRPL serialized-type ids (a subset; protocol constants).
const (
	stiUInt16    = 1
	stiUInt32    = 2
	stiUInt64    = 3
	stiAmount    = 6
	stiVL        = 7
	stiAccount   = 8
	stiInt32     = 10
	stiInt64     = 11
	stiObject    = 14
	stiArray     = 15
	stiVector256 = 19
)

// splitCode splits a field code (typeCode<<16 | fieldCode) into its parts.
func splitCode(code int32) (typeCode, fieldCode int32) {
	return code >> 16, code & 0xFFFF
}

// knownField reports whether (typeCode, fieldCode) names a defined field.
func knownField(typeCode, fieldCode int32) bool {
	_, err := definitions.Get().GetFieldNameByFieldHeader(
		definitions.FieldHeader{TypeCode: typeCode, FieldCode: fieldCode})
	return err == nil
}

// fieldReader walks a serialized object's fields and returns the value of the
// field identified by code, formatted exactly as rippled's getAnyFieldData:
// raw value (no length prefix) for accounts/blobs, little-endian for integers,
// raw wire bytes for everything else; arrays/objects are not leaf fields.
func fieldReader(objBytes []byte, code int32) ([]byte, wasm.HostFunctionError) {
	tc, fc := splitCode(code)
	if !knownField(tc, fc) {
		return nil, wasm.HfInvalidField
	}
	p := serdes.NewBinaryParser(objBytes, definitions.Get())
	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return nil, wasm.HfDecoding
		}
		if fi.FieldHeader.TypeCode == tc && fi.FieldHeader.FieldCode == fc {
			return extractValue(p, objBytes, fi)
		}
		if herr := skipValue(p, fi); herr != wasm.HfSuccess {
			return nil, herr
		}
	}
	return nil, wasm.HfFieldNotFound
}

func extractValue(p *serdes.BinaryParser, objBytes []byte, fi *definitions.FieldInstance) ([]byte, wasm.HostFunctionError) {
	switch fi.FieldHeader.TypeCode {
	case stiObject, stiArray, stiVector256:
		return nil, wasm.HfNotLeafField
	case stiAccount, stiVL:
		// Variable-length: return the raw value, without the length prefix.
		vlen, err := p.ReadVariableLength()
		if err != nil {
			return nil, wasm.HfDecoding
		}
		b, err := p.ReadBytes(vlen)
		if err != nil {
			return nil, wasm.HfDecoding
		}
		return append([]byte(nil), b...), wasm.HfSuccess
	case stiUInt16, stiUInt32, stiUInt64, stiInt32, stiInt64:
		raw, herr := consumeRaw(p, objBytes, fi)
		if herr != wasm.HfSuccess {
			return nil, herr
		}
		return reverse(raw), wasm.HfSuccess
	default:
		// Hash*, Amount, Number, Issue, ...: the wire value bytes as-is.
		return consumeRaw(p, objBytes, fi)
	}
}

// consumeRaw advances the parser past a field's value and returns the exact
// wire bytes it spanned, using the remaining-byte count as a cursor.
func consumeRaw(p *serdes.BinaryParser, objBytes []byte, fi *definitions.FieldInstance) ([]byte, wasm.HostFunctionError) {
	off0 := len(objBytes) - p.Remaining()
	if herr := skipValue(p, fi); herr != wasm.HfSuccess {
		return nil, herr
	}
	off1 := len(objBytes) - p.Remaining()
	if off0 < 0 || off1 > len(objBytes) || off0 > off1 {
		return nil, wasm.HfDecoding
	}
	return append([]byte(nil), objBytes[off0:off1]...), wasm.HfSuccess
}

// skipValue consumes a field's value through its type handler.
func skipValue(p *serdes.BinaryParser, fi *definitions.FieldInstance) wasm.HostFunctionError {
	st := types.GetSerializedType(fi.Type)
	if st == nil {
		return wasm.HfDecoding
	}
	if fi.IsVLEncoded {
		vlen, err := p.ReadVariableLength()
		if err != nil {
			return wasm.HfDecoding
		}
		if _, err := st.ToJSON(p, vlen); err != nil {
			return wasm.HfDecoding
		}
		return wasm.HfSuccess
	}
	if _, err := st.ToJSON(p); err != nil {
		return wasm.HfDecoding
	}
	return wasm.HfSuccess
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

// arrayLen returns the number of elements in an STArray or STVector256 field.
func arrayLen(objBytes []byte, code int32) (int32, wasm.HostFunctionError) {
	tc, fc := splitCode(code)
	if !knownField(tc, fc) {
		return 0, wasm.HfInvalidField
	}
	p := serdes.NewBinaryParser(objBytes, definitions.Get())
	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return 0, wasm.HfDecoding
		}
		if fi.FieldHeader.TypeCode == tc && fi.FieldHeader.FieldCode == fc {
			if tc != stiArray && tc != stiVector256 {
				return 0, wasm.HfNoArray
			}
			return decodeLen(p, fi)
		}
		if herr := skipValue(p, fi); herr != wasm.HfSuccess {
			return 0, herr
		}
	}
	return 0, wasm.HfFieldNotFound
}

// decodeLen decodes an array/vector field through its handler and returns its
// element count.
func decodeLen(p *serdes.BinaryParser, fi *definitions.FieldInstance) (int32, wasm.HostFunctionError) {
	st := types.GetSerializedType(fi.Type)
	if st == nil {
		return 0, wasm.HfDecoding
	}
	var (
		v   any
		err error
	)
	if fi.IsVLEncoded {
		vlen, e := p.ReadVariableLength()
		if e != nil {
			return 0, wasm.HfDecoding
		}
		v, err = st.ToJSON(p, vlen)
	} else {
		v, err = st.ToJSON(p)
	}
	if err != nil {
		return 0, wasm.HfDecoding
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return 0, wasm.HfNoArray
	}
	return int32(rv.Len()), wasm.HfSuccess
}
