package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMPTokenIssuance_Type verifies MPTokenIssuance returns correct type
func TestMPTokenIssuance_Type(t *testing.T) {
	mpt := &MPTokenIssuance{}
	assert.Equal(t, TypeMPTokenIssuance, mpt.Type())
	assert.Equal(t, "MPTokenIssuance", mpt.Type().String())
}

// TestMPTokenIssuance_Validate tests validation logic
// Reference: rippled/src/test/app/MPToken_test.cpp
func TestMPTokenIssuance_Validate(t *testing.T) {
	validIssuer := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	t.Run("Valid MPTokenIssuance with minimum fields", func(t *testing.T) {
		mpt := &MPTokenIssuance{
			Issuer:            validIssuer,
			Sequence:          1,
			OutstandingAmount: 0,
		}
		err := mpt.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid MPTokenIssuance with all optional fields", func(t *testing.T) {
		maxAmount := uint64(1000000000)
		lockedAmount := uint64(100000)
		metadata := []byte("token metadata")
		domainID := [32]byte{1, 2, 3}

		mpt := &MPTokenIssuance{
			Issuer:            validIssuer,
			Sequence:          1,
			TransferFee:       100,
			AssetScale:        6,
			OutstandingAmount: 500000,
			MaximumAmount:     &maxAmount,
			LockedAmount:      &lockedAmount,
			MPTokenMetadata:   &metadata,
			DomainID:          &domainID,
		}
		err := mpt.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty issuer", func(t *testing.T) {
		mpt := &MPTokenIssuance{
			Issuer: [20]byte{},
		}
		err := mpt.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "issuer")
	})

	t.Run("Invalid with outstanding exceeding maximum", func(t *testing.T) {
		maxAmount := uint64(100)
		mpt := &MPTokenIssuance{
			Issuer:            validIssuer,
			OutstandingAmount: 200,
			MaximumAmount:     &maxAmount,
		}
		err := mpt.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "outstanding amount exceeds maximum")
	})
}

// TestMPTokenIssuance_Hash tests hash computation
func TestMPTokenIssuance_Hash(t *testing.T) {
	mpt := &MPTokenIssuance{
		Issuer:   [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Sequence: 1,
	}

	hash1, err := mpt.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)
}

// TestMPToken_Type verifies MPToken returns correct type
func TestMPToken_Type(t *testing.T) {
	mpt := &MPToken{}
	assert.Equal(t, TypeMPToken, mpt.Type())
	assert.Equal(t, "MPToken", mpt.Type().String())
}

// TestMPToken_Validate tests MPToken validation logic
func TestMPToken_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validIssuanceID := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	t.Run("Valid MPToken with minimum fields", func(t *testing.T) {
		mpt := &MPToken{
			Account:           validAccount,
			MPTokenIssuanceID: validIssuanceID,
			MPTAmount:         0,
		}
		err := mpt.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid MPToken with balance", func(t *testing.T) {
		mpt := &MPToken{
			Account:           validAccount,
			MPTokenIssuanceID: validIssuanceID,
			MPTAmount:         1000000,
		}
		err := mpt.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid MPToken with locked amount", func(t *testing.T) {
		locked := uint64(500)
		mpt := &MPToken{
			Account:           validAccount,
			MPTokenIssuanceID: validIssuanceID,
			MPTAmount:         1000,
			LockedAmount:      &locked,
		}
		err := mpt.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty account", func(t *testing.T) {
		mpt := &MPToken{
			Account:           [20]byte{},
			MPTokenIssuanceID: validIssuanceID,
		}
		err := mpt.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid with empty issuance ID", func(t *testing.T) {
		mpt := &MPToken{
			Account:           validAccount,
			MPTokenIssuanceID: [32]byte{},
		}
		err := mpt.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "MPToken issuance ID")
	})
}

// TestMPToken_Hash tests MPToken hash computation
func TestMPToken_Hash(t *testing.T) {
	mpt := &MPToken{
		Account:           [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		MPTokenIssuanceID: [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
	}

	hash1, err := mpt.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different account should produce different hash
	mpt2 := &MPToken{
		Account:           [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		MPTokenIssuanceID: [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
	}
	hash2, err := mpt2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}
