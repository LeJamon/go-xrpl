package service

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
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

func TestSimulateTransactionUsesCurrentIdentityForBoundApply(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()

	primer := accountset.AccountSet(master).
		Fee(env.BaseFee()).
		Sequence(1).
		SetFlag(accounttx.AccountSetFlagAccountTxnID).
		Build()
	env.SignWith(primer, master)
	primerBlob, err := tx.SerializeTransaction(primer)
	require.NoError(t, err)
	primerParsed, err := tx.ParseFromBinary(primerBlob)
	require.NoError(t, err)
	primerResult, err := svc.SubmitTransaction(primerParsed, primerBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, primerResult.Result)
	require.True(t, primerResult.Applied)

	original := accountset.AccountSet(master).
		Fee(env.BaseFee()).
		Sequence(2).
		Build()
	env.SignWith(original, master)
	originalBlob, err := tx.SerializeTransaction(original)
	require.NoError(t, err)
	originalParsed, err := tx.ParseFromBinary(originalBlob)
	require.NoError(t, err)
	originalID, err := tx.ComputeTransactionHash(originalParsed)
	require.NoError(t, err)
	originalResult, err := svc.SubmitTransaction(originalParsed, originalBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, originalResult.Result)
	require.True(t, originalResult.Applied)
	retainedRaw := append([]byte(nil), originalParsed.GetRawBytes()...)
	openBefore := svc.GetOpenLedger()
	stateBefore, err := openBefore.StateMapHash()
	require.NoError(t, err)
	txBefore, err := openBefore.TxMapHash()
	require.NoError(t, err)

	// This object still retains the already-submitted wire transaction, but its
	// prepared fields now describe the next transaction in the account's chain.
	// The original ID is present in the open ledger, so a raw-byte hash would
	// incorrectly return tefALREADY. Simulation must thread the current ID into
	// AccountTxnID and metadata as it applies the current fields.
	nextSequence := uint32(3)
	originalParsed.GetCommon().Sequence = &nextSequence
	originalParsed.GetCommon().TxnSignature = ""
	currentID, err := tx.ComputeCurrentTransactionHash(originalParsed)
	require.NoError(t, err)
	require.NotEqual(t, originalID, currentID)

	result, err := svc.SimulateTransaction(originalParsed)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, result.Result)
	require.False(t, result.Applied)
	require.NotNil(t, result.Metadata)

	wantAccountTxnID := strings.ToUpper(hex.EncodeToString(currentID[:]))
	foundAccountTxnID := false
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType != "AccountRoot" || node.NodeType != "ModifiedNode" {
			continue
		}
		got, ok := node.FinalFields["AccountTxnID"].(string)
		if ok {
			require.Equal(t, wantAccountTxnID, got)
			foundAccountTxnID = true
			break
		}
	}
	require.True(t, foundAccountTxnID, "simulation metadata did not contain the current AccountTxnID")
	require.True(t, bytes.Equal(retainedRaw, originalParsed.GetRawBytes()), "simulation changed the caller's retained raw transaction")
	require.Same(t, openBefore, svc.GetOpenLedger())
	stateAfter, err := openBefore.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, stateBefore, stateAfter)
	txAfter, err := openBefore.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, txBefore, txAfter)
}
