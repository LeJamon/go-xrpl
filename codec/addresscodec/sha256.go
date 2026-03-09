package addresscodec

import (
	"crypto/sha256"

	"github.com/decred/dcrd/crypto/ripemd160"
)

// Sha256RipeMD160 returns the RIPEMD160 hash of the SHA256 hash of the given byte slice.
// It first applies SHA256 to the input, then RIPEMD160 to the SHA256 result.
func Sha256RipeMD160(b []byte) []byte {
	sha256 := sha256.New()
	sha256.Write(b)

	ripemd160 := ripemd160.New()
	ripemd160.Write(sha256.Sum(nil))

	return ripemd160.Sum(nil)
}
