package observability

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/prometheus/client_golang/prometheus"
)

var cleanResidency atomic.Pointer[shamap.CleanResidency]

func SetSHAMapCleanResidency(cache *shamap.CleanResidency) func() {
	cleanResidency.Store(cache)
	return func() { cleanResidency.CompareAndSwap(cache, nil) }
}

type shamapCollector struct {
	bytes  *prometheus.Desc
	nodes  *prometheus.Desc
	events *prometheus.Desc
}

func NewSHAMapCollector() prometheus.Collector {
	return &shamapCollector{
		bytes:  prometheus.NewDesc("goxrpl_shamap_cache_bytes", "Clean SHAMap cache accounting; excludes active operations and dirty trees.", []string{"state"}, nil),
		nodes:  prometheus.NewDesc("goxrpl_shamap_cache_nodes", "Unique clean nodes owned by the SHAMap cache.", nil, nil),
		events: prometheus.NewDesc("goxrpl_shamap_cache_events_total", "Clean SHAMap cache lookups, evictions and admission denials.", []string{"event"}, nil),
	}
}

func (c *shamapCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytes
	ch <- c.nodes
	ch <- c.events
}

func (c *shamapCollector) Collect(ch chan<- prometheus.Metric) {
	cache := cleanResidency.Load()
	if cache == nil {
		return
	}
	stats := cache.Stats()
	ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(stats.Limit), "limit")
	ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(stats.UsedBytes), "used")
	ch <- prometheus.MustNewConstMetric(c.nodes, prometheus.GaugeValue, float64(stats.Nodes))
	for _, event := range []struct {
		name  string
		value uint64
	}{
		{"hit", stats.Hits},
		{"miss", stats.Misses},
		{"eviction", stats.Evictions},
		{"admission_denied", stats.AdmissionDenials},
	} {
		ch <- prometheus.MustNewConstMetric(c.events, prometheus.CounterValue, float64(event.value), event.name)
	}
}
