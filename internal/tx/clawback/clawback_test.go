package clawback

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMPTIssuanceID = "000000000000000000000001000000000000000000000001"

func newTestMPTAmount(value int64, issuer string) state.Amount {
	return state.NewMPTAmountWithIssuanceID(value, issuer, testMPTIssuanceID)
}

type clawbackWithTopLevelMPTID struct {
	*Clawback
}

func (c *clawbackWithTopLevelMPTID) Flatten() (map[string]any, error) {
	values, err := c.Clawback.Flatten()
	if err != nil {
		return nil, err
	}
	values["MPTokenIssuanceID"] = testMPTIssuanceID
	return values, nil
}

// preflightClawback runs Clawback's preflight body in engine order: the
// preflight0 flags mask (GetFlagsMask), the rules-free Validate(), then
// PreflightRules() (the amount/holder body). The engine invokes them in exactly
// this sequence.
func preflightClawback(c *Clawback, rules *amendment.Rules) error {
	if c.GetFlags()&c.GetFlagsMask(rules) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "invalid flags")
	}
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
			Amount: newTestMPTAmount(100, "rIssuer"),
			Holder: "rHolder",
		}

		flat, err := clawbackTx.Flatten()
		require.NoError(t, err)

		assert.Equal(t, "rIssuer", flat["Account"])
		assert.Equal(t, "rHolder", flat["Holder"])
		assert.NotContains(t, flat, "MPTokenIssuanceID")
		amtMap, ok := flat["Amount"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "100", amtMap["value"])
		assert.Equal(t, testMPTIssuanceID, amtMap["mpt_issuance_id"])
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
		clawbackTx := NewMPTokenClawback("rIssuer", "rHolder", newTestMPTAmount(100, "rIssuer"))
		require.NotNil(t, clawbackTx)
		assert.Equal(t, "rIssuer", clawbackTx.Account)
		assert.Equal(t, "rHolder", clawbackTx.Holder)
		assert.Equal(t, testMPTIssuanceID, clawbackTx.Amount.MPTIssuanceID())
		assert.Equal(t, tx.TypeClawback, clawbackTx.TxType())
	})
}

func TestMPTokenClawbackWireGolden(t *testing.T) {
	clawbackTx := NewMPTokenClawback(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		newTestMPTAmount(100, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
	)
	seq := uint32(1)
	clawbackTx.Sequence = &seq
	clawbackTx.Fee = "10"

	jsonBytes, err := json.Marshal(clawbackTx)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":{"value":"100","mpt_issuance_id":"000000000000000000000001000000000000000000000001"},
		"Fee":"10",
		"Holder":"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		"Sequence":1,
		"TransactionType":"Clawback"
	}`, string(jsonBytes))
	assert.NotContains(t, string(jsonBytes), "MPTokenIssuanceID")

	wire, err := tx.SerializeTransaction(clawbackTx)
	require.NoError(t, err)
	assert.Equal(t,
		"12001E24000000016160000000000000006400000000000000000000000100000000000000000000000168400000000000000A73008114B5F762798A53D543A014CAF8B297CFF8F2F937E88B14550FC62003E785DC231A1058A05E56E3F09CF4E6",
		strings.ToUpper(hex.EncodeToString(wire)),
	)
	hash, err := tx.ComputeTransactionHash(clawbackTx)
	require.NoError(t, err)
	assert.Equal(t, "5C251B4B59260C9A86355912E7767860BAFF08E6E9FD93202B305202A4ECE3A1", strings.ToUpper(hex.EncodeToString(hash[:])))

	parsed, err := tx.ParseFromBinary(wire)
	require.NoError(t, err)
	parsedFields, err := parsed.Flatten()
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"value":           "100",
		"mpt_issuance_id": testMPTIssuanceID,
	}, parsedFields["Amount"])
	currentHash, err := tx.ComputeCurrentTransactionHash(parsed)
	require.NoError(t, err)
	assert.Equal(t, hash, currentHash)
	matches, err := tx.CurrentFieldsMatchRaw(parsed)
	require.NoError(t, err)
	assert.True(t, matches)
	flat, err := binarycodec.DecodeBytes(wire)
	require.NoError(t, err)
	assert.NotContains(t, flat, "MPTokenIssuanceID")
	amountJSON, err := json.Marshal(flat["Amount"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"100","mpt_issuance_id":"000000000000000000000001000000000000000000000001"}`, string(amountJSON))
}

func TestMPTokenClawbackTypedSerializationRejectsTopLevelIssuanceID(t *testing.T) {
	transaction := &clawbackWithTopLevelMPTID{Clawback: NewMPTokenClawback(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		newTestMPTAmount(100, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
	)}
	seq := uint32(1)
	transaction.Sequence = &seq
	transaction.Fee = "10"

	_, err := tx.SerializeTransaction(transaction)
	require.EqualError(t, err, "Field 'MPTokenIssuanceID' found in disallowed location.")
}

func TestMPTokenClawbackRawHashRejectsTopLevelIssuanceID(t *testing.T) {
	transaction := NewMPTokenClawback(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		newTestMPTAmount(100, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
	)
	seq := uint32(1)
	transaction.Sequence = &seq
	transaction.Fee = "10"

	fields, err := transaction.Flatten()
	require.NoError(t, err)
	tx.PopulateRequiredWireFields(fields, transaction.GetCommon())
	fields["MPTokenIssuanceID"] = testMPTIssuanceID
	wire, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	transaction.SetRawBytes(wire)

	_, err = tx.ComputeTransactionHash(transaction)
	require.EqualError(t, err, "Field 'MPTokenIssuanceID' found in disallowed location.")
}

// Amendment Tests

func TestClawbackRequiredAmendments(t *testing.T) {
	t.Run("IOU clawback has no amendment gate", func(t *testing.T) {
		clawbackTx := NewClawback("rIssuer", tx.NewIssuedAmountFromFloat64(100.0, "USD", "rHolder"))
		assert.Empty(t, clawbackTx.RequiredAmendments())
	})

	t.Run("MPToken clawback uses only the MPTokensV1 preflight-arm gate", func(t *testing.T) {
		clawbackTx := NewMPTokenClawback("rIssuer", "rHolder", newTestMPTAmount(100, "rIssuer"))
		assert.Empty(t, clawbackTx.RequiredAmendments())
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
