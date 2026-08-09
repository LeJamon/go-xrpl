package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

func mustNewOpenWithHeader(t *testing.T, h header.LedgerHeader, stateMap, txMap *shamap.SHAMap) *ledger.Ledger {
	t.Helper()
	l, err := ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	return l
}

func TestEvictOldHistoryLocked(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	makeLedger := func(seq uint32, salt byte) *ledger.Ledger {
		stateMap, err := svc.genesisLedger.StateMapSnapshot()
		if err != nil {
			t.Fatalf("StateMapSnapshot: %v", err)
		}
		txMap, err := svc.genesisLedger.TxMapSnapshot()
		if err != nil {
			t.Fatalf("TxMapSnapshot: %v", err)
		}
		var h header.LedgerHeader
		h.LedgerIndex = seq
		h.Hash[0] = salt
		h.Hash[1] = byte(seq)
		h.Hash[2] = byte(seq >> 8)
		l := mustNewOpenWithHeader(t, h, stateMap, txMap)
		var txHash [32]byte
		txHash[0] = 0xAA
		txHash[1] = byte(seq)
		txHash[2] = byte(seq >> 8)
		txData := make([]byte, 16)
		txData[0] = salt
		txData[1] = byte(seq)
		txData[2] = byte(seq >> 8)
		if err := l.AddTransaction(txHash, txData); err != nil {
			t.Fatalf("AddTransaction(seq=%d): %v", seq, err)
		}
		svc.txIndex[txHash] = seq
		svc.txPositionIndex[txHash] = 0
		return l
	}

	const totalLedgers = historyWindow * 3
	var latestSeq uint32 = 1
	for range totalLedgers {
		svc.ledgerHistory[latestSeq] = makeLedger(latestSeq, 0x42)
		latestSeq++
	}
	latestValidated := latestSeq - 1

	svc.mu.Lock()
	svc.evictOldHistoryLocked(latestValidated)
	svc.mu.Unlock()

	if got := len(svc.ledgerHistory); got != historyWindow {
		t.Errorf("ledgerHistory size after eviction: got %d, want %d", got, historyWindow)
	}

	cutoff := latestValidated - historyWindow
	for seq, l := range svc.ledgerHistory {
		if seq <= cutoff {
			t.Errorf("ledgerHistory[%d] survived eviction; cutoff=%d", seq, cutoff)
		}
		_ = l
	}

	for txHash, txSeq := range svc.txIndex {
		if txSeq <= cutoff {
			t.Errorf("txIndex[%x]=%d survived eviction; cutoff=%d", txHash[:4], txSeq, cutoff)
		}
	}
	if got, want := len(svc.txIndex), historyWindow; got != want {
		t.Errorf("txIndex size: got %d, want %d", got, want)
	}
	if got, want := len(svc.txPositionIndex), historyWindow; got != want {
		t.Errorf("txPositionIndex size: got %d, want %d", got, want)
	}
}

func TestEvictOldHistoryLocked_BelowWindow(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for seq := uint32(1); seq <= historyWindow/2; seq++ {
		stateMap, err := svc.genesisLedger.StateMapSnapshot()
		if err != nil {
			t.Fatalf("StateMapSnapshot: %v", err)
		}
		txMap, err := svc.genesisLedger.TxMapSnapshot()
		if err != nil {
			t.Fatalf("TxMapSnapshot: %v", err)
		}
		var h header.LedgerHeader
		h.LedgerIndex = seq
		svc.ledgerHistory[seq] = mustNewOpenWithHeader(t, h, stateMap, txMap)
	}

	before := len(svc.ledgerHistory)
	svc.mu.Lock()
	svc.evictOldHistoryLocked(historyWindow / 2)
	svc.mu.Unlock()

	if got := len(svc.ledgerHistory); got != before {
		t.Errorf("ledgerHistory size changed despite being below window: before=%d after=%d", before, got)
	}
}

func TestAcceptLedgerLoop_BoundsHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx := context.Background()
	for i := range historyWindow * 2 {
		if _, err := svc.AcceptLedger(ctx); err != nil {
			t.Fatalf("AcceptLedger #%d: %v", i, err)
		}
	}

	svc.mu.Lock()
	size := len(svc.ledgerHistory)
	svc.mu.Unlock()

	if size > historyWindow+1 {
		t.Errorf("ledgerHistory unbounded under AcceptLedger loop: got %d, want <= %d", size, historyWindow+1)
	}
	last := svc.GetClosedLedgerIndex()
	if got, want := svc.GetServerInfo().CompleteLedgers, formatRange(last-historyWindow+1, last); got != want {
		t.Errorf("complete_ledgers includes evicted in-memory history: got %q, want %q", got, want)
	}
}

// Covers the race where SetValidatedLedger arrives before the close
// and is drained + promoted inline — eviction must run on that path
// because no second SetValidatedLedger arrives for the same seq.
func TestValidatedPromotionEvictsHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	freshMaps := func() (*shamap.SHAMap, *shamap.SHAMap) {
		stateMap, err := svc.genesisLedger.StateMapSnapshot()
		if err != nil {
			t.Fatalf("StateMapSnapshot: %v", err)
		}
		txMap, err := svc.genesisLedger.TxMapSnapshot()
		if err != nil {
			t.Fatalf("TxMapSnapshot: %v", err)
		}
		return stateMap, txMap
	}

	const adoptedSeq uint32 = historyWindow + 50
	adoptedState, adoptedTx := freshMaps()
	var adoptedHeader header.LedgerHeader
	adoptedHeader.LedgerIndex = adoptedSeq
	adoptedHeader.Hash[0] = 0x77
	adopted := mustNewOpenWithHeader(t, adoptedHeader, adoptedState, adoptedTx)

	// Seed entries below the post-eviction cutoff so promotion has
	// observable work to do.
	cutoff := adoptedSeq - historyWindow
	for seq := uint32(1); seq <= cutoff; seq++ {
		st, tx := freshMaps()
		var h header.LedgerHeader
		h.LedgerIndex = seq
		svc.ledgerHistory[seq] = mustNewOpenWithHeader(t, h, st, tx)
	}

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(adopted)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()
	svc.SetValidatedLedger(adoptedSeq, adopted.Hash())
	svc.historyComponent.mu.RLock()
	size := len(svc.ledgerHistory)
	svc.historyComponent.mu.RUnlock()

	if size > historyWindow+1 {
		t.Errorf("validated promotion left ledgerHistory unbounded: got %d, want <= %d", size, historyWindow+1)
	}
}
