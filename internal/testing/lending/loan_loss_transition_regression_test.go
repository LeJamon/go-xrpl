package lending_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/stretchr/testify/require"
)

func lossExposure(t *testing.T, loan map[string]any, cash bool) state.XRPLNumber {
	t.Helper()
	if cash {
		return lossNumber(t, loan, "PrincipalOutstanding")
	}
	return lossNumber(t, loan, "TotalValueOutstanding").Sub(lossNumber(t, loan, "ManagementFeeOutstanding"))
}

func requireLoanFlags(t *testing.T, loan map[string]any, mask uint32, want bool) {
	t.Helper()
	flags, ok := loan["Flags"].(uint32)
	if !ok {
		t.Fatalf("Loan Flags = %v, want uint32", loan["Flags"])
	}
	if (flags&mask != 0) != want {
		t.Fatalf("Loan Flags = %d, mask %d presence = %t, want %t", flags, mask, flags&mask != 0, want)
	}
}

func TestLoanManageDirectDefaultAccounting(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("cash=%t/cleanup=%t", cash, cleanup), func(t *testing.T) {
				f := newLossLifecycleFixture(t, cash, cleanup)
				loanBefore := decodeLendingEntry(t, f.env, f.loanKey)
				vaultBefore := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerBefore := decodeLendingEntry(t, f.env, f.brokerKey)
				exposure := lossExposure(t, loanBefore, cash)
				nextDue, ok := loanBefore["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loanBefore["NextPaymentDueDate"])
				}
				grace, ok := loanBefore["GracePeriod"].(uint32)
				if !ok {
					t.Fatalf("Loan GracePeriod = %v, want uint32", loanBefore["GracePeriod"])
				}
				f.env.CloseToParentCloseTime(nextDue + grace + 1)
				submitLoanManage(t, f, lending.TfLoanDefault)

				loanAfter := decodeLendingEntry(t, f.env, f.loanKey)
				vaultAfter := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerAfter := decodeLendingEntry(t, f.env, f.brokerKey)
				covered := lossNumber(t, brokerBefore, "CoverAvailable").Sub(lossNumber(t, brokerAfter, "CoverAvailable"))
				if covered.Signum() <= 0 || covered.Cmp(exposure) >= 0 {
					t.Fatalf("direct default cover = %s, want positive amount below exposure %s", covered.String(), exposure.String())
				}
				requireLossNumberEqual(t, "DebtTotal delta", lossNumber(t, brokerAfter, "DebtTotal").Sub(lossNumber(t, brokerBefore, "DebtTotal")), exposure.Negate())
				requireLossNumberEqual(t, "AssetsTotal delta", lossNumber(t, vaultAfter, "AssetsTotal").Sub(lossNumber(t, vaultBefore, "AssetsTotal")), covered.Sub(exposure))
				requireLossNumberEqual(t, "AssetsAvailable delta", lossNumber(t, vaultAfter, "AssetsAvailable").Sub(lossNumber(t, vaultBefore, "AssetsAvailable")), covered)
				requireLossNumberEqual(t, "LossUnrealized after direct default", lossNumber(t, vaultAfter, "LossUnrealized"), state.NewXRPLNumber(0, 0))
				requireLoanFlags(t, loanAfter, lending.LsfLoanDefault, true)
				for _, field := range []string{"PrincipalOutstanding", "TotalValueOutstanding", "ManagementFeeOutstanding"} {
					requireLossNumberEqual(t, "Loan "+field+" after direct default", lossNumber(t, loanAfter, field), state.NewXRPLNumber(0, 0))
				}
				if remaining, present := loanAfter["PaymentRemaining"]; present {
					require.Equal(t, uint32(0), remaining)
				}
			})
		}
	}
}

func TestLoanPayReversesImpairmentWithExactAccounting(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("cash=%t/cleanup=%t", cash, cleanup), func(t *testing.T) {
				f := newLossLifecycleFixture(t, cash, cleanup)
				loanBeforeImpair := decodeLendingEntry(t, f.env, f.loanKey)
				exposure := lossExposure(t, loanBeforeImpair, cash)
				nextDue, ok := loanBeforeImpair["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loanBeforeImpair["NextPaymentDueDate"])
				}
				f.env.CloseToParentCloseTime(nextDue + 1)
				submitLoanManage(t, f, lending.TfLoanImpair)

				loanBeforePayment := decodeLendingEntry(t, f.env, f.loanKey)
				vaultBeforePayment := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerBeforePayment := decodeLendingEntry(t, f.env, f.brokerKey)
				requireLossNumberEqual(t, "LossUnrealized after impair", lossNumber(t, vaultBeforePayment, "LossUnrealized"), exposure)
				requireLoanFlags(t, loanBeforePayment, lending.LsfLoanImpaired, true)
				impairedDue, ok := loanBeforePayment["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("impaired Loan NextPaymentDueDate = %v, want uint32", loanBeforePayment["NextPaymentDueDate"])
				}
				f.env.CloseToParentCloseTime(impairedDue + 1)

				periodic := lossNumber(t, loanBeforePayment, "PeriodicPayment")
				paymentAmount := periodic.Add(state.NewNumberContext(state.MantissaScaleLarge, true).Int(1)).ToInt64WithMode(state.RoundUpward)
				payment := lending.NewLoanPay(f.borrower.Address, f.loanID, tx.NewXRPAmount(paymentAmount))
				if cleanup {
					late := lending.TfLoanLatePayment
					payment.GetCommon().Flags = &late
				}
				result := f.env.Submit(payment)
				jtx.RequireTxSuccess(t, result)

				loanAfterPayment := decodeLendingEntry(t, f.env, f.loanKey)
				vaultAfterPayment := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerAfterPayment := decodeLendingEntry(t, f.env, f.brokerKey)
				principalPaid := lossNumber(t, loanBeforePayment, "PrincipalOutstanding").Sub(lossNumber(t, loanAfterPayment, "PrincipalOutstanding"))
				loanValuePaid := lossNumber(t, loanBeforePayment, "TotalValueOutstanding").Sub(lossNumber(t, loanAfterPayment, "TotalValueOutstanding"))
				managementFeePaid := lossNumber(t, loanBeforePayment, "ManagementFeeOutstanding").Sub(lossNumber(t, loanAfterPayment, "ManagementFeeOutstanding"))
				trackedInterest := loanValuePaid.Sub(principalPaid).Sub(managementFeePaid)
				if principalPaid.Signum() <= 0 || trackedInterest.Signum() <= 0 {
					t.Fatalf("post-impairment payment deltas principal=%s trackedInterest=%s, want both positive", principalPaid.String(), trackedInterest.String())
				}
				beforeRemaining, ok := loanBeforePayment["PaymentRemaining"].(uint32)
				if !ok {
					t.Fatalf("Loan PaymentRemaining before payment = %v, want uint32", loanBeforePayment["PaymentRemaining"])
				}
				require.Equal(t, beforeRemaining-1, loanAfterPayment["PaymentRemaining"])
				requireLoanFlags(t, loanAfterPayment, lending.LsfLoanImpaired, false)
				requireLossNumberEqual(t, "LossUnrealized after payment", lossNumber(t, vaultAfterPayment, "LossUnrealized"), state.NewXRPLNumber(0, 0))

				debtDelta := lossNumber(t, brokerAfterPayment, "DebtTotal").Sub(lossNumber(t, brokerBeforePayment, "DebtTotal"))
				assetsDelta := lossNumber(t, vaultAfterPayment, "AssetsTotal").Sub(lossNumber(t, vaultBeforePayment, "AssetsTotal"))
				availableDelta := lossNumber(t, vaultAfterPayment, "AssetsAvailable").Sub(lossNumber(t, vaultBeforePayment, "AssetsAvailable"))
				if cash {
					requireLossNumberEqual(t, "cash DebtTotal delta", debtDelta, principalPaid.Negate())
					requireLossNumberEqual(t, "cash AssetsTotal delta", assetsDelta, trackedInterest)
				} else {
					requireLossNumberEqual(t, "legacy DebtTotal delta", debtDelta, principalPaid.Add(trackedInterest).Negate())
					requireLossNumberEqual(t, "legacy AssetsTotal delta", assetsDelta, state.NewXRPLNumber(0, 0))
				}
				requireLossNumberEqual(t, "AssetsAvailable delta", availableDelta, principalPaid.Add(trackedInterest))
			})
		}
	}
}
