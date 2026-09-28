package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/stretchr/testify/require"
)

func seedAsyncReplayFault(t *testing.T, svc *Service) replayfault.Fault {
	t.Helper()
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, true)
	evidence, err := json.Marshal(replayEvidence{
		Parent:         parent.Header(),
		Target:         target.Header(),
		Fees:           parent.Fees(),
		Authenticated:  true,
		ParentSnapshot: true,
		NetworkID:      svc.config.NetworkID,
	})
	require.NoError(t, err)
	svc.RecordReplayPreparationFailure(context.Background(), target.Header(), nil, parent, true, errors.New("replay test fault"))
	fault := svc.replayFaults.Snapshot()
	require.NotNil(t, fault)
	require.NoError(t, svc.replayFaults.Update(fault.ID, replayfault.Fault{
		ID:                  fault.ID,
		Class:               replayfault.ExecutionDisagreement,
		ParentHash:          parent.Hash(),
		TargetHash:          target.Hash(),
		Sequence:            target.Sequence(),
		Message:             "replay test fault",
		Evidence:            evidence,
		CreatedAt:           fault.CreatedAt,
		Revision:            fault.Revision,
		Attempts:            fault.Attempts,
		AcquisitionAttempts: fault.AcquisitionAttempts,
	}))
	return *svc.replayFaults.Snapshot()
}

func TestStartReplayFaultRecoveryAdmitsExactIDAndPublishesFailure(t *testing.T) {
	svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
	fault := seedAsyncReplayFault(t, svc)

	entered := make(chan struct{})
	release := make(chan struct{})
	svc.SetReplayParentAcquirer(func(uint32, [32]byte) error {
		close(entered)
		<-release
		return errors.New("acquisition failed")
	})

	require.ErrorIs(t, svc.StartReplayFaultRecovery("stale-id"), replayfault.ErrFaultIDMismatch)
	require.NoError(t, svc.StartReplayFaultRecovery(fault.ID))
	require.Eventually(t, func() bool {
		return svc.ReplayFaultStatus().Recovery.InFlight
	}, time.Second, time.Millisecond)
	require.Error(t, svc.StartReplayFaultRecovery(fault.ID))

	close(release)
	require.Eventually(t, func() bool {
		status := svc.ReplayFaultStatus()
		return !status.Recovery.InFlight && status.Recovery.LastError != ""
	}, time.Second, time.Millisecond)
	require.True(t, svc.ReplayBlocked())
}

func TestReplayRecoveryWorkerCancellationIsDrainedByStop(t *testing.T) {
	svc := replayFaultService(t, "")
	entered := make(chan struct{})
	done, err := svc.admitReplayRecoveryJob(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, err)
	<-entered

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("replay recovery worker was not canceled")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("service stop did not drain replay recovery worker")
	}
}
