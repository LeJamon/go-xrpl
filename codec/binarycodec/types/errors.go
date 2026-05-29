//revive:disable:var-naming
package types

import "errors"

var (
	errNotValidJSON         = errors.New("not a valid json")
	errDecodeClassicAddress = errors.New("unable to decode classic address")
	errReadBytes            = errors.New("read bytes error")
	// errStrayEndMarker mirrors rippled's "object terminator" reject
	// (STTx.cpp:104-105): an object/array end marker at the top level is
	// malformed input, not a legitimate terminator for a nested container.
	errStrayEndMarker = errors.New("object terminator")
)
