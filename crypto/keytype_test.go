package crypto

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyType_String(t *testing.T) {
	tests := []struct {
		name     string
		keyType  KeyType
		expected string
	}{
		{"Unknown", KeyTypeUnknown, "unknown"},
		{"Secp256k1", KeyTypeSecp256k1, "secp256k1"},
		{"Ed25519", KeyTypeEd25519, "ed25519"},
		{"Invalid value", KeyType(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.keyType.String())
		})
	}
}

func TestPublicKeyType(t *testing.T) {
	tests := []struct {
		name     string
		pubKey   string
		expected KeyType
	}{
		{
			name:     "Ed25519 key",
			pubKey:   "ED9434799226374926EDA3B54B1B461B4ABF7237962EAE18528FEA67595397FA32",
			expected: KeyTypeEd25519,
		},
		{
			name:     "Secp256k1 key with 02 prefix",
			pubKey:   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
			expected: KeyTypeSecp256k1,
		},
		{
			name:     "Secp256k1 key with 03 prefix",
			pubKey:   "0379B6FAD1F7FC97285A7A5C2F0A8DAA6FA70D4B4B1D26D1D8B4E31E7C37CCFE5B",
			expected: KeyTypeSecp256k1,
		},
		{
			name:     "Invalid prefix 04 (uncompressed)",
			pubKey:   "0430E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020" + "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD0",
			expected: KeyTypeUnknown,
		},
		{
			name:     "Too short",
			pubKey:   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD0",
			expected: KeyTypeUnknown,
		},
		{
			name:     "Too long",
			pubKey:   "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD02000",
			expected: KeyTypeUnknown,
		},
		{
			name:     "Empty",
			pubKey:   "",
			expected: KeyTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pubKey, _ := hex.DecodeString(tt.pubKey)
			result := PublicKeyType(pubKey)
			assert.Equal(t, tt.expected, result)
		})
	}
}
