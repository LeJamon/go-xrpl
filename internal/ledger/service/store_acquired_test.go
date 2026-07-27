package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

func acquiredLedgerFixture(t *testing.T, seq uint32, tag byte) (*header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap) {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	hash := [32]byte{tag}
	return &header.LedgerHeader{
		LedgerIndex: seq,
		Hash:        hash,
		AccountHash: stateRoot,
		TxHash:      txRoot,
	}, stateMap, txMap
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

func TestStoredLedgerDefersPendingTrustedValidationUntilConsensusSwitch(t *testing.T) {
	for _, validationFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "validation-before-store", false: "validation-after-store"}[validationFirst], func(t *testing.T) {
			svc, err := New(DefaultConfig())
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			closedSeq := svc.GetClosedLedgerIndex()
			startValidated := svc.GetValidatedLedger()
			require.NotNil(t, startValidated)
			h, stateMap, txMap := acquiredLedgerFixture(t, closedSeq+4, 0xC1)
			if validationFirst {
				svc.SetValidatedLedger(h.LedgerIndex, h.Hash)
			}
			require.NoError(t, svc.StoreLedgerWithState(context.Background(), h, stateMap, txMap))
			if !validationFirst {
				svc.SetValidatedLedger(h.LedgerIndex, h.Hash)
			}

			require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
			require.Equal(t, startValidated.Hash(), svc.GetValidatedLedger().Hash())
			stored, err := svc.GetLedgerByHash(h.Hash)
			require.NoError(t, err)
			require.False(t, stored.IsValidated())
			svc.mu.RLock()
			_, pending := svc.pendingLedgerValidations[h.LedgerIndex]
			svc.mu.RUnlock()
			require.True(t, pending)

			require.NoError(t, svc.SwitchToPreferredLedger(stored))
			validated := svc.GetValidatedLedger()
			require.NotNil(t, validated)
			require.Equal(t, h.LedgerIndex, validated.Sequence())
			require.Equal(t, h.Hash, validated.Hash())
			require.True(t, validated.IsValidated())
			require.Equal(t, h.LedgerIndex, svc.GetClosedLedgerIndex())
			svc.mu.RLock()
			_, pending = svc.pendingLedgerValidations[h.LedgerIndex]
			svc.mu.RUnlock()
			require.False(t, pending)
		})
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

	preferred, preferredState, preferredTx := acquiredLedgerFixture(t, historical.LedgerIndex+1, 0xC3)
	preferred.ParentHash = historical.Hash
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
	_, pending := svc.pendingLedgerValidations[historical.LedgerIndex]
	svc.mu.RUnlock()
	require.True(t, indexed)
	require.Equal(t, historical.LedgerIndex, indexedSeq)
	require.Equal(t, uint32(7), indexedPosition)
	require.False(t, pending)
}

func TestIngestHistoricalLedgerWithStateRejectsNonCanonicalHistory(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	historical, stateMap, txMap := acquiredLedgerFixture(t, svc.GetClosedLedgerIndex()+9, 0xC4)
	preferred, preferredState, preferredTx := acquiredLedgerFixture(t, historical.LedgerIndex+1, 0xC5)
	preferred.ParentHash = [32]byte{0xFF}
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
