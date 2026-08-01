package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
)

func TestRepairCleanerLedgerIndexReplacesWrongHistoryMapping(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	seq := uint32(10)
	hash := [32]byte{0xAA}
	parent := [32]byte{0x99}
	canonical := makeStubLedger(t, seq, hash, parent)
	wrong := makeStubLedger(t, seq, [32]byte{0xBB}, parent)

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.ledgerHistory[seq] = wrong
	svc.ledgerByHash[wrong.Hash()] = seq
	svc.persistedLedgers[hash] = canonical
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	repaired, err := svc.RepairCleanerLedgerIndex(
		context.Background(), seq, hash, parent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatal("wrong history mapping was not reported as repaired")
	}

	svc.historyComponent.mu.RLock()
	defer svc.historyComponent.mu.RUnlock()
	if got := svc.ledgerHistory[seq]; got != canonical {
		t.Fatalf("ledgerHistory[%d] = %p, want canonical %p", seq, got, canonical)
	}
	if got := svc.ledgerByHash[hash]; got != seq {
		t.Fatalf("ledgerByHash[%x] = %d, want %d", hash[:4], got, seq)
	}
	if _, ok := svc.ledgerByHash[wrong.Hash()]; ok {
		t.Fatal("stale hash-to-sequence mapping survived repair")
	}
}

func TestCleanerRepairRewritesMissingRelationalLedger(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repositories.Close() })

	cfg := DefaultConfig()
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint32(10)
	hash := [32]byte{0xAA}
	parent := [32]byte{0x99}
	canonical := makeStubLedger(t, seq, hash, parent)
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(canonical)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	repairTxns, err := svc.RepairCleanerLedgerIndex(ctx, seq, hash, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !repairTxns {
		t.Fatal("missing relational ledger did not force transaction repair")
	}
	if err := svc.RepairLedgerTransactions(ctx, seq); err != nil {
		t.Fatal(err)
	}

	info, err := repositories.Ledger().GetLedgerInfoBySeq(
		ctx, relationaldb.LedgerIndex(seq),
	)
	if err != nil {
		t.Fatal(err)
	}
	if [32]byte(info.Hash) != hash || [32]byte(info.ParentHash) != parent {
		t.Fatalf("repaired ledger info = hash %x parent %x", info.Hash[:4], info.ParentHash[:4])
	}
}
