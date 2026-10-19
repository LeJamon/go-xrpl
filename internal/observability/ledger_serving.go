package observability

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/prometheus/client_golang/prometheus"
)

// LedgerServeMetricsSnapshot is the metrics view of the router's two serving
// classes. The callback form keeps the metrics registry independent from the
// consensus router lifecycle.
type LedgerServeMetricsSnapshot struct {
	Ledger peermanagement.ServeSchedulerStats
	TxSet  peermanagement.ServeSchedulerStats
}

type ledgerServeMetricsSource struct {
	snapshot func() LedgerServeMetricsSnapshot
}

var observedLedgerServeMetrics atomic.Pointer[ledgerServeMetricsSource]

// SetLedgerServeMetrics publishes the router serving metrics source until the
// returned cleanup function is called. A cleanup only clears the source it
// installed, so a later node cannot be cleared by an older node shutting down.
func SetLedgerServeMetrics(snapshot func() LedgerServeMetricsSnapshot) func() {
	if snapshot == nil {
		return func() {}
	}
	source := &ledgerServeMetricsSource{snapshot: snapshot}
	observedLedgerServeMetrics.Store(source)
	return func() { observedLedgerServeMetrics.CompareAndSwap(source, nil) }
}

type ledgerServeCollector struct {
	queued    *prometheus.Desc
	active    *prometheus.Desc
	retained  *prometheus.Desc
	dropped   *prometheus.Desc
	completed *prometheus.Desc
	canceled  *prometheus.Desc
	queueWait *prometheus.Desc
	service   *prometheus.Desc
}

// NewLedgerServeCollector reports bounded GET_LEDGER serving by class.
func NewLedgerServeCollector() prometheus.Collector {
	labels := []string{"class"}
	return &ledgerServeCollector{
		queued:    prometheus.NewDesc("goxrpl_router_serve_queued", "Queued inbound GET_LEDGER serving jobs by class.", labels, nil),
		active:    prometheus.NewDesc("goxrpl_router_serve_active", "Active inbound GET_LEDGER serving jobs by class.", labels, nil),
		retained:  prometheus.NewDesc("goxrpl_router_serve_retained_bytes", "Estimated request bytes retained by queued, active, or disposing GET_LEDGER serving jobs by class.", labels, nil),
		dropped:   prometheus.NewDesc("goxrpl_router_serve_dropped_total", "Inbound GET_LEDGER serving jobs rejected by the class scheduler.", labels, nil),
		completed: prometheus.NewDesc("goxrpl_router_serve_completed_total", "Inbound GET_LEDGER serving jobs completed by class.", labels, nil),
		canceled:  prometheus.NewDesc("goxrpl_router_serve_canceled_total", "Inbound GET_LEDGER serving jobs canceled after admission by class.", labels, nil),
		queueWait: prometheus.NewDesc("goxrpl_router_serve_queue_wait_seconds", "Queue wait duration for inbound GET_LEDGER serving jobs by class.", labels, nil),
		service:   prometheus.NewDesc("goxrpl_router_serve_service_seconds", "Service duration for inbound GET_LEDGER serving jobs by class.", labels, nil),
	}
}

func (c *ledgerServeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queued
	ch <- c.active
	ch <- c.retained
	ch <- c.dropped
	ch <- c.completed
	ch <- c.canceled
	ch <- c.queueWait
	ch <- c.service
}

func (c *ledgerServeCollector) Collect(ch chan<- prometheus.Metric) {
	source := observedLedgerServeMetrics.Load()
	if source == nil || source.snapshot == nil {
		return
	}
	snapshot := source.snapshot()
	for _, class := range []struct {
		name  string
		stats peermanagement.ServeSchedulerStats
	}{
		{name: "ledger", stats: snapshot.Ledger},
		{name: "txset", stats: snapshot.TxSet},
	} {
		labels := []string{class.name}
		ch <- prometheus.MustNewConstMetric(c.queued, prometheus.GaugeValue, float64(class.stats.Queued), labels...)
		ch <- prometheus.MustNewConstMetric(c.active, prometheus.GaugeValue, float64(class.stats.Active), labels...)
		ch <- prometheus.MustNewConstMetric(c.retained, prometheus.GaugeValue, float64(class.stats.RetainedBytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.dropped, prometheus.CounterValue, float64(class.stats.Dropped), labels...)
		ch <- prometheus.MustNewConstMetric(c.completed, prometheus.CounterValue, float64(class.stats.Completed), labels...)
		ch <- prometheus.MustNewConstMetric(c.canceled, prometheus.CounterValue, float64(class.stats.Canceled), labels...)
		ch <- prometheus.MustNewConstHistogram(
			c.queueWait,
			class.stats.QueueWait.Count,
			class.stats.QueueWait.Sum.Seconds(),
			serveDurationBuckets(class.stats.QueueWait),
			labels...,
		)
		ch <- prometheus.MustNewConstHistogram(
			c.service,
			class.stats.Service.Count,
			class.stats.Service.Sum.Seconds(),
			serveDurationBuckets(class.stats.Service),
			labels...,
		)
	}
}

func serveDurationBuckets(stats peermanagement.ServeDurationStats) map[float64]uint64 {
	bounds := peermanagement.ServeDurationBucketBounds()
	buckets := make(map[float64]uint64, len(bounds))
	for i, bound := range bounds {
		buckets[bound] = stats.Buckets[i]
	}
	return buckets
}
