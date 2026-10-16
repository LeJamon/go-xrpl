package observability

import (
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestSHAMapCollectorReportsResidencyAndLifetime(t *testing.T) {
	family := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, family.Close()) })
	tree, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	key := [32]byte{1}
	require.NoError(t, tree.Put(key, make([]byte, 24)))
	require.NoError(t, tree.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	for range 2 {
		_, found, err := tree.Get(key)
		require.NoError(t, err)
		require.True(t, found)
	}
	_, err = tree.GetNodeFatByPath([32]byte{}, 0, 1, true)
	require.NoError(t, err)
	stats := family.SHAMapCleanResidency().Stats()
	require.Positive(t, stats.Hits)
	require.Positive(t, stats.Misses)
	cleanup := SetSHAMapCleanResidency(family.SHAMapCleanResidency())
	defer cleanup()
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewSHAMapCollector()))
	metrics, err := registry.Gather()
	require.NoError(t, err)
	values := map[string]float64{}
	for _, family := range metrics {
		for _, metric := range family.Metric {
			key := family.GetName()
			for _, label := range metric.Label {
				key += "/" + label.GetValue()
			}
			if metric.Gauge != nil {
				values[key] = metric.Gauge.GetValue()
			}
			if metric.Counter != nil {
				values[key] = metric.Counter.GetValue()
			}
		}
	}
	require.Equal(t, float64(stats.UsedBytes), values["goxrpl_shamap_cache_bytes/used"])
	require.Equal(t, float64(stats.Nodes), values["goxrpl_shamap_cache_nodes"])
	require.Equal(t, float64(stats.Hits), values["goxrpl_shamap_cache_events_total/hit"])
	require.Equal(t, float64(stats.Misses), values["goxrpl_shamap_cache_events_total/miss"])
	require.Equal(t, float64(stats.Foreground.Lookups), values["goxrpl_shamap_cache_access_total/foreground/lookup"])
	require.Equal(t, float64(stats.Foreground.Admissions), values["goxrpl_shamap_cache_access_total/foreground/admission"])
	require.Equal(t, float64(stats.Peer.Hits), values["goxrpl_shamap_cache_access_total/peer/hit"])
	require.Positive(t, values["goxrpl_shamap_cache_access_total/peer/hit"])
	require.Equal(t, float64(stats.Foreground.UsedBytes), values["goxrpl_shamap_cache_class_bytes/foreground/used"])
	require.Equal(t, float64(stats.Peer.Limit), values["goxrpl_shamap_cache_class_bytes/peer/limit"])
	cleanup()
	metrics, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, metrics)
}
