package cli

import (
	"net/http"
	"time"

	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const metricsNamespace = "goxrpl"

// newMetricsRegistry builds a Prometheus registry holding the standard Go
// runtime and process collectors plus goXRPL node-level metrics. A
// dedicated registry (rather than prometheus.DefaultRegisterer) keeps
// registration self-contained, so the metrics server can be stood up more
// than once without colliding on the package-global default registry.
func newMetricsRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   metricsNamespace,
		Name:        "build_info",
		Help:        "Constant 1, labelled with the running goXRPLd build version.",
		ConstLabels: prometheus.Labels{"version": version.Version},
	})
	buildInfo.Set(1)

	schedLatency := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "sched_latency_milliseconds",
		Help:      "Most recent goroutine-scheduler latency sample in milliseconds (rippled io_latency_probe analog).",
	}, func() float64 {
		return float64(observability.SchedLatencyMs())
	})

	ioLatencyEvents := prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "io_latency_events_total",
		Help:      "Scheduler-latency samples that crossed the 10ms reporting threshold.",
	}, func() float64 {
		return float64(observability.IOLatencyEventStats().Count)
	})

	reg.MustRegister(buildInfo, schedLatency, ioLatencyEvents)
	return reg
}

// startMetricsServer serves Prometheus metrics at /metrics on addr. It
// mirrors startPProfServer: a standalone auxiliary HTTP server enabled
// out-of-band (via the GOXRPL_METRICS env var) and never mounted on the
// public JSON-RPC ports, keeping scrape traffic and internal telemetry off
// the client-facing API surface.
func startMetricsServer(addr string) error {
	reg := newMetricsRegistry()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}
