package service

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSubmitTransactionOmitsStateWithoutValidatedLedger(t *testing.T) {
	svc, err := New(Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	svc.mu.Lock()
	svc.validatedLedger = nil
	svc.mu.Unlock()

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	destination := jtx.NewAccount("submit-state-destination")
	txn := payment.Pay(master, destination, 100_000_000).Fee(10).Sequence(1).Build()
	env.SignWith(txn, master)
	blob, err := tx.SerializeTransaction(txn)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	result, err := svc.SubmitTransaction(parsed, blob, false)
	if err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	if result.CurrentLedgerState != nil {
		t.Fatalf("submit state = %+v, want nil without validated ledger", result.CurrentLedgerState)
	}
}

func TestSubmitTransactionBaseFeeFailureLeavesLedgerUnchanged(t *testing.T) {
	svc, current, loanID := feeFailureLedger(t)
	master := jtx.MasterAccount()
	before, err := current.Read(keylet.Account(master.ID))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(before)
	require.NoError(t, err)
	pay := lending.NewLoanPay(master.Address, loanID, tx.NewXRPAmount(10))
	pay.Fee = "10"
	pay.SetSequence(account.Sequence)
	env := jtx.NewTestEnv(t)
	env.SignWith(pay, master)
	blob, err := tx.SerializeTransaction(pay)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	result, err := svc.SubmitTransaction(parsed, blob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TefEXCEPTION, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	require.Nil(t, result.CurrentLedgerState)
	require.Zero(t, svc.txQueue.Size())
	require.Zero(t, svc.openLedgerView.Current().TxCount())
	after, err := svc.openLedgerView.Current().Read(keylet.Account(master.ID))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestSubmitTransactionMissingAccountPrecedesBaseFeeFailure(t *testing.T) {
	svc, current, loanID := feeFailureLedger(t)
	unfunded := jtx.NewAccount("fee-failure-unfunded")
	pay := lending.NewLoanPay(unfunded.Address, loanID, tx.NewXRPAmount(10))
	pay.Fee = "10"
	pay.SetSequence(1)
	env := jtx.NewTestEnv(t)
	env.SignWith(pay, unfunded)
	blob, err := tx.SerializeTransaction(pay)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)

	before, err := current.Read(keylet.Account(unfunded.ID))
	require.NoError(t, err)
	beforeTxCount := current.TxCount()
	result, err := svc.SubmitTransaction(parsed, blob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TerNO_ACCOUNT, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	require.Nil(t, result.CurrentLedgerState)
	require.Zero(t, svc.txQueue.Size())
	require.Equal(t, beforeTxCount, svc.openLedgerView.Current().TxCount())
	after, err := svc.openLedgerView.Current().Read(keylet.Account(unfunded.ID))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

type submitStateFeeFailure struct{ tx.Transaction }

func (submitStateFeeFailure) CalculateBaseFee(tx.LedgerView, tx.EngineConfig) (uint64, error) {
	panic("controlled fee failure")
}

func TestSubmitLedgerStateOmitsFailedBaseFee(t *testing.T) {
	svc, err := New(Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		ConfiguredFees: &drops.Fees{Base: 42, Reserve: 10_000_000, Increment: 2_000_000},
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	current := svc.openLedgerView.Current()
	validated := svc.GetValidatedLedger()
	master := jtx.MasterAccount()
	destination := jtx.NewAccount("fee-failure-destination")
	txn := payment.Pay(master, destination, 100_000_000).Fee(10).Sequence(1).Build()

	for _, queue := range []*txq.TxQ{nil, svc.txQueue} {
		state := svc.submitLedgerState(current, txn, openledger.ApplyConfig{}, validated, queue)
		require.NotNil(t, state)
		require.Equal(t, uint64(10), state.OpenLedgerCost)
		require.Equal(t, uint32(1), state.AccountSequenceNext)
		require.Equal(t, uint32(1), state.AccountSequenceAvailable)
		require.Nil(t, svc.submitLedgerState(current, submitStateFeeFailure{txn}, openledger.ApplyConfig{}, validated, queue))
	}
	fee, err := svc.GetAutofillFee(submitStateFeeFailure{txn}, false, 10, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(42), fee)
	fee, err = svc.GetAutofillFee(txn, false, 10, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(10), fee)
	fee, err = svc.GetAutofillFee(nil, false, 10, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(42), fee)

	txn.GetCommon().SponsorSignature = &tx.SponsorSignature{
		Signers: make([]tx.SignerWrapper, 2),
	}
	state := svc.submitLedgerState(current, txn, openledger.ApplyConfig{}, validated, nil)
	require.NotNil(t, state)
	require.Equal(t, uint64(30), state.OpenLedgerCost)
}

func TestAutofillFeeReferenceFallbackUsesLedgerFloor(t *testing.T) {
	for _, referenceFee := range []uint64{5, 10, 42} {
		t.Run(fmt.Sprintf("reference fee %d", referenceFee), func(t *testing.T) {
			svc, err := New(Config{
				Standalone: true, GenesisConfig: genesis.DefaultConfig(),
				ConfiguredFees: &drops.Fees{Base: drops.XRPAmount(referenceFee)},
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			txn := payment.Pay(jtx.MasterAccount(), jtx.NewAccount("autofill-floor"), 100_000_000).Fee(10).Sequence(1).Build()
			for _, failed := range []tx.Transaction{nil, submitStateFeeFailure{txn}} {
				fee, err := svc.GetAutofillFee(failed, false, 10, 1)
				require.NoError(t, err)
				require.Equal(t, max(referenceFee, uint64(10)), fee)
				fee, err = svc.GetAutofillFee(failed, false, 1, 1)
				if referenceFee < 10 {
					var highFee *svcerr.HighFeeError
					require.ErrorAs(t, err, &highFee)
					require.Equal(t, uint64(10), highFee.Fee)
					require.Equal(t, referenceFee, highFee.Limit)
				} else {
					require.NoError(t, err)
					require.Equal(t, referenceFee, fee)
				}
			}
			fee, err := svc.GetAutofillFee(txn, false, 1, 1)
			require.NoError(t, err)
			require.Equal(t, uint64(10), fee)
		})
	}
}
