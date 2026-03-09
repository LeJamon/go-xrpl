//go:build ignore

package paychan

import (
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a valid hex public key (33 bytes compressed)
func makeValidPublicKey() string {
	return strings.Repeat("02", 1) + strings.Repeat("AB", 32) // 02 prefix + 32 bytes
}

// Helper to create a valid 256-bit hash (channel ID)
func makeValidChannelID() string {
	return strings.Repeat("AB", 32) // 32 bytes
}

// =============================================================================
// PaymentChannelCreate Validation Tests
// Based on rippled PayChan_test.cpp
// =============================================================================

func TestPaymentChannelCreateValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *PaymentChannelCreate
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - basic create",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: false,
		},
		{
			name: "valid - with CancelAfter",
			tx: func() *PaymentChannelCreate {
				cancelAfter := uint32(750000000)
				return &PaymentChannelCreate{
					BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
					Destination: "rDestination",
					Amount:      tx.NewXRPAmount("1000000"),
					SettleDelay: 3600,
					PublicKey:   makeValidPublicKey(),
					CancelAfter: &cancelAfter,
				}
			}(),
			wantErr: false,
		},
		{
			name: "valid - with DestinationTag",
			tx: func() *PaymentChannelCreate {
				destTag := uint32(12345)
				return &PaymentChannelCreate{
					BaseTx:         *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
					Destination:    "rDestination",
					Amount:         tx.NewXRPAmount("1000000"),
					SettleDelay:    3600,
					PublicKey:      makeValidPublicKey(),
					DestinationTag: &destTag,
				}
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - missing Destination",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "",
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "Destination",
		},
		{
			name: "invalid - missing Amount",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.Amount{},
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - non-XRP Amount (temBAD_AMOUNT)",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewIssuedAmount("100", "USD", "rIssuer"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - negative Amount",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("-1000000"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - zero Amount",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("0"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name: "invalid - destination same as source (temDST_IS_SRC)",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rIssuer", // Same as Account
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "self",
		},
		{
			name: "invalid - missing PublicKey",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   "",
			},
			wantErr: true,
			errMsg:  "PublicKey",
		},
		{
			name: "invalid - invalid PublicKey (not hex)",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   "not_valid_hex",
			},
			wantErr: true,
			errMsg:  "PublicKey",
		},
		{
			name: "invalid - PublicKey wrong length",
			tx: &PaymentChannelCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
				Destination: "rDestination",
				Amount:      tx.NewXRPAmount("1000000"),
				SettleDelay: 3600,
				PublicKey:   "ABCD", // Too short
			},
			wantErr: true,
			errMsg:  "PublicKey",
		},
		{
			name: "invalid - universal flags set",
			tx: func() *PaymentChannelCreate {
				pcc := &PaymentChannelCreate{
					BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rIssuer"),
					Destination: "rDestination",
					Amount:      tx.NewXRPAmount("1000000"),
					SettleDelay: 3600,
					PublicKey:   makeValidPublicKey(),
				}
				flags := uint32(tx.TfUniversal)
				pcc.Common.Flags = &flags
				return pcc
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
// PaymentChannelFund Validation Tests
// =============================================================================

func TestPaymentChannelFundValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *PaymentChannelFund
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - basic fund",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: makeValidChannelID(),
				Amount:  tx.NewXRPAmount("1000000"),
			},
			wantErr: false,
		},
		{
			name: "valid - with Expiration",
			tx: func() *PaymentChannelFund {
				exp := uint32(750000000)
				return &PaymentChannelFund{
					BaseTx:     *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
					Channel:    makeValidChannelID(),
					Amount:     tx.NewXRPAmount("1000000"),
					Expiration: &exp,
				}
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - missing Channel",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: "",
				Amount:  tx.NewXRPAmount("1000000"),
			},
			wantErr: true,
			errMsg:  "Channel",
		},
		{
			name: "invalid - invalid Channel (not hex)",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: "not_valid_hex",
				Amount:  tx.NewXRPAmount("1000000"),
			},
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - Channel wrong length",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: "ABCD",
				Amount:  tx.NewXRPAmount("1000000"),
			},
			wantErr: true,
			errMsg:  "hash",
		},
		{
			name: "invalid - missing Amount",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: makeValidChannelID(),
				Amount:  tx.Amount{},
			},
			wantErr: true,
			errMsg:  "Amount",
		},
		{
			name: "invalid - non-XRP Amount",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: makeValidChannelID(),
				Amount:  tx.NewIssuedAmount("100", "USD", "rIssuer"),
			},
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - negative Amount",
			tx: &PaymentChannelFund{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
				Channel: makeValidChannelID(),
				Amount:  tx.NewXRPAmount("-1000000"),
			},
			wantErr: true,
			errMsg:  "positive",
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
// PaymentChannelClaim Validation Tests
// =============================================================================

func TestPaymentChannelClaimValidation(t *testing.T) {
	tests := []struct {
		name    string
		tx      *PaymentChannelClaim
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name: "valid - basic claim (close)",
			tx: func() *PaymentChannelClaim {
				pcc := &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
					Channel: makeValidChannelID(),
				}
				pcc.SetClose()
				return pcc
			}(),
			wantErr: false,
		},
		{
			name: "valid - claim with Balance",
			tx: func() *PaymentChannelClaim {
				bal := tx.NewXRPAmount("500000")
				return &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
					Balance: &bal,
				}
			}(),
			wantErr: false,
		},
		{
			name: "valid - claim with Signature",
			tx: func() *PaymentChannelClaim {
				bal := tx.NewXRPAmount("500000")
				amt := tx.NewXRPAmount("600000")
				return &PaymentChannelClaim{
					BaseTx:    *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
					Channel:   makeValidChannelID(),
					Balance:   &bal,
					Amount:    &amt,
					Signature: strings.Repeat("AB", 64),
					PublicKey: makeValidPublicKey(),
				}
			}(),
			wantErr: false,
		},
		{
			name: "valid - renew only",
			tx: func() *PaymentChannelClaim {
				pcc := &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
				}
				pcc.SetRenew()
				return pcc
			}(),
			wantErr: false,
		},

		// Invalid cases
		{
			name: "invalid - missing Channel",
			tx: &PaymentChannelClaim{
				BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
				Channel: "",
			},
			wantErr: true,
			errMsg:  "Channel",
		},
		{
			name: "invalid - both tfClose and tfRenew set",
			tx: func() *PaymentChannelClaim {
				pcc := &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
				}
				pcc.SetClose()
				pcc.SetRenew()
				return pcc
			}(),
			wantErr: true,
			errMsg:  "tfClose",
		},
		{
			name: "invalid - Balance not XRP",
			tx: func() *PaymentChannelClaim {
				bal := tx.NewIssuedAmount("100", "USD", "rIssuer")
				return &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
					Balance: &bal,
				}
			}(),
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - Amount not XRP",
			tx: func() *PaymentChannelClaim {
				amt := tx.NewIssuedAmount("100", "USD", "rIssuer")
				return &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
					Amount:  &amt,
				}
			}(),
			wantErr: true,
			errMsg:  "XRP",
		},
		{
			name: "invalid - Balance greater than Amount",
			tx: func() *PaymentChannelClaim {
				bal := tx.NewXRPAmount("600000")
				amt := tx.NewXRPAmount("500000")
				return &PaymentChannelClaim{
					BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
					Channel: makeValidChannelID(),
					Balance: &bal,
					Amount:  &amt,
				}
			}(),
			wantErr: true,
			errMsg:  "exceed",
		},
		{
			name: "invalid - Signature without PublicKey",
			tx: func() *PaymentChannelClaim {
				bal := tx.NewXRPAmount("500000")
				return &PaymentChannelClaim{
					BaseTx:    *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
					Channel:   makeValidChannelID(),
					Balance:   &bal,
					Signature: strings.Repeat("AB", 64),
				}
			}(),
			wantErr: true,
			errMsg:  "PublicKey",
		},
		{
			name: "invalid - Signature without Balance",
			tx: &PaymentChannelClaim{
				BaseTx:    *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
				Channel:   makeValidChannelID(),
				Signature: strings.Repeat("AB", 64),
				PublicKey: makeValidPublicKey(),
			},
			wantErr: true,
			errMsg:  "Balance",
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

func TestPaymentChannelCreateFlatten(t *testing.T) {
	cancelAfter := uint32(750000000)
	destTag := uint32(12345)
	sourceTag := uint32(54321)

	tx := &PaymentChannelCreate{
		BaseTx:         *tx.NewBaseTx(tx.TypePaymentChannelCreate, "rOwner"),
		Destination:    "rDestination",
		Amount:         tx.NewXRPAmount("1000000"),
		SettleDelay:    3600,
		PublicKey:      makeValidPublicKey(),
		CancelAfter:    &cancelAfter,
		DestinationTag: &destTag,
		SourceTag:      &sourceTag,
	}

	flat, err := tx.Flatten()
	require.NoError(t, err)

	assert.Equal(t, "rOwner", flat["Account"])
	assert.Equal(t, "PaymentChannelCreate", flat["TransactionType"])
	assert.Equal(t, "rDestination", flat["Destination"])
	assert.Equal(t, "1000000", flat["Amount"])
	assert.Equal(t, uint32(3600), flat["SettleDelay"])
	assert.Equal(t, makeValidPublicKey(), flat["PublicKey"])
	assert.Equal(t, uint32(750000000), flat["CancelAfter"])
	assert.Equal(t, uint32(12345), flat["DestinationTag"])
	assert.Equal(t, uint32(54321), flat["SourceTag"])
}

func TestPaymentChannelFundFlatten(t *testing.T) {
	exp := uint32(750000000)
	tx := &PaymentChannelFund{
		BaseTx:     *tx.NewBaseTx(tx.TypePaymentChannelFund, "rOwner"),
		Channel:    makeValidChannelID(),
		Amount:     tx.NewXRPAmount("1000000"),
		Expiration: &exp,
	}

	flat, err := tx.Flatten()
	require.NoError(t, err)

	assert.Equal(t, "rOwner", flat["Account"])
	assert.Equal(t, "PaymentChannelFund", flat["TransactionType"])
	assert.Equal(t, makeValidChannelID(), flat["Channel"])
	assert.Equal(t, "1000000", flat["Amount"])
	assert.Equal(t, uint32(750000000), flat["Expiration"])
}

func TestPaymentChannelClaimFlatten(t *testing.T) {
	bal := tx.NewXRPAmount("500000")
	amt := tx.NewXRPAmount("600000")

	tx := &PaymentChannelClaim{
		BaseTx:    *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rDestination"),
		Channel:   makeValidChannelID(),
		Balance:   &bal,
		Amount:    &amt,
		Signature: strings.Repeat("AB", 64),
		PublicKey: makeValidPublicKey(),
	}

	flat, err := tx.Flatten()
	require.NoError(t, err)

	assert.Equal(t, "rDestination", flat["Account"])
	assert.Equal(t, "PaymentChannelClaim", flat["TransactionType"])
	assert.Equal(t, makeValidChannelID(), flat["Channel"])
	assert.Equal(t, "500000", flat["Balance"])
	assert.Equal(t, "600000", flat["Amount"])
	assert.Equal(t, strings.Repeat("AB", 64), flat["Signature"])
	assert.Equal(t, makeValidPublicKey(), flat["PublicKey"])
}

// =============================================================================
// Constructor Tests
// =============================================================================

func TestPaymentChannelConstructors(t *testing.T) {
	t.Run("NewPaymentChannelCreate", func(t *testing.T) {
		pcc := NewPaymentChannelCreate("rOwner", "rDest", tx.NewXRPAmount("1000000"), 3600, makeValidPublicKey())
		require.NotNil(t, pcc)
		assert.Equal(t, "rOwner", pcc.Account)
		assert.Equal(t, "rDest", pcc.Destination)
		assert.Equal(t, "1000000", pcc.Amount.Value)
		assert.Equal(t, uint32(3600), pcc.SettleDelay)
		assert.Equal(t, tx.TypePaymentChannelCreate, pcc.TxType())
	})

	t.Run("NewPaymentChannelFund", func(t *testing.T) {
		pcf := NewPaymentChannelFund("rOwner", makeValidChannelID(), tx.NewXRPAmount("1000000"))
		require.NotNil(t, pcf)
		assert.Equal(t, "rOwner", pcf.Account)
		assert.Equal(t, makeValidChannelID(), pcf.Channel)
		assert.Equal(t, "1000000", pcf.Amount.Value)
		assert.Equal(t, tx.TypePaymentChannelFund, pcf.TxType())
	})

	t.Run("NewPaymentChannelClaim", func(t *testing.T) {
		pcc := NewPaymentChannelClaim("rDest", makeValidChannelID())
		require.NotNil(t, pcc)
		assert.Equal(t, "rDest", pcc.Account)
		assert.Equal(t, makeValidChannelID(), pcc.Channel)
		assert.Equal(t, tx.TypePaymentChannelClaim, pcc.TxType())
	})
}

// =============================================================================
// Flag Tests
// =============================================================================

func TestPaymentChannelClaimFlags(t *testing.T) {
	t.Run("SetClose", func(t *testing.T) {
		tx := NewPaymentChannelClaim("rDest", makeValidChannelID())
		assert.False(t, tx.IsClose())
		tx.SetClose()
		assert.True(t, tx.IsClose())
	})

	t.Run("SetRenew", func(t *testing.T) {
		tx := NewPaymentChannelClaim("rOwner", makeValidChannelID())
		assert.False(t, tx.IsRenew())
		tx.SetRenew()
		assert.True(t, tx.IsRenew())
	})
}

// =============================================================================
// Amendment Tests
// =============================================================================

func TestPaymentChannelRequiredAmendments(t *testing.T) {
	t.Run("PaymentChannelCreate", func(t *testing.T) {
		pcc := &PaymentChannelCreate{}
		assert.Contains(t, pcc.RequiredAmendments(), tx.AmendmentPayChan)
	})

	t.Run("PaymentChannelFund", func(t *testing.T) {
		pcf := &PaymentChannelFund{}
		assert.Contains(t, pcf.RequiredAmendments(), tx.AmendmentPayChan)
	})

	t.Run("PaymentChannelClaim", func(t *testing.T) {
		pcc := &PaymentChannelClaim{}
		assert.Contains(t, pcc.RequiredAmendments(), tx.AmendmentPayChan)
	})
}
