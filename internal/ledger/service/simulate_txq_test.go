package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func TestSimulateTransactionUsesIsolatedTxQDryRunAdmission(t *testing.T) {
	config := DefaultConfig()
	queueConfig := txq.StandaloneConfig()
	queueConfig.MinimumTxnInLedgerStandalone = 1
	queueConfig.TargetTxnInLedger = 1
	config.TxQ = &queueConfig
	svc, err := New(config)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	for sequence := uint32(1); sequence <= 2; sequence++ {
		primer := accountset.AccountSet(master).
			Fee(env.BaseFee()).
			Sequence(sequence).
			Build()
		env.SignWith(primer, master)
		primerBlob, serializeErr := tx.SerializeTransaction(primer)
		require.NoError(t, serializeErr)
		primerParsed, parseErr := tx.ParseFromBinary(primerBlob)
		require.NoError(t, parseErr)
		primerResult, submitErr := svc.SubmitTransaction(primerParsed, primerBlob, false)
		require.NoError(t, submitErr)
		require.Equal(t, ter.TesSUCCESS, primerResult.Result)
		require.True(t, primerResult.Applied)
	}

	current := svc.GetOpenLedger()
	account, err := state.ReadAccountRoot(current, master.ID)
	require.NoError(t, err)
	require.NotNil(t, account)

	beforeState, err := current.StateMapHash()
	require.NoError(t, err)
	beforeTransactions, err := current.TxMapHash()
	require.NoError(t, err)
	beforeOpen := current

	// Seed the live queue with a current-sequence transaction. Simulation must
	// fork and inspect this queue state without changing the candidate or its
	// canonical blob.
	queued := accountset.AccountSet(master).
		Fee(env.BaseFee()).
		Sequence(account.Sequence).
		Build()
	env.SignWith(queued, master)
	queuedBlob, err := tx.SerializeTransaction(queued)
	require.NoError(t, err)
	queuedParsed, err := tx.ParseFromBinary(queuedBlob)
	require.NoError(t, err)
	queuedResult, err := svc.SubmitTransaction(queuedParsed, queuedBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TerQUEUED, queuedResult.Result)
	beforeQueue := svc.QueueAllTxs()
	require.Len(t, beforeQueue, 1)
	beforeQueueSize := svc.txQueue.Size()

	transaction := accountset.AccountSet(master).
		Fee(0).
		Sequence(account.Sequence).
		Build()

	result, err := svc.SimulateTransaction(transaction)
	require.NoError(t, err)
	require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)

	// Simulation must not publish a new open view, alter its state or insert a
	// candidate into the live queue.
	require.Same(t, beforeOpen, svc.GetOpenLedger())
	afterState, err := current.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, beforeState, afterState)
	afterTransactions, err := current.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, beforeTransactions, afterTransactions)
	require.Equal(t, beforeQueueSize, svc.txQueue.Size())
	require.Equal(t, beforeQueue, svc.QueueAllTxs())
	queuedHash, err := tx.ComputeTransactionHash(queuedParsed)
	require.NoError(t, err)
	queuedBlobAfter, found := svc.txQueue.GetTxBlob(queuedHash)
	require.True(t, found)
	require.Equal(t, queuedBlob, queuedBlobAfter)
}

func TestSimulateTransactionUsesCurrentFieldsForBoundTransaction(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	mutatedAccount := jtx.NewAccount("simulation-mutated-account")
	transaction := accountset.AccountSet(master).
		Fee(env.BaseFee()).
		Sequence(1).
		Build()
	env.SignWith(transaction, master)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)

	// Keep the retained wire bytes while changing the prepared transaction.
	// Simulation must use the current fields consistently, including Account.
	parsed.GetCommon().Account = mutatedAccount.Address
	parsed.GetCommon().TxnSignature = ""
	result, err := svc.SimulateTransaction(parsed)
	require.NoError(t, err)
	require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
}
