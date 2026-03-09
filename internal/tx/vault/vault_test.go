package vault

import (
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a valid 256-bit vault ID
func makeValidVaultID() string {
	return strings.Repeat("AB", 32) // 32 bytes = 64 hex chars
}

// Helper to create a zero vault ID
func makeZeroVaultID() string {
	return strings.Repeat("00", 32)
}

// =============================================================================
// VaultCreate Validation Tests
// Based on rippled VaultCreate.cpp
// =============================================================================

func TestVaultCreateValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultCreate
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - basic vault create with XRP",
			tx:      NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"}),
			wantErr: false,
		},
		{
			name:    "valid - vault create with IOU",
			tx:      NewVaultCreate("rOwner", tx.Asset{Currency: "USD", Issuer: "rIssuer"}),
			wantErr: false,
		},
		{
			name: "valid - vault create with Data",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.Data = "some vault data"
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - vault create with DomainID (private vault)",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.DomainID = makeValidVaultID()
				flags := VaultFlagPrivate
				v.Common.Flags = &flags
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - vault create with AssetsMaximum",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				max := int64(1000000)
				v.AssetsMaximum = &max
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - vault create with WithdrawalPolicy",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				policy := VaultStrategyFirstComeFirstServe
				v.WithdrawalPolicy = &policy
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - vault create with MPTokenMetadata",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.MPTokenMetadata = "vault share metadata"
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - private vault with share non-transferable",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				flags := VaultFlagPrivate | VaultFlagShareNonTransferable
				v.Common.Flags = &flags
				return v
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - missing Asset",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{})
				return v
			}(),
			wantErr: true,
			errMsg:  "Asset",
		},
		{
			name: "invalid - Data too long",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.Data = strings.Repeat("a", MaxVaultDataLength+1)
				return v
			}(),
			wantErr: true,
			errMsg:  "Data exceeds",
		},
		{
			name: "invalid - DomainID is zero",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.DomainID = makeZeroVaultID()
				flags := VaultFlagPrivate
				v.Common.Flags = &flags
				return v
			}(),
			wantErr: true,
			errMsg:  "DomainID cannot be zero",
		},
		{
			name: "invalid - DomainID on non-private vault",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.DomainID = makeValidVaultID()
				// No private flag set
				return v
			}(),
			wantErr: true,
			errMsg:  "private",
		},
		{
			name: "invalid - DomainID wrong length",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.DomainID = "ABCD" // Too short
				flags := VaultFlagPrivate
				v.Common.Flags = &flags
				return v
			}(),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - AssetsMaximum negative",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				max := int64(-100)
				v.AssetsMaximum = &max
				return v
			}(),
			wantErr: true,
			errMsg:  "negative",
		},
		{
			name: "invalid - invalid WithdrawalPolicy",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				policy := uint8(99) // Invalid
				v.WithdrawalPolicy = &policy
				return v
			}(),
			wantErr: true,
			errMsg:  "withdrawal policy",
		},
		{
			name: "invalid - MPTokenMetadata too long",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				v.MPTokenMetadata = strings.Repeat("a", MaxMPTokenMetadataLength+1)
				return v
			}(),
			wantErr: true,
			errMsg:  "MPTokenMetadata exceeds",
		},
		{
			name: "invalid - invalid flags",
			tx: func() *VaultCreate {
				v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
				flags := uint32(0xFFFF0000) // Invalid flags
				v.Common.Flags = &flags
				return v
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
// VaultSet Validation Tests
// Based on rippled VaultSet.cpp
// =============================================================================

func TestVaultSetValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultSet
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - update Data",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				v.Data = "new data"
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - update AssetsMaximum",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				max := int64(2000000)
				v.AssetsMaximum = &max
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - update DomainID",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				v.DomainID = makeValidVaultID()
				return v
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing VaultID",
			tx:      NewVaultSet("rOwner", ""),
			wantErr: true,
			errMsg:  "VaultID is required",
		},
		{
			name:    "invalid - VaultID is zero",
			tx:      NewVaultSet("rOwner", makeZeroVaultID()),
			wantErr: true,
			errMsg:  "VaultID cannot be zero",
		},
		{
			name:    "invalid - VaultID wrong length",
			tx:      NewVaultSet("rOwner", "ABCD"),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - Data too long",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				v.Data = strings.Repeat("a", MaxVaultDataLength+1)
				return v
			}(),
			wantErr: true,
			errMsg:  "Data exceeds",
		},
		{
			name: "invalid - AssetsMaximum negative",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				max := int64(-100)
				v.AssetsMaximum = &max
				return v
			}(),
			wantErr: true,
			errMsg:  "negative",
		},
		{
			name:    "invalid - nothing to update",
			tx:      NewVaultSet("rOwner", makeValidVaultID()),
			wantErr: true,
			errMsg:  "nothing to update",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *VaultSet {
				v := NewVaultSet("rOwner", makeValidVaultID())
				v.Data = "test"
				flags := uint32(tx.TfUniversalMask)
				v.Common.Flags = &flags
				return v
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
// VaultDelete Validation Tests
// Based on rippled VaultDelete.cpp
// =============================================================================

func TestVaultDeleteValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultDelete
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - delete vault",
			tx:      NewVaultDelete("rOwner", makeValidVaultID()),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing VaultID",
			tx:      NewVaultDelete("rOwner", ""),
			wantErr: true,
			errMsg:  "VaultID is required",
		},
		{
			name:    "invalid - VaultID is zero",
			tx:      NewVaultDelete("rOwner", makeZeroVaultID()),
			wantErr: true,
			errMsg:  "VaultID cannot be zero",
		},
		{
			name:    "invalid - VaultID wrong length",
			tx:      NewVaultDelete("rOwner", "ABCD"),
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *VaultDelete {
				v := NewVaultDelete("rOwner", makeValidVaultID())
				flags := uint32(tx.TfUniversalMask)
				v.Common.Flags = &flags
				return v
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
// VaultDeposit Validation Tests
// Based on rippled VaultDeposit.cpp
// =============================================================================

func TestVaultDepositValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultDeposit
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - deposit XRP",
			tx:      NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000)),
			wantErr: false,
		},
		{
			name:    "valid - deposit IOU",
			tx:      NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewIssuedAmountFromFloat64(100, "USD", "rIssuer")),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing VaultID",
			tx:      NewVaultDeposit("rOwner", "", tx.NewXRPAmount(1000000)),
			wantErr: true,
			errMsg:  "VaultID is required",
		},
		{
			name:    "invalid - VaultID is zero",
			tx:      NewVaultDeposit("rOwner", makeZeroVaultID(), tx.NewXRPAmount(1000000)),
			wantErr: true,
			errMsg:  "VaultID cannot be zero",
		},
		{
			name:    "invalid - missing Amount",
			tx:      NewVaultDeposit("rOwner", makeValidVaultID(), tx.Amount{}),
			wantErr: true,
			errMsg:  "Amount is required",
		},
		{
			name:    "invalid - zero Amount",
			tx:      NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(0)),
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid - negative Amount",
			tx:      NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(-100)),
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *VaultDeposit {
				v := NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
				flags := uint32(tx.TfUniversalMask)
				v.Common.Flags = &flags
				return v
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
// VaultWithdraw Validation Tests
// Based on rippled VaultWithdraw.cpp
// =============================================================================

func TestVaultWithdrawValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultWithdraw
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - withdraw XRP",
			tx:      NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000)),
			wantErr: false,
		},
		{
			name:    "valid - withdraw IOU",
			tx:      NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewIssuedAmountFromFloat64(100, "USD", "rIssuer")),
			wantErr: false,
		},
		{
			name: "valid - withdraw with Destination",
			tx: func() *VaultWithdraw {
				v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
				v.Destination = "rDestination"
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - withdraw with Destination and DestinationTag",
			tx: func() *VaultWithdraw {
				v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
				v.Destination = "rDestination"
				tag := uint32(12345)
				v.DestinationTag = &tag
				return v
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing VaultID",
			tx:      NewVaultWithdraw("rOwner", "", tx.NewXRPAmount(1000000)),
			wantErr: true,
			errMsg:  "VaultID is required",
		},
		{
			name:    "invalid - VaultID is zero",
			tx:      NewVaultWithdraw("rOwner", makeZeroVaultID(), tx.NewXRPAmount(1000000)),
			wantErr: true,
			errMsg:  "VaultID cannot be zero",
		},
		{
			name:    "invalid - missing Amount",
			tx:      NewVaultWithdraw("rOwner", makeValidVaultID(), tx.Amount{}),
			wantErr: true,
			errMsg:  "Amount is required",
		},
		{
			name:    "invalid - zero Amount",
			tx:      NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(0)),
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid - negative Amount",
			tx:      NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(-100)),
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - DestinationTag without Destination",
			tx: func() *VaultWithdraw {
				v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
				tag := uint32(12345)
				v.DestinationTag = &tag
				// No Destination set
				return v
			}(),
			wantErr: true,
			errMsg:  "DestinationTag without Destination",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *VaultWithdraw {
				v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
				flags := uint32(tx.TfUniversalMask)
				v.Common.Flags = &flags
				return v
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
// VaultClawback Validation Tests
// Based on rippled VaultClawback.cpp
// =============================================================================

func TestVaultClawbackValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *VaultClawback
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - clawback without amount (all)",
			tx:      NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder"),
			wantErr: false,
		},
		{
			name: "valid - clawback with amount",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				amt := tx.NewIssuedAmountFromFloat64(100, "USD", "rIssuer")
				v.Amount = &amt
				return v
			}(),
			wantErr: false,
		},
		{
			name: "valid - clawback with zero amount (means all)",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				amt := tx.NewIssuedAmountFromFloat64(0, "USD", "rIssuer")
				v.Amount = &amt
				return v
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - missing VaultID",
			tx:      NewVaultClawback("rIssuer", "", "rHolder"),
			wantErr: true,
			errMsg:  "VaultID is required",
		},
		{
			name:    "invalid - VaultID is zero",
			tx:      NewVaultClawback("rIssuer", makeZeroVaultID(), "rHolder"),
			wantErr: true,
			errMsg:  "VaultID cannot be zero",
		},
		{
			name:    "invalid - missing Holder",
			tx:      NewVaultClawback("rIssuer", makeValidVaultID(), ""),
			wantErr: true,
			errMsg:  "Holder is required",
		},
		{
			name:    "invalid - Holder is same as issuer",
			tx:      NewVaultClawback("rIssuer", makeValidVaultID(), "rIssuer"),
			wantErr: true,
			errMsg:  "same as issuer",
		},
		{
			name: "invalid - amount negative",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				amt := tx.NewIssuedAmountFromFloat64(-100, "USD", "rIssuer")
				v.Amount = &amt
				return v
			}(),
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - amount is XRP",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				amt := tx.NewXRPAmount(1000000)
				v.Amount = &amt
				return v
			}(),
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - amount issuer mismatch",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				amt := tx.NewIssuedAmountFromFloat64(100, "USD", "rOtherIssuer") // Different issuer
				v.Amount = &amt
				return v
			}(),
			wantErr: true,
			errMsg:  "issuer can clawback",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *VaultClawback {
				v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
				flags := uint32(tx.TfUniversalMask)
				v.Common.Flags = &flags
				return v
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

func TestVaultFlatten(t *testing.T) {
	t.Run("VaultCreate", func(t *testing.T) {
		v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
		v.Data = "test data"
		max := int64(1000000)
		v.AssetsMaximum = &max

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rOwner", flat["Account"])
		assert.Equal(t, "VaultCreate", flat["TransactionType"])
		assert.Equal(t, "test data", flat["Data"])
		assert.Equal(t, int64(1000000), flat["AssetsMaximum"])
	})

	t.Run("VaultSet", func(t *testing.T) {
		v := NewVaultSet("rOwner", makeValidVaultID())
		v.Data = "updated data"
		max := int64(2000000)
		v.AssetsMaximum = &max

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, makeValidVaultID(), flat["VaultID"])
		assert.Equal(t, "updated data", flat["Data"])
		assert.Equal(t, int64(2000000), flat["AssetsMaximum"])
	})

	t.Run("VaultDelete", func(t *testing.T) {
		v := NewVaultDelete("rOwner", makeValidVaultID())

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rOwner", flat["Account"])
		assert.Equal(t, "VaultDelete", flat["TransactionType"])
		assert.Equal(t, makeValidVaultID(), flat["VaultID"])
	})

	t.Run("VaultDeposit", func(t *testing.T) {
		v := NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rOwner", flat["Account"])
		assert.Equal(t, "VaultDeposit", flat["TransactionType"])
		assert.Equal(t, makeValidVaultID(), flat["VaultID"])
	})

	t.Run("VaultWithdraw", func(t *testing.T) {
		v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
		v.Destination = "rDestination"
		tag := uint32(12345)
		v.DestinationTag = &tag

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rOwner", flat["Account"])
		assert.Equal(t, "VaultWithdraw", flat["TransactionType"])
		assert.Equal(t, "rDestination", flat["Destination"])
		assert.Equal(t, uint32(12345), flat["DestinationTag"])
	})

	t.Run("VaultClawback", func(t *testing.T) {
		v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
		amt := tx.NewIssuedAmountFromFloat64(100, "USD", "rIssuer")
		v.Amount = &amt

		flat, err := v.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rIssuer", flat["Account"])
		assert.Equal(t, "VaultClawback", flat["TransactionType"])
		assert.Equal(t, "rHolder", flat["Holder"])
		assert.NotNil(t, flat["Amount"])
	})
}

// =============================================================================
// Constructor Tests
// =============================================================================

func TestVaultConstructors(t *testing.T) {
	t.Run("NewVaultCreate", func(t *testing.T) {
		v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
		require.NotNil(t, v)
		assert.Equal(t, "rOwner", v.Account)
		assert.Equal(t, "XRP", v.Asset.Currency)
		assert.Equal(t, tx.TypeVaultCreate, v.TxType())
	})

	t.Run("NewVaultSet", func(t *testing.T) {
		v := NewVaultSet("rOwner", makeValidVaultID())
		require.NotNil(t, v)
		assert.Equal(t, "rOwner", v.Account)
		assert.Equal(t, makeValidVaultID(), v.VaultID)
		assert.Equal(t, tx.TypeVaultSet, v.TxType())
	})

	t.Run("NewVaultDelete", func(t *testing.T) {
		v := NewVaultDelete("rOwner", makeValidVaultID())
		require.NotNil(t, v)
		assert.Equal(t, "rOwner", v.Account)
		assert.Equal(t, makeValidVaultID(), v.VaultID)
		assert.Equal(t, tx.TypeVaultDelete, v.TxType())
	})

	t.Run("NewVaultDeposit", func(t *testing.T) {
		v := NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
		require.NotNil(t, v)
		assert.Equal(t, "rOwner", v.Account)
		assert.Equal(t, makeValidVaultID(), v.VaultID)
		assert.Equal(t, "1000000", v.Amount.Value())
		assert.Equal(t, tx.TypeVaultDeposit, v.TxType())
	})

	t.Run("NewVaultWithdraw", func(t *testing.T) {
		v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
		require.NotNil(t, v)
		assert.Equal(t, "rOwner", v.Account)
		assert.Equal(t, makeValidVaultID(), v.VaultID)
		assert.Equal(t, "1000000", v.Amount.Value())
		assert.Equal(t, tx.TypeVaultWithdraw, v.TxType())
	})

	t.Run("NewVaultClawback", func(t *testing.T) {
		v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
		require.NotNil(t, v)
		assert.Equal(t, "rIssuer", v.Account)
		assert.Equal(t, makeValidVaultID(), v.VaultID)
		assert.Equal(t, "rHolder", v.Holder)
		assert.Equal(t, tx.TypeVaultClawback, v.TxType())
	})
}

// =============================================================================
// Amendment Tests
// =============================================================================

func TestVaultRequiredAmendments(t *testing.T) {
	t.Run("VaultCreate", func(t *testing.T) {
		v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})

	t.Run("VaultSet", func(t *testing.T) {
		v := NewVaultSet("rOwner", makeValidVaultID())
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})

	t.Run("VaultDelete", func(t *testing.T) {
		v := NewVaultDelete("rOwner", makeValidVaultID())
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})

	t.Run("VaultDeposit", func(t *testing.T) {
		v := NewVaultDeposit("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})

	t.Run("VaultWithdraw", func(t *testing.T) {
		v := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1000000))
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})

	t.Run("VaultClawback", func(t *testing.T) {
		v := NewVaultClawback("rIssuer", makeValidVaultID(), "rHolder")
		amendments := v.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureSingleAssetVault)
	})
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestVaultConstants(t *testing.T) {
	assert.Equal(t, 256, MaxVaultDataLength)
	assert.Equal(t, 1024, MaxMPTokenMetadataLength)
	assert.Equal(t, uint8(1), VaultStrategyFirstComeFirstServe)
	assert.Equal(t, uint32(0x00000001), VaultFlagPrivate)
	assert.Equal(t, uint32(0x00000002), VaultFlagShareNonTransferable)
}
