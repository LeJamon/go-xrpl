package types

import (
	"errors"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types/interfaces"
)

const (
	typeAccount  = 0x01
	typeCurrency = 0x10
	typeIssuer   = 0x20
	// typeAll is the union of the legal path-step type bits. rippled rejects any
	// step byte carrying a bit outside this set (STPathSet.cpp:84).
	typeAll = typeAccount | typeCurrency | typeIssuer

	pathsetEndByte    = 0x00
	pathSeparatorByte = 0xFF
)

// serializePathCurrency serializes a currency code for use in path steps.
// Unlike serializeIssuedCurrencyCode, this allows "XRP" which serializes to 20 zero bytes.
func serializePathCurrency(currency string) ([]byte, error) {
	if currency == "XRP" {
		return make([]byte, 20), nil
	}
	return serializeIssuedCurrencyCode(currency)
}

// PathSet is the binary codec for the PathSet field type — the set of payment
// paths carried by a Payment transaction.
type PathSet struct{}

// ErrInvalidPathSet is an error that's thrown when an invalid path set is provided.
var ErrInvalidPathSet = errors.New("invalid path set: expected [][]any")

// ErrEmptyPath mirrors rippled's "empty path" reject (STPathSet.cpp:72-76): a
// path set must contain at least one path and every path at least one element.
var ErrEmptyPath = errors.New("empty path")

// ErrBadPathElement mirrors rippled's "bad path element" reject
// (STPathSet.cpp:88): a step byte carrying a type bit outside typeAll.
var ErrBadPathElement = errors.New("bad path element")

// FromJSON attempts to serialize a path set from a JSON representation of a slice of paths to a byte array.
// It returns the byte array representation of the path set, or an error if the provided json does not represent a valid path set.
func (p PathSet) FromJSON(json any) ([]byte, error) {
	outer, ok := json.([]any)
	if !ok || len(outer) == 0 {
		return nil, ErrInvalidPathSet
	}
	if _, ok := outer[0].([]any); !ok {
		return nil, ErrInvalidPathSet
	}

	if !isPathSet(outer) {
		return nil, ErrInvalidPathSet
	}

	return newPathSet(outer)
}

// ToJSON decodes a path set from a binary representation using a provided binary parser, then translates it to a JSON representation.
// It returns a slice representing the JSON format of the path set, or an error if the path set could not be decoded or if an invalid step is encountered.
//
// The loop mirrors rippled's single-pass STPathSet constructor (STPathSet.cpp:60-92):
// both the boundary byte (0xFF) and the terminator (0x00) commit the current
// path, and rippled rejects either when that path is empty — so a 0xFF boundary
// followed straight by a 0x00 terminator is an empty trailing path, not a path
// the decoder may silently drop. The terminator additionally ends the set.
func (p PathSet) ToJSON(parser interfaces.BinaryParser, _ ...int) (any, error) {
	var pathSet []any
	var path []any

	for {
		// rippled reads the type byte unconditionally each iteration
		// (STPathSet.cpp:67), so a blob that ends before the 0x00 terminator fails
		// with a SerialIter underflow — surfaced here as the parser's out-of-bounds
		// read. A truncated path set is something Encode cannot represent either.
		iType, err := parser.ReadByte()
		if err != nil {
			return nil, err
		}

		if iType == pathsetEndByte || iType == pathSeparatorByte {
			// rippled rejects a boundary/terminator that closes a path with no
			// elements (STPathSet.cpp:69-76).
			if len(path) == 0 {
				return nil, ErrEmptyPath
			}
			pathSet = append(pathSet, path)
			path = nil
			if iType == pathsetEndByte {
				return pathSet, nil
			}
			continue
		}

		step, err := parsePathStep(parser, iType)
		if err != nil {
			return nil, err
		}
		annotateStepType(step)
		path = append(path, step)
	}
}

// annotateStepType records a decoded step's type bitmask, mirroring the type byte
// rippled stores ahead of each element. The bits are derived from the keys the
// step carries, matching the type byte parsePathStep validated.
func annotateStepType(step map[string]any) {
	stepType := 0
	if _, ok := step["account"]; ok {
		stepType |= typeAccount
	}
	if _, ok := step["currency"]; ok {
		stepType |= typeCurrency
	}
	if _, ok := step["issuer"]; ok {
		stepType |= typeIssuer
	}
	step["type"] = stepType
	step["type_hex"] = fmt.Sprintf("%016X", stepType)
}

// isPathSet determines if an array represents a valid path set.
// It checks if the array is either empty or if its first element is a valid path step.
func isPathSet(v []any) bool {
	return len(v) == 0 || len(v[0].([]any)) == 0 || isPathStep(v[0].([]any)[0].(map[string]any))
}

// isPathStep determines if a map represents a valid path step.
// It checks if any of the keys "account", "currency" or "issuer" are present in the map.
func isPathStep(v map[string]any) bool {
	return v["account"] != nil || v["currency"] != nil || v["issuer"] != nil
}

// pathStepMaxLen is the upper bound for a single serialized path step:
// 1 type byte + 20 bytes account + 20 bytes currency + 20 bytes issuer.
const pathStepMaxLen = 1 + 20 + 20 + 20

// newPathStep creates a path step from a map representation.
// It generates a byte array representation of the path step, encoding account, currency, and issuer information as appropriate.
func newPathStep(v map[string]any) ([]byte, error) {
	out := make([]byte, 1, pathStepMaxLen)
	dataType := 0x00

	if v["account"] != nil {
		addr, ok := v["account"].(string)
		if !ok {
			return nil, errors.New("path step: account is not a string")
		}
		_, account, err := addresscodec.DecodeClassicAddressToAccountID(addr)
		if err != nil {
			return nil, fmt.Errorf("path step: account %q: %w", addr, err)
		}
		out = append(out, account...)
		dataType |= typeAccount
	}
	if v["currency"] != nil {
		curr, ok := v["currency"].(string)
		if !ok {
			return nil, errors.New("path step: currency is not a string")
		}
		currency, err := serializePathCurrency(curr)
		if err != nil {
			return nil, fmt.Errorf("path step: currency %q: %w", curr, err)
		}
		out = append(out, currency...)
		dataType |= typeCurrency
	}
	if v["issuer"] != nil {
		addr, ok := v["issuer"].(string)
		if !ok {
			return nil, errors.New("path step: issuer is not a string")
		}
		_, issuer, err := addresscodec.DecodeClassicAddressToAccountID(addr)
		if err != nil {
			return nil, fmt.Errorf("path step: issuer %q: %w", addr, err)
		}
		out = append(out, issuer...)
		dataType |= typeIssuer
	}

	out[0] = byte(dataType)
	return out, nil
}

// newPath constructs a path from a slice of path steps.
// It generates a byte array representation of the path, encoding each path step in turn.
func newPath(v []any) ([]byte, error) {
	if len(v) == 0 {
		return nil, ErrInvalidPathSet
	}
	b := make([]byte, 0, len(v)*pathStepMaxLen)
	for _, step := range v {
		stepMap, ok := step.(map[string]any)
		if !ok {
			return nil, errors.New("path: step is not a map[string]any")
		}
		encoded, err := newPathStep(stepMap)
		if err != nil {
			return nil, err
		}
		b = append(b, encoded...)
	}
	return b, nil
}

// newPathSet constructs a path set from a slice of paths.
// It generates a byte array representation of the path set, encoding each path and adding path separators as appropriate.
func newPathSet(v []any) ([]byte, error) {
	if len(v) == 0 {
		return []byte{pathsetEndByte}, nil
	}
	b := make([]byte, 0, len(v)*pathStepMaxLen)
	for _, path := range v {
		inner, ok := path.([]any)
		if !ok {
			return nil, errors.New("path set: path is not a []any")
		}
		encoded, err := newPath(inner)
		if err != nil {
			return nil, err
		}
		b = append(b, encoded...)
		b = append(b, pathSeparatorByte)
	}
	b[len(b)-1] = pathsetEndByte
	return b, nil
}

// parsePathStep decodes a path step's fields from the parser, given the step's
// already-read type byte. It returns a map representing the path step, or an error.
func parsePathStep(parser interfaces.BinaryParser, dataType byte) (map[string]any, error) {
	// Reject type bits outside the legal set, as rippled does (STPathSet.cpp:84).
	// typeNone (0x00) and typeBoundary (0xFF) are handled by the caller, so any
	// byte reaching here must be a pure step-type bitmask.
	if dataType&^byte(typeAll) != 0 {
		return nil, ErrBadPathElement
	}

	step := make(map[string]any)

	operations := []struct {
		typeKey byte
		key     string
	}{
		{typeAccount, "account"},
		{typeCurrency, "currency"},
		{typeIssuer, "issuer"},
	}

	for _, op := range operations {
		if dataType&op.typeKey != 0 {
			bytes, err := parser.ReadBytes(20) // AccountID or Currency size
			if err != nil {
				return nil, err
			}

			if op.typeKey == typeCurrency {
				value, err := deserializeCurrencyCode(bytes)
				if err != nil {
					return nil, err
				}
				step[op.key] = value
			} else {
				value, err := addresscodec.Encode(bytes, []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
				if err != nil {
					return nil, err
				}
				step[op.key] = value
			}
		}
	}

	return step, nil
}
