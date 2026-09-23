package secp256k1

import (
	"bytes"
	"crypto/rand"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/stretchr/testify/require"
)

func newTestKey(t *testing.T) ([]byte, []byte) {
	t.Helper()
	secret := make([]byte, 32)
	for range 128 {
		_, err := rand.Read(secret)
		require.NoError(t, err)
		public, err := (Algorithm{}).DerivePublicKeyFromSecret(secret)
		if err == nil {
			t.Cleanup(func() { rootcrypto.SecureErase(secret) })
			return secret, public
		}
	}
	t.Fatal("could not generate a valid private key")
	return nil, nil
}

func sampleDigest(b byte) []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = b ^ byte(i)
	}
	return d
}

func TestSignVerifyDigestBytes_RoundTrip(t *testing.T) {
	priv, pub := newTestKey(t)
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
	priv, pub := newTestKey(t)
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
	priv, _ := newTestKey(t)
	_, otherPub := newTestKey(t)
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
	_, pub := newTestKey(t)
	if VerifyDigestBytes(sampleDigest(0x01), pub, []byte("not a der signature")) {
		t.Fatalf("VerifyDigestBytes accepted garbage signature")
	}
}

// The fixed digest, key and signature come from rippled's canonicality vectors.
func TestDigestBytes_RippledSignature(t *testing.T) {
	digest := mustDecodeHex(t, "34C19028C80D21F3F48C9354895F8D5BF0D5EE7FF457647CF655F5530A3022A7")
	secret := mustDecodeHex(t, "AA921417E7E5C299DA4EEC16D1CAA92F19B19F2A68511F68EC73BBB2F5236F3D")
	defer rootcrypto.SecureErase(secret)
	public := mustDecodeHex(t, "025096EB12D3E924234E7162369C11D8BF877EDA238778E7A31FF0AAC5D0DBCF37")
	expected := mustDecodeHex(t, "3045022100B49D07F0E934BA468C0EFC78117791408D1FB8B63A6492AD395AC2F360F246600220508739DB0A2EF81676E39F459C8BBB07A09C3E9F9BEB696294D524D479D62740")
	require.True(t, VerifyDigestBytes(digest, public, expected))
	signature, err := SignDigestBytes(digest, secret)
	require.NoError(t, err)
	require.Equal(t, expected, signature)
}

// Cross-impl: a signature produced by the existing Algorithm{}.SignDigest
// (hex API) must verify via VerifyDigestBytes. Confirms the byte-form
// API is interchangeable with the legacy hex one.
func TestVerifyDigestBytes_AcceptsLegacyHexSign(t *testing.T) {
	privBytes, pub := newTestKey(t)
	digest := sampleDigest(0x77)

	var d [32]byte
	copy(d[:], digest)
	sig, err := Algorithm{}.SignDigest(d, strings.ToUpper(hex.EncodeToString(privBytes)))
	if err != nil {
		t.Fatalf("SECP256K1.SignDigest: %v", err)
	}
	if !VerifyDigestBytes(digest, pub, sig) {
		t.Fatalf("VerifyDigestBytes rejected a signature produced via the hex API")
	}
}

func TestSignDigestBytes_BadInputs(t *testing.T) {
	priv, _ := newTestKey(t)

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
	priv, pub := newTestKey(t)
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
	priv, _ := newTestKey(t)
	sig, err := SignDigestBytes(sampleDigest(0x33), priv)
	if err != nil {
		t.Fatalf("SignDigestBytes: %v", err)
	}
	var parsed struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(sig, &parsed)
	require.NoError(t, err)
	require.Empty(t, rest)
	require.Positive(t, parsed.R.Sign())
	require.Positive(t, parsed.S.Sign())
	encoded, err := asn1.Marshal(parsed)
	require.NoError(t, err)
	if !bytes.Equal(encoded, sig) {
		t.Fatalf("DER bytes did not survive parse-serialize round-trip")
	}
}
