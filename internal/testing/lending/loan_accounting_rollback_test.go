package lending_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newAccountingAssetFixture(t *testing.T, cash, cleanup bool, kind string) *loanSetAssetFixture {
	t.Helper()
	var env *jtx.TestEnv
	if cash {
		env = newCashLendingEnvWithCleanup(t, cleanup)
	} else {
		env = newLegacyLendingEnvWithCleanup(t, cleanup)
	}
	return newLoanSetAssetFixtureWithEnv(t, env, kind, cash)
}

func accountingLoanPay(t *testing.T, f *loanSetAssetFixture, loanID string, amount tx.Amount) *lending.LoanPay {
	t.Helper()
	payment := lending.NewLoanPay(f.borrower.Address, loanID, amount)
	payment.GetCommon().Fee = "20"
	return payment
}

func assertAccountingEntriesEqual(t *testing.T, name string, want, got map[string]any) {
	t.Helper()
	require.Equal(t, want, got, "%s changed", name)
}

func TestLoanPayAssetPaymentRollbackAndAccounting(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, kind := range []string{"IOU", "MPT"} {
				t.Run(fmt.Sprintf("cash=%t/cleanup=%t/%s", cash, cleanup, kind), func(t *testing.T) {
					f := newAccountingAssetFixture(t, cash, cleanup, kind)
					loanID, loanKey := createMatrixLoan(t, f, false)
					f.fundHolding(f.borrower, 1_000)

					brokerKey := keylet.LoanBrokerByID(f.brokerKey)
					holdingKey := f.holdingKey(f.borrower)
					loanBefore := decodeLendingEntry(t, f.env, loanKey)
					vaultBefore := decodeLendingEntry(t, f.env, f.vaultKey)
					brokerBefore := decodeLendingEntry(t, f.env, brokerKey)
					holdingBefore := decodeLendingEntry(t, f.env, holdingKey)
					periodic := matrixNumber(t, loanBefore, "PeriodicPayment")
					one := state.NewNumberContext(state.MantissaScaleLarge, true).Int(1)
					if periodic.Cmp(one) <= 0 {
						t.Fatalf("PeriodicPayment = %s, want more than one asset unit", periodic.String())
					}

					failedBalance := f.env.Balance(f.borrower)
					failedSequence := f.env.Seq(f.borrower)
					failed := f.env.Submit(accountingLoanPay(
						t,
						f,
						loanID,
						matrixAmount(t, f, periodic.Sub(one)),
					))
					jtx.RequireTxClaimed(t, failed, "tecINSUFFICIENT_PAYMENT")
					require.Equal(t, failedBalance-failed.Fee, f.env.Balance(f.borrower))
					require.Equal(t, failedSequence+1, f.env.Seq(f.borrower))
					assertAccountingEntriesEqual(t, "Loan after failed payment", loanBefore, decodeLendingEntry(t, f.env, loanKey))
					assertAccountingEntriesEqual(t, "Vault after failed payment", vaultBefore, decodeLendingEntry(t, f.env, f.vaultKey))
					assertAccountingEntriesEqual(t, "LoanBroker after failed payment", brokerBefore, decodeLendingEntry(t, f.env, brokerKey))
					assertAccountingEntriesEqual(t, "Holding after failed payment", holdingBefore, decodeLendingEntry(t, f.env, holdingKey))

					loanBefore = decodeLendingEntry(t, f.env, loanKey)
					vaultBefore = decodeLendingEntry(t, f.env, f.vaultKey)
					brokerBefore = decodeLendingEntry(t, f.env, brokerKey)
					holdingBefore = decodeLendingEntry(t, f.env, holdingKey)
					paymentBalance := f.env.Balance(f.borrower)
					paymentSequence := f.env.Seq(f.borrower)
					payment := f.env.Submit(accountingLoanPay(
						t,
						f,
						loanID,
						matrixAmount(t, f, periodic.Add(one)),
					))
					jtx.RequireTxSuccess(t, payment)
					require.Equal(t, paymentBalance-payment.Fee, f.env.Balance(f.borrower))
					require.Equal(t, paymentSequence+1, f.env.Seq(f.borrower))

					loanAfter := decodeLendingEntry(t, f.env, loanKey)
					vaultAfter := decodeLendingEntry(t, f.env, f.vaultKey)
					brokerAfter := decodeLendingEntry(t, f.env, brokerKey)
					holdingAfter := decodeLendingEntry(t, f.env, holdingKey)
					principalPaid := matrixNumber(t, loanBefore, "PrincipalOutstanding").Sub(matrixNumber(t, loanAfter, "PrincipalOutstanding"))
					loanValuePaid := matrixNumber(t, loanBefore, "TotalValueOutstanding").Sub(matrixNumber(t, loanAfter, "TotalValueOutstanding"))
					managementFeePaid := matrixNumber(t, loanBefore, "ManagementFeeOutstanding").Sub(matrixNumber(t, loanAfter, "ManagementFeeOutstanding"))
					trackedInterest := loanValuePaid.Sub(principalPaid).Sub(managementFeePaid)
					if principalPaid.Signum() <= 0 || trackedInterest.Signum() <= 0 {
						t.Fatalf("payment deltas principal=%s trackedInterest=%s, want both positive", principalPaid.String(), trackedInterest.String())
					}
					remaining, ok := loanAfter["PaymentRemaining"].(uint32)
					if !ok {
						t.Fatalf("Loan PaymentRemaining = %v, want uint32", loanAfter["PaymentRemaining"])
					}
					beforeRemaining, ok := loanBefore["PaymentRemaining"].(uint32)
					if !ok {
						t.Fatalf("Loan PaymentRemaining before payment = %v, want uint32", loanBefore["PaymentRemaining"])
					}
					require.Equal(t, beforeRemaining-1, remaining)

					debtDelta := matrixNumber(t, brokerAfter, "DebtTotal").Sub(matrixNumber(t, brokerBefore, "DebtTotal"))
					assetsDelta := matrixNumber(t, vaultAfter, "AssetsTotal").Sub(matrixNumber(t, vaultBefore, "AssetsTotal"))
					availableDelta := matrixNumber(t, vaultAfter, "AssetsAvailable").Sub(matrixNumber(t, vaultBefore, "AssetsAvailable"))
					if cash {
						requireMatrixNumberEqual(t, "cash DebtTotal delta", debtDelta, principalPaid.Negate())
						requireMatrixNumberEqual(t, "cash AssetsTotal delta", assetsDelta, trackedInterest)
					} else {
						requireMatrixNumberEqual(t, "legacy DebtTotal delta", debtDelta, principalPaid.Add(trackedInterest).Negate())
						requireMatrixNumberEqual(t, "legacy AssetsTotal delta", assetsDelta, state.NewXRPLNumber(0, 0))
					}
					requireMatrixNumberEqual(t, "AssetsAvailable delta", availableDelta, principalPaid.Add(trackedInterest))
					require.NotEqual(t, holdingBefore, holdingAfter, "borrower holding should receive the payment debit")
				})
			}
		}
	}
}
