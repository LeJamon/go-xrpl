package entry

import (
	"bytes"
	"fmt"
)

type viaMapEncoder interface {
	encodeViaMap() ([]byte, error)
}

// verifyEncodeParity panics when the direct writer's result differs from the
// reference encoder in acceptance or bytes. Encode calls it only in builds
// tagged entryencodeparity, which turn any test suite into a differential
// test of the writer.
func verifyEncodeParity(e Entry, direct []byte, directErr error) {
	viaMap, mapErr := e.(viaMapEncoder).encodeViaMap()
	if (directErr == nil) != (mapErr == nil) || !bytes.Equal(direct, viaMap) {
		panic(fmt.Sprintf("ledgerfields: %s: direct writer diverges from map encoder:\n direct %X (err %v)\n map    %X (err %v)",
			e.Type(), direct, directErr, viaMap, mapErr))
	}
}
