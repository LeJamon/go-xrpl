package service

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/stretchr/testify/require"
)

func TestAcceptLedgerPropagatesOpenViewFailure(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	legacy := svc.openLedger
	queueSize := svc.txQueue.Size()
	queueMetrics := svc.txQueue.Metrics(legacy.TxCount())
	closed := svc.closedLedger
	validated := svc.validatedLedger
	history := maps.Clone(svc.ledgerHistory)
	txIndex := maps.Clone(svc.txIndex)
	txPositions := maps.Clone(svc.txPositionIndex)
	events := maps.Clone(svc.ledgerEventCandidates)
	current := svc.openLedgerView.Current()
	currentParent := current.ParentHash()
	svc.localTxs.PushBack(current.Sequence(), openledger.PendingTx{
		Blob: []byte{0xff},
		Hash: [32]byte{1},
	})

	_, err = svc.AcceptLedger(context.Background())
	require.Error(t, err)
	require.Same(t, legacy, svc.openLedger)
	require.False(t, svc.openLedger.IsClosed())
	require.Equal(t, queueSize, svc.txQueue.Size())
	require.Equal(t, queueMetrics, svc.txQueue.Metrics(legacy.TxCount()))
	require.Same(t, current, svc.openLedgerView.Current())
	require.Equal(t, currentParent, svc.openLedgerView.Current().ParentHash())
	require.Same(t, closed, svc.closedLedger)
	require.Same(t, validated, svc.validatedLedger)
	require.Equal(t, history, svc.ledgerHistory)
	require.Equal(t, txIndex, svc.txIndex)
	require.Equal(t, txPositions, svc.txPositionIndex)
	require.Equal(t, events, svc.ledgerEventCandidates)
}

func TestPreferredLedgerSwitchPropagatesOpenViewFailure(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	legacy := svc.openLedger
	queueSize := svc.txQueue.Size()
	queueMetrics := svc.txQueue.Metrics(legacy.TxCount())
	closed := svc.closedLedger
	validated := svc.validatedLedger
	history := maps.Clone(svc.ledgerHistory)
	txIndex := maps.Clone(svc.txIndex)
	txPositions := maps.Clone(svc.txPositionIndex)
	events := maps.Clone(svc.ledgerEventCandidates)
	current := svc.openLedgerView.Current()
	svc.localTxs.PushBack(current.Sequence(), openledger.PendingTx{
		Blob: []byte{0xff},
		Hash: [32]byte{1},
	})
	preferred, err := ledger.NewOpen(svc.closedLedger, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))

	require.Error(t, svc.SwitchToPreferredLedger(preferred))
	require.Same(t, legacy, svc.openLedger)
	require.False(t, svc.openLedger.IsClosed())
	require.Equal(t, queueSize, svc.txQueue.Size())
	require.Equal(t, queueMetrics, svc.txQueue.Metrics(legacy.TxCount()))
	require.Same(t, current, svc.openLedgerView.Current())
	require.Same(t, closed, svc.closedLedger)
	require.Same(t, validated, svc.validatedLedger)
	require.Equal(t, history, svc.ledgerHistory)
	require.Equal(t, txIndex, svc.txIndex)
	require.Equal(t, txPositions, svc.txPositionIndex)
	require.Equal(t, events, svc.ledgerEventCandidates)
}

func TestAcceptConsensusResultPropagatesOpenViewFailure(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	legacy := svc.openLedger
	queueSize := svc.txQueue.Size()
	queueMetrics := svc.txQueue.Metrics(legacy.TxCount())
	closed := svc.closedLedger
	validated := svc.validatedLedger
	history := maps.Clone(svc.ledgerHistory)
	txIndex := maps.Clone(svc.txIndex)
	txPositions := maps.Clone(svc.txPositionIndex)
	events := maps.Clone(svc.ledgerEventCandidates)
	current := svc.openLedgerView.Current()
	svc.localTxs.PushBack(current.Sequence(), openledger.PendingTx{
		Blob: []byte{0xff},
		Hash: [32]byte{1},
	})

	_, err = svc.AcceptConsensusResult(context.Background(), closed, nil, nil, time.Now(), true)
	require.Error(t, err)
	require.Same(t, legacy, svc.openLedger)
	require.False(t, svc.openLedger.IsClosed())
	require.Equal(t, queueSize, svc.txQueue.Size())
	require.Equal(t, queueMetrics, svc.txQueue.Metrics(legacy.TxCount()))
	require.Same(t, current, svc.openLedgerView.Current())
	require.Same(t, closed, svc.closedLedger)
	require.Same(t, validated, svc.validatedLedger)
	require.Equal(t, history, svc.ledgerHistory)
	require.Equal(t, txIndex, svc.txIndex)
	require.Equal(t, txPositions, svc.txPositionIndex)
	require.Equal(t, events, svc.ledgerEventCandidates)
}

func TestSubmitOpenLedgerTxRetainsPermanentLocalFailure(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	transaction := accounttx.NewAccountSet(master.Address)
	sequence := uint32(1)
	transaction.Sequence = &sequence
	transaction.Fee = "10"
	flags := uint32(1)
	transaction.Flags = &flags
	env.SignWith(transaction, master)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)

	result, err := svc.SubmitOpenLedgerTx(blob, true)
	require.NoError(t, err)
	require.Equal(t, openledger.ResultFailure, result)
	retained, ok := svc.localTxs.Get(hash)
	require.True(t, ok)
	require.Equal(t, blob, retained.Blob)
}
