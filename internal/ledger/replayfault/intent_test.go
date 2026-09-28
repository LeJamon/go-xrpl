package replayfault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplayIntentSurvivesFailedFaultWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	for _, scenario := range []string{"first replay", "healthy journal"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fault.json")
			store, err := Open(path)
			require.NoError(t, err)
			if scenario == "healthy journal" {
				id, err := store.BeginReplay(Fault{})
				require.NoError(t, err)
				require.NoError(t, store.CompleteReplay(id))
			}
			intent := Fault{ParentHash: [32]byte{1}, TargetHash: [32]byte{2}, Sequence: 3, Evidence: json.RawMessage(`{"transition":"original"}`)}
			id, err := store.BeginReplay(intent)
			require.NoError(t, err)
			require.False(t, store.Blocked())
			require.Nil(t, store.Status().Fault)
			require.NoError(t, os.Chmod(dir, 0500))
			t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
			require.Error(t, store.FailReplay(id, Fault{Class: ExecutionDisagreement, Message: "reproduced disagreement"}))
			require.True(t, store.Blocked())
			require.ErrorIs(t, store.CompleteReplay(id), ErrBlocked)
			require.NoError(t, os.Chmod(dir, 0700))
			restarted, err := Open(path)
			require.NoError(t, err)
			require.True(t, restarted.Blocked())
			fault := restarted.Snapshot()
			require.Equal(t, id, fault.ID)
			require.Equal(t, intent.TargetHash, fault.TargetHash)
			require.Equal(t, intent.ParentHash, fault.ParentHash)
			require.JSONEq(t, string(intent.Evidence), string(fault.Evidence))
			require.ErrorIs(t, restarted.WithValidator(func() error { t.Fatal("validator duty ran"); return nil }), ErrBlocked)
		})
	}
}

func TestReplayIntentOwnershipAndVerifiedCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	_, err = store.BeginReplay(Fault{TargetHash: [32]byte{2}})
	require.ErrorIs(t, err, ErrReplayInProgress)
	require.ErrorIs(t, store.FailReplay("stale", Fault{}), ErrFaultIDMismatch)
	require.ErrorIs(t, store.CompleteReplay("stale"), ErrFaultIDMismatch)
	restarted, err := Open(path)
	require.NoError(t, err)
	require.True(t, restarted.Blocked())
	require.Equal(t, id, restarted.Snapshot().ID)
	require.NoError(t, store.CompleteReplay(id))
	restarted, err = Open(path)
	require.NoError(t, err)
	require.False(t, restarted.Blocked())
	require.ErrorIs(t, store.FailReplay(id, Fault{}), ErrFaultIDMismatch)
}

func TestReplayIntentClearFailureStaysBlocked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	require.Error(t, store.CompleteReplay(id))
	require.True(t, store.Blocked())
	require.NoError(t, os.Chmod(dir, 0700))
	restarted, err := Open(path)
	require.NoError(t, err)
	require.True(t, restarted.Blocked())
	require.Equal(t, id, restarted.Snapshot().ID)
}

func TestCanceledReplayIntentCanBeRetriedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	require.ErrorIs(t, store.CancelReplay("stale"), ErrFaultIDMismatch)
	require.NoError(t, store.CancelReplay(id))
	require.ErrorIs(t, store.CancelReplay(id), ErrFaultIDMismatch)
	restarted, err := Open(path)
	require.NoError(t, err)
	require.False(t, restarted.Blocked())
	id, err = restarted.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	require.NoError(t, restarted.CompleteReplay(id))
}

func TestCanceledReplayIntentClearFailureStaysBlocked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fault.json")
	store, err := Open(path)
	require.NoError(t, err)
	id, err := store.BeginReplay(Fault{TargetHash: [32]byte{1}})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	require.Error(t, store.CancelReplay(id))
	require.True(t, store.Blocked())
	require.NoError(t, os.Chmod(dir, 0700))
	restarted, err := Open(path)
	require.NoError(t, err)
	require.True(t, restarted.Blocked())
	require.Equal(t, id, restarted.Snapshot().ID)
}
