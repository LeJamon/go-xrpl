package secp256k1

import (
	"encoding/hex"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
)

func BenchmarkValidateDigest(b *testing.B) {
	algo := Algorithm{}
	pub, err := hex.DecodeString("02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E")
	if err != nil {
		b.Fatal(err)
	}
	sig, err := hex.DecodeString("3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64")
	if err != nil {
		b.Fatal(err)
	}
	digest := sha512half.Sum([]byte("Hello World"))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !algo.ValidateDigest(digest, pub, sig) {
			b.Fatal("signature must verify")
		}
	}
}

func BenchmarkSignDigest(b *testing.B) {
	key, err := hex.DecodeString("B167A9F3B9E60A4F93695713682C102438620AA1785C3AE635F53E5B6261071A")
	if err != nil {
		b.Fatal(err)
	}
	defer rootcrypto.SecureErase(key)
	digest := sha512half.Sum([]byte("Hello World"))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := SignDigestBytes(digest[:], key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeriveKeypair(b *testing.B) {
	seed := []byte{229, 81, 182, 134, 131, 220, 192, 126, 133, 114, 150, 132, 140, 237, 222, 196}
	for _, tc := range []struct {
		name      string
		validator bool
	}{
		{"account", false},
		{"validator", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				key, _, err := (Algorithm{}).DeriveKeypairBytes(seed, tc.validator)
				if err != nil {
					b.Fatal(err)
				}
				rootcrypto.SecureErase(key)
			}
		})
	}
}

func BenchmarkDerivePublicGenerator(b *testing.B) {
	seed := []byte{229, 81, 182, 134, 131, 220, 192, 126, 133, 114, 150, 132, 140, 237, 222, 196}
	key, generator, err := (Algorithm{}).DeriveKeypairBytes(seed, true)
	if err != nil {
		b.Fatal(err)
	}
	rootcrypto.SecureErase(key)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := (Algorithm{}).DerivePublicKeyFromPublicGenerator(generator); err != nil {
			b.Fatal(err)
		}
	}
}
