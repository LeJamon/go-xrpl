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

func TestBootstrapLedgerWithStateAdoptsOnlyFirstPeerLedger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.True(t, svc.NeedsInitialSync())

	first, firstState, firstTx := acquiredLedgerFixture(t, 100, 0xB1)
	bootstrapped, err := svc.BootstrapLedgerWithState(t.Context(), first, firstState, firstTx)
	require.NoError(t, err)
	require.True(t, bootstrapped)
	require.False(t, svc.NeedsInitialSync())
	require.Equal(t, first.LedgerIndex, svc.GetClosedLedgerIndex())

	second, secondState, secondTx := acquiredLedgerFixture(t, 105, 0xB2)
	bootstrapped, err = svc.BootstrapLedgerWithState(t.Context(), second, secondState, secondTx)
	require.NoError(t, err)
	require.False(t, bootstrapped)
	require.Equal(t, first.LedgerIndex, svc.GetClosedLedgerIndex())
	stored, err := svc.GetLedgerByHash(second.Hash)
	require.NoError(t, err)
	require.Equal(t, second.LedgerIndex, stored.Sequence())
}

func TestStoredLedgerDrainsPendingTrustedValidationWithoutMovingClosed(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closedSeq := svc.GetClosedLedgerIndex()
	h, stateMap, txMap := acquiredLedgerFixture(t, closedSeq+4, 0xC1)
	svc.SetValidatedLedger(h.LedgerIndex, h.Hash)
	require.NoError(t, svc.StoreLedgerWithState(context.Background(), h, stateMap, txMap))

	require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	require.Equal(t, h.LedgerIndex, validated.Sequence())
	require.Equal(t, h.Hash, validated.Hash())
	require.True(t, validated.IsValidated())
	stored, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.True(t, stored.IsValidated())
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
