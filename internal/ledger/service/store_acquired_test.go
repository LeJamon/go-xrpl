package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
)

func acquiredLedgerFixture(t *testing.T, seq uint32, tag byte) (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap) {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	h := &header.LedgerHeader{
		LedgerIndex: seq,
		ParentHash:  [32]byte{tag},
		AccountHash: stateRoot,
		TxHash:      txRoot,
	}
	h.Hash = header.CalculateHash(*h)
	return h, stateMap, txMap
}

func durableAcquiredLedgerFixture(t *testing.T, svc *Service, seq uint32, tag byte) (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap) {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	stateKey := [32]byte{tag, byte(seq), byte(seq >> 8), 0xAC}
	require.NoError(t, stateMap.Put(stateKey, []byte("durable-acquired-state")))
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	h := &header.LedgerHeader{
		LedgerIndex:         seq,
		ParentHash:          [32]byte{tag},
		Drops:               svc.genesisLedger.TotalDrops(),
		AccountHash:         stateRoot,
		TxHash:              txRoot,
		CloseTime:           time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		ParentCloseTime:     time.Unix(1_699_999_990+int64(seq), 0).UTC(),
		CloseTimeResolution: 10,
	}
	h.Hash = header.CalculateHash(*h)
	return h, stateMap, txMap
}

func TestStoreLedgerWithStateDoesNotMoveCanonicalFrontier(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closedSeq := svc.GetClosedLedgerIndex()
	openSeq := svc.GetCurrentLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()
	h, stateMap, txMap := acquiredLedgerFixture(t, closedSeq+5, 0xA1)

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), h, stateMap, txMap))
	stored, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.Equal(t, h.LedgerIndex, stored.Sequence())
	require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
	require.Equal(t, closedHash, svc.GetClosedLedger().Hash())
	require.Equal(t, openSeq, svc.GetCurrentLedgerIndex())
	_, err = svc.AdoptedLedgerBySequence(h.LedgerIndex)
	require.ErrorIs(t, err, ErrLedgerNotFound)
}

func TestBootstrapLedgerWithStateStagesUntilConsensusSwitch(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.True(t, svc.NeedsInitialSync())
	originalClosed := svc.GetClosedLedger()
	require.NotNil(t, originalClosed)

	first, firstState, firstTx := acquiredLedgerFixture(t, 100, 0xB1)
	initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), first, firstState, firstTx)
	require.NoError(t, err)
	require.True(t, initialCandidate)
	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, originalClosed.Hash(), svc.GetClosedLedger().Hash())
	storedFirst, err := svc.GetLedgerByHash(first.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(storedFirst))
	require.False(t, svc.NeedsInitialSync())
	require.Equal(t, first.Hash, svc.GetClosedLedger().Hash())

	second, secondState, secondTx := acquiredLedgerFixture(t, 105, 0xB2)
	initialCandidate, err = svc.BootstrapLedgerWithState(t.Context(), second, secondState, secondTx)
	require.NoError(t, err)
	require.False(t, initialCandidate)
	require.Equal(t, first.LedgerIndex, svc.GetClosedLedgerIndex())
	stored, err := svc.GetLedgerByHash(second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.LedgerIndex, stored.Sequence())
}

func TestStoredLedgerValidationAdvancesIndependentlyOfConsensusSwitch(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	h, stateMap, txMap := acquiredLedgerFixture(t, closed.Sequence()+1, 0xC1)
	h.ParentHash = closed.Hash()
	txBlob, txHash := makeTxMetaBlobForTest(t, []byte("stored-validation-tx-padding-pad"), 3)
	require.NoError(t, txMap.PutWithNodeType(txHash, txBlob, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	h.TxHash = txRoot
	h.Hash = header.CalculateHash(*h)
	events := make(chan *LedgerAcceptedEvent, 1)
	svc.SetEventSink(EventSinkFunc(func(event *LedgerAcceptedEvent) error {
		events <- event
		return nil
	}))

	svc.SetValidatedLedger(h.LedgerIndex, h.Hash)
	require.Equal(t, closed.Hash(), svc.GetValidatedLedger().Hash())
	require.NoError(t, svc.StoreLedgerWithState(context.Background(), h, stateMap, txMap))
	require.Equal(t, closed.Hash(), svc.GetValidatedLedger().Hash())

	svc.PromoteStoredValidatedLedgerAt(h.LedgerIndex, h.Hash, time.Time{})
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	require.Equal(t, h.LedgerIndex, validated.Sequence())
	require.Equal(t, h.Hash, validated.Hash())
	require.True(t, validated.IsValidated())
	require.Equal(t, closed.Hash(), svc.GetClosedLedger().Hash())
	select {
	case event := <-events:
		require.Len(t, event.TransactionResults, 1)
		require.Equal(t, txHash, event.TransactionResults[0].TxHash)
		require.True(t, event.TransactionResults[0].Validated)
	case <-time.After(time.Second):
		t.Fatal("stored validated ledger event was not published")
	}
	svc.mu.RLock()
	indexedSeq, indexed := svc.txIndex[txHash]
	indexedPosition := svc.txPositionIndex[txHash]
	svc.mu.RUnlock()
	require.True(t, indexed)
	require.Equal(t, h.LedgerIndex, indexedSeq)
	require.Equal(t, uint32(3), indexedPosition)
	adopted, err := svc.AdoptedLedgerBySequence(h.LedgerIndex)
	require.NoError(t, err)
	require.Equal(t, h.Hash, adopted.Hash())

	svc.PromoteStoredValidatedLedgerAt(h.LedgerIndex, h.Hash, time.Time{})
	svc.SetValidatedLedgerAt(h.LedgerIndex, h.Hash, time.Time{})
	select {
	case <-events:
		t.Fatal("stored validated ledger event was published more than once")
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, svc.SwitchToPreferredLedger(validated))
	require.Equal(t, h.Hash, svc.GetClosedLedger().Hash())
}

func TestStoredLedgerPromotionLoadsAfterCacheEviction(t *testing.T) {
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 1
	cfg.NodeStore = db
	cfg.SHAMapFamily = shamapbackend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	first, firstState, firstTx := durableAcquiredLedgerFixture(t, svc, svc.GetClosedLedgerIndex()+1, 0xC3)
	second, secondState, secondTx := durableAcquiredLedgerFixture(t, svc, first.LedgerIndex+1, 0xC4)
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), first, firstState, firstTx))
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), second, secondState, secondTx))
	svc.FlushPersists()

	svc.historyComponent.mu.RLock()
	_, firstCached := svc.persistedLedgers[first.Hash]
	svc.historyComponent.mu.RUnlock()
	require.False(t, firstCached)

	svc.PromoteStoredValidatedLedgerAt(first.LedgerIndex, first.Hash, time.Time{})
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	require.Equal(t, first.Hash, validated.Hash())
	require.True(t, validated.IsValidated())
}

func TestStoredLedgerValidationPublishesTransactionResultsAfterConsensusSwitch(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	events := make(chan *LedgerAcceptedEvent, 1)
	svc.SetEventSink(EventSinkFunc(func(event *LedgerAcceptedEvent) error {
		events <- event
		return nil
	}))

	h, stateMap, txMap := acquiredLedgerFixture(t, svc.GetClosedLedgerIndex()+4, 0xC2)
	txBlob, txHash := makeTxMetaBlobForTest(t, []byte("stored-ledger-event-tx-padding"), 0)
	require.NoError(t, txMap.PutWithNodeType(txHash, txBlob, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	h.TxHash = txRoot
	h.Hash = header.CalculateHash(*h)

	require.NoError(t, svc.StoreLedgerWithState(t.Context(), h, stateMap, txMap))
	stored, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(stored))
	svc.SetValidatedLedgerAt(h.LedgerIndex, h.Hash, time.Now())

	select {
	case event := <-events:
		require.Equal(t, h.Hash, event.LedgerInfo.Hash)
		require.Len(t, event.TransactionResults, 1)
		require.Equal(t, txHash, event.TransactionResults[0].TxHash)
		require.True(t, event.TransactionResults[0].Validated)
	case <-time.After(time.Second):
		t.Fatal("validated ledger event was not published")
	}
}

func TestSwitchToCurrentPreferredLedgerCompletesInitialSync(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.True(t, svc.NeedsInitialSync())

	require.NoError(t, svc.SwitchToPreferredLedger(svc.GetClosedLedger()))
	require.False(t, svc.NeedsInitialSync())
}

func TestIngestHistoricalLedgerWithStatePreservesFrontiers(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	historical, stateMap, txMap := acquiredLedgerFixture(t, svc.GetClosedLedgerIndex()+9, 0xC2)
	txBlob, txHash := makeTxMetaBlobForTest(t, []byte("historical-ingest-tx-padding-padpad"), 7)
	require.NoError(t, txMap.PutWithNodeType(txHash, txBlob, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	historical.TxHash = txRoot
	historical.Hash = header.CalculateHash(*historical)

	preferred, preferredState, preferredTx := acquiredLedgerFixture(t, historical.LedgerIndex+1, 0xC3)
	preferred.ParentHash = historical.Hash
	preferred.Hash = header.CalculateHash(*preferred)
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), preferred, preferredState, preferredTx))
	storedPreferred, err := svc.GetLedgerByHash(preferred.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(storedPreferred))

	closedBefore := svc.GetClosedLedger()
	require.NotNil(t, closedBefore)
	openSeqBefore := svc.GetCurrentLedgerIndex()
	validatedBefore := svc.GetValidatedLedger()
	require.NotNil(t, validatedBefore)
	needsInitialSyncBefore := svc.NeedsInitialSync()
	svc.SetValidatedLedger(historical.LedgerIndex, historical.Hash)
	require.Equal(t, validatedBefore.Hash(), svc.GetValidatedLedger().Hash())

	require.NoError(t, svc.IngestHistoricalLedgerWithState(t.Context(), historical, stateMap, txMap))
	require.NoError(t, svc.IngestHistoricalLedgerWithState(t.Context(), historical, stateMap, txMap))
	svc.FlushPersists()

	got, err := svc.AdoptedLedgerBySequence(historical.LedgerIndex)
	require.NoError(t, err)
	require.Equal(t, historical.Hash, got.Hash())
	require.True(t, got.IsValidated())
	require.True(t, svc.hasCompleteLedger(got))
	require.Equal(t, closedBefore.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, openSeqBefore, svc.GetCurrentLedgerIndex())
	require.Equal(t, validatedBefore.Hash(), svc.GetValidatedLedger().Hash())
	require.Equal(t, needsInitialSyncBefore, svc.NeedsInitialSync())
	svc.mu.RLock()
	indexedSeq, indexed := svc.txIndex[txHash]
	indexedPosition := svc.txPositionIndex[txHash]
	svc.mu.RUnlock()
	require.True(t, indexed)
	require.Equal(t, historical.LedgerIndex, indexedSeq)
	require.Equal(t, uint32(7), indexedPosition)
}

func TestIngestHistoricalLedgerWithStateRejectsNonCanonicalHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	historical, stateMap, txMap := acquiredLedgerFixture(t, svc.GetClosedLedgerIndex()+9, 0xC4)
	preferred, preferredState, preferredTx := acquiredLedgerFixture(t, historical.LedgerIndex+1, 0xC5)
	preferred.ParentHash = [32]byte{0xFF}
	preferred.Hash = header.CalculateHash(*preferred)
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), preferred, preferredState, preferredTx))
	storedPreferred, err := svc.GetLedgerByHash(preferred.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(storedPreferred))

	closedBefore := svc.GetClosedLedger()
	require.Error(t, svc.IngestHistoricalLedgerWithState(t.Context(), historical, stateMap, txMap))
	_, err = svc.AdoptedLedgerBySequence(historical.LedgerIndex)
	require.ErrorIs(t, err, ErrLedgerNotFound)
	require.Equal(t, closedBefore.Hash(), svc.GetClosedLedger().Hash())

	current, currentState, currentTx := acquiredLedgerFixture(t, closedBefore.Sequence(), 0xC6)
	require.Error(t, svc.IngestHistoricalLedgerWithState(t.Context(), current, currentState, currentTx))
	_, err = svc.GetLedgerByHash(current.Hash)
	require.ErrorIs(t, err, ErrLedgerNotFound)
}

func TestStoredLedgerMovesCanonicalFrontierOnlyThroughConsensusSwitch(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	originalClosed := svc.GetClosedLedger()
	require.NotNil(t, originalClosed)
	h, stateMap, txMap := acquiredLedgerFixture(t, originalClosed.Sequence()+3, 0xD1)
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), h, stateMap, txMap))
	parent, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.Equal(t, originalClosed.Hash(), svc.GetClosedLedger().Hash())

	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	require.Equal(t, parent.Sequence(), svc.GetClosedLedgerIndex())
	require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, parent.Sequence()+1, svc.GetCurrentLedgerIndex())

	closedSeq, err := svc.AcceptConsensusResult(
		t.Context(), parent, nil, nil, time.Now().UTC(), true,
	)
	require.NoError(t, err)
	require.Equal(t, parent.Sequence()+1, closedSeq)
	require.Equal(t, parent.Hash(), svc.GetClosedLedger().ParentHash())
}
