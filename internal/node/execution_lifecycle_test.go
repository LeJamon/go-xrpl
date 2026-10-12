package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestStandaloneExecutionStoreLifetime(t *testing.T) {
	t.Run("ephemeral storage", func(t *testing.T) {
		testStandaloneExecutionStoreLifetime(t, "")
	})
	t.Run("persistent storage", func(t *testing.T) {
		testStandaloneExecutionStoreLifetime(t, filepath.Join(t.TempDir(), "nodes"))
	})
}

func testStandaloneExecutionStoreLifetime(t *testing.T, nodeDBPath string) {
	cfg := &config.Config{
		NetworkID: config.NetworkID{Set: true, ID: 0},
		NodeSize:  "small",
		Resources: config.ResourcesConfig{MemoryMB: 4096},
		NodeDB:    config.NodeDBConfig{Path: nodeDBPath},
	}
	runtime := newNodeRuntime(
		t.Context(), cfg, "", true,
		service.StartupConfig{Mode: service.StartupFresh},
		xrpllog.Discard(), xrpllog.Discard(), RunOptions{},
	)

	var releaseLease func()
	var closedExecution bool
	t.Cleanup(func() {
		if releaseLease != nil {
			releaseLease()
		}
		if runtime.ledger != nil {
			runtime.ledger.Stop()
		}
		if !closedExecution && runtime.executionCleanup != nil {
			_ = runtime.executionCleanup()
		}
		if runtime.ephemeralFamily != nil {
			_ = runtime.ephemeralFamily.Close()
		}
		if runtime.nodeFamily != nil {
			runtime.nodeFamily.FullBelowCache().Close()
		}
		if runtime.nodeStore != nil {
			_ = runtime.nodeStore.Close()
		}
		if runtime.memory != nil && runtime.memory.releaseStorage != nil {
			runtime.memory.releaseStorage()
		}
		if runtime.stopMemoryMetrics != nil {
			runtime.stopMemoryMetrics()
		}
	})

	require.NoError(t, runtime.configureMemory())
	require.NoError(t, runtime.configureStorage())
	require.NoError(t, runtime.configureLedger())
	require.NotNil(t, runtime.ledger)
	require.NotNil(t, runtime.ledger.GetOpenLedger())

	open := runtime.ledger.GetOpenLedger()
	store := open.ExecutionStore()
	require.NotNil(t, store)
	var stateRoot rootlease.Root
	for _, root := range store.Roots() {
		if root.Kind == shamap.TypeState && root.Hash != ([32]byte{}) {
			stateRoot = root
			break
		}
	}
	require.NotZero(t, stateRoot.Hash)
	if nodeDBPath != "" {
		flatPath := filepath.Join(cfg.LocalStateDir(), "execution-state")
		require.DirExists(t, flatPath)
		require.DirExists(t, filepath.Join(flatPath, "scratch"))
	}

	release, err := store.AcquireRoot(stateRoot)
	require.NoError(t, err)
	releaseLease = release

	closeErr := store.Close()
	require.ErrorContains(t, closeErr, "ledger root leases are still active")
	if nodeDBPath != "" {
		flatPath := filepath.Join(cfg.LocalStateDir(), "execution-state")
		require.DirExists(t, flatPath)
		require.DirExists(t, filepath.Join(flatPath, "scratch"))
	}

	release()
	releaseLease = nil
	runtime.prepareFastLoadCheckpoint = func(context.Context) (bool, error) { return false, nil }
	require.NoError(t, runtime.shutdownWithin(10*time.Second))
	closedExecution = true
	if nodeDBPath != "" {
		flatPath := filepath.Join(cfg.LocalStateDir(), "execution-state")
		require.DirExists(t, flatPath)
		require.NoDirExists(t, filepath.Join(flatPath, "scratch"))
	}
}
