package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type activationPauseBackend struct {
	*pivotScenarioBackend
	entered chan header.LedgerHeader
}

func (b *activationPauseBackend) Handoff(ctx context.Context, h header.LedgerHeader) error {
	b.entered <- h
	<-ctx.Done()
	return ctx.Err()
}

func TestPivotActivationRestartResumesAfterCanonicalInstall(t *testing.T) {
	db, rm := newStartupTestStorage(t, context.Background())
	faultPath := filepath.Join(t.TempDir(), "fault.json")
	cfg := DefaultConfig()
	cfg.Standalone = true
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = rm
	cfg.ReplayFaultPath = faultPath

	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	old := svc.GetClosedLedger()
	historical := replayFaultTarget(t, old, false)
	evidence, err := json.Marshal(replayEvidence{
		Parent:    old.Header(),
		Target:    historical.Header(),
		NetworkID: cfg.NetworkID,
	})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{
		Class:      replayfault.ExecutionDisagreement,
		ParentHash: old.Hash(),
		TargetHash: historical.Hash(),
		Sequence:   historical.Sequence(),
		Evidence:   evidence,
	}))
	fault := svc.replayFaults.Snapshot()
	require.NotNil(t, fault)

	pivot := replayFaultTarget(t, historical, false)
	backendFixture := &pivotScenarioBackend{
		ledgers: map[uint32]*ledger.Ledger{pivot.Sequence(): pivot},
		pivot:   pivot.Sequence(),
		live:    pivot.Sequence() + 3,
	}
	final := pivot
	for range 3 {
		final = replayFaultTarget(t, final, false)
		backendFixture.ledgers[final.Sequence()] = final
	}

	paused := &activationPauseBackend{
		pivotScenarioBackend: backendFixture,
		entered:              make(chan header.LedgerHeader, 1),
	}
	svc.SetPivotRecoveryBackend(paused)
	require.NoError(t, svc.StartReplayFaultRecoveryStrategy(fault.ID, string(replayfault.RecoveryStrategyVerifiedPivot)))

	select {
	case handedOff := <-paused.entered:
		require.Equal(t, final.Hash(), handedOff.Hash)
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not reach canonical handoff")
	}
	status := svc.ReplayFaultStatus()
	require.True(t, status.Blocked)
	require.True(t, status.Recovery.InFlight)
	require.Equal(t, replayfault.RecoveryPhaseActivating, status.Recovery.Phase)
	require.Equal(t, final.Sequence(), status.Recovery.FrontierSequence)
	require.Equal(t, final.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, final.Hash(), svc.GetValidatedLedger().Hash())

	// Stop cancels the coordinator while the durable canonical install is already
	// complete. The journal must retain the activating proof and remain blocked.
	svc.Stop()
	status = svc.ReplayFaultStatus()
	require.True(t, status.Blocked)
	require.False(t, status.Recovery.InFlight)
	require.Empty(t, status.Recovery.LastError)
	require.Equal(t, replayfault.RecoveryPhaseActivating, status.Recovery.Phase)
	require.Equal(t, fault.Sequence, status.Fault.Sequence)
	_, err = os.Stat(faultPath + "." + fault.ID + ".superseded")
	require.ErrorIs(t, err, os.ErrNotExist)

	restartCfg := cfg
	restartCfg.Startup = StartupConfig{Mode: StartupLoad, Ledger: fmt.Sprintf("%X", final.Hash())}
	restarted, err := New(restartCfg)
	require.NoError(t, err)
	require.NoError(t, restarted.Start())
	t.Cleanup(restarted.Stop)

	restartedStatus := restarted.ReplayFaultStatus()
	require.True(t, restartedStatus.Blocked)
	require.False(t, restartedStatus.Recovery.InFlight)
	require.Equal(t, replayfault.RecoveryPhaseActivating, restartedStatus.Recovery.Phase)
	require.Equal(t, fault.ID, restartedStatus.Fault.ID)
	require.Equal(t, fault.Sequence, restartedStatus.Fault.Sequence)
	require.Equal(t, final.Hash(), restarted.GetClosedLedger().Hash())
	require.Equal(t, final.Hash(), restarted.GetValidatedLedger().Hash())

	restarted.mu.Lock()
	restarted.replayRepairParent = old
	restarted.replayAuthenticate = func(h header.LedgerHeader) bool { return h.Hash == historical.Hash() }
	restarted.mu.Unlock()
	require.True(t, restarted.exactReplayTransitionUsable(context.Background(), restarted.replayFaults.Snapshot()))

	backendFixture.handoff = func(header.LedgerHeader) error { return nil }
	backendFixture.mu.Lock()
	backendFixture.acquired = nil
	backendFixture.mu.Unlock()
	restarted.SetPivotRecoveryBackend(backendFixture)
	require.Eventually(t, func() bool {
		return !restarted.ReplayBlocked()
	}, 10*time.Second, time.Millisecond, "status: %+v", restarted.ReplayFaultStatus())

	require.Equal(t, final.Hash(), restarted.GetClosedLedger().Hash())
	require.Equal(t, final.Hash(), restarted.GetValidatedLedger().Hash())
	require.Nil(t, restarted.replayFaults.Snapshot())
	archive, err := os.ReadFile(faultPath + "." + fault.ID + ".superseded")
	require.NoError(t, err)
	var archived replayfault.Fault
	require.NoError(t, json.Unmarshal(archive, &archived))
	require.NotNil(t, archived.Recovery)
	require.Equal(t, replayfault.RecoveryOutcomeSupersededByPivot, archived.Recovery.Outcome)
	require.Equal(t, final.Sequence(), archived.Recovery.FrontierSequence)
}
