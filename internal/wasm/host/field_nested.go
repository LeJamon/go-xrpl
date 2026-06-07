package host

import (
	"encoding/binary"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// maxWasmParamLength bounds a locator, matching rippled's Protocol.h (1KB).
const maxWasmParamLength = 1024

// parseLocator decodes a locator: a little-endian int32 sequence
// [fieldCode, (arrayIndex | fieldCode)...].
func parseLocator(b []byte) ([]int32, wasm.HostFunctionError) {
	if len(b) == 0 || len(b)%4 != 0 || len(b) > maxWasmParamLength {
		return nil, wasm.HfLocatorMalformed
	}
	loc := make([]int32, len(b)/4)
	for i := range loc {
		loc[i] = int32(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return loc, wasm.HfSuccess
}

// isEndMarker reports whether a field is an STObject (0xE1) or STArray (0xF1)
// end marker, which terminates a re-parsed nested region.
func isEndMarker(fi *definitions.FieldInstance) bool {
	h := fi.FieldHeader
	return (h.TypeCode == stiObject || h.TypeCode == stiArray) && h.FieldCode == 1
}

// fieldRegion finds the field with code in objBytes and returns the wire bytes
// its value spans (for containers, the nested content), plus its FieldInstance.
func fieldRegion(objBytes []byte, code int32) ([]byte, *definitions.FieldInstance, wasm.HostFunctionError) {
	tc, fc := splitCode(code)
	if !sentinelCode(code) && !knownField(tc, fc) {
		return nil, nil, wasm.HfInvalidField
	}
	p := serdes.NewBinaryParser(objBytes, definitions.Get())
	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return nil, nil, wasm.HfDecoding
		}
		if isEndMarker(fi) {
			break
		}
		off0 := len(objBytes) - p.Remaining()
		if herr := skipValue(p, fi); herr != wasm.HfSuccess {
			return nil, nil, herr
		}
		if fi.FieldHeader.TypeCode == tc && fi.FieldHeader.FieldCode == fc {
			off1 := len(objBytes) - p.Remaining()
			return objBytes[off0:off1], fi, wasm.HfSuccess
		}
	}
	return nil, nil, wasm.HfFieldNotFound
}

// arrayElement returns the value region + FieldInstance of the idx-th element of
// a serialized STArray (each element is an STObject wrapper).
func arrayElement(arrayRegion []byte, idx int32) ([]byte, *definitions.FieldInstance, wasm.HostFunctionError) {
	if idx < 0 {
		return nil, nil, wasm.HfIndexOutOfBounds
	}
	p := serdes.NewBinaryParser(arrayRegion, definitions.Get())
	var i int32
	for p.HasMore() {
		fi, err := p.ReadField()
		if err != nil {
			return nil, nil, wasm.HfDecoding
		}
		if isEndMarker(fi) {
			break
		}
		off0 := len(arrayRegion) - p.Remaining()
		if herr := skipValue(p, fi); herr != wasm.HfSuccess {
			return nil, nil, herr
		}
		if i == idx {
			off1 := len(arrayRegion) - p.Remaining()
			return arrayRegion[off0:off1], fi, wasm.HfSuccess
		}
		i++
	}
	return nil, nil, wasm.HfIndexOutOfBounds
}

// vec256Element returns the idx-th 32-byte hash of a serialized Vector256 field
// region (which carries a length prefix).
func vec256Element(region []byte, idx int32) ([]byte, wasm.HostFunctionError) {
	if idx < 0 {
		return nil, wasm.HfIndexOutOfBounds
	}
	p := serdes.NewBinaryParser(region, definitions.Get())
	vlen, err := p.ReadVariableLength()
	if err != nil {
		return nil, wasm.HfDecoding
	}
	data, err := p.ReadBytes(vlen)
	if err != nil {
		return nil, wasm.HfDecoding
	}
	start := int(idx) * 32
	if start+32 > len(data) {
		return nil, wasm.HfIndexOutOfBounds
	}
	return append([]byte(nil), data[start:start+32]...), wasm.HfSuccess
}

// located is the result of walking a locator: either a leaf field (region+fi)
// or a Vector256 element (hash).
type located struct {
	region []byte
	fi     *definitions.FieldInstance
	hash   []byte
}

// locate walks objBytes along a locator, mirroring rippled's locateField.
func locate(objBytes []byte, loc []int32) (located, wasm.HostFunctionError) {
	region, fi, herr := fieldRegion(objBytes, loc[0])
	if herr != wasm.HfSuccess {
		return located{}, herr
	}
	for i := 1; i < len(loc); i++ {
		switch fi.FieldHeader.TypeCode {
		case stiArray:
			r, f, herr := arrayElement(region, loc[i])
			if herr != wasm.HfSuccess {
				return located{}, herr
			}
			region, fi = r, f
		case stiObject:
			r, f, herr := fieldRegion(region, loc[i])
			if herr != wasm.HfSuccess {
				return located{}, herr
			}
			region, fi = r, f
		case stiVector256:
			h, herr := vec256Element(region, loc[i])
			if herr != wasm.HfSuccess {
				return located{}, herr
			}
			return located{hash: h}, wasm.HfSuccess
		default:
			// A simple field must be the last locator element.
			return located{}, wasm.HfLocatorMalformed
		}
	}
	return located{region: region, fi: fi}, wasm.HfSuccess
}

// extractRegionValue applies the getAnyFieldData formatting to a field's value
// region (the parser-driven counterpart of extractValue).
func extractRegionValue(region []byte, fi *definitions.FieldInstance) ([]byte, wasm.HostFunctionError) {
	switch fi.FieldHeader.TypeCode {
	case stiObject, stiArray, stiVector256:
		return nil, wasm.HfNotLeafField
	case stiAccount, stiVL:
		p := serdes.NewBinaryParser(region, definitions.Get())
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
		return reverse(region), wasm.HfSuccess
	case stiIssue:
		return issueValue(region), wasm.HfSuccess
	default:
		return append([]byte(nil), region...), wasm.HfSuccess
	}
}

func nestedField(objBytes, locBytes []byte) ([]byte, wasm.HostFunctionError) {
	loc, herr := parseLocator(locBytes)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	res, herr := locate(objBytes, loc)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if res.hash != nil {
		return res.hash, wasm.HfSuccess
	}
	return extractRegionValue(res.region, res.fi)
}

func nestedArrayLen(objBytes, locBytes []byte) (int32, wasm.HostFunctionError) {
	loc, herr := parseLocator(locBytes)
	if herr != wasm.HfSuccess {
		return 0, herr
	}
	res, herr := locate(objBytes, loc)
	if herr != wasm.HfSuccess {
		return 0, herr
	}
	if res.hash != nil {
		return 0, wasm.HfNoArray
	}
	tc := res.fi.FieldHeader.TypeCode
	if tc != stiArray && tc != stiVector256 {
		return 0, wasm.HfNoArray
	}
	return decodeLen(serdes.NewBinaryParser(res.region, definitions.Get()), res.fi)
}
