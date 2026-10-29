package lending_test

import (
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestLoanBrokerSetXRPValueRange(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		for _, tc := range []struct {
			name  string
			value string
			want  ter.Result
		}{
			{"maximum", "100000000000000000", ter.TesSUCCESS},
			{"above_maximum", "100000000000000001", ter.TefEXCEPTION},
			{"rounds_to_maximum", "100000000000000000.4", ter.TecPRECISION_LOSS},
			{"tie_rounds_to_maximum", "100000000000000000.5", ter.TecPRECISION_LOSS},
			{"rounds_above_maximum", "100000000000000000.6", ter.TefEXCEPTION},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				env := newLendingEnv(t)
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
				require.Equal(t, tc.want.String(), result.Code)
				if tc.want == ter.TefEXCEPTION {
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
				}
			})
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
		t.Run(tc.name, func(t *testing.T) {
			f := newLoanValueRangeFixture(t, tc.kind)
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
			require.Equal(t, tc.want.String(), result.Code)
			if tc.want == ter.TefEXCEPTION {
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
				if tc.want != ter.TesSUCCESS {
					require.Equal(t, balance-result.Fee, f.env.Balance(f.borrower))
				}
			}
		})
	}
}
