package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
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
	for _, window := range []uint32{64, 256, 384} {
		t.Run(fmt.Sprintf("window_%d", window), func(t *testing.T) {
			testEvictOldHistoryLocked(t, window)
		})
	}
}

func testEvictOldHistoryLocked(t *testing.T, window uint32) {
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = window
	svc, err := New(cfg)
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

	totalLedgers := window * 3
	var latestSeq uint32 = 1
	for range totalLedgers {
		svc.ledgerHistory[latestSeq] = makeLedger(latestSeq, 0x42)
		latestSeq++
	}
	latestValidated := latestSeq - 1

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.evictOldHistoryLocked(latestValidated)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	if got := len(svc.ledgerHistory); got != int(window) {
		t.Errorf("ledgerHistory size after eviction: got %d, want %d", got, window)
	}

	cutoff := latestValidated - window
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
	if got, want := len(svc.txIndex), int(window); got != want {
		t.Errorf("txIndex size: got %d, want %d", got, want)
	}
	if got, want := len(svc.txPositionIndex), int(window); got != want {
		t.Errorf("txPositionIndex size: got %d, want %d", got, want)
	}
}

func TestEvictOldHistoryLocked_BelowWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 64
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	window := svc.ledgerCacheSize()
	for seq := uint32(1); seq <= window/2; seq++ {
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
	svc.historyComponent.mu.Lock()
	svc.evictOldHistoryLocked(window / 2)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	if got := len(svc.ledgerHistory); got != before {
		t.Errorf("ledgerHistory size changed despite being below window: before=%d after=%d", before, got)
	}
}

func TestAcceptLedgerLoop_BoundsHistory(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 64
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx := context.Background()
	window := svc.ledgerCacheSize()
	for i := range window * 2 {
		if _, err := svc.AcceptLedger(ctx); err != nil {
			t.Fatalf("AcceptLedger #%d: %v", i, err)
		}
	}

	svc.mu.Lock()
	size := len(svc.ledgerHistory)
	svc.mu.Unlock()

	if size > int(window+1) {
		t.Errorf("ledgerHistory unbounded under AcceptLedger loop: got %d, want <= %d", size, window+1)
	}
	last := svc.GetClosedLedgerIndex()
	if got, want := svc.GetServerInfo().CompleteLedgers, formatRange(last-window+1, last); got != want {
		t.Errorf("complete_ledgers includes evicted in-memory history: got %q, want %q", got, want)
	}
}

func TestDelayedValidationSurvivesHistoryEviction(t *testing.T) {
	for _, evictCandidate := range []bool{false, true} {
		name := "retained_candidate"
		if evictCandidate {
			name = "durable_fallback"
		}
		t.Run(name, func(t *testing.T) {
			db := newTestNodeStore(t, 10_000)
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close nodestore: %v", err)
				}
			})
			cfg := DefaultConfig()
			cfg.LedgerCacheSize = 1
			cfg.NodeStore = db
			cfg.SHAMapFamily = shamapbackend.New(db)
			svc, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := svc.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(svc.Stop)

			ctx := context.Background()
			parent := svc.GetClosedLedger()
			closeTime := time.Now().UTC()
			firstSeq, err := svc.AcceptConsensusResult(ctx, parent, nil, nil, closeTime, true)
			if err != nil {
				t.Fatalf("AcceptConsensusResult(first): %v", err)
			}
			first := svc.GetClosedLedger()
			firstHash := first.Hash()
			secondSeq, err := svc.AcceptConsensusResult(ctx, first, nil, nil, closeTime.Add(time.Second), true)
			if err != nil {
				t.Fatalf("AcceptConsensusResult(second): %v", err)
			}
			second := svc.GetClosedLedger()
			svc.mu.RLock()
			_, pendingEvent := svc.pendingValidation[firstHash]
			svc.mu.RUnlock()
			if pendingEvent {
				t.Fatal("validation event retained without an event sink")
			}
			if evictCandidate {
				svc.FlushPersists()
				svc.mu.Lock()
				svc.drainValidationCandidateLocked(firstSeq, firstHash)
				svc.mu.Unlock()
			}

			svc.SetValidatedLedger(secondSeq, second.Hash())
			svc.historyComponent.mu.RLock()
			_, firstStillCached := svc.ledgerHistory[firstSeq]
			svc.historyComponent.mu.RUnlock()
			if firstStillCached {
				t.Fatalf("ledger %d remained in one-ledger history window", firstSeq)
			}
			if first.IsValidated() {
				t.Fatalf("ledger %d validated before delayed quorum", firstSeq)
			}

			svc.SetValidatedLedger(firstSeq, firstHash)
			if !first.IsValidated() && !evictCandidate {
				t.Fatalf("ledger %d was not validated after history eviction", firstSeq)
			}
			if evictCandidate {
				svc.FlushPersists()
				validated, err := svc.GetLedgerBySequence(firstSeq)
				if err != nil || !validated.IsValidated() {
					t.Fatalf("durable ledger %d was not validated after cache eviction: %v", firstSeq, err)
				}
			}
			svc.mu.RLock()
			candidate := svc.validationCandidates[firstSeq]
			svc.mu.RUnlock()
			if candidate != nil {
				t.Fatalf("ledger %d remained a validation candidate", firstSeq)
			}
		})
	}
}

func TestSetValidatedLedgerRejectsRetainedCandidateOnConflictingHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	seq, err := svc.AcceptConsensusResult(
		context.Background(), svc.GetClosedLedger(), nil, nil, time.Now().UTC(), true,
	)
	if err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}
	candidate := svc.GetClosedLedger()
	candidateHash := candidate.Hash()
	h, stateMap, txMap := acquiredLedgerFixture(t, seq, 0xF1)
	conflicting, err := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewFromHeader: %v", err)
	}
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(conflicting)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	svc.SetValidatedLedger(seq, candidateHash)
	if candidate.IsValidated() {
		t.Fatal("stale validation candidate replaced conflicting history")
	}
	svc.historyComponent.mu.RLock()
	got := svc.ledgerHistory[seq]
	svc.historyComponent.mu.RUnlock()
	if got == nil || got.Hash() != conflicting.Hash() {
		t.Fatal("conflicting history changed after stale validation")
	}
}

func TestForkCleanupDropsEvictedValidationCandidate(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	ctx := context.Background()
	closeTime := time.Now().UTC()
	parent := svc.GetClosedLedger()
	_, err = svc.AcceptConsensusResult(ctx, parent, nil, nil, closeTime, true)
	if err != nil {
		t.Fatalf("AcceptConsensusResult(parent): %v", err)
	}
	staleParent := svc.GetClosedLedger()
	staleSeq, err := svc.AcceptConsensusResult(
		ctx, staleParent, nil, nil, closeTime.Add(time.Second), true,
	)
	if err != nil {
		t.Fatalf("AcceptConsensusResult(stale): %v", err)
	}
	stale := svc.GetClosedLedger()
	staleHash := stale.Hash()
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.deleteHistoryLocked(staleSeq)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	replacementHeader, stateMap, txMap := acquiredLedgerFixture(t, staleSeq, 0xF2)
	replacement, err := ledger.NewFromHeader(*replacementHeader, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewFromHeader: %v", err)
	}
	if err := svc.SwitchToPreferredLedger(replacement); err != nil {
		t.Fatalf("SwitchToPreferredLedger: %v", err)
	}
	svc.mu.RLock()
	candidate := svc.validationCandidates[staleSeq]
	svc.mu.RUnlock()
	if candidate == nil || candidate.Hash() != replacement.Hash() {
		t.Fatal("fork cleanup retained the evicted stale validation candidate")
	}

	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.deleteHistoryLocked(staleSeq)
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()
	svc.SetValidatedLedger(staleSeq, staleHash)
	if stale.IsValidated() || svc.GetValidatedLedger().Hash() == staleHash {
		t.Fatal("orphaned validation candidate advanced the validated frontier")
	}
}

// Covers the race where SetValidatedLedger arrives before the close
// and is drained + promoted inline — eviction must run on that path
// because no second SetValidatedLedger arrives for the same seq.
func TestValidatedPromotionEvictsHistory(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 64
	svc, err := New(cfg)
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

	window := svc.ledgerCacheSize()
	adoptedSeq := window + 50
	adoptedState, adoptedTx := freshMaps()
	var adoptedHeader header.LedgerHeader
	adoptedHeader.LedgerIndex = adoptedSeq
	adoptedHeader.Hash[0] = 0x77
	adopted := mustNewOpenWithHeader(t, adoptedHeader, adoptedState, adoptedTx)

	// Seed entries below the post-eviction cutoff so promotion has
	// observable work to do.
	cutoff := adoptedSeq - window
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

	if size > int(window+1) {
		t.Errorf("validated promotion left ledgerHistory unbounded: got %d, want <= %d", size, window+1)
	}
}
