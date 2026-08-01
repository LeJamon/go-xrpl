package shamap

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type snapshotSequenceFamily struct {
	*memoryFamily
	mu          sync.Mutex
	entries     []FlushEntry
	storeErr    error
	storeCtxErr error
}

func newSnapshotSequenceFamily() *snapshotSequenceFamily {
	return &snapshotSequenceFamily{memoryFamily: newMemoryFamily()}
}

func (f *snapshotSequenceFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	f.mu.Lock()
	f.entries = append(f.entries, entries...)
	f.storeCtxErr = ctx.Err()
	err := f.storeErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.memoryFamily.StoreBatch(ctx, entries)
}

func (f *snapshotSequenceFamily) takeEntries() []FlushEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := append([]FlushEntry(nil), f.entries...)
	f.entries = nil
	return entries
}

func (f *snapshotSequenceFamily) setStoreError(err error) {
	f.mu.Lock()
	f.storeErr = err
	f.mu.Unlock()
}

func TestSnapshotWithLedgerSeqPreservesSourceSequence(t *testing.T) {
	family := newSnapshotSequenceFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	sm.SetLedgerSeq(17)
	if err := sm.Put(sme_keyFromByte(0x31), sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	snapshot, err := sm.SnapshotMutableWithLedgerSeq(91)
	if err != nil {
		t.Fatalf("SnapshotMutableWithLedgerSeq: %v", err)
	}
	entries := family.takeEntries()
	if len(entries) == 0 {
		t.Fatal("sequence-aware snapshot did not flush dirty nodes")
	}
	for _, entry := range entries {
		if entry.LedgerSeq != 91 {
			t.Fatalf("flushed ledger sequence = %d, want 91", entry.LedgerSeq)
		}
	}
	if got := sm.tree.ledgerSeq; got != 17 {
		t.Fatalf("source ledger sequence = %d, want 17", got)
	}
	if got := snapshot.tree.ledgerSeq; got != 91 {
		t.Fatalf("snapshot ledger sequence = %d, want 91", got)
	}

	if err := sm.Put(sme_keyFromByte(0x32), sme_data12(2)); err != nil {
		t.Fatalf("Put after snapshot: %v", err)
	}
	if _, err := sm.SnapshotImmutable(); err != nil {
		t.Fatalf("SnapshotImmutable: %v", err)
	}
	entries = family.takeEntries()
	if len(entries) == 0 {
		t.Fatal("source mutation did not flush")
	}
	for _, entry := range entries {
		if entry.LedgerSeq != 17 {
			t.Fatalf("source flush ledger sequence = %d, want 17", entry.LedgerSeq)
		}
	}
}

func TestSnapshotWithLedgerSeqFailurePreservesSource(t *testing.T) {
	family := newSnapshotSequenceFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	sm.SetLedgerSeq(23)
	if err := sm.Put(sme_keyFromByte(0x41), sme_data12(3)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	storeErr := errors.New("store failed")
	family.setStoreError(storeErr)
	snapshot, err := sm.SnapshotMutableWithLedgerSeq(99)
	if snapshot != nil {
		t.Fatal("failed snapshot returned a map")
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("snapshot error = %v, want %v", err, storeErr)
	}
	if got := sm.tree.ledgerSeq; got != 23 {
		t.Fatalf("source ledger sequence after failure = %d, want 23", got)
	}

	family.takeEntries()
	family.setStoreError(nil)
	if _, err := sm.SnapshotImmutable(); err != nil {
		t.Fatalf("SnapshotImmutable retry: %v", err)
	}
	entries := family.takeEntries()
	if len(entries) == 0 {
		t.Fatal("failed snapshot marked dirty nodes clean")
	}
	for _, entry := range entries {
		if entry.LedgerSeq != 23 {
			t.Fatalf("retry ledger sequence = %d, want 23", entry.LedgerSeq)
		}
	}
}

func TestSnapshotImmutableContextPassesCancellationToStore(t *testing.T) {
	family := newSnapshotSequenceFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := sm.Put(sme_keyFromByte(0x51), sme_data12(4)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := sm.SnapshotImmutableContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("SnapshotImmutableContext error = %v, want context.Canceled", err)
	}
	family.mu.Lock()
	storeCtxErr := family.storeCtxErr
	family.mu.Unlock()
	if !errors.Is(storeCtxErr, context.Canceled) {
		t.Fatalf("StoreBatch context error = %v, want context.Canceled", storeCtxErr)
	}
}
