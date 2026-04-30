package handlers

import (
	"math"
	"runtime/metrics"
	"sync"
)

const schedLatencyMetric = "/sched/latencies:seconds"

var (
	schedLatencyOnce   sync.Once
	schedLatencySample = []metrics.Sample{{Name: schedLatencyMetric}}
	schedLatencyMu     sync.Mutex
)

// schedLatencyMs returns the P99 of the Go runtime's goroutine
// scheduling-latency histogram in milliseconds (ceil). It is the
// goXRPL analog of rippled's beast::io_latency_probe metric — both
// report how long queued work waits before being dispatched. See
// rippled/include/xrpl/beast/asio/io_latency_probe.h:35 and
// rippled/src/xrpld/app/main/Application.cpp:127-141 for the
// rippled reference; see runtime/metrics for the Go-side source.
//
// The histogram is cumulative since process start, so the reported
// P99 reflects the worst sustained scheduling pressure over the
// process lifetime; it does not decrease without a restart.
func schedLatencyMs() int {
	schedLatencyMu.Lock()
	defer schedLatencyMu.Unlock()

	schedLatencyOnce.Do(func() {
		// Trigger one read so a missing metric is detected once
		// rather than on every RPC call. metrics.Read silently
		// sets Kind to KindBad when the metric is unknown.
		metrics.Read(schedLatencySample)
	})

	metrics.Read(schedLatencySample)
	if schedLatencySample[0].Value.Kind() != metrics.KindFloat64Histogram {
		return 0
	}
	h := schedLatencySample[0].Value.Float64Histogram()
	return p99CeilMs(h)
}

// p99CeilMs returns ceil-ms of the 99th percentile bucket upper
// bound of the histogram, or 0 if the histogram is empty. When the
// 99th percentile falls in the final +Inf bucket, the lower bound
// of that bucket is returned instead so the value remains finite.
func p99CeilMs(h *metrics.Float64Histogram) int {
	if h == nil || len(h.Counts) == 0 {
		return 0
	}
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	threshold := uint64(math.Ceil(float64(total) * 0.99))
	var cum uint64
	for i, c := range h.Counts {
		cum += c
		if cum >= threshold {
			upper := h.Buckets[i+1]
			if math.IsInf(upper, 1) {
				upper = h.Buckets[i]
			}
			return secondsCeilMs(upper)
		}
	}
	return 0
}

func secondsCeilMs(seconds float64) int {
	if !(seconds > 0) {
		return 0
	}
	return int(math.Ceil(seconds * 1000))
}
