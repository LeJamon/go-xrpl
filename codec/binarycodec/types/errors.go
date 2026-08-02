//revive:disable:var-naming
package types

import (
	"errors"
	"fmt"
)

// MaxJSONArrayElements caps how many elements a single JSON array field may hold
// when encoding JSON to binary, matching rippled's STParsedJSON
// maxSTParsedJSONArraySize (STParsedJSON.h). Fields exceeding it are rejected.
const MaxJSONArrayElements = 512

// JSONArrayTooLargeError is returned when a JSON array field exceeds
// MaxJSONArrayElements during JSON->binary encoding. Field names the offending
// field so RPC entry points can surface rippled's exact invalidParams message.
type JSONArrayTooLargeError struct {
	Field string
}

func (e *JSONArrayTooLargeError) Error() string {
	return fmt.Sprintf(
		"Field '%s' exceeds allowed JSON array size of %d elements per field.",
		e.Field, MaxJSONArrayElements)
}

var (
	errNotValidJSON         = errors.New("not a valid json")
	errDecodeClassicAddress = errors.New("unable to decode classic address")
	// errStrayEndMarker mirrors rippled's "object terminator" reject
	// (STTx.cpp:104-105): a top-level object end marker is malformed input,
	// not a legitimate terminator for a nested container.
	errStrayEndMarker = errors.New("object terminator")
	// errIllegalArrayEndMarker mirrors rippled's reject of an array end marker
	// found while parsing an object (STObject.cpp:259-263): the array terminator
	// is consumed by STArray, so encountering one inside an object means
	// malformed nesting at any depth, never a valid terminator.
	errIllegalArrayEndMarker  = errors.New("illegal end-of-array marker in object")
	errIllegalObjectEndMarker = errors.New("illegal end-of-object marker in array")
	// errMaxNestingDepth mirrors rippled's nesting cap (STVar.cpp:122,
	// STObject.cpp:89): a STObject/STArray nested past maxNestingDepth is
	// rejected. Without it a deeply nested blob recurses until the goroutine
	// stack overflows — a fatal error recover() cannot catch.
	errMaxNestingDepth = errors.New("maximum nesting depth exceeded")
)
