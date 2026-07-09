package ledgerstatefix

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LedgerStateFix Validation Tests
// Based on rippled LedgerStateFix.cpp

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
			// Pins finding LedgerStateFix-uint16: sfLedgerFixType is a UINT16, so a
			// wire value above 255 must decode and reach the default preflight arm
			// (tefINVALID_LEDGER_FIX_TYPE) rather than overflow a uint8 at parse.
			name:    "invalid - unknown fix type (256, needs uint16)",
			tx:      NewLedgerStateFix("rAdmin", 256),
			wantErr: true,
			errMsg:  "INVALID_LEDGER_FIX_TYPE",
		},
		// The universal-flags rejection moved from Validate() to the engine
		// FlagsMasker seam (preflight0), so it is covered by the engine precedence
		// pin-test rather than this Validate()-only table.
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

// Flatten Tests

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

// Constructor Tests

func TestLedgerStateFixConstructors(t *testing.T) {
	t.Run("NewLedgerStateFix", func(t *testing.T) {
		lsf := NewLedgerStateFix("rAdmin", LedgerFixTypeNFTokenPageLink)
		require.NotNil(t, lsf)
		assert.Equal(t, "rAdmin", lsf.Account)
		assert.Equal(t, uint16(1), lsf.LedgerFixType)
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

// Amendment Tests

func TestLedgerStateFixRequiredAmendments(t *testing.T) {
	lsf := NewNFTokenPageLinkFix("rAdmin", "rOwner")
	amendments := lsf.RequiredAmendments()
	assert.Contains(t, amendments, amendment.FeatureFixNFTokenPageLinks)
}

// Constants Tests

func TestLedgerStateFixConstants(t *testing.T) {
	assert.Equal(t, uint16(1), LedgerFixTypeNFTokenPageLink)
	assert.Equal(t, uint16(2), LedgerFixTypeBookExchangeRate)
}

// BookExchangeRate preflight (amendment gate + field shape).

func TestBookExchangeRatePreflightRules(t *testing.T) {
	rulesOn := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_2_0})
	rulesOff := amendment.EmptyRules()
	dir := "00000000000000000000000000000000000000000000000000000000ABCDABCD"

	t.Run("disabled → temDISABLED", func(t *testing.T) {
		l := NewBookExchangeRateFix("rAdmin", dir)
		err := l.PreflightRules(rulesOff)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "temDISABLED")
	})

	t.Run("enabled well-formed → nil", func(t *testing.T) {
		l := NewBookExchangeRateFix("rAdmin", dir)
		assert.NoError(t, l.PreflightRules(rulesOn))
	})

	t.Run("enabled missing BookDirectory → temINVALID", func(t *testing.T) {
		l := NewLedgerStateFix("rAdmin", LedgerFixTypeBookExchangeRate)
		err := l.PreflightRules(rulesOn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BookDirectory is required")
	})

	t.Run("enabled with unexpected Owner → temINVALID", func(t *testing.T) {
		l := NewBookExchangeRateFix("rAdmin", dir)
		l.Owner = "rOwner"
		err := l.PreflightRules(rulesOn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected field")
	})

	t.Run("amendment gate precedes shape check (disabled + malformed → temDISABLED)", func(t *testing.T) {
		l := NewLedgerStateFix("rAdmin", LedgerFixTypeBookExchangeRate) // no BookDirectory
		err := l.PreflightRules(rulesOff)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "temDISABLED")
	})

	t.Run("nfTokenPageLink is a no-op in PreflightRules", func(t *testing.T) {
		l := NewNFTokenPageLinkFix("rAdmin", "rOwner")
		assert.NoError(t, l.PreflightRules(rulesOn))
	})
}

// The bookExchangeRate fix rewrites only sfExchangeRate; a Parse→Serialize
// round-trip of a book directory must otherwise reproduce the entry byte for
// byte, or the mutation would fork ledger state.
func TestBookDirectoryRoundTripFidelity(t *testing.T) {
	var key [32]byte
	key[0] = 0xB0
	binary.BigEndian.PutUint64(key[24:], 0x1234_5678)

	dir := &state.DirectoryNode{
		RootIndex:         key,
		ExchangeRate:      0x9999_9999, // deliberately wrong
		TakerPaysCurrency: [20]byte{0: 1},
		TakerGetsCurrency: [20]byte{0: 2},
	}
	data1, err := state.SerializeDirectoryNode(dir, true)
	require.NoError(t, err)

	parsed, err := state.ParseDirectoryNode(data1)
	require.NoError(t, err)
	data2, err := state.SerializeDirectoryNode(parsed, true)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data1, data2), "unmodified round-trip must be byte-identical")

	// Rewriting only ExchangeRate leaves every other field intact.
	got, present := directoryExchangeRate(data1)
	require.True(t, present)
	require.Equal(t, uint64(0x9999_9999), got)

	parsed.ExchangeRate = binary.BigEndian.Uint64(key[24:])
	fixed, err := state.SerializeDirectoryNode(parsed, true)
	require.NoError(t, err)
	reparsed, err := state.ParseDirectoryNode(fixed)
	require.NoError(t, err)
	assert.Equal(t, uint64(0x1234_5678), reparsed.ExchangeRate)
	assert.Equal(t, dir.TakerPaysCurrency, reparsed.TakerPaysCurrency)
	assert.Equal(t, dir.TakerGetsCurrency, reparsed.TakerGetsCurrency)
	assert.Equal(t, dir.RootIndex, reparsed.RootIndex)
}
