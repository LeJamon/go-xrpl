package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type replayRecoverySyncFailureDatabase struct {
	*checkpointTrackingDatabase
	failAt int
	fail   error
	mu     sync.Mutex
	calls  int
}

func (d *replayRecoverySyncFailureDatabase) Sync(ctx context.Context) error {
	d.mu.Lock()
	d.calls++
	fail := d.calls == d.failAt
	d.mu.Unlock()
	if fail {
		return d.fail
	}
	return d.Database.Sync(ctx)
}

func prepareStateBaseRepairLifetime(t *testing.T, path string) (*stateBaseRecertificationFixture, replayfault.Fault, *ledger.Ledger, *int) {
	t.Helper()
	f := newStateBaseRecertificationFixture(t)
	f.svc.config.ReplayFaultPath = path
	faults, err := replayfault.Open(path)
	require.NoError(t, err)
	f.svc.replayFaults = faults
	f.invalidate(t)
	f.svc.invalidateCompleteLedger(f.validated.Sequence())
	f.svc.SetReplayTargetAuthenticator(func(h header.LedgerHeader) bool { return h.Hash == f.validated.Hash() })
	repairCalls := new(int)
	f.svc.SetReplayParentAcquirer(func(seq uint32, hash [32]byte) error {
		*repairCalls++
		require.Equal(t, f.validated.Sequence(), seq)
		require.Equal(t, f.validated.Hash(), hash)
		return nil
	})
	f.svc.recordStateBaseRecertificationFailure(t.Context(), f.validated.Header(), shamap.TypeState, &shamap.MissingNodeError{Hash: f.childHash})
	fault := f.svc.replayFaults.Snapshot()
	require.NotNil(t, fault)
	stateMap, err := f.validated.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := f.validated.TxMapSnapshot()
	require.NoError(t, err)
	targetHeader := f.validated.Header()
	targetHeader.Validated = false
	require.NoError(t, f.svc.StoreLedgerWithState(t.Context(), &targetHeader, stateMap, txMap))
	f.svc.mu.RLock()
	repaired := f.svc.replayRepairTarget
	f.svc.mu.RUnlock()
	require.NotNil(t, repaired)
	return f, *fault, repaired, repairCalls
}

func assertDutyBlocked(t *testing.T, svc *Service) {
	t.Helper()
	calls := 0
	require.ErrorIs(t, svc.WithValidatorDuty(func() error {
		calls++
		return nil
	}), replayfault.ErrBlocked)
	require.Zero(t, calls)
}

func assertRepairRetained(t *testing.T, svc *Service, repaired *ledger.Ledger) {
	t.Helper()
	svc.mu.RLock()
	got := svc.replayRepairTarget
	svc.mu.RUnlock()
	require.Same(t, repaired, got)
	assertDutyBlocked(t, svc)
}

func assertRepairReleased(t *testing.T, svc *Service) {
	t.Helper()
	svc.mu.RLock()
	parent, target := svc.replayRepairParent, svc.replayRepairTarget
	svc.mu.RUnlock()
	require.Nil(t, parent)
	require.Nil(t, target)
	require.NoError(t, svc.WithValidatorDuty(func() error { return nil }))
}

func TestStateBaseRepairRetainsExactTargetAfterTipPersistenceFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	f, fault, repaired, repairCalls := prepareStateBaseRepairLifetime(t, path)
	persistenceFailure := errors.New("repaired validated tip sync failed")
	f.svc.nodeStore = &replayRecoverySyncFailureDatabase{
		checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
		failAt:                     2,
		fail:                       persistenceFailure,
	}

	err := f.svc.RevalidateReplayFault(t.Context(), fault.ID)
	require.ErrorContains(t, err, "publish repaired validated tip")
	require.ErrorIs(t, err, persistenceFailure)
	require.True(t, f.svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	require.Same(t, repaired, f.svc.GetValidatedLedger())
	assertRepairRetained(t, f.svc, repaired)

	f.svc.nodeStore = f.db
	require.NoError(t, f.svc.RevalidateReplayFault(t.Context(), fault.ID))
	require.False(t, f.svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	assertRepairReleased(t, f.svc)
}

func TestStateBaseRepairRetainsExactTargetAfterRecertificationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	f, fault, repaired, repairCalls := prepareStateBaseRepairLifetime(t, path)
	f.svc.nodeStore = &staleFingerprintStateBaseRecertificationDatabase{
		checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
	}

	err := f.svc.RevalidateReplayFault(t.Context(), fault.ID)
	require.ErrorContains(t, err, "re-certify repaired state base")
	require.ErrorContains(t, err, "fingerprint changed")
	require.True(t, f.svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	require.Same(t, repaired, f.svc.GetValidatedLedger())
	assertRepairRetained(t, f.svc, repaired)

	f.svc.nodeStore = f.db
	require.NoError(t, f.svc.RevalidateReplayFault(t.Context(), fault.ID))
	require.False(t, f.svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	assertRepairReleased(t, f.svc)
}

func TestStateBaseRepairRetainsExactTargetAfterDurableFaultFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		block func(*testing.T, string, string)
		fix   func(*testing.T, string, string)
	}{{
		name: "archive",
		block: func(t *testing.T, path, id string) {
			require.NoError(t, os.Mkdir(path+"."+id+".resolved", 0o700))
		},
		fix: func(t *testing.T, path, id string) {
			require.NoError(t, os.Remove(path+"."+id+".resolved"))
		},
	}, {
		name: "clear",
		block: func(t *testing.T, path, _ string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o700))
		},
		fix: func(t *testing.T, path, _ string) {
			require.NoError(t, os.Remove(path))
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.json")
			f, fault, repaired, repairCalls := prepareStateBaseRepairLifetime(t, path)
			test.block(t, path, fault.ID)

			err := f.svc.RevalidateReplayFault(t.Context(), fault.ID)
			require.ErrorContains(t, err, "publish replay fault")
			require.True(t, f.svc.ReplayBlocked())
			require.Equal(t, 1, *repairCalls)
			require.Same(t, repaired, f.svc.GetValidatedLedger())
			assertRepairRetained(t, f.svc, repaired)

			test.fix(t, path, fault.ID)
			require.NoError(t, f.svc.RevalidateReplayFault(t.Context(), fault.ID))
			require.False(t, f.svc.ReplayBlocked())
			require.Equal(t, 1, *repairCalls)
			assertRepairReleased(t, f.svc)
		})
	}
}

func prepareGenericParentRepairLifetime(t *testing.T, path string) (*Service, replayfault.Fault, *ledger.Ledger, *int) {
	t.Helper()
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	evidence := replayEvidence{
		Parent: parent.Header(), Target: target.Header(), Fees: parent.Fees(),
		Authenticated: true, NetworkID: svc.config.NetworkID,
	}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{
		Class: replayfault.MissingState, ParentHash: parent.Hash(), TargetHash: target.Hash(),
		Sequence: target.Sequence(), Evidence: raw,
	}))
	repairCalls := new(int)
	svc.SetReplayParentAcquirer(func(seq uint32, hash [32]byte) error {
		*repairCalls++
		require.Equal(t, parent.Sequence(), seq)
		require.Equal(t, parent.Hash(), hash)
		stateMap, stateErr := parent.StateMapSnapshot()
		require.NoError(t, stateErr)
		txMap, txErr := parent.TxMapSnapshot()
		require.NoError(t, txErr)
		h := parent.Header()
		return svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap)
	})
	fault := svc.replayFaults.Snapshot()
	require.NotNil(t, fault)
	require.NoError(t, svc.requestReplayParentRepair(*fault, evidence))
	svc.mu.RLock()
	repaired := svc.replayRepairParent
	svc.mu.RUnlock()
	require.NotNil(t, repaired)
	return svc, *fault, repaired, repairCalls
}

func TestGenericParentRepairRetainsExactTargetAfterDurableFaultFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		block func(*testing.T, string, string)
		fix   func(*testing.T, string, string)
	}{{
		name: "archive",
		block: func(t *testing.T, path, id string) {
			require.NoError(t, os.Mkdir(path+"."+id+".resolved", 0o700))
		},
		fix: func(t *testing.T, path, id string) {
			require.NoError(t, os.Remove(path+"."+id+".resolved"))
		},
	}, {
		name: "clear",
		block: func(t *testing.T, path, _ string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o700))
		},
		fix: func(t *testing.T, path, _ string) {
			require.NoError(t, os.Remove(path))
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.json")
			svc, fault, repaired, repairCalls := prepareGenericParentRepairLifetime(t, path)
			test.block(t, path, fault.ID)

			err := svc.RevalidateReplayFault(t.Context(), fault.ID)
			require.ErrorContains(t, err, "publish replay fault")
			require.True(t, svc.ReplayBlocked())
			require.Equal(t, 1, *repairCalls)
			require.Equal(t, repaired.Hash(), svc.GetClosedLedger().Hash())
			require.NotSame(t, repaired, svc.GetClosedLedger())
			svc.mu.RLock()
			got := svc.replayRepairParent
			svc.mu.RUnlock()
			require.Same(t, repaired, got)
			assertDutyBlocked(t, svc)

			test.fix(t, path, fault.ID)
			require.NoError(t, svc.RevalidateReplayFault(t.Context(), fault.ID))
			require.False(t, svc.ReplayBlocked())
			require.Equal(t, 1, *repairCalls)
			assertRepairReleased(t, svc)
		})
	}
}

func TestExecutionRepairRetainsExactTargetAfterDurableFaultFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	closed := svc.GetClosedLedger()
	h := closed.Header()
	evidence := replayEvidence{
		Origin: replayFaultOriginExecution, RepairClass: replayfault.MissingState,
		Parent: closed.Header(), Target: h, Fees: closed.Fees(), NetworkID: svc.config.NetworkID,
		Authenticated: true, RepairHash: h.Hash, RepairSequence: h.LedgerIndex,
	}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{
		Class: replayfault.MissingState, ParentHash: h.ParentHash, TargetHash: h.Hash,
		Sequence: h.LedgerIndex, Evidence: raw,
	}))
	repairCalls := new(int)
	svc.SetReplayParentAcquirer(func(seq uint32, hash [32]byte) error {
		*repairCalls++
		require.Equal(t, h.LedgerIndex, seq)
		require.Equal(t, h.Hash, hash)
		stateMap, stateErr := closed.StateMapSnapshot()
		require.NoError(t, stateErr)
		txMap, txErr := closed.TxMapSnapshot()
		require.NoError(t, txErr)
		return svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap)
	})
	fault := svc.replayFaults.Snapshot()
	require.NotNil(t, fault)
	require.NoError(t, svc.requestReplayParentRepair(*fault, evidence))
	svc.mu.RLock()
	repaired := svc.replayRepairTarget
	svc.mu.RUnlock()
	require.NotNil(t, repaired)

	require.NoError(t, os.Mkdir(path+"."+fault.ID+".resolved", 0o700))
	err = svc.RevalidateReplayFault(t.Context(), fault.ID)
	require.ErrorContains(t, err, "publish replay fault")
	require.True(t, svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	require.Same(t, repaired, svc.GetClosedLedger())
	assertRepairRetained(t, svc, repaired)

	require.NoError(t, os.Remove(path+"."+fault.ID+".resolved"))
	require.NoError(t, svc.RevalidateReplayFault(t.Context(), fault.ID))
	require.False(t, svc.ReplayBlocked())
	require.Equal(t, 1, *repairCalls)
	assertRepairReleased(t, svc)
}

func TestReplayRepairCleanupPreservesReservationForNextFault(t *testing.T) {
	svc := replayFaultService(t, "")
	parent := svc.GetClosedLedger()
	evidence, err := json.Marshal(replayEvidence{
		Parent: parent.Header(), Target: parent.Header(), NetworkID: svc.config.NetworkID,
		Authenticated: true,
	})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{
		Class: replayfault.MissingState, ParentHash: parent.Header().ParentHash,
		TargetHash: parent.Hash(), Sequence: parent.Sequence(), Evidence: evidence,
	}))
	svc.mu.Lock()
	svc.replayRepairTarget = parent
	svc.mu.Unlock()

	svc.releaseReplayRepair(replayRepairReservation{target: parent})
	svc.mu.RLock()
	got := svc.replayRepairTarget
	svc.mu.RUnlock()
	require.Same(t, parent, got)
	require.True(t, svc.ReplayBlocked())
}

func TestStateBaseRepairReleasesSupersededParentReservation(t *testing.T) {
	f, fault, _, repairCalls := prepareStateBaseRepairLifetime(t, filepath.Join(t.TempDir(), "fault.json"))
	previous := replayRepairReservation{parent: f.validated}
	f.svc.mu.Lock()
	f.svc.replayRepairParent = previous.parent
	f.svc.mu.Unlock()
	f.svc.releaseReplayRepair(previous)
	f.svc.mu.RLock()
	retained := f.svc.replayRepairParent
	f.svc.mu.RUnlock()
	require.Same(t, previous.parent, retained)

	require.NoError(t, f.svc.RevalidateReplayFault(t.Context(), fault.ID))
	require.Equal(t, 1, *repairCalls)
	assertRepairReleased(t, f.svc)
}
