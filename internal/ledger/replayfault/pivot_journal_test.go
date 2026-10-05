package replayfault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifiedPivotJournalPersistsProofsAndFrontierAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)

	progress, err := store.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	pivotProof := json.RawMessage(`{"header":"pivot","quorum":3}`)
	progress, err = store.SetRecoveryPivot(id, progress.Generation, pivotProof)
	require.NoError(t, err)
	targetProof := json.RawMessage(`{"header":"successor","quorum":3}`)
	progress, err = store.SetRecoveryTarget(id, progress.Generation, targetProof)
	require.NoError(t, err)
	progress.PivotSequence = 100
	progress.PivotHash = [32]byte{1}
	progress.SuccessorSequence = 101
	progress.SuccessorHash = [32]byte{2}
	progress.FrontierSequence = 101
	progress.FrontierHash = [32]byte{2}
	for _, phase := range []RecoveryPhase{RecoveryPhaseAuthenticating, RecoveryPhaseAcquiringPivot} {
		progress.Phase = phase
		progress, err = store.UpdateRecovery(id, progress.Generation, progress)
		require.NoError(t, err)
	}
	progress.Phase = RecoveryPhaseCatchingUp
	frontierProof := json.RawMessage(`{"header":"successor","executed":true}`)
	progress, err = store.UpdateRecoveryWithFrontier(id, progress.Generation, progress, frontierProof)
	require.NoError(t, err)

	snapshot := store.Snapshot()
	require.JSONEq(t, string(pivotProof), string(snapshot.RecoveryPivot))
	require.JSONEq(t, string(targetProof), string(snapshot.RecoveryTarget))
	require.JSONEq(t, string(frontierProof), string(snapshot.RecoveryFrontier))
	require.Equal(t, uint32(101), snapshot.Recovery.FrontierSequence)
	require.NotZero(t, snapshot.Recovery.Generation)

	restarted, err := Open(path)
	require.NoError(t, err)
	restartedFault := restarted.Snapshot()
	require.JSONEq(t, string(pivotProof), string(restartedFault.RecoveryPivot))
	require.JSONEq(t, string(targetProof), string(restartedFault.RecoveryTarget))
	require.JSONEq(t, string(frontierProof), string(restartedFault.RecoveryFrontier))
	require.Equal(t, snapshot.Recovery.Generation, restartedFault.Recovery.Generation)
	require.False(t, restarted.Status().Recovery.InFlight, "restart must release stale worker ownership")
	require.True(t, restarted.Blocked())
}

func TestRecoveryGenerationAndFailureFingerprintSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)

	progress, err := store.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	staleGeneration := progress.Generation
	progress, err = store.SetRecoveryPivot(id, progress.Generation, json.RawMessage(`{"header":"pivot"}`))
	require.NoError(t, err)
	_, err = store.SetRecoveryTarget(id, staleGeneration, json.RawMessage(`{"header":"stale"}`))
	require.ErrorIs(t, err, ErrRecoveryGeneration)
	require.Equal(t, RecoveryPhaseSelectingPivot, store.Status().Recovery.Phase)

	cause := errors.New("authenticated successor disagrees with local replay")
	require.NoError(t, store.FailRecovery(id, progress.Generation, cause))
	wantFingerprint := sha256.Sum256([]byte(cause.Error()))
	status := store.Status()
	require.True(t, status.Blocked)
	require.Equal(t, RecoveryPhaseExecutionFailed, status.Recovery.Phase)
	require.Equal(t, cause.Error(), status.Recovery.LastError)
	require.Equal(t, hex.EncodeToString(wantFingerprint[:]), status.Recovery.FailureFingerprint)
	require.Equal(t, 1, status.Recovery.FailureAttempts)
	require.False(t, status.Recovery.InFlight)

	restarted, err := Open(path)
	require.NoError(t, err)
	restartedStatus := restarted.Status()
	require.True(t, restartedStatus.Blocked)
	require.Equal(t, RecoveryPhaseExecutionFailed, restartedStatus.Recovery.Phase)
	require.Equal(t, status.Recovery.FailureFingerprint, restartedStatus.Recovery.FailureFingerprint)
	require.Equal(t, status.Recovery.FailureAttempts, restartedStatus.Recovery.FailureAttempts)

	// A terminal mismatch never resumes implicitly. A new explicit admission
	// starts a fresh selecting phase while retaining the diagnostics.
	fresh, err := restarted.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	require.Equal(t, RecoveryPhaseSelectingPivot, fresh.Phase)
	require.True(t, fresh.InFlight)
	require.Equal(t, status.Recovery.FailureFingerprint, fresh.FailureFingerprint)
	require.Equal(t, 1, fresh.FailureAttempts)
	require.NotEqual(t, status.Recovery.Generation, fresh.Generation)
	require.Nil(t, restarted.Snapshot().RecoveryPivot)
	require.Nil(t, restarted.Snapshot().RecoveryTarget)
}

func TestSuspendedRecoveryRetainsRouteAndResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)
	progress, err := store.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	pivotProof := json.RawMessage(`{"header":"pivot"}`)
	progress, err = store.SetRecoveryPivot(id, progress.Generation, pivotProof)
	require.NoError(t, err)
	targetProof := json.RawMessage(`{"header":"successor"}`)
	progress, err = store.SetRecoveryTarget(id, progress.Generation, targetProof)
	require.NoError(t, err)

	cause := errors.New("recovery worker canceled during acquisition")
	require.NoError(t, store.SuspendRecovery(id, progress.Generation, cause))
	status := store.Status()
	require.True(t, status.Blocked)
	require.False(t, status.Recovery.InFlight)
	require.Equal(t, "recovery interrupted; verified progress retained", status.Recovery.BlockedReason)
	require.Equal(t, "wait for recovery to resume after restart", status.Recovery.OperatorAction)
	require.Equal(t, cause.Error(), status.Recovery.AcquisitionError)

	restarted, err := Open(path)
	require.NoError(t, err)
	restartedStatus := restarted.Status()
	require.True(t, restartedStatus.Blocked)
	require.False(t, restartedStatus.Recovery.InFlight)
	require.Equal(t, status.Recovery.BlockedReason, restartedStatus.Recovery.BlockedReason)
	require.Equal(t, status.Recovery.AcquisitionError, restartedStatus.Recovery.AcquisitionError)

	resumed, err := restarted.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	require.True(t, resumed.InFlight)
	require.Equal(t, RecoveryPhaseSelectingPivot, resumed.Phase)
	require.JSONEq(t, string(pivotProof), string(restarted.Snapshot().RecoveryPivot))
	require.JSONEq(t, string(targetProof), string(restarted.Snapshot().RecoveryTarget))
}

func TestBeginRecoveryUsesCurrentRevisionAndSaturatesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)
	fault := store.Snapshot()
	fault.Revision = "old-build-revision"
	fault.Attempts = int(^uint(0) >> 1)
	require.NoError(t, writeAtomic(path, fault))
	store, err := Open(path)
	require.NoError(t, err)

	progress, err := store.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	require.Equal(t, buildRevision(), progress.Revision)
	require.Equal(t, int(^uint(0)>>1), progress.Attempts)
	require.Equal(t, int(^uint(0)>>1), store.Snapshot().Attempts)
}

func TestOriginalEvidenceRemainsImmutableAndArchiveOutcomeIsExplicit(t *testing.T) {
	for _, test := range []struct {
		strategy RecoveryStrategy
		suffix   string
		outcome  RecoveryOutcome
	}{
		{RecoveryStrategyVerifiedPivot, ".superseded", RecoveryOutcomeSupersededByPivot},
		{RecoveryStrategyExactTransition, ".resolved", RecoveryOutcomeResolvedByReplay},
	} {
		t.Run(string(test.strategy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.json")
			store, id := newJournalFault(t, path)
			original := store.Snapshot().OriginalEvidence
			require.JSONEq(t, `{"diagnosis":"historical mismatch","detail":"full observation"}`, string(original))

			active := activateReadyRecovery(t, store, id, test.strategy)
			require.NoError(t, store.SuspendRecovery(id, active.Generation, errors.New("operator stopped recovery")))
			mutated := Fault{ID: id, Evidence: json.RawMessage(`{"diagnosis":"operator note"}`), Message: "updated"}
			require.NoError(t, store.Update(id, mutated))
			require.JSONEq(t, string(original), string(store.Snapshot().OriginalEvidence))

			progress, err := store.BeginRecovery(id, test.strategy)
			require.NoError(t, err)
			require.Equal(t, RecoveryPhaseActivating, progress.Phase)
			require.NoError(t, store.ActivateRecovery(id, progress.Generation))
			require.False(t, store.Blocked())
			require.Equal(t, test.outcome, store.Status().Recovery.Outcome)

			archivePath := path + "." + id + test.suffix
			archiveData, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			var archived Fault
			require.NoError(t, json.Unmarshal(archiveData, &archived))
			require.Equal(t, test.outcome, archived.Recovery.Outcome)
			require.JSONEq(t, string(original), string(archived.OriginalEvidence))
			require.JSONEq(t, `{"diagnosis":"operator note"}`, string(archived.Evidence))

			restarted, err := Open(path)
			require.NoError(t, err)
			require.False(t, restarted.Blocked())
			require.Equal(t, test.outcome, restarted.Status().Recovery.Outcome)
			require.Equal(t, store.generation, restarted.generation)
			require.Equal(t, store.Status().Recovery.Generation, restarted.Status().Recovery.Generation)
		})
	}
}

func TestActivateRecoveryArchiveFailureCanResumeAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)
	activateReadyRecovery(t, store, id, RecoveryStrategyVerifiedPivot)
	progress := store.Status().Recovery

	originalSync := syncDirectoryFn
	t.Cleanup(func() { syncDirectoryFn = originalSync })
	failed := false
	syncDirectoryFn = func(dir string) error {
		if !failed {
			failed = true
			return errors.New("injected archive fsync failure")
		}
		return originalSync(dir)
	}
	err := store.ActivateRecovery(id, progress.Generation)
	syncDirectoryFn = originalSync
	require.Error(t, err)
	require.True(t, store.Blocked())
	require.Error(t, store.Status().PersistenceError)

	restarted, err := Open(path)
	require.NoError(t, err)
	require.True(t, restarted.Blocked())
	require.Equal(t, RecoveryPhaseActivating, restarted.Status().Recovery.Phase)
	resumed, err := restarted.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	require.NoError(t, restarted.ActivateRecovery(id, resumed.Generation))
	require.False(t, restarted.Blocked())
	require.FileExists(t, path+"."+id+".superseded")
}

func TestActivateRecoveryClearFailureRestoresFaultAndArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, id := newJournalFault(t, path)
	activateReadyRecovery(t, store, id, RecoveryStrategyVerifiedPivot)
	progress := store.Status().Recovery

	originalSync := syncDirectoryFn
	t.Cleanup(func() { syncDirectoryFn = originalSync })
	callCount := 0
	syncDirectoryFn = func(dir string) error {
		callCount++
		if callCount == 2 {
			return errors.New("injected clear fsync failure")
		}
		return originalSync(dir)
	}
	err := store.ActivateRecovery(id, progress.Generation)
	syncDirectoryFn = originalSync
	require.Error(t, err)
	require.True(t, store.Blocked())
	require.NotNil(t, store.Snapshot())
	require.FileExists(t, path+"."+id+".superseded")

	restarted, err := Open(path)
	require.NoError(t, err)
	require.True(t, restarted.Blocked())
	require.Equal(t, RecoveryPhaseActivating, restarted.Status().Recovery.Phase)
	resumed, err := restarted.BeginRecovery(id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, err)
	require.NoError(t, restarted.ActivateRecovery(id, resumed.Generation))
	require.False(t, restarted.Blocked())
}

func newJournalFault(t *testing.T, path string) (*Store, string) {
	t.Helper()
	store, err := Open(path)
	require.NoError(t, err)
	intentEvidence := json.RawMessage(`{"intent":"replay","target":42}`)
	id, err := store.BeginReplay(Fault{ID: "fault", Class: ExecutionDisagreement, Evidence: intentEvidence})
	require.NoError(t, err)
	finalEvidence := json.RawMessage(`{"diagnosis":"historical mismatch","detail":"full observation"}`)
	require.NoError(t, store.FailReplay(id, Fault{
		Class:    ExecutionDisagreement,
		Evidence: finalEvidence,
		Message:  "replay disagreement",
	}))
	return store, id
}

func activateReadyRecovery(t *testing.T, store *Store, id string, strategy RecoveryStrategy) RecoveryProgress {
	t.Helper()
	progress, err := store.BeginRecovery(id, strategy)
	require.NoError(t, err)
	progress, err = store.SetRecoveryPivot(id, progress.Generation, json.RawMessage(`{"header":"pivot"}`))
	require.NoError(t, err)
	progress, err = store.SetRecoveryTarget(id, progress.Generation, json.RawMessage(`{"header":"successor"}`))
	require.NoError(t, err)
	progress.PivotSequence = 100
	progress.PivotHash = [32]byte{1}
	progress.SuccessorSequence = 101
	progress.SuccessorHash = [32]byte{2}
	progress.FrontierSequence = 101
	progress.FrontierHash = [32]byte{2}
	for _, phase := range []RecoveryPhase{RecoveryPhaseAuthenticating, RecoveryPhaseAcquiringPivot} {
		progress.Phase = phase
		progress, err = store.UpdateRecovery(id, progress.Generation, progress)
		require.NoError(t, err)
	}
	progress.Phase = RecoveryPhaseCatchingUp
	progress, err = store.UpdateRecoveryWithFrontier(id, progress.Generation, progress, json.RawMessage(`{"header":"successor","executed":true}`))
	require.NoError(t, err)
	progress.Phase = RecoveryPhaseReadyToActivate
	progress, err = store.UpdateRecovery(id, progress.Generation, progress)
	require.NoError(t, err)
	progress.Phase = RecoveryPhaseActivating
	progress, err = store.UpdateRecovery(id, progress.Generation, progress)
	require.NoError(t, err)
	return progress
}

func TestNewFaultDoesNotInheritCompletedRecoveryAdmission(t *testing.T) {
	store, id := newJournalFault(t, filepath.Join(t.TempDir(), "fault.json"))
	progress := activateReadyRecovery(t, store, id, RecoveryStrategyVerifiedPivot)
	require.NoError(t, store.ActivateRecovery(id, progress.Generation))
	next, err := store.BeginReplay(Fault{ID: "new-fault", Evidence: json.RawMessage(`{"next":true}`)})
	require.NoError(t, err)
	require.NoError(t, store.FailReplay(next, Fault{Class: ExecutionDisagreement}))
	status := store.Status()
	require.True(t, status.Blocked)
	require.Equal(t, next, status.Fault.ID)
	require.Empty(t, status.Recovery.ID)
	require.Empty(t, status.Recovery.Strategy)
	require.Nil(t, status.Fault.Recovery)
}
