package observability

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ExecutionSnapshot contains the bounded execution counters exposed by the
// metrics collector. The provider must return an in-memory snapshot.
type ExecutionSnapshot struct {
	RootBorrowers  uint64
	BuildTimeNanos uint64
	BuildNodes     uint64
	BuildErrors    uint64
	Frontier       uint64
	FlatHits       uint64
	RootFallbacks  uint64
}

// FlatSnapshot contains cached flat-state operational counters. Its provider
// must not perform storage I/O while a scrape is in progress.
type FlatSnapshot struct {
	OutboxCount int
	OutboxBytes int64
	OutboxAge   time.Duration
	Advancing   bool
	Failures    uint64
}

// SpillSnapshot contains the byte-accounting counters for one bounded spill
// account. Spill bytes are reported separately from managed RAM accounting.
type SpillSnapshot struct {
	Used   uint64
	Peak   uint64
	Denied uint64
}

// StateProvider supplies immutable snapshots to the collector. Callbacks are
// invoked during a scrape and must use only in-memory accounting APIs.
type StateProvider struct {
	Execution    func() ExecutionSnapshot
	Flat         func() FlatSnapshot
	Spill        func() SpillSnapshot
	SpillAccount string
}

type stateProvider struct {
	StateProvider
}

var nodeStateProvider atomic.Pointer[stateProvider]

// SetStateProvider publishes the node's bounded-state metrics source. The
// returned function unregisters exactly this provider and is safe to call more
// than once or after another provider has been installed.
func SetStateProvider(provider StateProvider) func() {
	holder := &stateProvider{StateProvider: provider}
	nodeStateProvider.Store(holder)
	return func() { nodeStateProvider.CompareAndSwap(holder, nil) }
}

type stateCollector struct {
	rootBorrowers *prometheus.Desc
	buildTime     *prometheus.Desc
	buildNodes    *prometheus.Desc
	buildErrors   *prometheus.Desc
	frontier      *prometheus.Desc
	flatHits      *prometheus.Desc
	rootFallbacks *prometheus.Desc
	outboxCount   *prometheus.Desc
	outboxBytes   *prometheus.Desc
	outboxAge     *prometheus.Desc
	advancing     *prometheus.Desc
	flatFailures  *prometheus.Desc
	spillUsed     *prometheus.Desc
	spillPeak     *prometheus.Desc
	spillDenied   *prometheus.Desc
}

// NewStateCollector returns a callback-based collector for bounded execution,
// flat-state and spill-account metrics. It emits no samples until a provider
// is installed, preserving disabled execution paths.
func NewStateCollector() prometheus.Collector {
	return &stateCollector{
		rootBorrowers: prometheus.NewDesc(
			"goxrpl_execution_root_borrowers",
			"Currently active bounded execution root borrowers.", nil, nil),
		buildTime: prometheus.NewDesc(
			"goxrpl_execution_build_time_seconds_total",
			"Cumulative bounded execution commitment time in seconds.", nil, nil),
		buildNodes: prometheus.NewDesc(
			"goxrpl_execution_build_nodes_total",
			"Cumulative canonical nodes emitted by bounded execution commitment.", nil, nil),
		buildErrors: prometheus.NewDesc(
			"goxrpl_execution_build_errors_total",
			"Cumulative bounded execution commitment failures.", nil, nil),
		frontier: prometheus.NewDesc(
			"goxrpl_execution_frontier",
			"Largest bounded execution commitment frontier observed.", nil, nil),
		flatHits: prometheus.NewDesc(
			"goxrpl_execution_flat_hits_total",
			"Reads served by the matching flat-state base.", nil, nil),
		rootFallbacks: prometheus.NewDesc(
			"goxrpl_execution_root_fallbacks_total",
			"Reads served by authenticated root traversal after flat-state fallback.", nil, nil),
		outboxCount: prometheus.NewDesc(
			"goxrpl_flat_outbox_count",
			"Cached number of pending flat-state publication obligations.", nil, nil),
		outboxBytes: prometheus.NewDesc(
			"goxrpl_flat_outbox_bytes",
			"Cached bytes retained by pending flat-state publication obligations.", nil, nil),
		outboxAge: prometheus.NewDesc(
			"goxrpl_flat_outbox_age_seconds",
			"Age of the oldest pending flat-state publication obligation.", nil, nil),
		advancing: prometheus.NewDesc(
			"goxrpl_flat_advancing",
			"Whether flat-state advancement or recovery is in progress.", nil, nil),
		flatFailures: prometheus.NewDesc(
			"goxrpl_flat_failures_total",
			"Flat-state transition failures observed by this process.", nil, nil),
		spillUsed: prometheus.NewDesc(
			"goxrpl_state_spill_disk_used_bytes",
			"Bytes currently charged to the bounded state spill account.", []string{"account"}, nil),
		spillPeak: prometheus.NewDesc(
			"goxrpl_state_spill_disk_peak_bytes",
			"Peak bytes charged to the bounded state spill account.", []string{"account"}, nil),
		spillDenied: prometheus.NewDesc(
			"goxrpl_state_spill_disk_denied_total",
			"Spill reservations denied by the bounded state disk account.", []string{"account"}, nil),
	}
}

func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.rootBorrowers
	ch <- c.buildTime
	ch <- c.buildNodes
	ch <- c.buildErrors
	ch <- c.frontier
	ch <- c.flatHits
	ch <- c.rootFallbacks
	ch <- c.outboxCount
	ch <- c.outboxBytes
	ch <- c.outboxAge
	ch <- c.advancing
	ch <- c.flatFailures
	ch <- c.spillUsed
	ch <- c.spillPeak
	ch <- c.spillDenied
}

func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	holder := nodeStateProvider.Load()
	if holder == nil {
		return
	}
	provider := holder.StateProvider
	if provider.Execution != nil {
		execution := provider.Execution()
		ch <- prometheus.MustNewConstMetric(c.rootBorrowers, prometheus.GaugeValue, float64(execution.RootBorrowers))
		ch <- prometheus.MustNewConstMetric(c.buildTime, prometheus.CounterValue, float64(execution.BuildTimeNanos)/float64(time.Second))
		ch <- prometheus.MustNewConstMetric(c.buildNodes, prometheus.CounterValue, float64(execution.BuildNodes))
		ch <- prometheus.MustNewConstMetric(c.buildErrors, prometheus.CounterValue, float64(execution.BuildErrors))
		ch <- prometheus.MustNewConstMetric(c.frontier, prometheus.GaugeValue, float64(execution.Frontier))
		ch <- prometheus.MustNewConstMetric(c.flatHits, prometheus.CounterValue, float64(execution.FlatHits))
		ch <- prometheus.MustNewConstMetric(c.rootFallbacks, prometheus.CounterValue, float64(execution.RootFallbacks))
	}
	if provider.Flat != nil {
		flat := provider.Flat()
		age := flat.OutboxAge
		if age < 0 {
			age = 0
		}
		advancing := 0.0
		if flat.Advancing {
			advancing = 1
		}
		ch <- prometheus.MustNewConstMetric(c.outboxCount, prometheus.GaugeValue, float64(flat.OutboxCount))
		ch <- prometheus.MustNewConstMetric(c.outboxBytes, prometheus.GaugeValue, float64(flat.OutboxBytes))
		ch <- prometheus.MustNewConstMetric(c.outboxAge, prometheus.GaugeValue, age.Seconds())
		ch <- prometheus.MustNewConstMetric(c.advancing, prometheus.GaugeValue, advancing)
		ch <- prometheus.MustNewConstMetric(c.flatFailures, prometheus.CounterValue, float64(flat.Failures))
	}
	if provider.Spill != nil {
		spill := provider.Spill()
		account := provider.SpillAccount
		if account == "" {
			account = "state_scratch"
		}
		ch <- prometheus.MustNewConstMetric(c.spillUsed, prometheus.GaugeValue, float64(spill.Used), account)
		ch <- prometheus.MustNewConstMetric(c.spillPeak, prometheus.GaugeValue, float64(spill.Peak), account)
		ch <- prometheus.MustNewConstMetric(c.spillDenied, prometheus.CounterValue, float64(spill.Denied), account)
	}
}
