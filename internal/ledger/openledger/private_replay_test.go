package openledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestOpenLedgerAcceptPrivateCancellationAfterPrecommitDoesNotPublish(t *testing.T) {
	env := jtx.NewTestEnv(t)
	parent := closedParent(t, env)
	ol, err := openledger.New(parent, openledger.Config{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := ol.Current()
	var precommit, modifier, publication int
	err = ol.AcceptWithPrecommitContext(
		ctx,
		parent,
		nil,
		false,
		nil,
		openledger.ApplyConfig{
			Rules:                          amendment.AllSupportedRules(),
			AllowPostPrecommitCancellation: true,
		},
		nil,
		func() {
			precommit++
			cancel()
		},
		func(*ledger.Ledger) { modifier++ },
		nil,
		func(func()) { publication++ },
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Same(t, before, ol.Current())
	require.Equal(t, 1, precommit)
	require.Zero(t, modifier)
	require.Zero(t, publication)
}

func TestApplyTxsSlowDiagnosticIsBounded(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("slow-diagnostic-alice")
	bob := jtx.NewAccount("slow-diagnostic-bob")
	carol := jtx.NewAccount("slow-diagnostic-carol")
	env.Fund(alice, bob, carol)
	view := freshView(t, env)

	first := pendingPayment(t, env, payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)), alice)
	second := pendingPayment(t, env, payment.Pay(bob, carol, 1_000_000).Sequence(env.Seq(bob)), bob)
	var diagnostics []openledger.SlowTransactionDiagnostic
	var retries []openledger.PendingTx
	err := openledger.ApplyTxs(
		view,
		[]openledger.PendingTx{first, second},
		&retries,
		openledger.ApplyConfig{
			BaseFee:                  10,
			ReserveBase:              200_000_000,
			ReserveIncrement:         50_000_000,
			LedgerSequence:           view.Sequence(),
			Rules:                    amendment.AllSupportedRules(),
			SlowTransactionThreshold: time.Nanosecond,
			SlowTransactionObserver: func(d openledger.SlowTransactionDiagnostic) {
				diagnostics = append(diagnostics, d)
			},
		},
	)

	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	require.Equal(t, first.Hash, diagnostics[0].Hash)
	require.Equal(t, tx.TypePayment, diagnostics[0].Type)
	require.Equal(t, "initial", diagnostics[0].Phase)
	require.Equal(t, 0, diagnostics[0].Pass)
	require.Positive(t, diagnostics[0].Duration)
}
