//revive:disable:var-naming
package types

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// SerializedType is an interface representing any type that can be serialized
// and deserialized to and from JSON.
// The FromJson method takes a JSON value and converts it to a byte slice.
// The ToJson method takes a BinaryParser and optional parameters, and converts
// the serialized byte data back to a JSON value.
type SerializedType interface {
	FromJSON(json any) ([]byte, error)
	ToJSON(parser *serdes.BinaryParser, opts ...int) (any, error)
}

// SerializedTypeFor returns a fresh SerializedType instance for the named XRPL
// field type, so the appropriate methods of that type can be called. It returns
// nil for an unknown type name; callers must nil-check the result.
func SerializedTypeFor(t string) SerializedType {
	return jsonFields.serializedType(t)
}

// EncodeDecodedValue re-encodes one decoded field value without its field header.
func EncodeDecodedValue(typeName string, value any) ([]byte, error) {
	st := decodedFields.serializedType(typeName)
	if st == nil {
		return nil, fmt.Errorf("unknown serialized type %q", typeName)
	}
	return decodedFields.encode(st, value)
}

type fieldSource uint8

const (
	jsonFields fieldSource = iota
	decodedFields
	trustedFields
)

func (source fieldSource) serializedType(t string) SerializedType {
	switch t {
	case "UInt8":
		return &UInt8{}
	case "UInt16":
		return &UInt16{}
	case "UInt32":
		return &UInt32{}
	case "UInt64":
		return &UInt64{}
	case "Hash128":
		return NewHash128()
	case "Hash160":
		return NewHash160()
	case "Hash192":
		return NewHash192()
	case "Hash256":
		return NewHash256()
	case "AccountID":
		return &AccountID{}
	case "Amount":
		return &Amount{}
	case "Vector256":
		return &Vector256{}
	case "Blob":
		return &Blob{}
	case "STObject":
		return &STObject{binarySerializer: serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()), source: source}
	case "STArray":
		return &STArray{source: source}
	case "PathSet":
		return &PathSet{source: source}
	case "XChainBridge":
		return &XChainBridge{}
	case "Issue":
		return &Issue{}
	case "Currency":
		return &Currency{}
	case "Number":
		return &Number{}
	case "Int32":
		return &Int32{}
	case "Int64":
		return &Int64{}
	}
	return nil
}

func (source fieldSource) encode(st SerializedType, value any) ([]byte, error) {
	if source != jsonFields {
		if codec, ok := st.(interface {
			fromFields(any, fieldSource) ([]byte, error)
		}); ok {
			return codec.fromFields(value, source)
		}
	}
	return st.FromJSON(value)
}
