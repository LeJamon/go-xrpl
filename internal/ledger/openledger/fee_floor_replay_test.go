package openledger_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

func feeReplayConfig(sequence uint32, baseFee uint64, tracker *feetrack.LoadFeeTrack) openledger.ApplyConfig {
	return openledger.ApplyConfig{
		BaseFee:          baseFee,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   sequence,
		Rules:            amendment.AllSupportedRules(),
		FeeTrack:         tracker,
	}
}

func feeAccountRoot(t *testing.T, view *ledger.Ledger, accountID [20]byte) *state.AccountRoot {
	t.Helper()
	root, err := state.ReadAccountRoot(view, accountID)
	require.NoError(t, err)
	require.NotNil(t, root)
	return root
}

func requirePaymentState(t *testing.T, view *ledger.Ledger, alice, bob [20]byte, sourceBalance uint64, sourceSequence uint32, destinationBalance uint64) {
	t.Helper()
	source := feeAccountRoot(t, view, alice)
	require.Equal(t, sourceBalance, source.Balance)
	require.Equal(t, sourceSequence, source.Sequence)
	destination := feeAccountRoot(t, view, bob)
	require.Equal(t, destinationBalance, destination.Balance)
}

func TestOpenLedgerAcceptRejectsPriorCurrentBelowBaseFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("fee-replay-base-alice")
	bob := jtx.NewAccount("fee-replay-base-bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{Rules: amendment.AllSupportedRules()})
	require.NoError(t, err)
	queue, err := txq.New(txq.DefaultConfig())
	require.NoError(t, err)

	transaction := payment.Pay(alice, bob, 1_000_000).Fee(9).Sequence(env.Seq(alice)).Build()
	pending, err := openledger.ParsePendingTx(buildSignedBlobOL(t, env, transaction, alice))
	require.NoError(t, err)

	parentAlice := feeAccountRoot(t, parent, alice.AccountID())
	parentBob := feeAccountRoot(t, parent, bob.AccountID())

	admitCfg := feeReplayConfig(ol.Current().Sequence(), 9, nil)
	out := ol.SubmitDetailed(pending, admitCfg, queue)
	require.Equal(t, ter.TesSUCCESS, out.Result)
	require.True(t, out.Applied)
	require.Zero(t, queue.Size())
	require.True(t, ledgerTxExists(t, ol.Current(), pending.Hash))

	requirePaymentState(t, ol.Current(), alice.AccountID(), bob.AccountID(), parentAlice.Balance-1_000_009, parentAlice.Sequence+1, parentBob.Balance+1_000_000)

	var retries []openledger.PendingTx
	replayCfg := feeReplayConfig(ol.Current().Sequence(), 10, nil)
	require.NoError(t, ol.Accept(parent, nil, false, &retries, replayCfg, queue, nil, nil))
	require.Empty(t, retries)

	current := ol.Current()
	require.False(t, ledgerTxExists(t, current, pending.Hash))
	requirePaymentState(t, current, alice.AccountID(), bob.AccountID(), parentAlice.Balance, parentAlice.Sequence, parentBob.Balance)
}

func TestOpenLedgerAcceptRejectsPriorCurrentAfterLoadRise(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("fee-replay-load-alice")
	bob := jtx.NewAccount("fee-replay-load-bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{Rules: amendment.AllSupportedRules()})
	require.NoError(t, err)
	queue, err := txq.New(txq.DefaultConfig())
	require.NoError(t, err)
	tracker := feetrack.New()
	cfg := feeReplayConfig(ol.Current().Sequence(), 10, tracker)

	transaction := payment.Pay(alice, bob, 1_000_000).Fee(10).Sequence(env.Seq(alice)).Build()
	pending, err := openledger.ParsePendingTx(buildSignedBlobOL(t, env, transaction, alice))
	require.NoError(t, err)

	parentAlice := feeAccountRoot(t, parent, alice.AccountID())
	parentBob := feeAccountRoot(t, parent, bob.AccountID())

	out := ol.SubmitDetailed(pending, cfg, queue)
	require.Equal(t, ter.TesSUCCESS, out.Result)
	require.True(t, out.Applied)
	require.Zero(t, queue.Size())
	require.True(t, ledgerTxExists(t, ol.Current(), pending.Hash))

	requirePaymentState(t, ol.Current(), alice.AccountID(), bob.AccountID(), parentAlice.Balance-1_000_010, parentAlice.Sequence+1, parentBob.Balance+1_000_000)

	tracker.SetRemoteFee(2 * feetrack.LoadBase)
	var retries []openledger.PendingTx
	require.NoError(t, ol.Accept(parent, nil, false, &retries, cfg, queue, nil, nil))
	require.Empty(t, retries)

	current := ol.Current()
	require.False(t, ledgerTxExists(t, current, pending.Hash))
	requirePaymentState(t, current, alice.AccountID(), bob.AccountID(), parentAlice.Balance, parentAlice.Sequence, parentBob.Balance)
}

func TestApplyTxsBuildLedgerAllowsBelowBaseFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("fee-build-alice")
	bob := jtx.NewAccount("fee-build-bob")
	env.Fund(alice, bob)
	view := freshView(t, env)

	transaction := payment.Pay(alice, bob, 1_000_000).Fee(1).Sequence(env.Seq(alice)).Build()
	pending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, transaction, alice))
	require.NoError(t, err)
	beforeAlice := feeAccountRoot(t, view, alice.AccountID())
	beforeBob := feeAccountRoot(t, view, bob.AccountID())

	var retries []openledger.PendingTx
	tracker := feetrack.New()
	tracker.SetRemoteFee(2 * feetrack.LoadBase)
	cfg := feeReplayConfig(view.Sequence(), 10, tracker)
	cfg.Mode = openledger.BuildLedgerMode
	require.NoError(t, openledger.ApplyTxs(view, []openledger.PendingTx{pending}, &retries, cfg))
	require.Empty(t, retries)
	require.True(t, ledgerTxExists(t, view, pending.Hash))

	requirePaymentState(t, view, alice.AccountID(), bob.AccountID(), beforeAlice.Balance-1_000_001, beforeAlice.Sequence+1, beforeBob.Balance+1_000_000)
}
