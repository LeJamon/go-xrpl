package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type blockedDiagnosticAdmission struct {
	nodestore.Database
	nodestore.DurableDatabase
	entered chan struct{}
}

func (d *blockedDiagnosticAdmission) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	close(d.entered)
	<-ctx.Done()
	return [32]byte{}, nil, ctx.Err()
}

func TestReplayDiagnosticRetainsMemoryUntilShutdown(t *testing.T) {
	svc := replayFaultService(t, "")
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, true)
	budget := shamap.NewAcquisitionBudget(512 << 20)
	replay, err := inbound.NewStoredLedgerReplayWithMemory(parent, target, nil, budget)
	require.NoError(t, err)
	defer replay.ReleaseMemory()
	charge := budget.Snapshot().Used
	require.Positive(t, charge)
	cfg := svc.EngineConfigForReplay(parent)
	evidence := replayEvidenceFor(replay, cfg, true)
	id, err := svc.replayFaults.BeginReplay(replayfault.Fault{ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence()})
	require.NoError(t, err)
	blocked := &blockedDiagnosticAdmission{entered: make(chan struct{})}
	svc.nodeStore = blocked
	svc.recordReplayFailure(t.Context(), id, replay, cfg, evidence, errors.New("diagnostic test failure"), false)
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("diagnostic worker did not start")
	}
	replay.ReleaseMemory()
	require.Equal(t, charge, budget.Snapshot().Used, "asynchronous diagnostic source must remain charged")
	svc.Stop()
	require.Zero(t, budget.Snapshot().Used, "shutdown must drain diagnostic ownership")
}

func TestRejectedReplayDiagnosticReleasesLease(t *testing.T) {
	svc := replayFaultService(t, "")
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, true)
	budget := shamap.NewAcquisitionBudget(512 << 20)
	replay, err := inbound.NewStoredLedgerReplayWithMemory(parent, target, nil, budget)
	require.NoError(t, err)
	defer replay.ReleaseMemory()
	_, err = svc.admitReplayRecoveryJob(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	require.NoError(t, err)
	cfg := svc.EngineConfigForReplay(parent)
	id, err := svc.replayFaults.BeginReplay(replayfault.Fault{ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence()})
	require.NoError(t, err)
	svc.recordReplayFailure(t.Context(), id, replay, cfg, replayEvidenceFor(replay, cfg, true), errors.New("diagnostic test failure"), false)
	replay.ReleaseMemory()
	require.Zero(t, budget.Snapshot().Used, "rejected worker must release its lease")
}
