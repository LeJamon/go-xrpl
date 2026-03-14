package permissioneddomain

import (
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	tx2 "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a valid 256-bit domain ID
func makeValidDomainID() string {
	return strings.Repeat("AB", 32) // 32 bytes
}

// Helper to create a zero domain ID
func makeZeroDomainID() string {
	return strings.Repeat("00", 32)
}

// Helper to create a valid credential type hex string
func makeCredTypeHex(byteLen int) string {
	return strings.Repeat("AB", byteLen)
}

// =============================================================================
// PermissionedDomainSet Validation Tests
// Based on rippled PermissionedDomains_test.cpp
// =============================================================================

func TestPermissionedDomainSetValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *PermissionedDomainSet
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - create new domain with one credential",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				return tx
			}(),
			wantErr: false,
		},
		{
			name: "valid - create domain with multiple credentials",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				tx.AddAcceptedCredential("rIssuer2", makeCredTypeHex(20))
				tx.AddAcceptedCredential("rIssuer3", makeCredTypeHex(30))
				return tx
			}(),
			wantErr: false,
		},
		{
			name: "valid - modify existing domain",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.DomainID = makeValidDomainID()
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				return tx
			}(),
			wantErr: false,
		},
		{
			name: "valid - maximum credentials (10)",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				for i := 0; i < 10; i++ {
					tx.AddAcceptedCredential("rIssuer"+string(rune('0'+i)), makeCredTypeHex(10+i))
				}
				return tx
			}(),
			wantErr: false,
		},
		{
			name: "valid - maximum CredentialType length (64 bytes)",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(64))
				return tx
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - DomainID is zero",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.DomainID = makeZeroDomainID()
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				return tx
			}(),
			wantErr: true,
			errMsg:  "zero",
		},
		{
			name: "invalid - DomainID wrong length",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.DomainID = "ABCD" // Too short
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				return tx
			}(),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - DomainID not valid hex",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.DomainID = "not_valid_hex_string_at_all!!"
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				return tx
			}(),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - too many credentials (>10)",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				for i := 0; i < 11; i++ {
					tx.AddAcceptedCredential("rIssuer"+string(rune('0'+i)), makeCredTypeHex(10+i))
				}
				return tx
			}(),
			wantErr: true,
			errMsg:  "too many",
		},
		{
			name: "invalid - duplicate credential",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10)) // Same issuer and type
				return tx
			}(),
			wantErr: true,
			errMsg:  "duplicate",
		},
		{
			name: "invalid - empty Issuer",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AcceptedCredentials = []AcceptedCredential{
					{Credential: AcceptedCredentialData{Issuer: "", CredentialType: makeCredTypeHex(10)}},
				}
				return tx
			}(),
			wantErr: true,
			errMsg:  "Issuer",
		},
		{
			name: "invalid - empty CredentialType",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AcceptedCredentials = []AcceptedCredential{
					{Credential: AcceptedCredentialData{Issuer: "rIssuer1", CredentialType: ""}},
				}
				return tx
			}(),
			wantErr: true,
			errMsg:  "CredentialType",
		},
		{
			name: "invalid - CredentialType not valid hex",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AcceptedCredentials = []AcceptedCredential{
					{Credential: AcceptedCredentialData{Issuer: "rIssuer1", CredentialType: "not_hex!"}},
				}
				return tx
			}(),
			wantErr: true,
			errMsg:  "hex",
		},
		{
			name: "invalid - CredentialType too long (>64 bytes)",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(65))
				return tx
			}(),
			wantErr: true,
			errMsg:  "exceeds",
		},
		{
			name: "invalid - non-universal flags set",
			tx: func() *PermissionedDomainSet {
				tx := NewPermissionedDomainSet("rOwner")
				tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
				flags := uint32(0x00010000) // Not a universal flag
				tx.Common.Flags = &flags
				return tx
			}(),
			wantErr: true,
			errMsg:  "invalid flags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tx.Common.Fee = "12"
			seq := uint32(1)
			tt.tx.Common.Sequence = &seq

			err := tt.tx.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// PermissionedDomainDelete Validation Tests
// =============================================================================

func TestPermissionedDomainDeleteValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *PermissionedDomainDelete
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - delete domain",
			tx:      NewPermissionedDomainDelete("rOwner", makeValidDomainID()),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing DomainID",
			tx:      NewPermissionedDomainDelete("rOwner", ""),
			wantErr: true,
			errMsg:  "DomainID",
		},
		{
			name:    "invalid - DomainID is zero",
			tx:      NewPermissionedDomainDelete("rOwner", makeZeroDomainID()),
			wantErr: true,
			errMsg:  "zero",
		},
		{
			name:    "invalid - DomainID wrong length",
			tx:      NewPermissionedDomainDelete("rOwner", "ABCD"),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name:    "invalid - DomainID not valid hex",
			tx:      NewPermissionedDomainDelete("rOwner", "not_valid_hex"),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - non-universal flags set",
			tx: func() *PermissionedDomainDelete {
				tx := NewPermissionedDomainDelete("rOwner", makeValidDomainID())
				flags := uint32(0x00010000) // Not a universal flag
				tx.Common.Flags = &flags
				return tx
			}(),
			wantErr: true,
			errMsg:  "invalid flags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tx.Common.Fee = "12"
			seq := uint32(1)
			tt.tx.Common.Sequence = &seq

			err := tt.tx.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Flatten Tests
// =============================================================================

func TestPermissionedDomainSetFlatten(t *testing.T) {
	t.Run("create new domain", func(t *testing.T) {
		tx := NewPermissionedDomainSet("rOwner")
		tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))
		tx.AddAcceptedCredential("rIssuer2", makeCredTypeHex(20))

		flat, err := tx.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rOwner", flat["Account"])
		assert.Equal(t, "PermissionedDomainSet", flat["TransactionType"])
		_, hasDomainID := flat["DomainID"]
		assert.False(t, hasDomainID) // Not set for creation

		// ReflectFlatten converts struct slices to []map[string]any
		creds, ok := flat["AcceptedCredentials"].([]map[string]any)
		require.True(t, ok, "AcceptedCredentials should be []map[string]any")
		assert.Len(t, creds, 2)
	})

	t.Run("modify existing domain", func(t *testing.T) {
		tx := NewPermissionedDomainSet("rOwner")
		tx.DomainID = makeValidDomainID()
		tx.AddAcceptedCredential("rIssuer1", makeCredTypeHex(10))

		flat, err := tx.Flatten()
		require.NoError(t, err)

		assert.Equal(t, makeValidDomainID(), flat["DomainID"])
	})
}

func TestPermissionedDomainDeleteFlatten(t *testing.T) {
	tx := NewPermissionedDomainDelete("rOwner", makeValidDomainID())

	flat, err := tx.Flatten()
	require.NoError(t, err)

	assert.Equal(t, "rOwner", flat["Account"])
	assert.Equal(t, "PermissionedDomainDelete", flat["TransactionType"])
	assert.Equal(t, makeValidDomainID(), flat["DomainID"])
}

// =============================================================================
// Constructor Tests
// =============================================================================

func TestPermissionedDomainConstructors(t *testing.T) {
	t.Run("NewPermissionedDomainSet", func(t *testing.T) {
		tx := NewPermissionedDomainSet("rOwner")
		require.NotNil(t, tx)
		assert.Equal(t, "rOwner", tx.Account)
		assert.Equal(t, tx2.TypePermissionedDomainSet, tx.TxType())
		assert.Empty(t, tx.DomainID)
		assert.Empty(t, tx.AcceptedCredentials)
	})

	t.Run("NewPermissionedDomainDelete", func(t *testing.T) {
		tx := NewPermissionedDomainDelete("rOwner", makeValidDomainID())
		require.NotNil(t, tx)
		assert.Equal(t, "rOwner", tx.Account)
		assert.Equal(t, makeValidDomainID(), tx.DomainID)
		assert.Equal(t, tx2.TypePermissionedDomainDelete, tx.TxType())
	})
}

// =============================================================================
// AddAcceptedCredential Test
// =============================================================================

func TestPermissionedDomainSetAddAcceptedCredential(t *testing.T) {
	tx := NewPermissionedDomainSet("rOwner")

	tx.AddAcceptedCredential("rIssuer1", "4142434445")
	tx.AddAcceptedCredential("rIssuer2", "4647484950")

	require.Len(t, tx.AcceptedCredentials, 2)

	assert.Equal(t, "rIssuer1", tx.AcceptedCredentials[0].Credential.Issuer)
	assert.Equal(t, "4142434445", tx.AcceptedCredentials[0].Credential.CredentialType)

	assert.Equal(t, "rIssuer2", tx.AcceptedCredentials[1].Credential.Issuer)
	assert.Equal(t, "4647484950", tx.AcceptedCredentials[1].Credential.CredentialType)
}

// =============================================================================
// Amendment Tests
// =============================================================================

func TestPermissionedDomainRequiredAmendments(t *testing.T) {
	t.Run("PermissionedDomainSet", func(t *testing.T) {
		tx := NewPermissionedDomainSet("rOwner")
		amendments := tx.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeaturePermissionedDomains)
		assert.Contains(t, amendments, amendment.FeatureCredentials)
	})

	t.Run("PermissionedDomainDelete", func(t *testing.T) {
		tx := NewPermissionedDomainDelete("rOwner", makeValidDomainID())
		amendments := tx.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeaturePermissionedDomains)
	})
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestPermissionedDomainConstants(t *testing.T) {
	assert.Equal(t, 10, MaxPermissionedDomainCredentials)
}
