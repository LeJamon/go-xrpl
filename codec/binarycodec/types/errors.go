//revive:disable:var-naming
package types

import "errors"

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
	errIllegalArrayEndMarker = errors.New("illegal end-of-array marker in object")
	// errMaxNestingDepth mirrors rippled's nesting cap (STVar.cpp:122,
	// STObject.cpp:89): a STObject/STArray nested past maxNestingDepth is
	// rejected. Without it a deeply nested blob recurses until the goroutine
	// stack overflows — a fatal error recover() cannot catch.
	errMaxNestingDepth = errors.New("maximum nesting depth exceeded")
)
