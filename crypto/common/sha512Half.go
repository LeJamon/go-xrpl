package common

import "crypto/sha512"

// Sha512Half Returns the first 32 bytes of a sha512 hash of a byte[]
func Sha512Half(args ...[]byte) [32]byte {
	hasher := sha512.New()
	for _, arg := range args {
		hasher.Write(arg)
	}
	fullHash := hasher.Sum(nil)
	return [32]byte(fullHash[:32])
}
