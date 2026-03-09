package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPermissionedDomain_Type verifies PermissionedDomain returns correct type
func TestPermissionedDomain_Type(t *testing.T) {
	pd := &PermissionedDomain{}
	assert.Equal(t, TypePermissionedDomain, pd.Type())
	assert.Equal(t, "PermissionedDomain", pd.Type().String())
}

// TestPermissionedDomain_Validate tests validation logic
// Reference: rippled/src/test/app/PermissionedDomains_test.cpp
func TestPermissionedDomain_Validate(t *testing.T) {
	validOwner := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validIssuer := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	validCredType := []byte("kyc_verified")

	t.Run("Valid PermissionedDomain with one credential", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:    validOwner,
			Sequence: 1,
			AcceptedCredentials: []AcceptedCredential{
				{Issuer: validIssuer, CredentialType: validCredType},
			},
		}
		err := pd.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid PermissionedDomain with multiple credentials", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:    validOwner,
			Sequence: 1,
			AcceptedCredentials: []AcceptedCredential{
				{Issuer: validIssuer, CredentialType: []byte("kyc")},
				{Issuer: validOwner, CredentialType: []byte("accredited")},
			},
		}
		err := pd.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid PermissionedDomain with max credentials (10)", func(t *testing.T) {
		creds := make([]AcceptedCredential, 10)
		for i := range creds {
			creds[i] = AcceptedCredential{
				Issuer:         validIssuer,
				CredentialType: []byte("cred"),
			}
		}
		pd := &PermissionedDomain{
			Owner:               validOwner,
			Sequence:            1,
			AcceptedCredentials: creds,
		}
		err := pd.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty owner", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:    [20]byte{},
			Sequence: 1,
			AcceptedCredentials: []AcceptedCredential{
				{Issuer: validIssuer, CredentialType: validCredType},
			},
		}
		err := pd.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "owner")
	})

	t.Run("Invalid with no accepted credentials", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:               validOwner,
			Sequence:            1,
			AcceptedCredentials: []AcceptedCredential{},
		}
		err := pd.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one accepted credential")
	})

	t.Run("Invalid with too many credentials (>10)", func(t *testing.T) {
		creds := make([]AcceptedCredential, 11)
		for i := range creds {
			creds[i] = AcceptedCredential{
				Issuer:         validIssuer,
				CredentialType: []byte("cred"),
			}
		}
		pd := &PermissionedDomain{
			Owner:               validOwner,
			Sequence:            1,
			AcceptedCredentials: creds,
		}
		err := pd.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot exceed 10")
	})

	t.Run("Invalid with empty credential issuer", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:    validOwner,
			Sequence: 1,
			AcceptedCredentials: []AcceptedCredential{
				{Issuer: [20]byte{}, CredentialType: validCredType},
			},
		}
		err := pd.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "credential issuer")
	})

	t.Run("Invalid with empty credential type", func(t *testing.T) {
		pd := &PermissionedDomain{
			Owner:    validOwner,
			Sequence: 1,
			AcceptedCredentials: []AcceptedCredential{
				{Issuer: validIssuer, CredentialType: []byte{}},
			},
		}
		err := pd.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "credential type")
	})
}

// TestPermissionedDomain_Hash tests hash computation
func TestPermissionedDomain_Hash(t *testing.T) {
	pd := &PermissionedDomain{
		Owner:    [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Sequence: 1,
		AcceptedCredentials: []AcceptedCredential{
			{Issuer: [20]byte{1}, CredentialType: []byte("kyc")},
		},
	}

	hash1, err := pd.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different owner should produce different hash
	pd2 := &PermissionedDomain{
		Owner:    [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		Sequence: 1,
		AcceptedCredentials: []AcceptedCredential{
			{Issuer: [20]byte{1}, CredentialType: []byte("kyc")},
		},
	}
	hash2, err := pd2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}

// TestAcceptedCredential tests the AcceptedCredential struct
func TestAcceptedCredential(t *testing.T) {
	ac := AcceptedCredential{
		Issuer:         [20]byte{1, 2, 3, 4, 5},
		CredentialType: []byte("kyc_verified"),
	}

	assert.Equal(t, byte(1), ac.Issuer[0])
	assert.Equal(t, "kyc_verified", string(ac.CredentialType))
}
