//revive:disable:var-naming
package types

import (
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
		return NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
	case "STArray":
		return &STArray{}
	case "PathSet":
		return &PathSet{}
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

func setSkipJSONArrayLimit(st SerializedType, skip bool) {
	switch v := st.(type) {
	case *STObject:
		v.skipJSONArrayLimit = skip
	case *STArray:
		v.skipJSONArrayLimit = skip
	case *PathSet:
		v.skipJSONArrayLimit = skip
	}
}
