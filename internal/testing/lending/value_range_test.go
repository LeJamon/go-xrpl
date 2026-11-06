package lending_test

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// setValueRangeCleanup toggles fixCleanup3_5_0, under which a value out of the
// asset's range is rejected with tecPRECISION_LOSS instead of tefEXCEPTION.
func setValueRangeCleanup(env *jtx.TestEnv, cleanup bool) {
	if cleanup {
		env.EnableFeature("fixCleanup3_5_0")
	} else {
		env.DisableFeature("fixCleanup3_5_0")
	}
	env.Close()
}

func valueRangeName(cleanup bool) string {
	if cleanup {
		return "with fixCleanup3_5_0"
	}
	return "without fixCleanup3_5_0"
}

func valueRangeResult(want ter.Result, cleanup bool) ter.Result {
	if cleanup && want == ter.TefEXCEPTION {
		return ter.TecPRECISION_LOSS
	}
	return want
}

func TestLoanBrokerSetXRPValueRange(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, mode := range []string{"create", "update"} {
			for _, tc := range []struct {
				name  string
				value string
				want  ter.Result
			}{
				{"maximum", "100000000000000000", ter.TesSUCCESS},
				{"fractional", "1000.5", ter.TecPRECISION_LOSS},
				{"above_maximum", "100000000000000001", ter.TefEXCEPTION},
				{"rounds_to_maximum", "100000000000000000.4", ter.TecPRECISION_LOSS},
				{"tie_rounds_to_maximum", "100000000000000000.5", ter.TecPRECISION_LOSS},
				{"rounds_above_maximum", "100000000000000000.6", ter.TefEXCEPTION},
			} {
				t.Run(valueRangeName(cleanup)+"/"+mode+"/"+tc.name, func(t *testing.T) {
					env := newLendingEnv(t)
					setValueRangeCleanup(env, cleanup)
					want := valueRangeResult(tc.want, cleanup)
					owner := jtx.NewAccount("range-owner")
					env.FundAmount(owner, uint64(jtx.XRP(100_000)))
					vid := setupXRPVault(t, env, owner, 1000)
					id := brokerID(owner, env.Seq(owner))
					if mode == "update" {
						jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
					}
					env.Close()
					set := lending.NewLoanBrokerSet(owner.Address, vid)
					set.DebtMaximum = &tc.value
					if mode == "update" {
						set.LoanBrokerID = &id
					}
					before := loanSetLedgerState(t, env)
					balance, sequence, owners := env.Balance(owner), env.Seq(owner), env.OwnerCount(owner)
					result := env.Submit(set)
					require.Equal(t, want.String(), result.Code)
					if want == ter.TefEXCEPTION {
						require.False(t, result.Applied)
						require.Zero(t, result.Fee)
						require.Nil(t, result.Metadata)
						require.Equal(t, before, loanSetLedgerState(t, env))
						env.Close()
						require.Equal(t, balance, env.Balance(owner))
						require.Equal(t, sequence, env.Seq(owner))
						require.Equal(t, owners, env.OwnerCount(owner))
					} else {
						require.True(t, result.Applied)
						require.Equal(t, uint64(10), result.Fee)
						require.Equal(t, balance-result.Fee, env.Balance(owner))
						require.Equal(t, sequence+1, env.Seq(owner))
						if want == ter.TecPRECISION_LOSS {
							require.NotNil(t, result.Metadata)
							require.Equal(t, owners, env.OwnerCount(owner))
							after := loanSetLedgerState(t, env)
							delete(before, keylet.Account(owner.AccountID()).Key)
							delete(after, keylet.Account(owner.AccountID()).Key)
							require.Equal(t, before, after)
						}
					}
				})
			}
		}
	}
}

func newLoanValueRangeFixture(t *testing.T, kind string) *loanSetAssetFixture {
	t.Helper()
	if kind != "XRP" {
		return newLoanSetAssetFixture(t, kind)
	}
	env := newLendingEnv(t)
	owner, borrower := jtx.NewAccount("owner"), jtx.NewAccount("borrower")
	env.FundAmount(owner, uint64(jtx.XRP(100_000)))
	env.FundAmount(borrower, uint64(jtx.XRP(100_000)))
	vid := setupXRPVault(t, env, owner, 10_000)
	id := brokerID(owner, env.Seq(owner))
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
	env.Close()
	return &loanSetAssetFixture{env: env, owner: owner, borrower: borrower, brokerID: id}
}

func TestLoanSetAssetValueRange(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			kind      string
			principal string
			feeField  string
			feeValue  string
			want      ter.Result
		}{
			{"xrp_principal_above_maximum", "XRP", "100000000000000001", "", "", ter.TefEXCEPTION},
			{"xrp_origination_fee_above_maximum", "XRP", "100000000000000001", "LoanOriginationFee", "100000000000000001", ter.TefEXCEPTION},
			{"xrp_service_fee_above_maximum", "XRP", "1000", "LoanServiceFee", "100000000000000001", ter.TefEXCEPTION},
			{"xrp_late_fee_above_maximum", "XRP", "1000", "LatePaymentFee", "100000000000000001", ter.TefEXCEPTION},
			{"xrp_close_fee_above_maximum", "XRP", "1000", "ClosePaymentFee", "100000000000000001", ter.TefEXCEPTION},
			{"xrp_principal_at_maximum", "XRP", "100000000000000000", "", "", ter.TecINSUFFICIENT_FUNDS},
			{"xrp_service_fee_at_maximum", "XRP", "1000", "LoanServiceFee", "100000000000000000", ter.TesSUCCESS},
			{"xrp_fractional_principal", "XRP", "1000.5", "", "", ter.TecPRECISION_LOSS},
			{"xrp_fee_rounds_to_maximum", "XRP", "1000", "ClosePaymentFee", "100000000000000000.4", ter.TecPRECISION_LOSS},
			{"xrp_fee_rounds_above_maximum", "XRP", "1000", "ClosePaymentFee", "100000000000000000.6", ter.TefEXCEPTION},
			{"xrp_principal_precision_before_fee_range", "XRP", "1000.5", "ClosePaymentFee", "100000000000000001", ter.TecPRECISION_LOSS},
			{"mpt_overflow", "MPT", "1e19", "", "", ter.TefEXCEPTION},
			{"iou_overflow", "IOU", "1e96", "", "", ter.TefEXCEPTION},
			{"iou_underflow", "IOU", "1e-200", "", "", ter.TecPRECISION_LOSS},
		} {
			t.Run(valueRangeName(cleanup)+"/"+tc.name, func(t *testing.T) {
				f := newLoanValueRangeFixture(t, tc.kind)
				setValueRangeCleanup(f.env, cleanup)
				want := valueRangeResult(tc.want, cleanup)
				set := lending.NewLoanSet(f.borrower.Address, f.brokerID, tc.principal)
				switch tc.feeField {
				case "LoanOriginationFee":
					set.LoanOriginationFee = &tc.feeValue
				case "LoanServiceFee":
					set.LoanServiceFee = &tc.feeValue
				case "LatePaymentFee":
					set.LatePaymentFee = &tc.feeValue
				case "ClosePaymentFee":
					set.ClosePaymentFee = &tc.feeValue
				}
				set.Counterparty = f.owner.Address
				set.GetCommon().Fee = "20"
				set.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
				signature, err := txsign.SignCounterpartyWithRules(set,
					strings.ToUpper(f.owner.PublicKeyHex()),
					"00"+strings.ToUpper(f.owner.PrivateKeyHex()), f.env.Rules())
				require.NoError(t, err)
				set.GetCommon().CounterpartySignature = signature
				before := loanSetLedgerState(t, f.env)
				balance, sequence, owners := f.env.Balance(f.borrower), f.env.Seq(f.borrower), f.env.OwnerCount(f.borrower)
				result := f.env.Submit(set)
				require.Equal(t, want.String(), result.Code)
				if want == ter.TefEXCEPTION {
					require.False(t, result.Applied)
					require.Zero(t, result.Fee)
					require.Nil(t, result.Metadata)
					require.Equal(t, before, loanSetLedgerState(t, f.env))
					f.env.Close()
					require.Equal(t, balance, f.env.Balance(f.borrower))
					require.Equal(t, sequence, f.env.Seq(f.borrower))
					require.Equal(t, owners, f.env.OwnerCount(f.borrower))
				} else {
					require.True(t, result.Applied)
					require.Equal(t, uint64(20), result.Fee)
					require.Equal(t, sequence+1, f.env.Seq(f.borrower))
					if want != ter.TesSUCCESS {
						require.Equal(t, balance-result.Fee, f.env.Balance(f.borrower))
					}
				}
			})
		}
	}
}

func TestLoanBrokerSetMPTDebtMaximumBoundaries(t *testing.T) {
	scales := []struct {
		name   string
		fix320 bool
		fix330 bool
	}{
		{name: "legacy"},
		{name: "large320", fix320: true},
		{name: "large330", fix330: true},
	}
	values := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "exact maximum", value: "9223372036854775807", valid: true},
		{name: "below maximum", value: "9223372036854775806", valid: true},
		// The mantissa is a valid signed wire value; exponent 1 puts the
		// canonical value above the MPT maximum without using raw 2^63.
		{name: "canonical above maximum", value: "922337203685477581e1"},
	}

	for _, scale := range scales {
		for _, tc := range values {
			t.Run(scale.name+"/"+tc.name, func(t *testing.T) {
				env := newLendingEnv(t)
				if scale.fix320 {
					env.EnableFeature("fixCleanup3_2_0")
				} else {
					env.DisableFeature("fixCleanup3_2_0")
				}
				if scale.fix330 {
					env.EnableFeature("fixCleanup3_3_0")
				} else {
					env.DisableFeature("fixCleanup3_3_0")
				}
				env.Close()
				f := newLoanSetAssetFixtureWithEnv(t, env, "MPT", false)
				brokerKey := keylet.LoanBrokerByID(f.brokerKey)
				require.Equal(t, scale.fix320, f.env.Rules().Enabled(amendment.FeatureFixCleanup3_2_0))
				require.Equal(t, scale.fix330, f.env.Rules().Enabled(amendment.FeatureFixCleanup3_3_0))

				before := loanSetLedgerState(t, f.env)
				value := tc.value
				set := lending.NewLoanBrokerSet(f.owner.Address, f.vaultID)
				set.LoanBrokerID = &f.brokerID
				set.DebtMaximum = &value
				result := f.env.Submit(set)

				if tc.valid {
					jtx.RequireTxSuccess(t, result)
					assertLendingField(t, "LoanBroker", decodeLendingEntry(t, f.env, brokerKey), "DebtMaximum", tc.value)
					return
				}

				require.Equal(t, ter.TemINVALID.String(), result.Code)
				require.False(t, result.Applied)
				require.Zero(t, result.Fee)
				require.Nil(t, result.Metadata)
				require.Equal(t, before, loanSetLedgerState(t, f.env))
			})
		}
	}
}
