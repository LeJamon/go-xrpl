//revive:disable:var-naming
package types

import "errors"

var (
	errNotValidJSON         = errors.New("not a valid json")
	errDecodeClassicAddress = errors.New("unable to decode classic address")
	errReadBytes            = errors.New("read bytes error")
	// errStrayEndMarker mirrors rippled's "object terminator" reject
	// (STTx.cpp:104-105): a top-level object end marker is malformed input,
	// not a legitimate terminator for a nested container.
	errStrayEndMarker = errors.New("object terminator")
	// errIllegalArrayEndMarker mirrors rippled's reject of an array end marker
	// found while parsing an object (STObject.cpp:259-263): the array terminator
	// is consumed by STArray, so encountering one inside an object means
	// malformed nesting at any depth, never a valid terminator.
	errIllegalArrayEndMarker = errors.New("Illegal end-of-array marker in object")
)
