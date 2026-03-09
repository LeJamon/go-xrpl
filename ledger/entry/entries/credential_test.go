package entry

import (
	"testing"

	"github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredential_Type verifies Credential returns correct type
func TestCredential_Type(t *testing.T) {
	cred := &Credential{}
	assert.Equal(t, entry.TypeCredential, cred.Type())
	assert.Equal(t, "Credential", cred.Type().String())
}

// TestCredential_Validate tests Credential validation logic
// Reference: rippled/src/test/app/Credentials_test.cpp
func TestCredential_Validate(t *testing.T) {
	validSubject := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validIssuer := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	validCredType := []byte("kyc_verification")

	t.Run("Valid Credential with minimum fields", func(t *testing.T) {
		// Reference: Credentials_test.cpp testSuccessful
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validIssuer,
			CredentialType: validCredType,
		}
		err := cred.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Credential with expiration", func(t *testing.T) {
		expiration := uint32(1700000000)
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validIssuer,
			CredentialType: validCredType,
			Expiration:     &expiration,
		}
		err := cred.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Credential with URI", func(t *testing.T) {
		// Reference: Credentials_test.cpp testSuccessful - credentials::uri(uri)
		uri := "https://credentials.example.com/verify"
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validIssuer,
			CredentialType: validCredType,
			URI:            &uri,
		}
		err := cred.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Credential self-issued", func(t *testing.T) {
		// Reference: Credentials_test.cpp testSuccessful - "Create for themself"
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validSubject, // Same as subject
			CredentialType: validCredType,
		}
		err := cred.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty subject", func(t *testing.T) {
		cred := &Credential{
			Subject:        [20]byte{},
			Issuer:         validIssuer,
			CredentialType: validCredType,
		}
		err := cred.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "subject")
	})

	t.Run("Invalid with empty issuer", func(t *testing.T) {
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         [20]byte{},
			CredentialType: validCredType,
		}
		err := cred.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "issuer")
	})

	t.Run("Invalid with empty credential type", func(t *testing.T) {
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validIssuer,
			CredentialType: []byte{},
		}
		err := cred.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "credential type is required")
	})

	t.Run("Invalid with credential type too long", func(t *testing.T) {
		// Credential type cannot exceed 64 bytes
		longCredType := make([]byte, 65)
		cred := &Credential{
			Subject:        validSubject,
			Issuer:         validIssuer,
			CredentialType: longCredType,
		}
		err := cred.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot exceed 64")
	})
}

// TestCredential_Hash tests Credential hash computation
func TestCredential_Hash(t *testing.T) {
	cred := &Credential{
		Subject:        [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Issuer:         [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		CredentialType: []byte("kyc"),
	}

	hash1, err := cred.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different subject should produce different hash
	cred2 := &Credential{
		Subject:        [20]byte{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		Issuer:         [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		CredentialType: []byte("kyc"),
	}
	hash2, err := cred2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}
