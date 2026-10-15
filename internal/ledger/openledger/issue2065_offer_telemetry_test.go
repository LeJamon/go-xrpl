package openledger_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestApplyTxsOfferTelemetryPreservesLedgerRootsAndMetadata(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.VerifySignatures = true
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	observedView := freshView(t, env)
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no parent ledger")
	}
	controlView, err := ledger.NewOpen(parent, time.Unix(1_000_000, 0))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}

	transaction := payment.Pay(alice, bob, 300_000_000).
		Sequence(env.Seq(alice)).
		Build()
	blob := buildSignedBlob(t, env, transaction, alice)
	observedPending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx observed: %v", err)
	}
	controlPending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx control: %v", err)
	}

	baseConfig := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   observedView.Sequence(),
		Rules:            amendment.AllSupportedRules(),
	}
	var diagnostics []openledger.SlowTransactionDiagnostic
	observedConfig := baseConfig
	observedConfig.SlowTransactionThreshold = time.Nanosecond
	observedConfig.SlowTransactionObserver = func(d openledger.SlowTransactionDiagnostic) {
		diagnostics = append(diagnostics, d)
	}
	var observedRetries []openledger.PendingTx
	if err := openledger.ApplyTxs(observedView, []openledger.PendingTx{observedPending}, &observedRetries, observedConfig); err != nil {
		t.Fatalf("ApplyTxs observed: %v", err)
	}

	var controlRetries []openledger.PendingTx
	if err := openledger.ApplyTxs(controlView, []openledger.PendingTx{controlPending}, &controlRetries, baseConfig); err != nil {
		t.Fatalf("ApplyTxs control: %v", err)
	}
	if len(diagnostics) == 0 {
		t.Fatal("enabled telemetry did not report the transaction")
	}
	if diagnostics[0].OffersConsidered != 0 {
		t.Fatalf("direct payment walked %d offers, want 0", diagnostics[0].OffersConsidered)
	}

	observedState, err := observedView.StateMapHash()
	if err != nil {
		t.Fatalf("observed StateMapHash: %v", err)
	}
	controlState, err := controlView.StateMapHash()
	if err != nil {
		t.Fatalf("control StateMapHash: %v", err)
	}
	if observedState != controlState {
		t.Fatalf("telemetry changed state root: observed=%x control=%x", observedState, controlState)
	}
	observedTx, err := observedView.TxMapHash()
	if err != nil {
		t.Fatalf("observed TxMapHash: %v", err)
	}
	controlTx, err := controlView.TxMapHash()
	if err != nil {
		t.Fatalf("control TxMapHash: %v", err)
	}
	if observedTx != controlTx {
		t.Fatalf("telemetry changed transaction root: observed=%x control=%x", observedTx, controlTx)
	}

	observedMeta, observedOK, err := observedView.GetTransaction(observedPending.Hash)
	if err != nil || !observedOK {
		t.Fatalf("observed transaction metadata: ok=%v err=%v", observedOK, err)
	}
	controlMeta, controlOK, err := controlView.GetTransaction(controlPending.Hash)
	if err != nil || !controlOK {
		t.Fatalf("control transaction metadata: ok=%v err=%v", controlOK, err)
	}
	if !bytes.Equal(observedMeta, controlMeta) {
		t.Fatal("telemetry changed transaction metadata")
	}
}

func TestApplyTxsOfferTelemetryReportsWalkedBookOffers(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.VerifySignatures = true
	gateway := jtx.NewAccount("issue-2065-telemetry-gateway")
	maker := jtx.NewAccount("issue-2065-telemetry-maker")
	taker := jtx.NewAccount("issue-2065-telemetry-taker")
	env.Fund(gateway, maker, taker)

	usd := func(amount float64) tx.Amount { return jtx.USD(gateway, amount) }
	env.Trust(maker, usd(100))
	env.Trust(taker, usd(100))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gateway, maker, usd(100)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gateway, taker, usd(100)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(offertest.OfferCreate(
		maker,
		jtx.XRPTxAmountFromXRP(100),
		usd(100),
	).Build()))
	env.Close()

	view := freshView(t, env)
	transaction := offertest.OfferCreate(
		taker,
		usd(100),
		jtx.XRPTxAmountFromXRP(100),
	).Sequence(env.Seq(taker)).Build()
	blob := buildSignedBlob(t, env, transaction, taker)
	pending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	var diagnostics []openledger.SlowTransactionDiagnostic
	cfg := openledger.ApplyConfig{
		BaseFee:                  10,
		ReserveBase:              200_000_000,
		ReserveIncrement:         50_000_000,
		LedgerSequence:           view.Sequence(),
		Rules:                    amendment.AllSupportedRules(),
		SlowTransactionThreshold: time.Nanosecond,
		SlowTransactionObserver: func(d openledger.SlowTransactionDiagnostic) {
			diagnostics = append(diagnostics, d)
		},
	}
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pending}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}
	if len(diagnostics) == 0 {
		t.Fatal("enabled telemetry did not report the transaction")
	}
	if diagnostics[0].OffersConsidered == 0 {
		t.Fatalf("book payment reported no walked offers: %+v", diagnostics[0])
	}
}
