package observability

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type TransactionIngressMetricsSnapshot struct {
	Frames, ParseWork, ParseErrors                   uint64
	InputHits, InputMisses, InputBypass, Deduped     uint64
	ParseWorkUs, QueueLatencyUs, QueueLatencySamples uint64
	MemoEvictions, MemoEntries, MemoPayloadBytes     uint64
	PrivateAttempts, StaleRetries, Reparses          uint64
	PrivateAttemptDuration, StaleRetryDuration       time.Duration
	ReparseDuration                                  time.Duration
}

type transactionIngressMetricsSource struct {
	snapshot func() TransactionIngressMetricsSnapshot
}

var observedTransactionIngressMetrics atomic.Pointer[transactionIngressMetricsSource]

func SetTransactionIngressMetrics(snapshot func() TransactionIngressMetricsSnapshot) func() {
	if snapshot == nil {
		return func() {}
	}
	source := &transactionIngressMetricsSource{snapshot: snapshot}
	observedTransactionIngressMetrics.Store(source)
	return func() { observedTransactionIngressMetrics.CompareAndSwap(source, nil) }
}

type transactionIngressMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(TransactionIngressMetricsSnapshot) float64
}

type transactionIngressCollector struct {
	metrics   []transactionIngressMetric
	inputs    *prometheus.Desc
	queueWait *prometheus.Desc
}

func NewTransactionIngressCollector() prometheus.Collector {
	c := &transactionIngressCollector{
		inputs:    prometheus.NewDesc("goxrpl_transaction_ingress_inputs_total", "Exact-input memo lookups; misses and bypasses are not counts of globally unique transactions.", []string{"result"}, nil),
		queueWait: prometheus.NewDesc("goxrpl_transaction_ingress_queue_wait_seconds", "Time from inbound receipt to transaction worker execution.", nil, nil),
	}
	for _, metric := range []struct {
		name, help string
		kind       prometheus.ValueType
		value      func(TransactionIngressMetricsSnapshot) float64
	}{
		{"frames_total", "Incoming transaction frames handled by transaction workers.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.Frames) }},
		{"parse_total", "Full transaction parses in router ingress, including admitted memo hits.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.ParseWork) }},
		{"parse_errors_total", "Failed full transaction parses in router ingress.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.ParseErrors) }},
		{"parse_seconds_total", "Wall time spent parsing transactions in router ingress.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.ParseWorkUs) / 1e6 }},
		{"suppressed_total", "Transactions suppressed by canonical identity and signature context.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.Deduped) }},
		{"memo_evictions_total", "Exact-input memo entries removed after expiry, capacity pressure, parse failure, or shutdown.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.MemoEvictions) }},
		{"memo_entries", "Exact-input memo entries, including in-flight parses.", prometheus.GaugeValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.MemoEntries) }},
		{"memo_payload_bytes", "Raw payload bytes retained by the exact-input memo, including in-flight entries; bookkeeping is separately bounded by the entry cap.", prometheus.GaugeValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.MemoPayloadBytes) }},
		{"private_attempts_total", "Private open-ledger submission attempts.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.PrivateAttempts) }},
		{"private_attempt_seconds_total", "Wall time spent executing private open-ledger submission attempts.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return s.PrivateAttemptDuration.Seconds() }},
		{"stale_retries_total", "Private open-ledger attempts retried after their snapshot became stale.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.StaleRetries) }},
		{"stale_retry_seconds_total", "Wall time spent in private attempts discarded after a stale snapshot.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return s.StaleRetryDuration.Seconds() }},
		{"private_reparses_total", "Transaction reparses for private submission ownership or stale retries; excludes TxQ canonicalization.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return float64(s.Reparses) }},
		{"private_reparse_seconds_total", "Wall time spent reparsing for private submission ownership or stale retries.", prometheus.CounterValue, func(s TransactionIngressMetricsSnapshot) float64 { return s.ReparseDuration.Seconds() }},
	} {
		c.metrics = append(c.metrics, transactionIngressMetric{
			desc: prometheus.NewDesc("goxrpl_transaction_ingress_"+metric.name, metric.help, nil, nil),
			kind: metric.kind, value: metric.value,
		})
	}
	return c
}

func (c *transactionIngressCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, metric := range c.metrics {
		ch <- metric.desc
	}
	ch <- c.inputs
	ch <- c.queueWait
}

func (c *transactionIngressCollector) Collect(ch chan<- prometheus.Metric) {
	source := observedTransactionIngressMetrics.Load()
	if source == nil {
		return
	}
	s := source.snapshot()
	for _, metric := range c.metrics {
		ch <- prometheus.MustNewConstMetric(metric.desc, metric.kind, metric.value(s))
	}
	for _, input := range []struct {
		name  string
		count uint64
	}{{"hit", s.InputHits}, {"miss", s.InputMisses}, {"bypass", s.InputBypass}} {
		ch <- prometheus.MustNewConstMetric(c.inputs, prometheus.CounterValue, float64(input.count), input.name)
	}
	ch <- prometheus.MustNewConstSummary(c.queueWait, s.QueueLatencySamples, float64(s.QueueLatencyUs)/1e6, nil)
}
