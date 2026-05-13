package service

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
)

// TestEvictOldHistoryLocked verifies that ledgerHistory and the tx
// indexes are pruned at the historyWindow boundary, with entries
// above the cutoff preserved intact.
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
		l := ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})
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

	// Populate 3 * historyWindow entries so eviction has plenty to chew on.
	const totalLedgers = historyWindow * 3
	var latestSeq uint32 = 1
	for i := 0; i < totalLedgers; i++ {
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

	// Tx-index entries for evicted ledgers must be gone; entries for
	// surviving ledgers must remain.
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

// TestEvictOldHistoryLocked_BelowWindow verifies that no eviction
// occurs while the validated sequence is still within the first
// historyWindow ledgers (a node fresh out of genesis).
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
		svc.ledgerHistory[seq] = ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})
	}

	before := len(svc.ledgerHistory)
	svc.mu.Lock()
	svc.evictOldHistoryLocked(historyWindow / 2)
	svc.mu.Unlock()

	if got := len(svc.ledgerHistory); got != before {
		t.Errorf("ledgerHistory size changed despite being below window: before=%d after=%d", before, got)
	}
}

// TestAcceptLedgerLoop_BoundsHistory drives the operational path:
// AcceptLedgerAt is called repeatedly past the historyWindow boundary,
// and ledgerHistory must remain bounded.
func TestAcceptLedgerLoop_BoundsHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < historyWindow*2; i++ {
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
}

// TestDrainPendingValidation_EvictsHistory verifies that the inline
// validation-drain promotion path triggers cache eviction. This is the
// race where SetValidatedLedger arrives before the close, gets
// stashed, and the close drains + promotes inline — without this
// eviction call, the AcceptConsensusResult and adoptLedgerWithState
// callers would leak entries past the window.
func TestDrainPendingValidation_EvictsHistory(t *testing.T) {
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

	// Build a synthetic adopted ledger at a seq well past the window
	// so eviction has work to do.
	const adoptedSeq uint32 = historyWindow + 50
	adoptedState, adoptedTx := freshMaps()
	var adoptedHeader header.LedgerHeader
	adoptedHeader.LedgerIndex = adoptedSeq
	adoptedHeader.Hash[0] = 0x77
	adopted := ledger.NewOpenWithHeader(adoptedHeader, adoptedState, adoptedTx, drops.Fees{})

	// Stash old entries below the post-eviction cutoff that the drain
	// must clean up.
	cutoff := adoptedSeq - historyWindow
	for seq := uint32(1); seq <= cutoff; seq++ {
		st, tx := freshMaps()
		var h header.LedgerHeader
		h.LedgerIndex = seq
		svc.ledgerHistory[seq] = ledger.NewOpenWithHeader(h, st, tx, drops.Fees{})
	}

	// Stash a pending validation so drainPendingLedgerValidationLocked
	// will promote the adopted ledger inline.
	svc.mu.Lock()
	svc.stashPendingLedgerValidationLocked(adoptedSeq, adopted.Hash())
	promoted := svc.drainPendingLedgerValidationLocked(adoptedSeq, adopted)
	size := len(svc.ledgerHistory)
	svc.mu.Unlock()

	if !promoted {
		t.Fatalf("drainPendingLedgerValidationLocked did not promote — stash setup is wrong")
	}
	if size > historyWindow+1 {
		t.Errorf("inline drain-promote left ledgerHistory unbounded: got %d, want <= %d", size, historyWindow+1)
	}
}
