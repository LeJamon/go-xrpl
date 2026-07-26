package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestNodeStoreSweepIntervalForSize(t *testing.T) {
	tests := map[string]time.Duration{
		"tiny":    10 * time.Second,
		"small":   30 * time.Second,
		"medium":  60 * time.Second,
		"large":   90 * time.Second,
		"huge":    120 * time.Second,
		"":        60 * time.Second,
		"invalid": 60 * time.Second,
	}
	for nodeSize, want := range tests {
		t.Run(nodeSize, func(t *testing.T) {
			require.Equal(t, want, nodeStoreSweepIntervalForSize(nodeSize))
		})
	}
}

func TestServiceNodeStoreSweeperRunsWhileIdleAndStops(t *testing.T) {
	family := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, family.Close()) })

	cfg := DefaultConfig()
	cfg.NodeSize = "large"
	cfg.SHAMapFamily = family
	service, err := New(cfg)
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, service.sweepInterval)
	service.sweepInterval = time.Millisecond
	t.Cleanup(service.Stop)
	require.NoError(t, service.Start())

	cache := family.FullBelowCache()
	cache.Insert(cache.Generation(), [32]byte{1})
	initial := cache.Stats().Sweeps
	require.Eventually(t, func() bool {
		return cache.Stats().Sweeps >= initial+2
	}, time.Second, time.Millisecond, "periodic sweeps stopped when insertions stopped")

	service.Stop()
	stopped := cache.Stats().Sweeps
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, stopped, cache.Stats().Sweeps, "sweep continued after service shutdown")

	service.sweepMu.Lock()
	require.Nil(t, service.sweepCancel)
	require.Nil(t, service.sweepDone)
	service.sweepMu.Unlock()

	service.Stop()
}
