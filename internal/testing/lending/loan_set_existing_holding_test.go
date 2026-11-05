package lending_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttest "github.com/LeJamon/go-xrpl/internal/testing/accountset"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

type loanSetExistingHoldingCase struct {
	name            string
	cleanup         bool
	borrowerHolding bool
	ownerHolding    bool
	feePresent      bool
	feeValue        string
	feePositive     bool
	wantCode        string
}

func TestLoanSetExistingIOUHoldingAfterIssuerClearsDefaultRipple(t *testing.T) {
	// With no origination fee, the owner's missing line is checked during Apply,
	// where strong authorization returns tecNO_LINE.
	tests := []loanSetExistingHoldingCase{
		{
			name:     "legacy/borrower-absent/owner-absent/fee-absent",
			wantCode: jtx.TerNO_RIPPLE,
		},
		{
			name:            "legacy/borrower-present/owner-present/fee-absent",
			borrowerHolding: true,
			ownerHolding:    true,
			wantCode:        jtx.TerNO_RIPPLE,
		},
		{
			name:     "cleanup/borrower-absent/owner-absent/fee-absent",
			cleanup:  true,
			wantCode: jtx.TerNO_RIPPLE,
		},
		{
			name:            "cleanup/borrower-present/owner-absent/fee-absent",
			cleanup:         true,
			borrowerHolding: true,
			wantCode:        jtx.TecNO_LINE,
		},
		{
			name:            "cleanup/borrower-present/owner-absent/fee-explicit-zero",
			cleanup:         true,
			borrowerHolding: true,
			feePresent:      true,
			feeValue:        "0",
			wantCode:        jtx.TecNO_LINE,
		},
		{
			name:            "cleanup/borrower-present/owner-absent/fee-positive",
			cleanup:         true,
			borrowerHolding: true,
			feePresent:      true,
			feeValue:        "1",
			feePositive:     true,
			wantCode:        jtx.TerNO_RIPPLE,
		},
		{
			name:            "cleanup/borrower-present/owner-present/fee-absent",
			cleanup:         true,
			borrowerHolding: true,
			ownerHolding:    true,
			wantCode:        jtx.TesSUCCESS,
		},
		{
			name:            "cleanup/borrower-present/owner-present/fee-explicit-zero",
			cleanup:         true,
			borrowerHolding: true,
			ownerHolding:    true,
			feePresent:      true,
			feeValue:        "0",
			wantCode:        jtx.TesSUCCESS,
		},
		{
			name:            "cleanup/borrower-present/owner-present/fee-positive",
			cleanup:         true,
			borrowerHolding: true,
			ownerHolding:    true,
			feePresent:      true,
			feeValue:        "1",
			feePositive:     true,
			wantCode:        jtx.TesSUCCESS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCashLendingEnvWithCleanup(t, tc.cleanup)
			if env.Rules().Enabled(amendment.FeatureFixCleanup3_4_0) != tc.cleanup {
				t.Fatal("unexpected fixCleanup3_4_0 state")
			}
			if !env.Rules().Enabled(amendment.FeatureLendingProtocolV1_1) {
				t.Fatal("LendingProtocolV1_1 is disabled")
			}
			f := newLoanSetAssetFixtureWithEnv(t, env, "IOU", true)
			if tc.borrowerHolding {
				f.createHolding(f.borrower)
			}
			if tc.ownerHolding {
				f.createHolding(f.owner)
			}

			jtx.RequireTxSuccess(t, env.Submit(accounttest.AccountSet(f.issuer).
				ClearFlag(accounttx.AccountSetFlagDefaultRipple).
				Build()))
			env.Close()
			if env.AccountInfo(f.issuer).Flags&state.LsfDefaultRipple != 0 {
				t.Fatal("issuer DefaultRipple remained enabled")
			}

			before := loanSetLedgerState(t, env)
			beforeBorrowerBalance := env.Balance(f.borrower)
			beforeBorrowerSequence := env.Seq(f.borrower)
			beforeBorrowerOwnerCount := env.OwnerCount(f.borrower)
			beforeOwnerOwnerCount := env.OwnerCount(f.owner)
			result := submitLoanSetWithOriginationFee(t, f, tc.feePresent, tc.feeValue)

			switch tc.wantCode {
			case jtx.TesSUCCESS:
				jtx.RequireTxSuccess(t, result)
				loan := decodeLendingEntry(t, env, keylet.Loan(f.brokerKey, 1))
				assertLendingField(t, "Loan", loan, "Borrower", f.borrower.Address)
				assertLendingField(t, "Loan", loan, "PrincipalOutstanding", "1000")
				if tc.feePositive {
					assertLendingField(t, "Loan", loan, "LoanOriginationFee", "1")
				}
				borrowerWant := int64(1000)
				ownerWant := int64(0)
				if tc.feePositive {
					borrowerWant = 999
					ownerWant = 1
				}
				assertLoanSetIOUBalance(t, f, f.borrower, borrowerWant)
				assertLoanSetIOUBalance(t, f, f.owner, ownerWant)
				if got := env.OwnerCount(f.borrower); got != beforeBorrowerOwnerCount+1 {
					t.Errorf("borrower OwnerCount = %d, want %d", got, beforeBorrowerOwnerCount+1)
				}
				if got := env.OwnerCount(f.owner); got != beforeOwnerOwnerCount {
					t.Errorf("owner OwnerCount = %d, want %d", got, beforeOwnerOwnerCount)
				}

			case jtx.TerNO_RIPPLE:
				jtx.RequireTxFail(t, result, tc.wantCode)
				if result.Applied {
					t.Fatal("terNO_RIPPLE transaction was applied")
				}
				assertLoanSetLedgerUnchanged(t, before, loanSetLedgerState(t, env), "terNO_RIPPLE")

			case jtx.TecNO_LINE:
				jtx.RequireTxClaimed(t, result, tc.wantCode)
				if !result.Applied || result.Fee != 20 {
					t.Fatalf("claimed failure applied/fee = %t/%d, want true/20", result.Applied, result.Fee)
				}
				after := loanSetLedgerState(t, env)
				borrowerKey := keylet.Account(f.borrower.AccountID()).Key
				delete(before, borrowerKey)
				delete(after, borrowerKey)
				assertLoanSetLedgerUnchanged(t, before, after, "tecNO_LINE")
				if got := env.Balance(f.borrower); got != beforeBorrowerBalance-result.Fee {
					t.Errorf("borrower balance after claimed failure = %d, want %d", got, beforeBorrowerBalance-result.Fee)
				}
				if got := env.Seq(f.borrower); got != beforeBorrowerSequence+1 {
					t.Errorf("borrower sequence after claimed failure = %d, want %d", got, beforeBorrowerSequence+1)
				}

			default:
				t.Fatalf("unsupported expected result %q", tc.wantCode)
			}
		})
	}
}

func assertLoanSetLedgerUnchanged(t *testing.T, before, after map[[32]byte][]byte, result string) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("ledger entry count after %s = %d, want %d", result, len(after), len(before))
	}
	for key, want := range before {
		got, ok := after[key]
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("ledger entry %X changed after %s", key, result)
		}
	}
}

func submitLoanSetWithOriginationFee(t *testing.T, f *loanSetAssetFixture, present bool, value string) jtx.TxResult {
	t.Helper()
	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	loanSet.Counterparty = f.owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	if present {
		fee := value
		loanSet.LoanOriginationFee = &fee
	}
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
		f.env.Rules(),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	return f.env.Submit(loanSet)
}

func assertLoanSetIOUBalance(t *testing.T, f *loanSetAssetFixture, account *jtx.Account, want int64) {
	t.Helper()
	if got := f.env.IOUBalance(account, f.issuer, "USD"); got.Compare(f.amount(want)) != 0 {
		t.Errorf("%s USD balance = %s, want %d", account.Name, got.Value(), want)
	}
}
