package peermanagement

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test vectors - private keys for direct import
// From rippled SecretKey_test.cpp secp256k1TestVectors
var privateKeyTestVectors = []struct {
	name       string
	privateKey string
	publicKey  string
}{
	{
		// From rippled test vector 0
		name:       "vector1",
		privateKey: "1ACAAEDECE405B2A958212629E16F2EB46B153EEE94CDD350FDEFF52795525B7",
		publicKey:  "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
	},
}

// TestNewIdentity tests creating a new random identity
// Reference: rippled SecretKey_test.cpp testSigning
func TestNewIdentity(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	require.NotNil(t, id)

	// Public key should be 33 bytes (compressed)
	assert.Len(t, id.PublicKey(), 33)

	// Private key hex should be 66 chars (with 00 prefix)
	assert.Len(t, id.PrivateKeyHex(), 66)
	assert.True(t, strings.HasPrefix(id.PrivateKeyHex(), "00"))

	// Encoded public key should start with 'n'
	encoded := id.EncodedPublicKey()
	assert.True(t, strings.HasPrefix(encoded, "n"))
}

// TestNewIdentityFromSeed tests creating identity from seed
func TestNewIdentityFromSeed(t *testing.T) {
	// Test that same seed produces same identity
	seed1 := []byte("test_seed_16bytes")
	id1, err := NewIdentityFromSeed(seed1)
	require.NoError(t, err)

	id2, err := NewIdentityFromSeed(seed1)
	require.NoError(t, err)

	// Same seed should produce same keys
	assert.Equal(t, id1.PublicKeyHex(), id2.PublicKeyHex())
	assert.Equal(t, id1.PrivateKeyHex(), id2.PrivateKeyHex())

	// Different seeds should produce different keys
	seed2 := []byte("different_seed_1")
	id3, err := NewIdentityFromSeed(seed2)
	require.NoError(t, err)

	assert.NotEqual(t, id1.PublicKeyHex(), id3.PublicKeyHex())
}

// TestNewIdentityFromSeed_TooShort tests that short seeds are rejected
func TestNewIdentityFromSeed_TooShort(t *testing.T) {
	shortSeed := make([]byte, 15) // Less than 16 bytes
	_, err := NewIdentityFromSeed(shortSeed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least 16 bytes")
}

// TestNewIdentityFromPrivateKey tests creating identity from hex private key
// Reference: rippled SecretKey_test.cpp test vectors
func TestNewIdentityFromPrivateKey(t *testing.T) {
	for _, tv := range privateKeyTestVectors {
		t.Run(tv.name, func(t *testing.T) {
			// Try with 00 prefix
			privKeyWithPrefix := "00" + tv.privateKey
			id, err := NewIdentityFromPrivateKey(privKeyWithPrefix)
			require.NoError(t, err)
			require.NotNil(t, id)

			// Verify public key matches expected
			assert.Equal(t, strings.ToLower(tv.publicKey), strings.ToLower(id.PublicKeyHex()),
				"public key mismatch")

			// Try without prefix
			id2, err := NewIdentityFromPrivateKey(tv.privateKey)
			require.NoError(t, err)
			require.NotNil(t, id2)

			// Both should produce same public key
			assert.Equal(t, id.PublicKeyHex(), id2.PublicKeyHex())
		})
	}
}

// TestNewIdentityFromPrivateKey_Invalid tests invalid private keys
func TestNewIdentityFromPrivateKey_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		privKey string
	}{
		{"empty", ""},
		{"invalid hex", "ZZZZ"},
		{"too short", "1234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewIdentityFromPrivateKey(tt.privKey)
			assert.Error(t, err)
		})
	}
}

// TestSign tests signing a message
// Reference: rippled SecretKey_test.cpp testDigestSigning
func TestSign(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	message := []byte("test message to sign")

	sig, err := id.Sign(message)
	require.NoError(t, err)
	require.NotNil(t, sig)

	// Signature should be DER-encoded, typically 70-72 bytes
	assert.True(t, len(sig) >= 68 && len(sig) <= 73,
		"signature length %d outside expected range", len(sig))

	// Verify the signature
	parsedSig, err := ecdsa.ParseDERSignature(sig)
	require.NoError(t, err)

	// Hash the message the same way Sign does
	h := sha512.New()
	h.Write(message)
	hash := h.Sum(nil)[:32]

	// Verify signature is valid
	assert.True(t, parsedSig.Verify(hash, id.BtcecPublicKey()),
		"signature verification failed")
}

// TestSign_DifferentMessages tests that different messages produce different signatures
func TestSign_DifferentMessages(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	msg1 := []byte("message one")
	msg2 := []byte("message two")

	sig1, err := id.Sign(msg1)
	require.NoError(t, err)

	sig2, err := id.Sign(msg2)
	require.NoError(t, err)

	// Signatures should be different
	assert.False(t, bytes.Equal(sig1, sig2))

	// Verify sig1 doesn't verify msg2
	parsedSig1, err := ecdsa.ParseDERSignature(sig1)
	require.NoError(t, err)

	h := sha512.New()
	h.Write(msg2)
	wrongHash := h.Sum(nil)[:32]

	assert.False(t, parsedSig1.Verify(wrongHash, id.BtcecPublicKey()),
		"signature should not verify wrong message")
}

// TestEncodedPublicKey tests the Base58 encoding of public keys
// Reference: rippled PublicKey_test.cpp testBase58
func TestEncodedPublicKey(t *testing.T) {
	// Test that encoded key starts with 'n' (node public key prefix)
	id, err := NewIdentity()
	require.NoError(t, err)

	encoded := id.EncodedPublicKey()

	// Must start with 'n'
	assert.True(t, strings.HasPrefix(encoded, "n"),
		"encoded public key should start with 'n', got: %s", encoded)

	// Length should be around 52-53 characters for node public keys
	assert.True(t, len(encoded) >= 50 && len(encoded) <= 55,
		"encoded length %d outside expected range", len(encoded))
}

// TestGenerateSeed tests random seed generation
func TestGenerateSeed(t *testing.T) {
	seed1, err := GenerateSeed()
	require.NoError(t, err)
	assert.Len(t, seed1, 16)

	seed2, err := GenerateSeed()
	require.NoError(t, err)
	assert.Len(t, seed2, 16)

	// Seeds should be different (with overwhelming probability)
	assert.False(t, bytes.Equal(seed1, seed2))
}

// TestIdentityRoundTrip tests creating identity, exporting, and reimporting
func TestIdentityRoundTrip(t *testing.T) {
	// Create original identity
	original, err := NewIdentity()
	require.NoError(t, err)

	// Export private key
	privKeyHex := original.PrivateKeyHex()

	// Reimport
	restored, err := NewIdentityFromPrivateKey(privKeyHex)
	require.NoError(t, err)

	// Public keys should match
	assert.Equal(t, original.PublicKeyHex(), restored.PublicKeyHex())
	assert.Equal(t, original.EncodedPublicKey(), restored.EncodedPublicKey())

	// Signatures should verify with either identity
	message := []byte("roundtrip test")
	sig, err := original.Sign(message)
	require.NoError(t, err)

	parsedSig, err := ecdsa.ParseDERSignature(sig)
	require.NoError(t, err)

	h := sha512.New()
	h.Write(message)
	hash := h.Sum(nil)[:32]

	assert.True(t, parsedSig.Verify(hash, restored.BtcecPublicKey()))
}

// Test vectors from rippled PublicKey_test.cpp testBase58
var base58TestVectors = []struct {
	name      string
	publicKey string // hex-encoded compressed public key
}{
	{
		name:      "masterpassphrase_secp256k1",
		publicKey: "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
	},
}

// TestNewPublicKeyToken tests creating a PublicKeyToken from raw bytes
func TestNewPublicKeyToken(t *testing.T) {
	for _, tv := range base58TestVectors {
		t.Run(tv.name, func(t *testing.T) {
			pubKeyBytes, err := hex.DecodeString(tv.publicKey)
			require.NoError(t, err)

			pk, err := NewPublicKeyToken(pubKeyBytes)
			require.NoError(t, err)
			require.NotNil(t, pk)

			// Round-trip: bytes should match
			assert.Equal(t, pubKeyBytes, pk.Bytes())
		})
	}
}

// TestNewPublicKeyToken_Invalid tests invalid public key inputs
func TestNewPublicKeyToken_Invalid(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too_short", make([]byte, 32)},
		{"too_long", make([]byte, 34)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPublicKeyToken(tt.data)
			assert.Error(t, err)
		})
	}
}

// TestParsePublicKeyToken_Empty tests empty string parsing
func TestParsePublicKeyToken_Empty(t *testing.T) {
	_, err := ParsePublicKeyToken("")
	assert.Error(t, err)
}

// TestParsePublicKeyToken_Whitespace tests whitespace-only string
func TestParsePublicKeyToken_Whitespace(t *testing.T) {
	_, err := ParsePublicKeyToken(" ")
	assert.Error(t, err)
}

// TestParsePublicKeyToken_Malformed tests malformed input
func TestParsePublicKeyToken_Malformed(t *testing.T) {
	_, err := ParsePublicKeyToken("!ty89234gh45")
	assert.Error(t, err)
}

// TestPublicKeyTokenEncode tests encoding a public key to Base58
func TestPublicKeyTokenEncode(t *testing.T) {
	for _, tv := range base58TestVectors {
		t.Run(tv.name, func(t *testing.T) {
			pubKeyBytes, err := hex.DecodeString(tv.publicKey)
			require.NoError(t, err)

			pk, err := NewPublicKeyToken(pubKeyBytes)
			require.NoError(t, err)

			encoded := pk.Encode()

			// Must start with 'n' (node public key prefix)
			assert.True(t, strings.HasPrefix(encoded, "n"),
				"encoded key should start with 'n', got: %s", encoded)

			// Should be non-empty
			assert.NotEmpty(t, encoded)
		})
	}
}

// TestPublicKeyTokenEncodeDecodeRoundTrip tests round-trip encoding and decoding
func TestPublicKeyTokenEncodeDecodeRoundTrip(t *testing.T) {
	// Generate multiple random keys and verify round-trip
	for i := 0; i < 10; i++ {
		t.Run("key_"+string(rune('A'+i)), func(t *testing.T) {
			// Generate random key
			privKey, err := btcec.NewPrivateKey()
			require.NoError(t, err)

			pk := NewPublicKeyTokenFromBtcec(privKey.PubKey())

			// Encode
			encoded := pk.Encode()
			assert.NotEmpty(t, encoded)
			assert.True(t, strings.HasPrefix(encoded, "n"))

			// Decode
			decoded, err := ParsePublicKeyToken(encoded)
			require.NoError(t, err)

			// Should be equal
			assert.True(t, pk.Equal(decoded),
				"round-trip failed: original != decoded")

			// Bytes should match
			assert.Equal(t, pk.Bytes(), decoded.Bytes())
		})
	}
}

// TestPublicKeyTokenEquality tests the Equal method
func TestPublicKeyTokenEquality(t *testing.T) {
	// Generate two different keys
	privKey1, _ := btcec.NewPrivateKey()
	privKey2, _ := btcec.NewPrivateKey()

	pk1 := NewPublicKeyTokenFromBtcec(privKey1.PubKey())
	pk2 := NewPublicKeyTokenFromBtcec(privKey2.PubKey())

	// Same key should be equal to itself
	assert.True(t, pk1.Equal(pk1))
	assert.True(t, pk2.Equal(pk2))

	// Different keys should not be equal
	assert.False(t, pk1.Equal(pk2))
	assert.False(t, pk2.Equal(pk1))

	// Copy should be equal
	pk1Copy, _ := NewPublicKeyToken(pk1.Bytes())
	assert.True(t, pk1.Equal(pk1Copy))
}

// TestPublicKeyTokenEquality_Nil tests Equal with nil values
func TestPublicKeyTokenEquality_Nil(t *testing.T) {
	privKey, _ := btcec.NewPrivateKey()
	pk := NewPublicKeyTokenFromBtcec(privKey.PubKey())

	assert.False(t, pk.Equal(nil))

	var nilPk *PublicKeyToken
	assert.True(t, nilPk.Equal(nil))
}

// TestChecksumValidation tests that invalid checksums are rejected
func TestChecksumValidation(t *testing.T) {
	// Generate a valid encoded key
	privKey, _ := btcec.NewPrivateKey()
	pk := NewPublicKeyTokenFromBtcec(privKey.PubKey())
	valid := pk.Encode()

	// Corrupt the last character (part of checksum). Pick a replacement
	// that is guaranteed to differ from the original so the corruption is
	// never a no-op (both 'X' and 'Y' are valid base58 characters).
	replacement := byte('X')
	if valid[len(valid)-1] == replacement {
		replacement = 'Y'
	}
	corrupted := valid[:len(valid)-1] + string(replacement)
	_, err := ParsePublicKeyToken(corrupted)
	assert.Error(t, err)
}

// TestTLSCertificatePEM_Cached pins the rippled-style `static
// defaultCert` behavior: the anonymous TLS keypair must be generated
// once per Identity and reused. RSA-2048 keygen is 50–200 ms; the
// outbound peer-dial path called this on every Connect before the
// caching change.
func TestTLSCertificatePEM_Cached(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	cert1, key1, err := id.TLSCertificatePEM()
	require.NoError(t, err)
	require.NotEmpty(t, cert1)
	require.NotEmpty(t, key1)

	cert2, key2, err := id.TLSCertificatePEM()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(cert1, cert2),
		"second call must return the same cert PEM (cached)")
	assert.True(t, bytes.Equal(key1, key2),
		"second call must return the same key PEM (cached)")

	// Different identities must produce different certs.
	id2, err := NewIdentity()
	require.NoError(t, err)
	cert3, _, err := id2.TLSCertificatePEM()
	require.NoError(t, err)
	assert.False(t, bytes.Equal(cert1, cert3),
		"distinct identities must produce distinct certificates")
}
