package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
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
