package node

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
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
	cleanSnapshot := memory.cleanNodes.Snapshot()
	nodeSnapshot := memory.nodes.Snapshot()
	relaySnapshot := memory.relay.Snapshot()
	residual := initial.SharedAvailable + cleanSnapshot.Minimum + nodeSnapshot.Minimum + relaySnapshot.Minimum
	require.Equal(t, residual/4, cleanSnapshot.Minimum)
	require.Equal(t, residual/2, cleanSnapshot.Maximum)
	require.Equal(t, residual/8, nodeSnapshot.Minimum)
	require.Equal(t, residual/4, nodeSnapshot.Maximum)
	require.Zero(t, relaySnapshot.Minimum)
	require.Equal(t, residual/16, relaySnapshot.Maximum)
	peerConfig := peermanagement.DefaultConfig()
	for _, option := range adaptor.OverlayOptionsFromConfig(cfg) {
		option(&peerConfig)
	}
	generalMinimum := uint64(peermanagement.MinimumOutboundRetainedBytes(0))
	criticalMinimum := uint64(peermanagement.MinimumOutboundRetainedBytes(peerConfig.MaxPeers)) - generalMinimum
	expectedProgressMinimums := map[string]uint64{
		"acquisition":         config.MinAcquisitionMemoryMB << 20,
		"storage":             memory.storageBytes,
		"persistence":         shamap.DirtyWorkspaceBytes,
		"replay_verification": 2 * service.ReplayVerificationWorkspaceBytes,
		"full_below":          shamap.FullBelowCacheMemoryBytes,
		"peer_inbound":        uint64(peermanagement.DefaultInboundRetainedBytes),
		"peer_critical":       criticalMinimum,
		"peer_outbound":       generalMinimum,
	}
	for _, account := range initial.Accounts {
		if account.Minimum == 0 {
			continue
		}
		if expected, ok := expectedProgressMinimums[account.Name]; ok {
			require.Equal(t, expected, account.Minimum, account.Name)
		} else {
			switch account.Name {
			case "node_cache", "shamap_cache", "relay_cache":
				continue
			default:
				t.Fatalf("unexpected progress reserve %q", account.Name)
			}
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
		default:
			t.Fatalf("unexpected progress reserve %q", account.Name)
		}
		require.True(t, target.TryReserve(account.Minimum), account.Name)
		target.Release(account.Minimum)
	}
	require.Zero(t, memory.pool.Snapshot().Used)
	exerciseCacheWarmOrders(t, memory)

	pressure := memory.acquisition.Capacity()
	require.Equal(t, pressure, memory.acquisition.Snapshot().Available)
	require.True(t, memory.acquisition.TryReserve(pressure))
	for _, cache := range cacheAccounts(memory) {
		minimum := cache.Snapshot().Minimum
		require.True(t, cache.TryReserve(minimum), cache.Snapshot().Name)
		require.False(t, cache.TryReserve(1), cache.Snapshot().Name)
	}
	for _, cache := range cacheAccounts(memory) {
		cache.Release(cache.Snapshot().Used)
	}
	memory.acquisition.Release(pressure)
	require.Zero(t, memory.pool.Snapshot().Used)
}

func TestNodeMemoryStandaloneCacheShares(t *testing.T) {
	const gib = uint64(1 << 30)
	memory, err := newNodeMemory(&config.Config{Resources: config.ResourcesConfig{MemoryMB: 4 << 10}}, true, nil)
	require.NoError(t, err)
	initial := memory.pool.Snapshot()
	cleanSnapshot := memory.cleanNodes.Snapshot()
	nodeSnapshot := memory.nodes.Snapshot()
	residual := initial.SharedAvailable + cleanSnapshot.Minimum + nodeSnapshot.Minimum
	require.Equal(t, initial.Limit, 4*gib)
	require.Equal(t, initial.Limit, residual)
	require.Equal(t, residual/4, cleanSnapshot.Minimum)
	require.Equal(t, residual/2, cleanSnapshot.Maximum)
	require.Equal(t, residual/8, nodeSnapshot.Minimum)
	require.Equal(t, residual/4, nodeSnapshot.Maximum)
	require.Zero(t, memory.relay.Snapshot().Minimum)
	require.Equal(t, residual/16, memory.relay.Snapshot().Maximum)
	exerciseCacheWarmOrders(t, memory)
	require.Zero(t, memory.pool.Snapshot().Used)
}

func TestNodeMemoryTinyResidualUsesExplicitCacheMaximum(t *testing.T) {
	for _, test := range []struct {
		name     string
		limit    uint64
		residual uint64
		divisor  uint64
		want     uint64
	}{
		{name: "zero residual", limit: 1 << 20, residual: 0, divisor: 4, want: 1},
		{name: "rounded residual", limit: 1 << 20, residual: 1, divisor: 16, want: 1},
		{name: "zero pool", limit: 0, residual: 0, divisor: 16, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			maximum := boundedCacheMaximum(test.limit, test.residual, test.divisor)
			require.Equal(t, test.want, maximum)
			pool := membudget.New(test.limit)
			account, err := pool.NewAccount("cache", 0, maximum)
			require.NoError(t, err)
			require.Equal(t, test.want, account.Limit())
		})
	}
}

func cacheAccounts(memory *nodeMemory) []*membudget.Account {
	return []*membudget.Account{memory.cleanNodes, memory.nodes, memory.relay}
}

func exerciseCacheWarmOrders(t *testing.T, memory *nodeMemory) {
	t.Helper()
	for _, order := range [][]*membudget.Account{
		{memory.cleanNodes, memory.nodes, memory.relay},
		{memory.nodes, memory.cleanNodes, memory.relay},
	} {
		for _, cache := range order {
			snapshot := cache.Snapshot()
			require.True(t, cache.TryReserve(snapshot.Maximum), snapshot.Name)
		}
		for index := len(order) - 1; index >= 0; index-- {
			cache := order[index]
			cache.Release(cache.Snapshot().Used)
		}
		require.Zero(t, memory.pool.Snapshot().Used)
	}
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
			tree, err := shamap.NewBacked(shamap.TypeState, runtime.nodeFamily)
			require.NoError(t, err)
			keys := make([][32]byte, 64)
			for i := range keys {
				keys[i] = sha256.Sum256([]byte{byte(i)})
				require.NoError(t, tree.Put(keys[i], make([]byte, 24)))
			}
			require.NoError(t, tree.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error {
				return runtime.nodeFamily.StoreBatch(t.Context(), entries)
			}))
			for _, key := range keys {
				_, found, err := tree.Get(key)
				require.NoError(t, err)
				require.True(t, found)
			}
			stats := runtime.nodeFamily.SHAMapCleanResidency().Stats()
			require.Positive(t, stats.UsedBytes)
			require.Positive(t, stats.Nodes)
			require.Equal(t, runtime.memory.cleanNodes.Limit(), stats.Limit)
			for _, account := range runtime.memory.pool.Snapshot().Accounts {
				if account.Name == "shamap_cache" {
					require.Equal(t, stats.UsedBytes, account.Used)
				}
			}
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
