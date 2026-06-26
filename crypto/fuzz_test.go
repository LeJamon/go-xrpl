package crypto

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

func FuzzDERSigToRS(f *testing.F) {
	seeds := []string{
		// Valid DER signature from test suite
		"3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
		// Empty input
		"",
		// Just a sequence tag
		"30",
		// Empty DER sequence
		"3000",
		// Invalid signature tag
		"3145022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
		// Invalid length
		"3044022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64",
		// Leftover bytes
		"300702010102010101",
		// Minimal valid DER
		"3006020101020101",
	}
	for _, s := range seeds {
		b, _ := hex.DecodeString(s)
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r, s, err := DERSigToRS(data)
		if err != nil {
			return
		}

		// Round-trip: re-encode the parsed r/s and parse again. The values must
		// survive unchanged.
		reencoded := EncodeDERSignature(new(big.Int).SetBytes(r), new(big.Int).SetBytes(s))
		r2, s2, err := DERSigToRS(reencoded)
		if err != nil {
			t.Fatalf("DERSigToRS failed on re-encoded signature: r=%x s=%x err=%v", r, s, err)
		}
		if !bytes.Equal(r, r2) {
			t.Fatalf("r mismatch after round-trip: %x != %x", r, r2)
		}
		if !bytes.Equal(s, s2) {
			t.Fatalf("s mismatch after round-trip: %x != %x", s, s2)
		}
	})
}

func FuzzECDSACanonicality(f *testing.F) {
	// Valid fully canonical DER signature
	validSig, _ := hex.DecodeString("304402206878b5690514437a2342405029426cc2b25b4a03fc396fef845d656cf62bad2c022018610a8d37f65ad02af907c8cb8f72becd0de43de7d5f42fefccb6c2a391a67c")
	f.Add(validSig)
	// Another valid signature from test data
	validSig2, _ := hex.DecodeString("3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64")
	f.Add(validSig2)
	// Too short (5 bytes)
	f.Add([]byte{0x30, 0x03, 0x02, 0x01, 0x01})
	// Too long (80 bytes)
	f.Add(make([]byte, 80))
	// Empty
	f.Add([]byte{})
	// Minimal valid
	f.Add([]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01})
	// Invalid sequence tag
	f.Add([]byte{0x31, 0x06, 0x02, 0x01, 0x00, 0x02, 0x01, 0x00})
	// Negative R (high bit set)
	f.Add([]byte{0x30, 0x06, 0x02, 0x01, 0x80, 0x02, 0x01, 0x01})

	f.Fuzz(func(t *testing.T, sig []byte) {
		result := ECDSACanonicality(sig)

		// Result must be one of the three valid enum values
		if result != CanonicityNone && result != CanonicityCanonical && result != CanonicityFullyCanonical {
			t.Fatalf("unexpected canonicality value: %d", result)
		}
	})
}

func FuzzEd25519Canonical(f *testing.F) {
	// 64 bytes of zeros
	f.Add(make([]byte, 64))
	// 63 bytes (wrong length)
	f.Add(make([]byte, 63))
	// 65 bytes (wrong length)
	f.Add(make([]byte, 65))
	// Empty
	f.Add([]byte{})
	// Valid ed25519 signature from test data
	validEd25519, _ := hex.DecodeString("e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b")
	f.Add(validEd25519)
	// Another valid ed25519 signature from tests
	validEd25519_2, _ := hex.DecodeString("C001CB8A9883497518917DD16391930F4FEE39CEA76C846CFF4330BA44ED19DC4730056C2C6D7452873DE8120A5023C6807135C6329A89A13BA1D476FE8E7100")
	f.Add(validEd25519_2)

	f.Fuzz(func(t *testing.T, sig []byte) {
		// Must not panic
		_ = Ed25519Canonical(sig)
	})
}
