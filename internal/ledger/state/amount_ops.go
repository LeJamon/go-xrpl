package state

import (
	"bytes"
)

// CompareAccountIDs compares two 20-byte account IDs lexicographically.
// Returns -1, 0, or 1. The "low" account in a trust line is the one that
// sorts first.
func CompareAccountIDs(a, b [20]byte) int {
	return bytes.Compare(a[:], b[:])
}

// EncodeAccountIDSafe encodes a 20-byte account ID, returning empty string on error
func EncodeAccountIDSafe(accountID [20]byte) string {
	s, _ := EncodeAccountID(accountID)
	return s
}

func CalculateQualityWithNumberContext(
	takerPays, takerGets Amount,
	ctx NumberContext,
) uint64 {
	return GetRateWithNumberContext(takerPays, takerGets, ctx)
}
