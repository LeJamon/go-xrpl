package service

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func preferredSwitchPaymentBlob(t testing.TB, env *jtx.TestEnv, sender, receiver *jtx.Account, amount, fee uint64, sequence uint32) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)
	transaction := payment.Pay(sender, receiver, amount).Fee(fee).Sequence(sequence).Build()
	env.SignWith(transaction, sender)
	txJSON, err := transaction.Flatten()
	require.NoError(t, err)
	blobHex, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	blob, err := hex.DecodeString(blobHex)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func TestPreferredLedgerSwitchReplaysHeldLocalBeforeQueuedCompetitor(t *testing.T) {
	cfg := DefaultConfig()
	queueCfg := txq.StandaloneConfig()
	queueCfg.MinimumTxnInLedgerStandalone = 1
	queueCfg.TargetTxnInLedger = 1
	cfg.TxQ = &queueCfg
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	fundedDestination := jtx.NewAccount("preferred-funded-destination")
	priorOne, _ := preferredSwitchPaymentBlob(t, env, master, fundedDestination, 20_000_000, 10, 1)
	priorTwo, _ := preferredSwitchPaymentBlob(t, env, master, fundedDestination, 1_000_000, 10, 2)
	for _, blob := range [][]byte{priorOne, priorTwo} {
		result, submitErr := svc.SubmitOpenLedgerTx(blob, false)
		require.NoError(t, submitErr)
		require.Equal(t, openledger.ResultSuccess, result)
	}
	require.Equal(t, uint32(2), svc.openLedgerView.Current().TxCount())

	localBlob, localHash := preferredSwitchPaymentBlob(
		t, env, master, fundedDestination, 1_000_000, 10, 3,
	)
	local, err := openledger.ParsePendingTx(localBlob)
	require.NoError(t, err)
	current := svc.openLedgerView.Current()
	svc.localTxs.PushBack(current.Sequence(), local)

	competitorBlob, competitorHash := preferredSwitchPaymentBlob(
		t, env, master, fundedDestination, 2_000_000, 10, 3,
	)
	queued, err := svc.SubmitOpenLedgerTxDetailed(competitorBlob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TerQUEUED, queued.Result)
	require.False(t, queued.Applied)
	require.True(t, svc.txQueue.Size() > 0)
	_, held := svc.localTxs.Get(competitorHash)
	require.False(t, held)

	preferred, err := ledger.NewOpen(svc.closedLedger, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))
	require.NoError(t, svc.SwitchToPreferredLedger(preferred))

	current = svc.openLedgerView.Current()
	exists, err := current.TxExists(localHash)
	require.NoError(t, err)
	require.True(t, exists, "held local transaction must replay before queue promotion")
	exists, err = current.TxExists(competitorHash)
	require.NoError(t, err)
	require.False(t, exists, "same-sequence queued competitor must not win over held local")
	queuedBlob, queuedOK := svc.txQueue.GetTxBlob(competitorHash)
	require.True(t, queuedOK, "queued competitor remains queued after local replay")
	require.Equal(t, competitorBlob, queuedBlob)
}
