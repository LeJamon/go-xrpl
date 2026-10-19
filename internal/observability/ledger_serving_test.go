package observability

import (
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestLedgerServeCollectorReportsBothClasses(t *testing.T) {
	snapshot := LedgerServeMetricsSnapshot{
		Ledger: peermanagement.ServeSchedulerStats{
			Queued:        3,
			Active:        2,
			RetainedBytes: 4096,
			Dropped:       4,
			Completed:     5,
			Canceled:      6,
			QueueWait: peermanagement.ServeDurationStats{
				Count:   2,
				Sum:     150 * time.Millisecond,
				Buckets: [8]uint64{1, 2, 2, 2, 2, 2, 2, 2},
			},
			Service: peermanagement.ServeDurationStats{
				Count:   2,
				Sum:     3 * time.Second,
				Buckets: [8]uint64{0, 0, 0, 0, 0, 1, 2, 2},
			},
		},
		TxSet: peermanagement.ServeSchedulerStats{
			Queued:        7,
			Active:        1,
			RetainedBytes: 8192,
			Dropped:       8,
			Completed:     9,
			Canceled:      10,
			QueueWait: peermanagement.ServeDurationStats{
				Count:   1,
				Sum:     2 * time.Millisecond,
				Buckets: [8]uint64{1, 1, 1, 1, 1, 1, 1, 1},
			},
			Service: peermanagement.ServeDurationStats{
				Count:   1,
				Sum:     500 * time.Millisecond,
				Buckets: [8]uint64{0, 0, 0, 0, 0, 1, 1, 1},
			},
		},
	}
	cleanup := SetLedgerServeMetrics(func() LedgerServeMetricsSnapshot { return snapshot })
	defer cleanup()

	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewLedgerServeCollector()))
	families, err := registry.Gather()
	require.NoError(t, err)

	for _, name := range []string{
		"goxrpl_router_serve_queued",
		"goxrpl_router_serve_active",
		"goxrpl_router_serve_retained_bytes",
		"goxrpl_router_serve_dropped_total",
		"goxrpl_router_serve_completed_total",
		"goxrpl_router_serve_canceled_total",
		"goxrpl_router_serve_queue_wait_seconds",
		"goxrpl_router_serve_service_seconds",
	} {
		require.NotNil(t, metricFamily(families, name), "missing metric family %s", name)
	}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "goxrpl_router_serve_") {
			continue
		}
		for _, metric := range family.Metric {
			require.Len(t, metric.Label, 1)
			require.Equal(t, "class", metric.Label[0].GetName())
			require.Contains(t, []string{"ledger", "txset"}, metric.Label[0].GetValue())
		}
	}

	queued := metricFamily(families, "goxrpl_router_serve_queued")
	require.Equal(t, map[string]float64{"ledger": 3, "txset": 7}, gaugeValuesByClass(queued))
	active := metricFamily(families, "goxrpl_router_serve_active")
	require.Equal(t, map[string]float64{"ledger": 2, "txset": 1}, gaugeValuesByClass(active))
	retained := metricFamily(families, "goxrpl_router_serve_retained_bytes")
	require.Equal(t, map[string]float64{"ledger": 4096, "txset": 8192}, gaugeValuesByClass(retained))

	dropped := metricFamily(families, "goxrpl_router_serve_dropped_total")
	require.Equal(t, map[string]float64{"ledger": 4, "txset": 8}, counterValuesByClass(dropped))
	completed := metricFamily(families, "goxrpl_router_serve_completed_total")
	require.Equal(t, map[string]float64{"ledger": 5, "txset": 9}, counterValuesByClass(completed))
	canceled := metricFamily(families, "goxrpl_router_serve_canceled_total")
	require.Equal(t, map[string]float64{"ledger": 6, "txset": 10}, counterValuesByClass(canceled))

	queueWait := metricFamily(families, "goxrpl_router_serve_queue_wait_seconds")
	service := metricFamily(families, "goxrpl_router_serve_service_seconds")
	for _, family := range []*metricFamilyView{
		{family: queueWait, want: map[string]struct {
			count   uint64
			sum     float64
			buckets [8]uint64
		}{
			"ledger": {count: 2, sum: 0.15, buckets: [8]uint64{1, 2, 2, 2, 2, 2, 2, 2}},
			"txset":  {count: 1, sum: 0.002, buckets: [8]uint64{1, 1, 1, 1, 1, 1, 1, 1}},
		}},
		{family: service, want: map[string]struct {
			count   uint64
			sum     float64
			buckets [8]uint64
		}{
			"ledger": {count: 2, sum: 3, buckets: [8]uint64{0, 0, 0, 0, 0, 1, 2, 2}},
			"txset":  {count: 1, sum: 0.5, buckets: [8]uint64{0, 0, 0, 0, 0, 1, 1, 1}},
		}},
	} {
		for class, want := range family.want {
			metric := histogramMetricByClass(family.family, class)
			require.Equal(t, want.count, metric.Histogram.GetSampleCount())
			require.InDelta(t, want.sum, metric.Histogram.GetSampleSum(), 1e-12)
			bounds := peermanagement.ServeDurationBucketBounds()
			require.Len(t, metric.Histogram.Bucket, len(bounds))
			for i, bound := range bounds {
				require.InDelta(t, bound, metric.Histogram.Bucket[i].GetUpperBound(), 1e-12)
				require.Equal(t, want.buckets[i], metric.Histogram.Bucket[i].GetCumulativeCount())
			}
			for _, label := range metric.Label {
				require.Equal(t, "class", label.GetName())
			}
		}
	}

	cleanup()
	families, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, families)
}

type metricFamilyView struct {
	family *dto.MetricFamily
	want   map[string]struct {
		count   uint64
		sum     float64
		buckets [8]uint64
	}
}

func metricFamily(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

func gaugeValuesByClass(family *dto.MetricFamily) map[string]float64 {
	values := make(map[string]float64, len(family.Metric))
	for _, metric := range family.Metric {
		values[classLabel(metric)] = metric.Gauge.GetValue()
	}
	return values
}

func counterValuesByClass(family *dto.MetricFamily) map[string]float64 {
	values := make(map[string]float64, len(family.Metric))
	for _, metric := range family.Metric {
		values[classLabel(metric)] = metric.Counter.GetValue()
	}
	return values
}

func histogramMetricByClass(family *dto.MetricFamily, class string) *dto.Metric {
	for _, metric := range family.Metric {
		if classLabel(metric) == class {
			return metric
		}
	}
	return nil
}

func classLabel(metric *dto.Metric) string {
	for _, label := range metric.Label {
		if label.GetName() == "class" {
			return label.GetValue()
		}
	}
	return ""
}
