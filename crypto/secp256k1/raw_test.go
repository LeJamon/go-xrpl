package secp256k1

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

func newTestKey(t *testing.T) (*btcec.PrivateKey, []byte, []byte) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("NewPrivateKey: %v", err)
	}
	return priv, priv.Serialize(), priv.PubKey().SerializeCompressed()
}

func sampleDigest(b byte) []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = b ^ byte(i)
	}
	return d
}

func TestSignVerifyDigestBytes_RoundTrip(t *testing.T) {
	_, priv, pub := newTestKey(t)
	digest := sampleDigest(0x42)

	sig, err := SignDigestBytes(digest, priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	if !VerifyDigestBytes(digest, pub, sig) {
		t.Fatalf("VerifyDigestBytes rejected own signature")
	}
}

func TestVerifyDigestBytes_WrongDigest(t *testing.T) {
	_, priv, pub := newTestKey(t)
	digest := sampleDigest(0x42)

	sig, err := SignDigestBytes(digest, priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	if VerifyDigestBytes(sampleDigest(0x99), pub, sig) {
		t.Fatalf("VerifyDigestBytes accepted signature over a different digest")
	}
}

func TestVerifyDigestBytes_WrongKey(t *testing.T) {
	_, priv, _ := newTestKey(t)
	_, _, otherPub := newTestKey(t)
	digest := sampleDigest(0x42)

	sig, err := SignDigestBytes(digest, priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	if VerifyDigestBytes(digest, otherPub, sig) {
		t.Fatalf("VerifyDigestBytes accepted signature with mismatched key")
	}
}

func TestVerifyDigestBytes_GarbageSig(t *testing.T) {
	_, _, pub := newTestKey(t)
	if VerifyDigestBytes(sampleDigest(0x01), pub, []byte("not a der signature")) {
		t.Fatalf("VerifyDigestBytes accepted garbage signature")
	}
}

// Cross-impl: a signature produced via btcecdsa.Sign + Serialize (the
// path peermanagement/identity.go used pre-consolidation) must verify
// via VerifyDigestBytes. Locks in wire-compatibility with any peer
// already using that path.
func TestVerifyDigestBytes_AcceptsBtcecdsaSignature(t *testing.T) {
	priv, _, pub := newTestKey(t)
	digest := sampleDigest(0x55)

	sig := btcecdsa.Sign(priv, digest).Serialize()
	if !VerifyDigestBytes(digest, pub, sig) {
		t.Fatalf("VerifyDigestBytes did not accept a btcecdsa-produced signature")
	}
}

// Inverse: a signature produced via SignDigestBytes must verify with
// the btcec/btcecdsa parser too. Catches divergence in DER encoding.
func TestSignDigestBytes_AcceptedByBtcecdsa(t *testing.T) {
	priv, privBytes, _ := newTestKey(t)
	digest := sampleDigest(0xAA)

	sig, err := SignDigestBytes(digest, privBytes)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	parsed, err := btcecdsa.ParseDERSignature(sig)
	if err != nil {
		t.Fatalf("btcecdsa.ParseDERSignature: %v", err)
	}
	if !parsed.Verify(digest, priv.PubKey()) {
		t.Fatalf("btcecdsa rejected signature produced by SignDigestBytes")
	}
}

// Cross-impl: a signature produced by the existing SECP256K1().SignDigest
// (hex API) must verify via VerifyDigestBytes. Confirms the byte-form
// API is interchangeable with the legacy hex one.
func TestVerifyDigestBytes_AcceptsLegacyHexSign(t *testing.T) {
	_, privBytes, pub := newTestKey(t)
	digest := sampleDigest(0x77)

	var d [32]byte
	copy(d[:], digest)
	sig, err := SECP256K1().SignDigest(d, strings.ToUpper(hex.EncodeToString(privBytes)))
	if err != nil {
		t.Fatalf("SECP256K1.SignDigest: %v", err)
	}
	if !VerifyDigestBytes(digest, pub, sig) {
		t.Fatalf("VerifyDigestBytes rejected a signature produced via the hex API")
	}
}

func TestSignDigestBytes_BadInputs(t *testing.T) {
	_, priv, _ := newTestKey(t)

	if _, err := SignDigestBytes(make([]byte, 31), priv); err == nil {
		t.Fatalf("SignDigestBytes accepted a 31-byte digest")
	}
	if _, err := SignDigestBytes(make([]byte, 33), priv); err == nil {
		t.Fatalf("SignDigestBytes accepted a 33-byte digest")
	}
	if _, err := SignDigestBytes(sampleDigest(1), make([]byte, 31)); err == nil {
		t.Fatalf("SignDigestBytes accepted a 31-byte private key")
	}
}

func TestVerifyDigestBytes_BadDigestLen(t *testing.T) {
	_, priv, pub := newTestKey(t)
	sig, err := SignDigestBytes(sampleDigest(0x10), priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	// Truncated digest → false.
	if VerifyDigestBytes(make([]byte, 16), pub, sig) {
		t.Fatalf("VerifyDigestBytes accepted a 16-byte digest")
	}
}

// Sanity: the DER bytes from SignDigestBytes survive a parse-serialize
// round-trip (i.e., they're well-formed DER, not just bytes that happen
// to pass our verifier).
func TestSignDigestBytes_OutputIsValidDER(t *testing.T) {
	_, priv, _ := newTestKey(t)
	sig, err := SignDigestBytes(sampleDigest(0x33), priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	parsed, err := btcecdsa.ParseDERSignature(sig)
	if err != nil {
		t.Fatalf("ParseDERSignature: %v", err)
	}
	if !bytes.Equal(parsed.Serialize(), sig) {
		t.Fatalf("DER bytes did not survive parse-serialize round-trip")
	}
}
