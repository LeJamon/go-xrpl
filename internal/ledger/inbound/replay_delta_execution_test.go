package inbound

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestReplayDeltaRetainsBuiltRootUntilLastOwnerReleases(t *testing.T) {
	pool := membudget.New(32 << 20)
	workspace, err := pool.NewAccount("work", 16<<20, 16<<20)
	require.NoError(t, err)
	changes, err := pool.NewAccount("changes", 0, 1<<20)
	require.NoError(t, err)
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family: backend.NewMemory(), Workspace: workspace,
		Overlay: overlay.Config{MemoryBudget: changes, MaxMemoryBytes: 128 << 10,
			MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	parent := makeGenesisLedger(t)
	t.Cleanup(parent.Discard)
	require.NoError(t, parent.EnableBoundedExecution(t.Context(), store))
	resp, hdr := buildEmptyClosedSuccessorResponse(t, parent)
	replay := armReplayDeltaWith(t, parent, resp, hdr)
	derived, err := replay.Apply(tx.EngineConfig{})
	require.NoError(t, err)
	require.Equal(t, hdr.Hash, derived.Hash())
	want := rootlease.Root{Hash: hdr.AccountHash, Kind: shamap.TypeState}
	require.Contains(t, store.Roots(), want)
	releaseDiagnostic := replay.RetainMemory()
	require.NotNil(t, releaseDiagnostic)
	t.Cleanup(releaseDiagnostic)
	replay.ReleaseMemory()
	require.Contains(t, store.Roots(), want)
	releaseDiagnostic()
	require.Empty(t, store.Roots())
	require.Zero(t, changes.Snapshot().Used)
	_, err = replay.Apply(tx.EngineConfig{})
	require.ErrorContains(t, err, "ownership has ended")
	_, err = replay.Result()
	require.ErrorContains(t, err, "ownership has ended")
	_, err = replay.TransactionMapSnapshotContext(t.Context())
	require.ErrorContains(t, err, "ownership has ended")

	releasedBeforeApply := armReplayDeltaWith(t, parent, resp, hdr)
	releasedBeforeApply.ReleaseMemory()
	_, err = releasedBeforeApply.Apply(tx.EngineConfig{})
	require.ErrorContains(t, err, "ownership has ended")
	require.Empty(t, store.Roots())
}
