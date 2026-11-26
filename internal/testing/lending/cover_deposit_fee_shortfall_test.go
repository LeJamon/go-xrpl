package lending_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanBrokerCoverDeposit_XRPFeeShortfallFailsProcessing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    bool
		want    string
		applied bool
	}{
		{name: "ClosedView", want: "tecFAILED_PROCESSING", applied: true},
		{name: "OpenView", open: true, want: "telFAILED_PROCESSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLendingEnv(t)
			owner := jtx.NewAccount("cover-drain-owner")
			sink := jtx.NewAccount("cover-drain-sink")
			env.FundAmount(owner, 10_000_000_000)
			env.Fund(sink)
			vaultID := setupXRPVault(t, env, owner, 1_000_000_000)
			brokerSequence := env.Seq(owner)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
			brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

			// The whole liquid balance passes preclaim, but a fee above the
			// reserve leaves the post-fee balance one drop short of it.
			const amount = int64(100_000_000)
			reserve := env.ReserveBase() + uint64(env.OwnerCount(owner))*env.ReserveIncrement()
			fee := reserve + 1
			target := reserve + uint64(amount)
			balance := env.Balance(owner)
			require.Greater(t, balance, target+env.BaseFee())
			jtx.RequireTxSuccess(t, env.Submit(paytest.Pay(owner, sink, balance-target-env.BaseFee()).Build()))
			require.Equal(t, target, env.Balance(owner))
			beforeBroker, err := env.LedgerEntry(brokerKey)
			require.NoError(t, err)
			beforeSequence := env.Seq(owner)

			env.SetOpenLedger(tc.open)
			deposit := lending.NewLoanBrokerCoverDeposit(owner.Address, brokerID(owner, brokerSequence), tx.NewXRPAmount(amount))
			deposit.GetCommon().Fee = fmt.Sprint(fee)
			result := env.Submit(deposit)
			env.SetOpenLedger(true)

			require.Equal(t, tc.want, result.Code, result.Message)
			require.Equal(t, tc.applied, result.Applied)
			wantBalance, wantSequence := target, beforeSequence
			if tc.applied {
				wantBalance -= fee
				wantSequence++
			}
			require.Equal(t, wantBalance, env.Balance(owner))
			require.Equal(t, wantSequence, env.Seq(owner))
			afterBroker, err := env.LedgerEntry(brokerKey)
			require.NoError(t, err)
			require.Equal(t, beforeBroker, afterBroker)
		})
	}
}
