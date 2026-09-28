package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/stretchr/testify/require"
)

func TestReplayIntentWriteFailurePreventsExecution(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	dir := t.TempDir()
	svc := replayFaultService(t, filepath.Join(dir, "fault.json"))
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, true)
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	derived, err := svc.ApplyReplay(context.Background(), replay, svc.EngineConfigForReplay(parent), true)
	require.ErrorContains(t, err, "persist replay intent")
	require.Nil(t, derived)
	require.Empty(t, replay.Evidence().Error, "engine must not execute an unjournaled transition")
	require.True(t, svc.ReplayBlocked())
	require.Same(t, parent, svc.GetClosedLedger())
}

func TestVerifiedReplayClearsDurableIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	svc := replayFaultService(t, path)
	parent := svc.GetClosedLedger()
	target := replayFaultTarget(t, parent, false)
	replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
	require.NoError(t, err)
	derived, err := svc.ApplyReplay(context.Background(), replay, svc.EngineConfigForReplay(parent), true)
	require.NoError(t, err)
	require.Equal(t, target.Hash(), derived.Hash())
	restarted, err := replayfault.Open(path)
	require.NoError(t, err)
	require.False(t, restarted.Blocked())
}
