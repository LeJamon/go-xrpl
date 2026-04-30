// Package observability hosts process-level metrics surfaced to RPC.
//
// The Go analog of rippled's beast::io_latency_probe lives here: a
// ~100ms-cadence sampler that snapshots the runtime's goroutine
// scheduling-latency histogram, computes the worst latency observed in
// the last interval, and publishes that value atomically. The RPC
// server_info handler reads it via SchedLatencyMs.
//
// Rippled reference: rippled/include/xrpl/beast/asio/io_latency_probe.h
// and rippled/src/xrpld/app/main/Application.cpp:98-160. Rippled stores
// the elapsed time of the most recent sample in a single atomic and
// overwrites on each new sample. This package mirrors those semantics
// against runtime/metrics' /sched/latencies:seconds histogram: each
// 100ms tick diffs the cumulative histogram, picks the upper bound of
// the highest non-empty bucket of the diff, and overwrites the
// published value. Reading is a single atomic load — like rippled's
// lastSample_.load() — and the value is volatile and self-resetting,
// not cumulative.
package observability

import (
	"context"
	"math"
	"runtime/metrics"
	"sync/atomic"
	"time"
)

// SamplerInterval matches rippled's 100ms io_latency_probe period
// (Application.cpp:473).
const SamplerInterval = 100 * time.Millisecond

const schedLatencyMetric = "/sched/latencies:seconds"

// publishedNs holds the worst scheduling latency observed in the last
// sampler interval, in nanoseconds. Zero before the first tick.
var publishedNs atomic.Int64

// StartSchedLatencySampler launches a single background goroutine that
// snapshots /sched/latencies:seconds every SamplerInterval, diffs
// against the previous snapshot, and publishes the upper bound of the
// highest non-empty bucket of the diff. The goroutine exits when ctx
// is cancelled. Safe to call multiple times — only the first call
// starts a goroutine; subsequent calls are no-ops.
func StartSchedLatencySampler(ctx context.Context) {
	if !startedFlag.CompareAndSwap(false, true) {
		return
	}
	go runSampler(ctx)
}

var startedFlag atomic.Bool

func runSampler(ctx context.Context) {
	sample := []metrics.Sample{{Name: schedLatencyMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindFloat64Histogram {
		return
	}

	// Seed previous with the histogram at startup so the first tick
	// reports latency since process start, not since "always".
	prev := copyCounts(sample[0].Value.Float64Histogram())

	ticker := time.NewTicker(SamplerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			metrics.Read(sample)
			h := sample[0].Value.Float64Histogram()
			if h == nil {
				continue
			}
			ns := maxIntervalNs(h, prev)
			publishedNs.Store(ns)
			prev = copyCounts(h)
		}
	}
}

// SchedLatencyMs returns the worst goroutine scheduling latency
// observed in the most recent sampler interval, in milliseconds (ceil).
// Returns 0 before the first sample tick or if the runtime metric is
// unavailable.
func SchedLatencyMs() int {
	ns := publishedNs.Load()
	if ns <= 0 {
		return 0
	}
	return int(math.Ceil(float64(ns) / float64(time.Millisecond)))
}

// maxIntervalNs returns the upper bound (in nanoseconds) of the highest
// histogram bucket that received samples in (prev, h]. When the highest
// bucket with new samples is the +Inf bucket, the lower bound is
// returned instead so the value remains finite.
func maxIntervalNs(h *metrics.Float64Histogram, prev []uint64) int64 {
	if h == nil || len(h.Counts) == 0 {
		return 0
	}
	for i := len(h.Counts) - 1; i >= 0; i-- {
		var prevCount uint64
		if i < len(prev) {
			prevCount = prev[i]
		}
		if h.Counts[i] > prevCount {
			upper := h.Buckets[i+1]
			if math.IsInf(upper, 1) {
				upper = h.Buckets[i]
			}
			if upper <= 0 {
				return 0
			}
			return int64(upper * float64(time.Second))
		}
	}
	return 0
}

func copyCounts(h *metrics.Float64Histogram) []uint64 {
	if h == nil {
		return nil
	}
	out := make([]uint64, len(h.Counts))
	copy(out, h.Counts)
	return out
}

// resetForTest is for unit tests only. It clears the published value
// and the started flag so repeated Start calls in tests don't no-op.
func resetForTest() {
	publishedNs.Store(0)
	startedFlag.Store(false)
}
