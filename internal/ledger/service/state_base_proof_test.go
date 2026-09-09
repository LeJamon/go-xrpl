package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func newPendingStateBaseCandidate(t *testing.T) (*Service, *ledger.Ledger, []*ledger.Ledger) {
	t.Helper()
	ctx := t.Context()
	db := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tracked := &stateBaseLateTipDatabase{KVDatabase: db}
	repositories := newTestRepositories(t, ctx)
	writer := newFastLoadCheckpointService(t, tracked, repositories, true)
	require.NoError(t, writer.Start())
	var persisted []*ledger.Ledger
	for range 2 {
		_, err := writer.AcceptLedger(ctx)
		require.NoError(t, err)
		persisted = append(persisted, writer.GetValidatedLedger())
	}
	writer.Stop()

	reader := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, reader.Start())
	t.Cleanup(reader.Stop)
	current := reader.GetValidatedLedger()
	h := current.Header()
	h.LedgerIndex += 10
	h.ParentHash = current.Hash()
	h.Validated = false
	h.Hash = header.CalculateHash(h)
	stateMap, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, h.AccountHash, reader.shamapFamily)
	require.NoError(t, err)
	require.NoError(t, stateMap.StartSync())
	require.NoError(t, stateMap.FinishSyncContext(ctx))
	require.NoError(t, reader.StoreLedgerWithState(ctx, &h, stateMap, nil))
	reader.FlushPersists()
	candidate, err := reader.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	return reader, candidate, persisted
}

func TestValidatedStateBaseOlderPersistencePreservesNewerCandidate(t *testing.T) {
	for _, test := range []struct {
		name  string
		index int
	}{
		{name: "matching proof", index: 1},
		{name: "older mismatched proof", index: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, candidate, persisted := newPendingStateBaseCandidate(t)
			proofBefore, found := svc.currentValidatedStateBaseProof()
			require.True(t, found)
			require.NoError(t, svc.persistValidatedLedger(t.Context(), persisted[test.index], true))
			proofAfter, found := svc.currentValidatedStateBaseProof()
			require.True(t, found)
			require.Equal(t, proofBefore, proofAfter)
			pending, found := svc.currentValidatedStateBaseCandidate()
			require.True(t, found)
			require.Equal(t, candidate.Hash(), pending.ledgerHash)

			require.NoError(t, svc.SwitchToPreferredLedger(candidate))
			svc.SetValidatedLedgerAt(candidate.Sequence(), candidate.Hash(), time.Time{})
			svc.FlushPersists()
			root, release, available, err := svc.AcquireValidatedStateBase(t.Context())
			require.NoError(t, err)
			require.True(t, available)
			defer release()
			require.Equal(t, candidate.Header().AccountHash, root)
			_, found = svc.currentValidatedStateBaseCandidate()
			require.False(t, found)
		})
	}
}

func TestValidatedStateBaseSameSequencePersistencePreservesAlternateCandidate(t *testing.T) {
	for _, matchingProof := range []bool{true, false} {
		name := "mismatched proof"
		if matchingProof {
			name = "matching proof"
		}
		t.Run(name, func(t *testing.T) {
			svc, candidate, _ := newPendingStateBaseCandidate(t)
			h := candidate.Header()
			h.CloseFlags ^= header.LCFNoConsensusTime
			h.Hash = header.CalculateHash(h)
			stateMap, err := candidate.StateMapSnapshot()
			require.NoError(t, err)
			txMap, err := candidate.TxMapSnapshot()
			require.NoError(t, err)
			older, err := ledger.NewFromHeader(h, stateMap, txMap, drops.Fees{})
			require.NoError(t, err)
			require.NoError(t, older.SetValidated())
			if matchingProof {
				proof, found := svc.currentValidatedStateBaseProof()
				require.True(t, found)
				svc.rememberValidatedStateBase(h, proof.nodeStoreFingerprint)
				svc.rememberValidatedStateBaseCandidate(candidate.Header())
			}
			require.NoError(t, svc.persistValidatedLedger(t.Context(), older, true))
			pending, found := svc.currentValidatedStateBaseCandidate()
			require.True(t, found)
			require.Equal(t, candidate.Hash(), pending.ledgerHash)

			require.NoError(t, svc.SwitchToPreferredLedger(candidate))
			svc.SetValidatedLedgerAt(candidate.Sequence(), candidate.Hash(), time.Time{})
			svc.FlushPersists()
			root, release, available, err := svc.AcquireValidatedStateBase(t.Context())
			require.NoError(t, err)
			require.True(t, available)
			defer release()
			require.Equal(t, candidate.Header().AccountHash, root)
		})
	}
}

func TestValidatedStateBaseTipOnlyPublicationPromotesCandidate(t *testing.T) {
	for _, relational := range []bool{true, false} {
		name := "with relational database"
		if !relational {
			name = "nodestore only"
		}
		t.Run(name, func(t *testing.T) {
			svc, candidate, _ := newPendingStateBaseCandidate(t)
			if !relational {
				svc.relationalDB = nil
			}
			require.NoError(t, candidate.SetValidated())
			require.NoError(t, svc.persistValidatedLedger(t.Context(), candidate, false))
			require.True(t, svc.hasDurableCompleteLedger(candidate))
			require.NoError(t, svc.SwitchToPreferredLedger(candidate))
			svc.SetValidatedLedgerAt(candidate.Sequence(), candidate.Hash(), time.Time{})
			svc.FlushPersists()
			root, release, available, err := svc.AcquireValidatedStateBase(t.Context())
			require.NoError(t, err)
			require.True(t, available)
			defer release()
			require.Equal(t, candidate.Header().AccountHash, root)
		})
	}
}

type stateBaseLateTipDatabase struct {
	*nodestore.KVDatabase
	beforeSync func()
}

func (d *stateBaseLateTipDatabase) Sync(ctx context.Context) error {
	if d.beforeSync != nil {
		callback := d.beforeSync
		d.beforeSync = nil
		callback()
	}
	return d.KVDatabase.Sync(ctx)
}

func TestValidatedStateBaseLateTipPublicationPromotesCandidate(t *testing.T) {
	svc, candidate, _ := newPendingStateBaseCandidate(t)
	require.NoError(t, candidate.SetValidated())
	job := &persistJob{
		l:               candidate,
		validated:       true,
		completionToken: svc.beginValidatedPersistence(candidate.Sequence(), candidate.Hash()),
	}
	svc.nodeStore.(*stateBaseLateTipDatabase).beforeSync = func() { job.updatesTip.Store(true) }
	svc.runPersistJob(job)
	proof, found := svc.currentValidatedStateBaseProof()
	require.True(t, found)
	require.Equal(t, candidate.Sequence(), proof.sequence)
	require.Equal(t, candidate.Hash(), proof.ledgerHash)
	_, found = svc.currentValidatedStateBaseCandidate()
	require.False(t, found)
}
