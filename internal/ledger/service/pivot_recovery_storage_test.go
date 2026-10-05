package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type recoveryStrictFamily struct {
	inner   *backend.NodeStore
	root    map[[32]byte]struct{}
	corrupt bool
}

func (f *recoveryStrictFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.inner.Fetch(ctx, hash)
}

func (f *recoveryStrictFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.inner.FetchDurable(ctx, hash)
	if err != nil || data == nil || !f.corrupt {
		return data, err
	}
	if _, isRoot := f.root[hash]; !isRoot {
		return []byte("corrupt durable descendant"), nil
	}
	return data, nil
}

func (f *recoveryStrictFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.inner.StoreBatch(ctx, entries)
}

func (f *recoveryStrictFamily) FullBelowCache() *shamap.FullBelowCache {
	return f.inner.FullBelowCache()
}

func recoveryStorageCandidate(t *testing.T, svc *Service, seq uint32, tag byte) *ledger.Ledger {
	t.Helper()
	h, stateMap, txMap := durableAcquiredLedgerFixture(t, svc, seq, tag)
	candidate, err := ledger.NewFromHeader(*h, stateMap, txMap, svc.GetClosedLedger().Fees())
	require.NoError(t, err)
	require.True(t, candidate.IsImmutable())
	return candidate
}

func TestStageRecoveryLedgerPersistsWithoutServiceVisibilityAndPinsGeneration(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	candidate := recoveryStorageCandidate(t, svc, svc.GetClosedLedgerIndex()+4, 0xD1)
	stage, err := svc.StageRecoveryLedger(t.Context(), candidate)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stage.Retire()) })

	node, err := db.Fetch(t.Context(), nodestore.Hash256(candidate.Hash()))
	require.NoError(t, err)
	require.NotNil(t, node)
	require.Equal(t, nodestore.NodeLedger, node.Type)
	require.Equal(t, candidate.Sequence(), node.LedgerSeq)

	svc.historyComponent.mu.RLock()
	_, inHistory := svc.ledgerByHash[candidate.Hash()]
	_, inPersistedCache := svc.persistedLedgers[candidate.Hash()]
	svc.historyComponent.mu.RUnlock()
	require.False(t, inHistory)
	require.False(t, inPersistedCache)
	_, err = svc.GetLedgerByHash(candidate.Hash())
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.NotEqual(t, candidate.Hash(), svc.GetValidatedLedger().Hash())

	deleteDone := make(chan struct{})
	go func() {
		_, _ = db.DeleteBefore(context.Background(), 1, 1)
		close(deleteDone)
	}()
	select {
	case <-deleteDone:
		t.Fatal("online deletion crossed the recovery storage admission")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, stage.Retire())
	select {
	case <-deleteDone:
	case <-time.After(time.Second):
		t.Fatal("online deletion did not resume after recovery storage retirement")
	}
	require.ErrorIs(t, stage.Install(t.Context()), ErrRecoveryStorageRetired)
}

func TestStageRecoveryLedgerFlushesDirtyBackedMapBeforeStrictVerify(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	base := recoveryStorageCandidate(t, svc, svc.GetClosedLedgerIndex()+4, 0xD4)
	require.NoError(t, svc.persistToNodeStore(t.Context(), base, base.Sequence()))
	stateRoot, err := base.StateMapHash()
	require.NoError(t, err)
	backedState, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, stateRoot, cfg.SHAMapFamily)
	require.NoError(t, err)
	require.NoError(t, backedState.Put([32]byte{0xD5, 0x04}, []byte("replayed-successor-state")))
	updatedStateRoot, err := backedState.Hash()
	require.NoError(t, err)
	require.NotEqual(t, stateRoot, updatedStateRoot)

	h := base.Header()
	h.AccountHash = updatedStateRoot
	h.Hash = header.CalculateHash(h)
	candidate, err := ledger.NewFromHeader(h, backedState, shamap.New(shamap.TypeTransaction), base.Fees())
	require.NoError(t, err)
	stage, err := svc.StageRecoveryLedger(t.Context(), candidate)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stage.Retire()) })
}

func TestReplayRecoveryFrontierStoreReloadsDetachedLedger(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	candidate := recoveryStorageCandidate(t, svc, svc.GetClosedLedgerIndex()+3, 0xD6)
	parent := svc.GetClosedLedger()
	fault := replayfault.Fault{
		ID:         "frontier-store-fault",
		ParentHash: parent.Hash(),
		TargetHash: candidate.Hash(),
		Sequence:   candidate.Sequence(),
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, fault))
	fault = *svc.replayFaults.Snapshot()
	progress, err := svc.replayFaults.BeginRecovery(fault.ID, replayfault.RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	for _, phase := range []replayfault.RecoveryPhase{replayfault.RecoveryPhaseAuthenticating, replayfault.RecoveryPhaseAcquiringPivot} {
		progress.Phase = phase
		progress, err = svc.replayFaults.UpdateRecovery(fault.ID, progress.Generation, progress)
		require.NoError(t, err)
	}
	progress.Phase = replayfault.RecoveryPhaseCatchingUp
	progress.FrontierSequence = candidate.Sequence()
	progress.FrontierHash = candidate.Hash()
	status := svc.ReplayFaultStatus()
	require.Equal(t, progress.Generation, status.Recovery.Generation)
	require.NoError(t, svc.StageReplayRecoveryFrontier(t.Context(), status.Recovery.ID, status.Recovery.Generation, candidate))
	_, err = svc.LoadReplayRecoveryFrontier(t.Context(), status.Recovery.ID, status.Recovery.Generation, candidate.Header())
	require.ErrorIs(t, err, ErrRecoveryStorageFrontierNotFound)
	frontierProof, err := json.Marshal(map[string]any{
		"ledger_index": candidate.Sequence(),
		"ledger_hash":  candidate.Hash(),
		"executed":     true,
	})
	require.NoError(t, err)
	progress, err = svc.replayFaults.UpdateRecoveryWithFrontier(fault.ID, progress.Generation, progress, frontierProof)
	require.NoError(t, err)
	status = svc.ReplayFaultStatus()
	loaded, err := svc.LoadReplayRecoveryFrontier(t.Context(), status.Recovery.ID, status.Recovery.Generation, candidate.Header())
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, candidate.Hash(), loaded.Hash())
	require.ErrorIs(t, func() error {
		_, err := svc.GetLedgerByHash(candidate.Hash())
		return err
	}(), svcerr.ErrLedgerNotFound)
	require.True(t, svc.ReplayBlocked())
}

func TestStageRecoveryLedgerRejectsCorruptDurableDescendantDespiteWarmProof(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	inner := backend.New(db)
	family := &recoveryStrictFamily{inner: inner, root: make(map[[32]byte]struct{})}
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = family
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	candidate := recoveryStorageCandidate(t, svc, svc.GetClosedLedgerIndex()+4, 0xD3)
	require.NoError(t, svc.persistToNodeStore(t.Context(), candidate, candidate.Sequence()))
	stateRoot, err := candidate.StateMapHash()
	require.NoError(t, err)
	family.root[stateRoot] = struct{}{}
	stateMap, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, stateRoot, family)
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	backed, err := ledger.NewFromHeader(candidate.Header(), stateMap, txMap, candidate.Fees())
	require.NoError(t, err)

	// Populate the ordinary family proof while durable bytes are still intact.
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), backed))
	family.corrupt = true
	_, err = svc.StageRecoveryLedger(t.Context(), backed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovery state tree")
	// Staging remains detached even when verification rejects the durable tree.
	_, err = svc.GetLedgerByHash(candidate.Hash())
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
}

func TestActivateRecoveryStorageRequiresVerifiedPivotJournalAndExactGeneration(t *testing.T) {
	svc := replayFaultService(t, "")
	candidate := recoveryStorageCandidate(t, svc, svc.GetClosedLedgerIndex()+2, 0xD2)
	stage, err := svc.StageRecoveryLedger(t.Context(), candidate)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stage.Retire()) })

	parent := svc.GetClosedLedger()
	fault := replayfault.Fault{
		ID:         "storage-activation-fault",
		ParentHash: parent.Hash(),
		TargetHash: candidate.Hash(),
		Sequence:   candidate.Sequence(),
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, fault))
	fault = *svc.replayFaults.Snapshot()
	entered := make(chan RecoveryStorageActivation, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- svc.replayFaults.Revalidate(t.Context(), fault.ID, func(context.Context, replayfault.Fault) error {
			status := svc.ReplayFaultStatus()
			entered <- RecoveryStorageActivation{
				FaultID:    status.Recovery.ID,
				Generation: status.Recovery.Generation,
			}
			<-release
			return nil
		})
	}()
	activation := <-entered
	require.True(t, svc.ReplayBlocked())
	releaseRetention, err := svc.AcquireRecoveryRetention(t.Context(), activation.FaultID, candidate.Sequence()-1)
	require.NoError(t, err)
	releaseRetention()
	_, err = svc.AcquireRecoveryRetention(t.Context(), activation.FaultID, candidate.Sequence())
	require.ErrorIs(t, err, ErrRecoveryStorageActivationStale)
	before := svc.GetValidatedLedger().Hash()

	stale := activation
	stale.Generation++
	require.ErrorIs(t, svc.ActivateRecoveryStorage(t.Context(), stage, stale), ErrRecoveryStorageActivationStale)
	require.Equal(t, before, svc.GetValidatedLedger().Hash())
	require.True(t, svc.ReplayBlocked())
	svc.historyComponent.mu.RLock()
	_, visible := svc.ledgerByHash[candidate.Hash()]
	_, cached := svc.persistedLedgers[candidate.Hash()]
	svc.historyComponent.mu.RUnlock()
	require.False(t, visible)
	require.False(t, cached)

	// Revalidate supplies an in-flight token without a verified-pivot journal
	// phase. It must never authorize publishing an arbitrary snapshot.
	require.ErrorIs(t, svc.ActivateRecoveryStorage(t.Context(), stage, activation), ErrRecoveryStorageActivationStale)
	require.Equal(t, before, svc.GetValidatedLedger().Hash())
	require.True(t, svc.ReplayBlocked())
	svc.historyComponent.mu.RLock()
	_, visible = svc.ledgerByHash[candidate.Hash()]
	_, cached = svc.persistedLedgers[candidate.Hash()]
	svc.historyComponent.mu.RUnlock()
	require.False(t, visible)
	require.False(t, cached)
	require.False(t, svc.RecoveryFrontierReady(candidate.Header()))

	close(release)
	require.NoError(t, <-done)
	require.False(t, svc.ReplayBlocked())
}

func TestRecoveryActivationGuardSerializesJournalUpdate(t *testing.T) {
	svc := replayFaultService(t, "")
	parent := svc.GetClosedLedger()
	fault := replayfault.Fault{
		ID:         "activation-guard-fault",
		ParentHash: parent.Hash(),
		TargetHash: parent.Hash(),
		Sequence:   parent.Sequence() + 1,
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, fault))

	progress, err := svc.replayFaults.BeginRecovery(fault.ID, replayfault.RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	pivotHash := [32]byte{0x31}
	successorHash := [32]byte{0x32}
	progress.PivotSequence = fault.Sequence + 1
	progress.PivotHash = pivotHash
	for _, phase := range []replayfault.RecoveryPhase{
		replayfault.RecoveryPhaseAuthenticating,
		replayfault.RecoveryPhaseAcquiringPivot,
		replayfault.RecoveryPhaseCatchingUp,
	} {
		progress.Phase = phase
		if phase == replayfault.RecoveryPhaseCatchingUp {
			progress.FrontierSequence = progress.PivotSequence
			progress.FrontierHash = pivotHash
		}
		progress, err = svc.replayFaults.UpdateRecovery(fault.ID, progress.Generation, progress)
		require.NoError(t, err)
	}
	progress.SuccessorSequence = progress.PivotSequence + 1
	progress.SuccessorHash = successorHash
	progress.FrontierSequence = progress.SuccessorSequence
	progress.FrontierHash = successorHash
	progress.Phase = replayfault.RecoveryPhaseReadyToActivate
	progress, err = svc.replayFaults.UpdateRecovery(fault.ID, progress.Generation, progress)
	require.NoError(t, err)
	progress.Phase = replayfault.RecoveryPhaseActivating
	progress, err = svc.replayFaults.UpdateRecovery(fault.ID, progress.Generation, progress)
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- svc.replayFaults.WithRecoveryActivation(fault.ID, progress.Generation, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	updated := *svc.replayFaults.Snapshot()
	updated.Message = "diagnostic update"
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- svc.replayFaults.Update(fault.ID, updated)
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("journal update crossed the activation guard: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-guardDone)
	require.NoError(t, <-updateDone)
	status := svc.ReplayFaultStatus()
	require.False(t, status.Recovery.InFlight)
	require.Equal(t, "diagnostic update", status.Fault.Message)
}
