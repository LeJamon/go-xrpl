package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
)

func loanBrokerPseudoAccount(t *testing.T, f *loanSetAssetFixture) [20]byte {
	t.Helper()
	data, err := f.env.LedgerEntry(keylet.LoanBrokerByID(f.brokerKey))
	if err != nil {
		t.Fatalf("read LoanBroker: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode LoanBroker: %v", err)
	}
	address, ok := fields["Account"].(string)
	if !ok {
		t.Fatalf("LoanBroker Account = %#v, want address", fields["Account"])
	}
	account, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("decode LoanBroker Account: %v", err)
	}
	return account
}

func deepFreezeLoanSetTrustLine(t *testing.T, f *loanSetAssetFixture, account [20]byte) {
	t.Helper()
	lineKey := keylet.Line(account, f.issuer.AccountID(), "USD")
	data, err := f.env.LedgerEntry(lineKey)
	if err != nil {
		t.Fatalf("read trust line: %v", err)
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		t.Fatalf("parse trust line: %v", err)
	}
	if state.CompareAccountIDs(f.issuer.AccountID(), account) > 0 {
		line.Flags |= state.LsfHighFreeze | state.LsfHighDeepFreeze
	} else {
		line.Flags |= state.LsfLowFreeze | state.LsfLowDeepFreeze
	}
	data, err = state.SerializeRippleState(line)
	if err != nil {
		t.Fatalf("serialize trust line: %v", err)
	}
	if err := f.env.Ledger().Update(lineKey, data); err != nil {
		t.Fatalf("update trust line: %v", err)
	}
}

func loanVaultPseudoAccount(t *testing.T, f *loanSetAssetFixture) [20]byte {
	t.Helper()
	return lendingAccountID(t, decodeLendingEntry(t, f.env, f.vaultKey), "Vault")
}

func TestLoanManageDefaultFreezeBoundaries(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "legacy"
		if cleanup {
			name = "fixCleanup3_4_0"
		}
		t.Run(name, func(t *testing.T) {
			env := newPaymentLegacyLendingEnvWithCleanup(t, cleanup)
			f := newLoanSetAssetFixtureWithEnv(t, env, "IOU", false)
			f.createHolding(f.owner)
			f.createHolding(f.borrower)
			f.fundHolding(f.owner, 10_000)

			brokerSequence := env.Seq(f.owner)
			brokerSet := lending.NewLoanBrokerSet(f.owner.Address, f.vaultID)
			coverRate := uint32(100_000)
			brokerSet.CoverRateMinimum = &coverRate
			brokerSet.CoverRateLiquidation = &coverRate
			jtx.RequireTxSuccess(t, env.Submit(brokerSet))
			f.brokerID = brokerID(f.owner, brokerSequence)
			f.brokerKey = keylet.LoanBroker(f.owner.AccountID(), brokerSequence).Key
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(
				f.owner.Address,
				f.brokerID,
				f.amount(10_000),
			)))

			loanID, loanKey := createMatrixLoan(t, f, false)
			deepFreezeLoanSetTrustLine(t, f, loanBrokerPseudoAccount(t, f))
			deepFreezeLoanSetTrustLine(t, f, loanVaultPseudoAccount(t, f))

			loan := decodeLendingEntry(t, env, loanKey)
			nextDue, ok := loan["NextPaymentDueDate"].(uint32)
			if !ok {
				t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
			}
			grace, ok := loan["GracePeriod"].(uint32)
			if !ok {
				t.Fatalf("Loan GracePeriod = %v, want uint32", loan["GracePeriod"])
			}

			// A regular payment remains blocked by the frozen destination lines;
			// the default exemption is limited to LoanManage with tfLoanDefault.
			env.CloseToParentCloseTime(nextDue)
			periodic := matrixNumber(t, loan, "PeriodicPayment")
			payment := lending.NewLoanPay(f.borrower.Address, loanID, matrixAmount(t, f, periodic.Add(state.NewNumberContext(state.MantissaScaleLarge, true).Int(1))))
			payResult := env.Submit(payment)
			jtx.RequireTxClaimed(t, payResult, jtx.TecFROZEN)

			env.CloseToParentCloseTime(nextDue + grace + 1)
			manage := lending.NewLoanManage(f.owner.Address, loanID)
			flags := lending.TfLoanDefault
			manage.GetCommon().Flags = &flags
			defaultResult := env.Submit(manage)
			if cleanup {
				jtx.RequireTxSuccess(t, defaultResult)
			} else {
				jtx.RequireTxClaimed(t, defaultResult, jtx.TecINVARIANT_FAILED)
			}
		})
	}
}

func TestLoanSetBrokerDeepFreeze(t *testing.T) {
	tests := []struct {
		name   string
		target func(*testing.T, *loanSetAssetFixture) [20]byte
	}{
		{
			name: "owner",
			target: func(_ *testing.T, f *loanSetAssetFixture) [20]byte {
				return f.owner.AccountID()
			},
		},
		{
			name:   "pseudo-account",
			target: loanBrokerPseudoAccount,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, "IOU")
			f.createHolding(f.owner)
			deepFreezeLoanSetTrustLine(t, f, test.target(t, f))

			jtx.RequireTxClaimed(t, submitLoanSet(t, f, f.borrower, f.owner, false), jtx.TecFROZEN)
			if f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) || f.env.LedgerEntryExists(f.holdingKey(f.borrower)) {
				t.Fatal("deep-frozen LoanSet committed loan-related state")
			}
		})
	}

	t.Run("unfrozen", func(t *testing.T) {
		f := newLoanSetAssetFixture(t, "IOU")
		f.createHolding(f.owner)
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
		assertLoanSetOwnedObjects(t, f, f.borrower)
	})
}
