package openledger_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
)

// A panic escaping the engine's apply recover drops only that transaction on
// the initial pass and on every ledger-build pass; on the open ledger's retry
// passes it aborts the apply.
func TestApplyTxs_ApplyErrorDropsTransaction(t *testing.T) {
	tests := []struct {
		name string
		mode openledger.Mode
		// panicAt is the 1-based transaction-phase observation that panics:
		// alice->bob applies first on the initial pass, bob->carol only on
		// the retry pass once bob exists.
		panicAt     int
		wantErr     bool
		wantApplied [2]bool
	}{
		{name: "open initial pass", mode: openledger.OpenLedgerMode, panicAt: 1, wantApplied: [2]bool{false, false}},
		{name: "build initial pass", mode: openledger.BuildLedgerMode, panicAt: 1, wantApplied: [2]bool{false, false}},
		{name: "build retry pass", mode: openledger.BuildLedgerMode, panicAt: 2, wantApplied: [2]bool{true, false}},
		{name: "open retry pass", mode: openledger.OpenLedgerMode, panicAt: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			carol := jtx.NewAccount("carol")
			env.Fund(alice, carol)
			view := freshView(t, env)

			fund := payment.Pay(alice, bob, 300_000_000).Sequence(env.Seq(alice)).Build()
			spend := payment.Pay(bob, carol, 5_000_000).Sequence(view.Sequence()).Build()
			fundTx, err := openledger.ParsePendingTx(buildSignedBlob(t, env, fund, alice))
			if err != nil {
				t.Fatalf("ParsePendingTx fund: %v", err)
			}
			spendTx, err := openledger.ParsePendingTx(buildSignedBlob(t, env, spend, bob))
			if err != nil {
				t.Fatalf("ParsePendingTx spend: %v", err)
			}

			observed := 0
			var retries []openledger.PendingTx
			err = openledger.ApplyTxs(view, []openledger.PendingTx{spendTx, fundTx}, &retries, openledger.ApplyConfig{
				BaseFee:          10,
				ReserveBase:      200_000_000,
				ReserveIncrement: 50_000_000,
				LedgerSequence:   view.Sequence(),
				Rules:            amendment.AllSupportedRules(),
				Mode:             tt.mode,
				ApplyObserver: func(phase txengine.ApplyPhase) {
					if phase != txengine.ApplyPhaseTransaction {
						return
					}
					observed++
					if observed == tt.panicAt {
						panic("injected apply failure")
					}
				},
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("ApplyTxs succeeded, want the retry-pass failure to propagate")
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyTxs: %v", err)
			}
			if got := ledgerTxExists(t, view, fundTx.Hash); got != tt.wantApplied[0] {
				t.Errorf("alice->bob applied = %v, want %v", got, tt.wantApplied[0])
			}
			if got := ledgerTxExists(t, view, spendTx.Hash); got != tt.wantApplied[1] {
				t.Errorf("bob->carol applied = %v, want %v", got, tt.wantApplied[1])
			}
			for _, r := range retries {
				if r.Hash == fundTx.Hash {
					t.Error("dropped alice->bob was kept for retry")
				}
			}
		})
	}
}
