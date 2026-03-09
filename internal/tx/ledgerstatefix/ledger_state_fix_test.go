package ledgerstatefix

import (
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// LedgerStateFix Validation Tests
// Based on rippled LedgerStateFix.cpp
// =============================================================================

func TestLedgerStateFixValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *LedgerStateFix
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - nfTokenPageLink fix with Owner",
			tx:      NewNFTokenPageLinkFix("rAdmin", "rOwner"),
			wantErr: false,
		},
		{
			name: "valid - using NewLedgerStateFix with Owner set",
			tx: func() *LedgerStateFix {
				l := NewLedgerStateFix("rAdmin", LedgerFixTypeNFTokenPageLink)
				l.Owner = "rOwner"
				return l
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name:    "invalid - nfTokenPageLink without Owner",
			tx:      NewLedgerStateFix("rAdmin", LedgerFixTypeNFTokenPageLink),
			wantErr: true,
			errMsg:  "Owner is required",
		},
		{
			name:    "invalid - unknown fix type (0)",
			tx:      NewLedgerStateFix("rAdmin", 0),
			wantErr: true,
			errMsg:  "INVALID_LEDGER_FIX_TYPE",
		},
		{
			name:    "invalid - unknown fix type (99)",
			tx:      NewLedgerStateFix("rAdmin", 99),
			wantErr: true,
			errMsg:  "INVALID_LEDGER_FIX_TYPE",
		},
		{
			name:    "invalid - unknown fix type (255)",
			tx:      NewLedgerStateFix("rAdmin", 255),
			wantErr: true,
			errMsg:  "INVALID_LEDGER_FIX_TYPE",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *LedgerStateFix {
				l := NewNFTokenPageLinkFix("rAdmin", "rOwner")
				flags := uint32(tx.TfUniversalMask)
				l.Common.Flags = &flags
				return l
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

func TestLedgerStateFixFlatten(t *testing.T) {
	t.Run("nfTokenPageLink fix", func(t *testing.T) {
		l := NewNFTokenPageLinkFix("rAdmin", "rOwner")

		flat, err := l.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rAdmin", flat["Account"])
		assert.Equal(t, "LedgerStateFix", flat["TransactionType"])
		assert.Equal(t, int(1), flat["LedgerFixType"])
		assert.Equal(t, "rOwner", flat["Owner"])
	})

	t.Run("without Owner", func(t *testing.T) {
		l := NewLedgerStateFix("rAdmin", LedgerFixTypeNFTokenPageLink)

		flat, err := l.Flatten()
		require.NoError(t, err)

		assert.Equal(t, int(1), flat["LedgerFixType"])
		_, hasOwner := flat["Owner"]
		assert.False(t, hasOwner)
	})
}

// =============================================================================
// Constructor Tests
// =============================================================================

func TestLedgerStateFixConstructors(t *testing.T) {
	t.Run("NewLedgerStateFix", func(t *testing.T) {
		lsf := NewLedgerStateFix("rAdmin", LedgerFixTypeNFTokenPageLink)
		require.NotNil(t, lsf)
		assert.Equal(t, "rAdmin", lsf.Account)
		assert.Equal(t, uint8(1), lsf.LedgerFixType)
		assert.Equal(t, "", lsf.Owner)
		assert.Equal(t, tx.TypeLedgerStateFix, lsf.TxType())
	})

	t.Run("NewNFTokenPageLinkFix", func(t *testing.T) {
		lsf := NewNFTokenPageLinkFix("rAdmin", "rOwner")
		require.NotNil(t, lsf)
		assert.Equal(t, "rAdmin", lsf.Account)
		assert.Equal(t, LedgerFixTypeNFTokenPageLink, lsf.LedgerFixType)
		assert.Equal(t, "rOwner", lsf.Owner)
		assert.Equal(t, tx.TypeLedgerStateFix, lsf.TxType())
	})
}

// =============================================================================
// Amendment Tests
// =============================================================================

func TestLedgerStateFixRequiredAmendments(t *testing.T) {
	lsf := NewNFTokenPageLinkFix("rAdmin", "rOwner")
	amendments := lsf.RequiredAmendments()
	assert.Contains(t, amendments, amendment.FeatureFixNFTokenPageLinks)
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestLedgerStateFixConstants(t *testing.T) {
	assert.Equal(t, uint8(1), LedgerFixTypeNFTokenPageLink)
}
