package clawback

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMPTIssuanceID = "000000000000000000000001000000000000000000000001"

func newTestMPTAmount(value int64, issuer string) state.Amount {
	return state.NewMPTAmountWithIssuanceID(value, issuer, testMPTIssuanceID)
}

// preflightClawback runs Clawback's preflight body in engine order: the
// rules-free Validate() (flags mask) followed by PreflightRules() (the
// amount/holder body). The engine invokes them in exactly this sequence.
func preflightClawback(c *Clawback, rules *amendment.Rules) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return c.PreflightRules(rules)
}

func allRules() *amendment.Rules { return amendment.AllSupportedRules() }

// Clawback Validation Tests
// Based on rippled Clawback_test.cpp

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
				Amount: newTestMPTAmount(100, "rIssuer"),
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
			errMsg:  "positive", // isXRP folded into the Issue-arm temBAD_AMOUNT
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
			errMsg:  "positive",
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
				Amount: newTestMPTAmount(100, "rIssuer"),
				Holder: "rIssuer", // Same as Account
			},
			wantErr: true,
			errMsg:  "Holder cannot be the same as issuer",
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

			err := preflightClawback(tt.tx, allRules())
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

// TestClawbackPreflightOrder pins the per-arm precedence findings: rippled checks
// the holder shape before the amount's XRP/zero/negative rejection in both the
// Issue arm and the MPT arm, so a transaction that is malformed on both counts
// surfaces the holder temMALFORMED, not temBAD_AMOUNT.
// Reference: rippled Clawback.cpp preflightHelper<Issue>/<MPTIssue>.
func TestClawbackPreflightOrder(t *testing.T) {
	// Finding 1 — IOU/Issue arm: Holder-present temMALFORMED wins over a zero,
	// negative, or XRP amount that would otherwise be temBAD_AMOUNT.
	t.Run("IOU arm: Holder beats zero amount", func(t *testing.T) {
		c := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: tx.NewIssuedAmountFromFloat64(0.0, "USD", "rHolder"),
			Holder: "rSomeone", // Holder must not be present for IOU clawback
		}
		require.ErrorContains(t, preflightClawback(c, allRules()), "temMALFORMED")
	})
	t.Run("IOU arm: Holder beats native XRP amount", func(t *testing.T) {
		c := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: tx.NewXRPAmount(1000000),
			Holder: "rSomeone",
		}
		require.ErrorContains(t, preflightClawback(c, allRules()), "temMALFORMED")
	})

	// Finding 2 — MPT arm: the holder-shape temMALFORMED checks (missing holder,
	// holder==account) win over a zero/negative amount's temBAD_AMOUNT.
	t.Run("MPT arm: missing Holder beats zero amount", func(t *testing.T) {
		c := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: newTestMPTAmount(0, "rIssuer"), // zero → temBAD_AMOUNT if reached
			// Holder omitted → temMALFORMED must fire first
		}
		require.ErrorContains(t, preflightClawback(c, allRules()), "temMALFORMED")
	})
	t.Run("MPT arm: Holder==Account beats zero amount", func(t *testing.T) {
		c := &Clawback{
			BaseTx: *tx.NewBaseTx(tx.TypeClawback, "rIssuer"),
			Amount: newTestMPTAmount(0, "rIssuer"),
			Holder: "rIssuer", // == Account → temMALFORMED before amount check
		}
		require.ErrorContains(t, preflightClawback(c, allRules()), "temMALFORMED")
	})
}

// Flatten Tests

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

// Constructor Tests

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

// Amendment Tests

func TestClawbackRequiredAmendments(t *testing.T) {
	t.Run("IOU clawback requires Clawback amendment", func(t *testing.T) {
		clawbackTx := NewClawback("rIssuer", tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"))
		amendments := clawbackTx.RequiredAmendments()
		assert.Contains(t, amendments, amendment.FeatureClawback)
		assert.NotContains(t, amendments, amendment.FeatureMPTokensV1)
	})

	t.Run("MPToken clawback gates on Clawback only; MPTokensV1 is a preflight-arm gate", func(t *testing.T) {
		clawbackTx := NewMPTokenClawback("rIssuer", "rHolder", testMPTIssuanceID, newTestMPTAmount(100, "rIssuer"))
		amendments := clawbackTx.RequiredAmendments()
		assert.Equal(t, [][32]byte{amendment.FeatureClawback}, amendments)
		// MPTokensV1 is enforced inside the MPT preflight arm (temDISABLED), not
		// as a macro gate — so a bad flag/fee is not masked by temDISABLED.
		rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).DisableByName("MPTokensV1").Build()
		require.ErrorContains(t, clawbackTx.PreflightRules(rules), "temDISABLED")
	})
}

// Transaction Type Tests

func TestClawbackTransactionType(t *testing.T) {
	clawbackTx := &Clawback{}
	assert.Equal(t, tx.TypeClawback, clawbackTx.TxType())
}
