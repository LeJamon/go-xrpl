package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeValidatorKeyWithMaster(t *testing.T) {
	// Derive a keypair from a known test seed and encode its public key
	// as a base58 node public key, then decode it back.
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	encoded, err := addresscodec.EncodeNodePublicKey(identity.SigningPubKey())
	require.NoError(t, err)
	assert.True(t, len(encoded) > 0)
	assert.Equal(t, byte('n'), encoded[0])

	nodeID, master, err := DecodeValidatorKeyWithMaster(encoded)
	assert.NoError(t, err)
	assert.Equal(t, identity.NodeID, nodeID)
	assert.NotEqual(t, [33]byte{}, master)
}

func TestDecodeValidatorKeyWithMasterInvalid(t *testing.T) {
	_, _, err := DecodeValidatorKeyWithMaster("invalid-key")
	assert.Error(t, err)
}
