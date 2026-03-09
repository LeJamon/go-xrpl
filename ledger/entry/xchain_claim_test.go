package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestXChainOwnedClaimID_Type verifies XChainOwnedClaimID returns correct type
func TestXChainOwnedClaimID_Type(t *testing.T) {
	claim := &XChainOwnedClaimID{}
	assert.Equal(t, TypeXChainOwnedClaimID, claim.Type())
	assert.Equal(t, "XChainOwnedClaimID", claim.Type().String())
}

// TestXChainOwnedClaimID_Validate tests validation logic
// Reference: rippled/src/test/app/XChain_test.cpp
func TestXChainOwnedClaimID_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validSource := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	t.Run("Valid XChainOwnedClaimID", func(t *testing.T) {
		claim := &XChainOwnedClaimID{
			Account:          validAccount,
			OtherChainSource: validSource,
			XChainClaimID:    1,
			SignatureReward:  100,
		}
		err := claim.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid XChainOwnedClaimID with attestations", func(t *testing.T) {
		claim := &XChainOwnedClaimID{
			Account:          validAccount,
			OtherChainSource: validSource,
			XChainClaimID:    1,
			SignatureReward:  100,
			XChainClaimAttestations: []XChainClaimAttestation{
				{
					AttestationSignerAccount: validAccount,
					Amount:                   1000000,
					Destination:              validSource,
				},
			},
		}
		err := claim.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty account", func(t *testing.T) {
		claim := &XChainOwnedClaimID{
			Account:          [20]byte{},
			OtherChainSource: validSource,
		}
		err := claim.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid with empty other chain source", func(t *testing.T) {
		claim := &XChainOwnedClaimID{
			Account:          validAccount,
			OtherChainSource: [20]byte{},
		}
		err := claim.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "other chain source")
	})
}

// TestXChainOwnedClaimID_Hash tests hash computation
func TestXChainOwnedClaimID_Hash(t *testing.T) {
	claim := &XChainOwnedClaimID{
		Account:          [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		OtherChainSource: [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
	}

	hash1, err := claim.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)
}

// TestXChainOwnedCreateAccountClaimID_Type verifies type
func TestXChainOwnedCreateAccountClaimID_Type(t *testing.T) {
	claim := &XChainOwnedCreateAccountClaimID{}
	assert.Equal(t, TypeXChainOwnedCreateAccountClaimID, claim.Type())
	assert.Equal(t, "XChainOwnedCreateAccountClaimID", claim.Type().String())
}

// TestXChainOwnedCreateAccountClaimID_Validate tests validation
func TestXChainOwnedCreateAccountClaimID_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	t.Run("Valid XChainOwnedCreateAccountClaimID", func(t *testing.T) {
		claim := &XChainOwnedCreateAccountClaimID{
			Account:                  validAccount,
			XChainAccountCreateCount: 1,
		}
		err := claim.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid with attestations", func(t *testing.T) {
		claim := &XChainOwnedCreateAccountClaimID{
			Account:                  validAccount,
			XChainAccountCreateCount: 1,
			XChainCreateAccountAttestations: []XChainCreateAccountAttestation{
				{
					AttestationSignerAccount: validAccount,
					Amount:                   10000000,
					SignatureReward:          100,
				},
			},
		}
		err := claim.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty account", func(t *testing.T) {
		claim := &XChainOwnedCreateAccountClaimID{
			Account: [20]byte{},
		}
		err := claim.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})
}

// TestXChainOwnedCreateAccountClaimID_Hash tests hash computation
func TestXChainOwnedCreateAccountClaimID_Hash(t *testing.T) {
	claim := &XChainOwnedCreateAccountClaimID{
		Account: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
	}

	hash1, err := claim.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)
}

// TestXChainClaimAttestation tests the attestation struct
func TestXChainClaimAttestation(t *testing.T) {
	att := XChainClaimAttestation{
		AttestationSignerAccount: [20]byte{1, 2, 3},
		PublicKey:                [33]byte{4, 5, 6},
		Amount:                   1000000,
		Destination:              [20]byte{7, 8, 9},
		WasLockingChainSend:      true,
	}

	assert.Equal(t, uint64(1000000), att.Amount)
	assert.True(t, att.WasLockingChainSend)
}
