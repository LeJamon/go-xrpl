package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDelegate_Type verifies Delegate returns correct type
func TestDelegate_Type(t *testing.T) {
	delegate := &Delegate{}
	assert.Equal(t, TypeDelegate, delegate.Type())
	assert.Equal(t, "Delegate", delegate.Type().String())
}

// TestDelegate_Validate tests Delegate validation logic
// Reference: rippled/src/test/app/Delegate_test.cpp
func TestDelegate_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validAuthorize := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	t.Run("Valid Delegate with one permission", func(t *testing.T) {
		delegate := &Delegate{
			Account:   validAccount,
			Authorize: validAuthorize,
			Permissions: []Permission{
				{PermissionType: "Payment"},
			},
		}
		err := delegate.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Delegate with multiple permissions", func(t *testing.T) {
		delegate := &Delegate{
			Account:   validAccount,
			Authorize: validAuthorize,
			Permissions: []Permission{
				{PermissionType: "Payment"},
				{PermissionType: "TrustSet"},
				{PermissionType: "OfferCreate"},
			},
		}
		err := delegate.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty account", func(t *testing.T) {
		delegate := &Delegate{
			Account:   [20]byte{},
			Authorize: validAuthorize,
			Permissions: []Permission{
				{PermissionType: "Payment"},
			},
		}
		err := delegate.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid with empty authorize", func(t *testing.T) {
		delegate := &Delegate{
			Account:   validAccount,
			Authorize: [20]byte{},
			Permissions: []Permission{
				{PermissionType: "Payment"},
			},
		}
		err := delegate.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "authorize")
	})

	t.Run("Invalid with self-delegation", func(t *testing.T) {
		// Cannot delegate to self
		delegate := &Delegate{
			Account:   validAccount,
			Authorize: validAccount, // Same as account
			Permissions: []Permission{
				{PermissionType: "Payment"},
			},
		}
		err := delegate.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot delegate to self")
	})

	t.Run("Invalid with no permissions", func(t *testing.T) {
		delegate := &Delegate{
			Account:     validAccount,
			Authorize:   validAuthorize,
			Permissions: []Permission{},
		}
		err := delegate.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one permission")
	})
}

// TestDelegate_Hash tests Delegate hash computation
func TestDelegate_Hash(t *testing.T) {
	delegate := &Delegate{
		Account:   [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Authorize: [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		Permissions: []Permission{
			{PermissionType: "Payment"},
		},
	}

	hash1, err := delegate.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different account should produce different hash
	delegate2 := &Delegate{
		Account:   [20]byte{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		Authorize: [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		Permissions: []Permission{
			{PermissionType: "Payment"},
		},
	}
	hash2, err := delegate2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}

// TestPermission tests the Permission struct
func TestPermission(t *testing.T) {
	perm := Permission{
		PermissionType: "Payment",
	}

	assert.Equal(t, "Payment", perm.PermissionType)
}
