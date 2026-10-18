package observability

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/prometheus/client_golang/prometheus"
)

type nodeStoreMetrics struct{ db nodestore.Database }

var observedNodeStore atomic.Pointer[nodeStoreMetrics]

func SetNodeStore(db nodestore.Database) func() {
	source := &nodeStoreMetrics{db: db}
	observedNodeStore.Store(source)
	return func() { observedNodeStore.CompareAndSwap(source, nil) }
}

type nodeStoreCollector struct{ events *prometheus.Desc }

func NewNodeStoreCollector() prometheus.Collector {
	return &nodeStoreCollector{events: prometheus.NewDesc("goxrpl_nodestore_cache_access_total", "NodeStore positive-cache activity by access class; reservation denials include recovered attempts.", []string{"class", "event"}, nil)}
}

func (c *nodeStoreCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.events }

func (c *nodeStoreCollector) Collect(ch chan<- prometheus.Metric) {
	source := observedNodeStore.Load()
	if source == nil || source.db == nil {
		return
	}
	stats := source.db.Stats()
	for _, class := range []struct {
		name  string
		stats nodestore.CacheAccessStats
	}{{"foreground", stats.Foreground}, {"peer", stats.Peer}} {
		for _, event := range []struct {
			name  string
			value uint64
		}{
			{"lookup", class.stats.Lookups}, {"hit", class.stats.Hits},
			{"admission", class.stats.Admissions}, {"bypass", class.stats.Bypasses},
			{"eviction", class.stats.Evictions}, {"admission_denied", class.stats.AdmissionDenials},
			{"reservation_denied", class.stats.ReservationDenials},
		} {
			ch <- prometheus.MustNewConstMetric(c.events, prometheus.CounterValue, float64(event.value), class.name, event.name)
		}
	}
}
