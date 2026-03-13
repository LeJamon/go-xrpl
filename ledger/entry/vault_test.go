package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVault_Type verifies Vault returns correct type
func TestVault_Type(t *testing.T) {
	vault := &Vault{}
	assert.Equal(t, TypeVault, vault.Type())
	assert.Equal(t, "Vault", vault.Type().String())
}

// TestVault_Validate tests Vault validation logic
// Reference: rippled/src/test/app/Vault_test.cpp
func TestVault_Validate(t *testing.T) {
	validOwner := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validAccount := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	validShareMPTID := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	t.Run("Valid Vault with minimum fields", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Vault with allow loss policy", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyAllowLoss,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Vault with assets maximum", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      500000,
			AssetsAvailable:  500000,
			AssetsMaximum:    1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Vault with unrealized loss", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  900000,
			LossUnrealized:   100000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Vault with optional data", func(t *testing.T) {
		data := []byte("vault metadata")
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
			Data:             &data,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty owner", func(t *testing.T) {
		vault := &Vault{
			Owner:            [20]byte{},
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "owner")
	})

	t.Run("Invalid with empty account", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          [20]byte{},
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid with empty share MPT ID", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       [32]byte{},
			AssetsTotal:      1000000,
			AssetsAvailable:  1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "share MPT ID")
	})

	t.Run("Invalid with available exceeding total", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      1000000,
			AssetsAvailable:  1500000, // Greater than total
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "available assets cannot exceed total")
	})

	t.Run("Invalid with total exceeding maximum", func(t *testing.T) {
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      2000000, // Greater than maximum
			AssetsAvailable:  2000000,
			AssetsMaximum:    1000000,
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "total assets cannot exceed maximum")
	})

	t.Run("Valid with unlimited maximum (zero)", func(t *testing.T) {
		// AssetsMaximum = 0 means unlimited
		vault := &Vault{
			Owner:            validOwner,
			Account:          validAccount,
			ShareMPTID:       validShareMPTID,
			AssetsTotal:      10000000000,
			AssetsAvailable:  10000000000,
			AssetsMaximum:    0, // Unlimited
			WithdrawalPolicy: WithdrawalPolicyStrict,
		}
		err := vault.Validate()
		assert.NoError(t, err)
	})
}

// TestVault_Hash tests Vault hash computation
func TestVault_Hash(t *testing.T) {
	vault := &Vault{
		Owner:            [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Account:          [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		ShareMPTID:       [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		AssetsTotal:      1000000,
		AssetsAvailable:  1000000,
		WithdrawalPolicy: WithdrawalPolicyStrict,
	}

	hash1, err := vault.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different owner should produce different hash
	vault2 := &Vault{
		Owner:            [20]byte{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		Account:          [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		ShareMPTID:       [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		AssetsTotal:      1000000,
		AssetsAvailable:  1000000,
		WithdrawalPolicy: WithdrawalPolicyStrict,
	}
	hash2, err := vault2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}

// TestWithdrawalPolicy tests the WithdrawalPolicy constants
func TestWithdrawalPolicy(t *testing.T) {
	assert.Equal(t, WithdrawalPolicy(0), WithdrawalPolicyStrict)
	assert.Equal(t, WithdrawalPolicy(1), WithdrawalPolicyAllowLoss)
}
