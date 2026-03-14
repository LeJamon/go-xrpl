package clawback

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Clawback Validation Tests
// Based on rippled Clawback_test.cpp
// =============================================================================

func TestClawbackValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *Clawback
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - basic IOU clawback",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"), // Holder in issuer field
			},
			wantErr: false,
		},
		{
			name: "valid - MPToken clawback with Holder",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(100.0, "MPT", "rIssuer"),
				Holder: "rHolder",
			},
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - missing Amount",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.Amount{},
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - XRP Amount (cannot claw back XRP)",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewXRPAmount(1000000),
			},
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - negative Amount",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(-100.0, "USD", "rHolder"),
			},
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - zero Amount",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(0.0, "USD", "rHolder"),
			},
			wantErr: true,
			errMsg:  "Amount is required",
		},
		{
			name: "invalid - IOU clawback from self",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(100.0, "USD", "rIssuer"), // Same as Account
			},
			wantErr: true,
			errMsg:  "positive", // temBAD_AMOUNT in rippled
		},
		{
			name: "invalid - MPToken clawback - Holder same as issuer",
			tx: &Clawback{
				BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
				Amount: tx.NewIssuedAmountFromFloat64(100.0, "MPT", "rIssuer"),
				Holder: "rIssuer", // Same as Account
			},
			wantErr: true,
			errMsg:  "issuer",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *Clawback {
				clawbackTx := &Clawback{
					BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
					Amount: tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"),
				}
				flags := tx.TfUniversalMask
				clawbackTx.Common.Flags = &flags
				return clawbackTx
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

func TestClawbackFlatten(t *testing.T) {
	t.Run("IOU clawback", func(t *testing.T) {
		clawbackTx := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"),
		}

		flat, err := clawbackTx.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rIssuer", flat["Account"])
		assert.Equal(t, "Clawback", flat["TransactionType"])

		amtMap, ok := flat["Amount"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "100", amtMap["value"])
		assert.Equal(t, "USD", amtMap["currency"])
		assert.Equal(t, "rHolder", amtMap["issuer"])
	})

	t.Run("MPToken clawback", func(t *testing.T) {
		clawbackTx := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: tx.NewIssuedAmountFromFloat64(100.0, "MPT", "rIssuer"),
			Holder: "rHolder",
		}

		flat, err := clawbackTx.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rIssuer", flat["Account"])
		assert.Equal(t, "rHolder", flat["Holder"])
	})
}

// =============================================================================
// Constructor Tests
// =============================================================================

func TestClawbackConstructors(t *testing.T) {
	t.Run("NewClawback (IOU)", func(t *testing.T) {
		clawbackTx := NewClawback("rIssuer", tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"))
		require.NotNil(t, clawbackTx)
		assert.Equal(t, "rIssuer", clawbackTx.Account)
		assert.Equal(t, "100", clawbackTx.Amount.Value())
		assert.Equal(t, "USD", clawbackTx.Amount.Currency)
		assert.Equal(t, "rHolder", clawbackTx.Amount.Issuer)
		assert.Equal(t, tx.TypeClawback, clawbackTx.TxType())
	})

	t.Run("NewMPTokenClawback", func(t *testing.T) {
		clawbackTx := NewMPTokenClawback("rIssuer", "rHolder", "000000000000000000000001", tx.NewIssuedAmountFromFloat64(100.0, "MPT", "rIssuer"))
		require.NotNil(t, clawbackTx)
		assert.Equal(t, "rIssuer", clawbackTx.Account)
		assert.Equal(t, "rHolder", clawbackTx.Holder)
		assert.Equal(t, tx.TypeClawback, clawbackTx.TxType())
	})
}

// =============================================================================
// Amendment Tests
// =============================================================================

func TestClawbackRequiredAmendments(t *testing.T) {
	t.Run("IOU clawback requires Clawback amendment", func(t *testing.T) {
		clawbackTx := NewClawback("rIssuer", tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"))
		amendments := clawbackTx.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureClawback)
		assert.NotContains(t, amendments, amendment.FeatureMPTokensV1)
	})

	t.Run("MPToken clawback requires Clawback and MPTokensV1 amendments", func(t *testing.T) {
		clawbackTx := NewMPTokenClawback("rIssuer", "rHolder", "000000000000000000000001", tx.NewIssuedAmountFromFloat64(100.0, "MPT", "rIssuer"))
		amendments := clawbackTx.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureClawback)
		assert.Contains(t, amendments, amendment.FeatureMPTokensV1)
	})
}

// =============================================================================
// Transaction Type Tests
// =============================================================================

func TestClawbackTransactionType(t *testing.T) {
	clawbackTx := &Clawback{}
	assert.Equal(t, tx.TypeClawback, clawbackTx.TxType())
}
