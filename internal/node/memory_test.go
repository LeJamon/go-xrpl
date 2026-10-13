package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestNodeMemoryAutomaticSizingAndProgressReserves(t *testing.T) {
	const gib = uint64(1 << 30)
	cfg := &config.Config{NodeDB: config.NodeDBConfig{Path: filepath.Join(t.TempDir(), "nodes"), OnlineDelete: 512}}
	var calls int
	memory, err := newNodeMemory(cfg, false, func() (membudget.Headroom, error) {
		calls++
		return membudget.Headroom{Available: 11 * gib, Source: "fixture"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	initial := memory.pool.Snapshot()
	require.Equal(t, 11*gib*33/100, initial.Limit)
	require.Equal(t, initial.Limit, memory.acquisition.Limit())
	require.Equal(t, uint64(1<<30), memory.storageBytes)
	require.True(t, memory.nodes.TryReserve(initial.SharedAvailable))
	require.False(t, memory.nodes.TryReserve(1))
	for _, account := range initial.Accounts {
		if account.Minimum == 0 {
			continue
		}
		var target *membudget.Account
		switch account.Name {
		case "acquisition":
			target = memory.acquisition
		case "storage":
			target = memory.storage
		case "persistence":
			target = memory.persistence
		case "replay_verification":
			target = memory.replayVerification
		case "peer_inbound":
			target = memory.inbound
		case "peer_outbound":
			target = memory.outbound
		case "peer_critical":
			target = memory.outboundCritical
		case "full_below":
			target = memory.fullBelow
		case "state_overlay":
			target = memory.state
		case "state_roots":
			target = memory.stateRoots
		case "state_storage":
			target = memory.stateStorage
		default:
			t.Fatalf("unexpected progress reserve %q", account.Name)
		}
		require.True(t, target.TryReserve(account.Minimum), account.Name)
		target.Release(account.Minimum)
	}
	memory.nodes.Release(initial.SharedAvailable)
	require.Zero(t, memory.pool.Snapshot().Used)
}

func TestNodeMemoryFixedSizingAndInvalidAllowance(t *testing.T) {
	probe := func() (membudget.Headroom, error) {
		t.Fatal("explicit allowance must not probe")
		return membudget.Headroom{}, nil
	}
	cfg := &config.Config{Resources: config.ResourcesConfig{MemoryMB: 4096}, Acquisition: config.AcquisitionConfig{MemoryMB: 2048}}
	memory, err := newNodeMemory(cfg, false, probe)
	require.NoError(t, err)
	require.Equal(t, uint64(4<<30), memory.pool.Snapshot().Limit)
	require.Equal(t, uint64(2<<30), memory.acquisition.Limit())
	cfg.Resources.MemoryMB = 512
	_, err = newNodeMemory(cfg, false, probe)
	require.ErrorContains(t, err, "resources:")
	cfg.Resources.MemoryMB = 0
	_, err = newNodeMemory(cfg, false, func() (membudget.Headroom, error) { return membudget.Headroom{}, errors.New("probe unavailable") })
	require.ErrorContains(t, err, "configure resources.memory_mb explicitly")
}

func TestNodeMemoryStorageFailureAndShutdownRelease(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "startup failure"}[fail], func(t *testing.T) {
			cfg := &config.Config{Resources: config.ResourcesConfig{MemoryMB: 2048}, NodeDB: config.NodeDBConfig{Path: filepath.Join(t.TempDir(), "nodes")}}
			if fail {
				cfg.DatabasePath = "postgres://%"
			}
			runtime := newNodeRuntime(context.Background(), cfg, "", true, service.StartupConfig{}, xrpllog.Discard(), xrpllog.Discard(), RunOptions{})
			defer runtime.cancel(nil)
			var err error
			runtime.memory, err = newNodeMemory(cfg, true, nil)
			require.NoError(t, err)
			err = runtime.configureStorage()
			if fail {
				require.Error(t, err)
				require.Zero(t, runtime.memory.pool.Snapshot().Used)
				return
			}
			require.NoError(t, err)
			require.Equal(t, runtime.memory.storageBytes, runtime.memory.pool.Snapshot().Used)
			require.NoError(t, runtime.shutdownWithin(5*time.Second))
			require.Zero(t, runtime.memory.pool.Snapshot().Used)
		})
	}
}

type memoryCloseErrorStore struct {
	nodestore.Database
	err error
}

func (s *memoryCloseErrorStore) Close() error { return errors.Join(s.Database.Close(), s.err) }

func TestNodeMemoryStorageCloseErrorStillReturnsCommitment(t *testing.T) {
	cfg := &config.Config{Resources: config.ResourcesConfig{MemoryMB: 2048}, NodeDB: config.NodeDBConfig{Path: filepath.Join(t.TempDir(), "nodes")}}
	runtime := newNodeRuntime(context.Background(), cfg, "", true, service.StartupConfig{}, xrpllog.Discard(), xrpllog.Discard(), RunOptions{})
	defer runtime.cancel(nil)
	var err error
	runtime.memory, err = newNodeMemory(cfg, true, nil)
	require.NoError(t, err)
	require.NoError(t, runtime.configureStorage())
	closeErr := errors.New("close flush failed")
	runtime.nodeStore = &memoryCloseErrorStore{Database: runtime.nodeStore, err: closeErr}
	require.ErrorIs(t, runtime.shutdownWithin(5*time.Second), closeErr)
	require.Zero(t, runtime.memory.pool.Snapshot().Used)
	require.ErrorIs(t, runtime.shutdownWithin(5*time.Second), closeErr)
	require.Zero(t, runtime.memory.pool.Snapshot().InvalidReleases)
}
