package secp256k1

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/crypto/common"
)

// BenchmarkValidateDigest exercises the active backend (libsecp256k1
// via cgo by default; decred pure-Go when built with CGO_ENABLED=0).
func BenchmarkValidateDigest(b *testing.B) {
	algo := SECP256K1()
	pub, err := hex.DecodeString("02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E")
	if err != nil {
		b.Fatal(err)
	}
	sig, err := hex.DecodeString("3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64")
	if err != nil {
		b.Fatal(err)
	}
	digest := common.Sha512Half([]byte("Hello World"))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !algo.ValidateDigest(digest, pub, sig) {
			b.Fatal("signature must verify")
		}
	}
}
