package observability

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestMemoryCollectorReportsProtectedAndElasticCapacity(t *testing.T) {
	pool := membudget.New(100)
	account, err := pool.NewAccount("acquisition", 40, 100)
	require.NoError(t, err)
	require.True(t, account.TryReserve(25))
	require.False(t, account.TryReserve(100))
	account.Release(5)
	cleanup := SetMemoryPool(pool)
	defer cleanup()
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(NewMemoryCollector()))
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
	require.Equal(t, float64(40), values["goxrpl_memory_budget_bytes/acquisition/reserved"])
	require.Equal(t, float64(20), values["goxrpl_memory_budget_bytes/total/used"])
	require.Equal(t, float64(60), values["goxrpl_memory_budget_bytes/total/available"])
	require.Equal(t, float64(1), values["goxrpl_memory_budget_denied_total/acquisition"])
	require.Equal(t, float64(5), values["goxrpl_memory_budget_released_bytes_total/acquisition"])
	cleanup()
	metrics, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, metrics)
}
