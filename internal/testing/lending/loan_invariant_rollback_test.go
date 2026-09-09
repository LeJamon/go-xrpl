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

func TestLoanPayRejectsUnalignedLegacyImpairmentSchedule(t *testing.T) {
	for _, cash := range []bool{false, true} {
		t.Run(fmt.Sprintf("cash=%t", cash), func(t *testing.T) {
			f := newLossLifecycleFixture(t, cash, false)
			loan := decodeLendingEntry(t, f.env, f.loanKey)
			due, ok := loan["NextPaymentDueDate"].(uint32)
			require.True(t, ok)
			f.env.CloseToParentCloseTime(due + 1)
			submitLoanManage(t, f, lending.TfLoanImpair)
			f.env.CloseToParentCloseTime(due + 2)

			loanBefore := decodeLendingEntry(t, f.env, f.loanKey)
			vaultBefore := decodeLendingEntry(t, f.env, f.vaultKey)
			brokerBefore := decodeLendingEntry(t, f.env, f.brokerKey)
			borrowerBalance := f.env.Balance(f.borrower)
			ownerBalance := f.env.Balance(f.owner)
			sequence := f.env.Seq(f.borrower)
			amount := lossNumber(t, loanBefore, "PeriodicPayment").ToInt64WithMode(state.RoundUpward) + 1
			payment := lending.NewLoanPay(f.borrower.Address, f.loanID, tx.NewXRPAmount(amount))
			payment.Fee = "20"
			result := f.env.Submit(payment)

			jtx.RequireTxClaimed(t, result, jtx.TecINVARIANT_FAILED)
			require.Equal(t, uint64(20), result.Fee)
			require.Equal(t, borrowerBalance-20, f.env.Balance(f.borrower))
			require.Equal(t, ownerBalance, f.env.Balance(f.owner))
			require.Equal(t, sequence+1, f.env.Seq(f.borrower))
			require.Equal(t, loanBefore, decodeLendingEntry(t, f.env, f.loanKey))
			require.Equal(t, vaultBefore, decodeLendingEntry(t, f.env, f.vaultKey))
			require.Equal(t, brokerBefore, decodeLendingEntry(t, f.env, f.brokerKey))
		})
	}
}
