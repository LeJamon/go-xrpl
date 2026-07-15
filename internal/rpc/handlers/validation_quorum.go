package handlers

import "math"

func validationQuorumForRPC(quorum int) uint32 {
	// Rippled uses an internal size_t maximum for "unavailable", then
	// serializes it through Json::UInt.
	if quorum == math.MaxInt {
		return math.MaxUint32
	}
	if quorum <= 0 {
		return 0
	}
	if uint64(quorum) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(quorum)
}
