package observability

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/prometheus/client_golang/prometheus"
)

var nodeMemoryPool atomic.Pointer[membudget.Pool]

// SetMemoryPool publishes the node's managed-memory accounting until cleanup.
func SetMemoryPool(pool *membudget.Pool) func() {
	nodeMemoryPool.Store(pool)
	return func() { nodeMemoryPool.CompareAndSwap(pool, nil) }
}

type memoryCollector struct {
	bytes    *prometheus.Desc
	denied   *prometheus.Desc
	released *prometheus.Desc
	invalid  *prometheus.Desc
}

// NewMemoryCollector reports reservations separately from the process RSS collector.
func NewMemoryCollector() prometheus.Collector {
	return &memoryCollector{
		bytes:    prometheus.NewDesc("goxrpl_memory_budget_bytes", "Managed memory accounting; reserved bytes are protected capacity, not resident memory.", []string{"consumer", "state"}, nil),
		denied:   prometheus.NewDesc("goxrpl_memory_budget_denied_total", "Memory reservations refused by the shared allowance or consumer ceiling.", []string{"consumer"}, nil),
		released: prometheus.NewDesc("goxrpl_memory_budget_released_bytes_total", "Managed memory credits returned after ownership ends.", []string{"consumer"}, nil),
		invalid:  prometheus.NewDesc("goxrpl_memory_budget_invalid_releases_total", "Rejected attempts to return more managed memory than a consumer owns.", []string{"consumer"}, nil),
	}
}

func (c *memoryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytes
	ch <- c.denied
	ch <- c.released
	ch <- c.invalid
}

func (c *memoryCollector) Collect(ch chan<- prometheus.Metric) {
	pool := nodeMemoryPool.Load()
	if pool == nil {
		return
	}
	snapshot := pool.Snapshot()
	bytes := func(name, state string, value uint64) {
		ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(value), name, state)
	}
	bytes("total", "limit", snapshot.Limit)
	bytes("total", "used", snapshot.Used)
	bytes("total", "peak", snapshot.Peak)
	bytes("total", "reserved", snapshot.Reserved)
	bytes("total", "available", snapshot.SharedAvailable)
	for _, account := range snapshot.Accounts {
		bytes(account.Name, "limit", account.Maximum)
		bytes(account.Name, "used", account.Used)
		bytes(account.Name, "peak", account.Peak)
		bytes(account.Name, "reserved", account.Reserved)
		bytes(account.Name, "available", account.Available)
		ch <- prometheus.MustNewConstMetric(c.denied, prometheus.CounterValue, float64(account.Denied), account.Name)
		ch <- prometheus.MustNewConstMetric(c.released, prometheus.CounterValue, float64(account.Released), account.Name)
		ch <- prometheus.MustNewConstMetric(c.invalid, prometheus.CounterValue, float64(account.InvalidReleases), account.Name)
	}
}
