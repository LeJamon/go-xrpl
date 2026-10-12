package observability

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestStateCollectorReportsBoundedSnapshots(t *testing.T) {
	var executionCalls, flatCalls, spillCalls atomic.Int32
	cleanup := SetStateProvider(StateProvider{
		Execution: func() ExecutionSnapshot {
			executionCalls.Add(1)
			return ExecutionSnapshot{
				RootBorrowers:  3,
				BuildTimeNanos: 2_500_000_000,
				BuildNodes:     17,
				BuildErrors:    2,
				Frontier:       9,
				FlatHits:       23,
				RootFallbacks:  4,
			}
		},
		Flat: func() FlatSnapshot {
			flatCalls.Add(1)
			return FlatSnapshot{
				OutboxCount: 4,
				OutboxBytes: 2048,
				OutboxAge:   7500 * time.Millisecond,
				Advancing:   true,
				Failures:    2,
			}
		},
		Spill: func() SpillSnapshot {
			spillCalls.Add(1)
			return SpillSnapshot{Used: 1024, Peak: 4096, Denied: 5}
		},
		SpillAccount: "state_scratch",
	})
	defer cleanup()

	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(NewStateCollector()))
	metrics, err := registry.Gather()
	require.NoError(t, err)

	values := map[string]float64{}
	for _, family := range metrics {
		for _, metric := range family.Metric {
			key := family.GetName()
			for _, label := range metric.Label {
				key += "/" + label.GetName() + "=" + label.GetValue()
			}
			switch {
			case metric.Gauge != nil:
				values[key] = metric.Gauge.GetValue()
			case metric.Counter != nil:
				values[key] = metric.Counter.GetValue()
			}
		}
	}
	require.Equal(t, float64(3), values["goxrpl_execution_root_borrowers"])
	require.Equal(t, float64(2.5), values["goxrpl_execution_build_time_seconds_total"])
	require.Equal(t, float64(17), values["goxrpl_execution_build_nodes_total"])
	require.Equal(t, float64(2), values["goxrpl_execution_build_errors_total"])
	require.Equal(t, float64(9), values["goxrpl_execution_frontier"])
	require.Equal(t, float64(23), values["goxrpl_execution_flat_hits_total"])
	require.Equal(t, float64(4), values["goxrpl_execution_root_fallbacks_total"])
	require.Equal(t, float64(4), values["goxrpl_flat_outbox_count"])
	require.Equal(t, float64(2048), values["goxrpl_flat_outbox_bytes"])
	require.Equal(t, 7.5, values["goxrpl_flat_outbox_age_seconds"])
	require.Equal(t, float64(1), values["goxrpl_flat_advancing"])
	require.Equal(t, float64(2), values["goxrpl_flat_failures_total"])
	require.Equal(t, float64(1024), values["goxrpl_state_spill_disk_used_bytes/account=state_scratch"])
	require.Equal(t, float64(4096), values["goxrpl_state_spill_disk_peak_bytes/account=state_scratch"])
	require.Equal(t, float64(5), values["goxrpl_state_spill_disk_denied_total/account=state_scratch"])
	require.Equal(t, int32(1), executionCalls.Load())
	require.Equal(t, int32(1), flatCalls.Load())
	require.Equal(t, int32(1), spillCalls.Load())

	for _, family := range metrics {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				require.NotEqual(t, "hash", label.GetName())
				require.NotEqual(t, "ledger", label.GetName())
			}
		}
	}

	cleanup()
	metrics, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, metrics)
}

func TestStateCollectorDisabledWithoutProvider(t *testing.T) {
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(NewStateCollector()))
	metrics, err := registry.Gather()
	require.NoError(t, err)
	require.Empty(t, metrics)
}
