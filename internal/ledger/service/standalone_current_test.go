package service

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
)

func TestStandaloneCloseRejectsMalformedPublishedTransaction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	hash := [32]byte{0xA7}
	var insertErr error
	if !svc.openLedgerView.Modify(func(view *ledger.Ledger) bool {
		insertErr = view.AddTransaction(hash, []byte{0x12, 0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		return insertErr == nil
	}) {
		t.Fatalf("inject malformed transaction: %v", insertErr)
	}
	before := svc.GetOpenLedger()
	_, err = svc.AcceptLedger(t.Context())
	if err == nil || !strings.Contains(err.Error(), "collect open transactions for close") {
		t.Fatalf("close error=%v", err)
	}
	if svc.GetClosedLedger() != parent || svc.GetOpenLedger() != before {
		t.Fatal("failed close changed the published ledger frontier")
	}
	if found, err := before.TxExists(hash); err != nil || !found {
		t.Fatalf("failed close lost the input: present=%t err=%v", found, err)
	}
}
