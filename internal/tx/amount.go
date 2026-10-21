package tx

import binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"

// IsLegalNetAmount reports whether an amount satisfies rippled's isLegalNet
// predicate. Non-native amounts have no native supply cap; native amounts may
// not exceed the protocol XRP limit in magnitude.
func IsLegalNetAmount(amount Amount) bool {
	if !amount.IsNative() {
		return true
	}
	drops := amount.Drops()
	if drops < 0 {
		return uint64(-(drops+1))+1 <= uint64(binarytypes.MaxDrops)
	}
	return uint64(drops) <= uint64(binarytypes.MaxDrops)
}
