package relationaldb

import (
	"context"
	"testing"
)

// fakeLedgerRepo records delete calls and reports a configurable minimum
// sequence. Only the methods LedgerPruner exercises are implemented; the rest
// of LedgerRepository is satisfied by the embedded nil interface and must not
// be called.
type fakeLedgerRepo struct {
	LedgerRepository
	min        *LedgerIndex
	deleteArgs []LedgerIndex
}

func (f *fakeLedgerRepo) GetMinLedgerSeq(context.Context) (*LedgerIndex, error) { return f.min, nil }
func (f *fakeLedgerRepo) DeleteLedgersBySeq(_ context.Context, maxSeq LedgerIndex) error {
	f.deleteArgs = append(f.deleteArgs, maxSeq)
	return nil
}

type fakeTxRepo struct {
	TransactionRepository
	min        *LedgerIndex
	deleteArgs []LedgerIndex
}

func (f *fakeTxRepo) GetTransactionsMinLedgerSeq(context.Context) (*LedgerIndex, error) {
	return f.min, nil
}
func (f *fakeTxRepo) DeleteTransactionsBeforeLedgerSeq(_ context.Context, seq LedgerIndex) error {
	f.deleteArgs = append(f.deleteArgs, seq)
	return nil
}

type fakeAcctTxRepo struct {
	AccountTransactionRepository
	min        *LedgerIndex
	deleteArgs []LedgerIndex
}

func (f *fakeAcctTxRepo) GetAccountTransactionsMinLedgerSeq(context.Context) (*LedgerIndex, error) {
	return f.min, nil
}
func (f *fakeAcctTxRepo) DeleteAccountTransactionsBeforeLedgerSeq(_ context.Context, seq LedgerIndex) error {
	f.deleteArgs = append(f.deleteArgs, seq)
	return nil
}

type fakeManager struct {
	RepositoryManager
	ledger *fakeLedgerRepo
	tx     *fakeTxRepo
	acct   *fakeAcctTxRepo
}

func (m *fakeManager) Ledger() LedgerRepository                         { return m.ledger }
func (m *fakeManager) Transaction() TransactionRepository               { return m.tx }
func (m *fakeManager) AccountTransaction() AccountTransactionRepository { return m.acct }

func seqPtr(v uint32) *LedgerIndex { idx := LedgerIndex(v); return &idx }

func newFakeManager(ledgerMin, txMin, acctMin *LedgerIndex) *fakeManager {
	return &fakeManager{
		ledger: &fakeLedgerRepo{min: ledgerMin},
		tx:     &fakeTxRepo{min: txMin},
		acct:   &fakeAcctTxRepo{min: acctMin},
	}
}

func TestLedgerPruner_DeletesBelowBoundary(t *testing.T) {
	m := newFakeManager(seqPtr(1), seqPtr(1), seqPtr(1))
	p := NewLedgerPruner(m, 0)

	if err := p.DeleteLedgersBefore(context.Background(), 100); err != nil {
		t.Fatalf("DeleteLedgersBefore: %v", err)
	}

	// Ledgers delete is inclusive (<=), so the boundary is passed as 99 to
	// delete strictly below 100. Tx/AcctTx deletes are exclusive (<), so they
	// receive 100 directly.
	if got := m.ledger.deleteArgs; len(got) != 1 || got[0] != 99 {
		t.Fatalf("ledger delete args = %v, want [99]", got)
	}
	if got := m.tx.deleteArgs; len(got) != 1 || got[0] != 100 {
		t.Fatalf("tx delete args = %v, want [100]", got)
	}
	if got := m.acct.deleteArgs; len(got) != 1 || got[0] != 100 {
		t.Fatalf("acct delete args = %v, want [100]", got)
	}
}

func TestLedgerPruner_SkipsWhenMinAtOrAboveBoundary(t *testing.T) {
	// Every table's minimum is already at the boundary → nothing to delete.
	m := newFakeManager(seqPtr(100), seqPtr(100), seqPtr(100))
	p := NewLedgerPruner(m, 0)

	if err := p.DeleteLedgersBefore(context.Background(), 100); err != nil {
		t.Fatalf("DeleteLedgersBefore: %v", err)
	}
	if len(m.ledger.deleteArgs)+len(m.tx.deleteArgs)+len(m.acct.deleteArgs) != 0 {
		t.Fatal("no deletes expected when min >= boundary")
	}
}

func TestLedgerPruner_SkipsEmptyTables(t *testing.T) {
	// nil min seq models an empty table.
	m := newFakeManager(nil, nil, nil)
	p := NewLedgerPruner(m, 0)

	if err := p.DeleteLedgersBefore(context.Background(), 100); err != nil {
		t.Fatalf("DeleteLedgersBefore: %v", err)
	}
	if len(m.ledger.deleteArgs)+len(m.tx.deleteArgs)+len(m.acct.deleteArgs) != 0 {
		t.Fatal("no deletes expected on empty tables")
	}
}

func TestLedgerPruner_Batched(t *testing.T) {
	// min=1, boundary=10, batch=3 → exclusive delete steps walk
	// 4, 7, 10 for the tx/acct tables.
	m := newFakeManager(seqPtr(1), seqPtr(1), seqPtr(1))
	p := NewLedgerPruner(m, 3)

	if err := p.DeleteLedgersBefore(context.Background(), 10); err != nil {
		t.Fatalf("DeleteLedgersBefore: %v", err)
	}

	wantTx := []LedgerIndex{4, 7, 10}
	if got := m.tx.deleteArgs; !equalSeqs(got, wantTx) {
		t.Fatalf("tx batched delete args = %v, want %v", got, wantTx)
	}
	if got := m.acct.deleteArgs; !equalSeqs(got, wantTx) {
		t.Fatalf("acct batched delete args = %v, want %v", got, wantTx)
	}
	// Ledgers (inclusive) reach the same steps minus one: 3, 6, 9.
	wantLedger := []LedgerIndex{3, 6, 9}
	if got := m.ledger.deleteArgs; !equalSeqs(got, wantLedger) {
		t.Fatalf("ledger batched delete args = %v, want %v", got, wantLedger)
	}
}

func TestLedgerPruner_ZeroBoundaryNoOp(t *testing.T) {
	m := newFakeManager(seqPtr(1), seqPtr(1), seqPtr(1))
	p := NewLedgerPruner(m, 0)
	if err := p.DeleteLedgersBefore(context.Background(), 0); err != nil {
		t.Fatalf("DeleteLedgersBefore: %v", err)
	}
	if len(m.ledger.deleteArgs)+len(m.tx.deleteArgs)+len(m.acct.deleteArgs) != 0 {
		t.Fatal("zero boundary must delete nothing")
	}
}

func equalSeqs(a, b []LedgerIndex) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Compile-time check: *LedgerPruner is usable as the rotator's relational
// pruner.
var _ interface {
	DeleteLedgersBefore(context.Context, uint32) error
} = (*LedgerPruner)(nil)
