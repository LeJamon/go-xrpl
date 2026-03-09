package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDID_Type verifies DID returns correct type
func TestDID_Type(t *testing.T) {
	did := &DID{}
	assert.Equal(t, TypeDID, did.Type())
	assert.Equal(t, "DID", did.Type().String())
}

// TestDID_Validate tests DID validation logic
// Reference: rippled/src/test/app/DID_test.cpp
func TestDID_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	uri := "https://example.com/did"
	doc := []byte("DID Document content")
	data := []byte("attestation data")

	t.Run("Valid DID with URI only", func(t *testing.T) {
		// Reference: DID_test.cpp testSetValidInitial - "only URI"
		did := &DID{
			Account: validAccount,
			URI:     &uri,
		}
		err := did.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid DID with DIDDocument only", func(t *testing.T) {
		// Reference: DID_test.cpp testSetValidInitial - "only DIDDocument"
		did := &DID{
			Account:     validAccount,
			DIDDocument: &doc,
		}
		err := did.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid DID with Data only", func(t *testing.T) {
		// Reference: DID_test.cpp testSetValidInitial - "only Data"
		did := &DID{
			Account: validAccount,
			Data:    &data,
		}
		err := did.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid DID with URI and Data", func(t *testing.T) {
		// Reference: DID_test.cpp testSetValidInitial - "URI + Data"
		did := &DID{
			Account: validAccount,
			URI:     &uri,
			Data:    &data,
		}
		err := did.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid DID with all fields", func(t *testing.T) {
		// Reference: DID_test.cpp testSetValidInitial - "URI + DIDDocument + Data"
		did := &DID{
			Account:     validAccount,
			URI:         &uri,
			DIDDocument: &doc,
			Data:        &data,
		}
		err := did.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid DID with empty account", func(t *testing.T) {
		did := &DID{
			Account: [20]byte{},
			URI:     &uri,
		}
		err := did.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid DID with no fields set", func(t *testing.T) {
		// Reference: DID_test.cpp testSetInvalid - "no fields" -> temEMPTY_DID
		did := &DID{
			Account: validAccount,
		}
		err := did.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one")
	})

	t.Run("Invalid DID with all nil optional fields", func(t *testing.T) {
		// Reference: DID_test.cpp testSetInvalid - "all empty fields" -> temEMPTY_DID
		did := &DID{
			Account:     validAccount,
			URI:         nil,
			DIDDocument: nil,
			Data:        nil,
		}
		err := did.Validate()
		assert.Error(t, err)
	})
}

// TestDID_Hash tests DID hash computation
func TestDID_Hash(t *testing.T) {
	uri := "test"
	did := &DID{
		Account: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		URI:     &uri,
	}

	hash1, err := did.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Same DID should produce same hash
	hash2, err := did.Hash()
	require.NoError(t, err)
	assert.Equal(t, hash1, hash2)

	// Different account should produce different hash
	did2 := &DID{
		Account: [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		URI:     &uri,
	}
	hash3, err := did2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash3)
}
