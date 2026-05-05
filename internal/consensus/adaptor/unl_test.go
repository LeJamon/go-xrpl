package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeValidatorKey(t *testing.T) {
	// Generate a known validator key for testing.
	// Use the secp256k1 algorithm to derive a keypair from a test seed,
	// then encode the public key as a node public key.
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	// Encode the public key as a base58 node public key
	encoded, err := addresscodec.EncodeNodePublicKey(identity.SigningPubKey())
	require.NoError(t, err)
	assert.True(t, len(encoded) > 0)
	assert.Equal(t, byte('n'), encoded[0])

	// Decode it back
	decoded, err := DecodeValidatorKey(encoded)
	assert.NoError(t, err)
	assert.Equal(t, identity.NodeID, decoded)
}

func TestDecodeValidatorKeyInvalid(t *testing.T) {
	_, err := DecodeValidatorKey("invalid-key")
	assert.Error(t, err)
}

func TestNewUNL(t *testing.T) {
	id1, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	key1, err := addresscodec.EncodeNodePublicKey(id1.SigningPubKey())
	require.NoError(t, err)

	// Use a second secp256k1 seed
	id2, err := NewValidatorIdentity("spqPaiDYkYJ2H7cpziSk9XWyAeCPE")
	require.NoError(t, err)
	key2, err := addresscodec.EncodeNodePublicKey(id2.SigningPubKey())
	require.NoError(t, err)

	unl, err := NewUNL([]string{key1, key2})
	require.NoError(t, err)

	assert.Equal(t, 2, unl.Size())
	assert.True(t, unl.IsTrusted(id1.NodeID))
	assert.True(t, unl.IsTrusted(id2.NodeID))
	assert.False(t, unl.IsTrusted(consensus.NodeID{0x99}))

	validators := unl.Validators()
	assert.Len(t, validators, 2)
}

func TestUNLDeduplication(t *testing.T) {
	id, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	key, err := addresscodec.EncodeNodePublicKey(id.SigningPubKey())
	require.NoError(t, err)

	// Duplicate keys should be deduplicated
	unl, err := NewUNL([]string{key, key, key})
	require.NoError(t, err)
	assert.Equal(t, 1, unl.Size())
}

func TestUNLEmpty(t *testing.T) {
	unl, err := NewUNL(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, unl.Size())
	assert.Equal(t, 0, unl.Quorum())
}

func TestCalcQuorum(t *testing.T) {
	tests := []struct {
		n        int
		expected int
	}{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 4},
		{5, 4},
		{10, 8},
		{20, 16},
		{100, 80},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, CalcQuorum(tt.n), "CalcQuorum(%d)", tt.n)
	}
}

func TestUNLQuorum(t *testing.T) {
	// Create 5 validator keys using secp256k1 seeds
	seeds := []string{
		"snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"spqPaiDYkYJ2H7cpziSk9XWyAeCPE",
		"spiByapWt2LvpmbrB7374eS9dbNVk",
		"spizqKqVpcPE8hZy4nFUbmMSaMZWx",
		"sp1o6ZeTweRbXMYAY6VvFtGcwpERb",
	}
	var keys []string
	for _, seed := range seeds {
		id, err := NewValidatorIdentity(seed)
		require.NoError(t, err)
		key, err := addresscodec.EncodeNodePublicKey(id.SigningPubKey())
		require.NoError(t, err)
		keys = append(keys, key)
	}

	unl, err := NewUNL(keys)
	require.NoError(t, err)
	assert.Equal(t, 5, unl.Size())
	assert.Equal(t, 4, unl.Quorum()) // ceil(5 * 0.8) = 4
}
