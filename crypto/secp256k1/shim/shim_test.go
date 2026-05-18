//go:build cgo

package shim

import (
	"encoding/hex"
	"testing"
)

// Vector lifted from the package-level Validate tests.
const (
	testMsg       = "Hello World"
	testPubHex    = "02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E"
	testSigDERHex = "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64"
)

func sha512HalfStr(s string) [32]byte {
	return sha512HalfBytes([]byte(s))
}

func TestVerifyDigest_Valid(t *testing.T) {
	pub := mustHex(t, testPubHex)
	sig := mustHex(t, testSigDERHex)
	digest := sha512HalfStr(testMsg)
	if !VerifyDigest(digest[:], pub, sig) {
		t.Fatal("expected libsecp256k1 to accept the known-good signature")
	}
}

func TestVerifyDigest_RejectsTamperedDigest(t *testing.T) {
	pub := mustHex(t, testPubHex)
	sig := mustHex(t, testSigDERHex)
	digest := sha512HalfStr(testMsg)
	digest[0] ^= 0x01
	if VerifyDigest(digest[:], pub, sig) {
		t.Fatal("expected libsecp256k1 to reject a tampered digest")
	}
}

func TestVerifyDigest_RejectsMalformedSig(t *testing.T) {
	pub := mustHex(t, testPubHex)
	digest := sha512HalfStr(testMsg)
	if VerifyDigest(digest[:], pub, []byte{0x30, 0x00}) {
		t.Fatal("expected libsecp256k1 to reject malformed DER")
	}
}

func TestVerifyDigest_RejectsBadInputs(t *testing.T) {
	if VerifyDigest(nil, []byte{1}, []byte{1}) {
		t.Fatal("nil hash should be rejected")
	}
	hash := make([]byte, 32)
	if VerifyDigest(hash, nil, []byte{1}) {
		t.Fatal("nil pubkey should be rejected")
	}
	if VerifyDigest(hash, []byte{1}, nil) {
		t.Fatal("nil sig should be rejected")
	}
	if VerifyDigest(make([]byte, 31), []byte{1}, []byte{1}) {
		t.Fatal("short hash should be rejected")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}
