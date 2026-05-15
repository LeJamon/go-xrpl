package addresscodec

import (
	"crypto/sha256"

	"github.com/decred/dcrd/crypto/ripemd160"
)

// Sha256RipeMD160 returns the RIPEMD160 hash of the SHA256 hash of the given byte slice.
// It first applies SHA256 to the input, then RIPEMD160 to the SHA256 result.
func Sha256RipeMD160(b []byte) []byte {
	sha := sha256.Sum256(b)
	r := ripemd160.New()
	r.Write(sha[:])
	return r.Sum(nil)
}
