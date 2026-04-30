// Package observability hosts process-level metrics surfaced to RPC.
//
// The Go analog of rippled's beast::io_latency_probe lives here: a
// ~100ms-cadence sampler that posts one goroutine, measures how long
// the runtime takes to schedule it onto a P, and atomically stores
// that elapsed time. The RPC server_info handler reads it via
// SchedLatencyMs.
//
// Rippled reference: rippled/include/xrpl/beast/asio/io_latency_probe.h
// and rippled/src/xrpld/app/main/Application.cpp:98-160. Rippled
// posts a sample task to its io_service (a shared pool of N=6 worker
// threads) and measures the queue-wait before any worker dispatches
// it. The Go analog is a `go func()` against the runtime's runqueue,
// which is dispatched by GOMAXPROCS Ps — same shape, different pool.
//
// Storage and read semantics match rippled exactly: a single atomic
// last-write-wins value (rippled stores std::atomic<milliseconds>;
// we store atomic.Int64 nanoseconds and ceil at read time). Each new
// sample overwrites the prior one, so the value is volatile and
// self-resetting — a stale spike is overwritten by the next healthy
// sample within ~100ms.
package observability

import (
	"context"
	"math"
	"sync/atomic"
	"time"
)

// SamplerInterval matches rippled's 100ms io_latency_probe period
// (Application.cpp:473).
const SamplerInterval = 100 * time.Millisecond

// publishedNs holds the most recent sample's elapsed time in
// nanoseconds. Zero before the first sample. Last-write-wins —
// mirrors rippled's lastSample_ atomic (Application.cpp:104,132).
var publishedNs atomic.Int64

// startedFlag guards against double-start; a process only needs one
// sampler, regardless of how many init paths call Start.
var startedFlag atomic.Bool

// StartSchedLatencySampler launches the sampler goroutine. The
// goroutine exits when ctx is cancelled. Subsequent calls are no-ops.
func StartSchedLatencySampler(ctx context.Context) {
	if !startedFlag.CompareAndSwap(false, true) {
		return
	}
	go runSampler(ctx)
}

func runSampler(ctx context.Context) {
	ticker := time.NewTicker(SamplerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			postSample()
		}
	}
}

// postSample is the Go analog of rippled's m_ios.post(sample_op).
// `posted` is captured before the `go` statement, mirroring
// sample_op's `m_start = Clock::now()` at construction time
// (io_latency_probe.h:113). The spawned goroutine measures
// time.Since(posted) on its first instruction — that's the time it
// spent waiting in the runqueue for a P to dispatch it, exactly the
// same quantity rippled measures inside sample_op::operator()
// (io_latency_probe.h:215-218).
func postSample() {
	posted := time.Now()
	go func() {
		elapsed := time.Since(posted)
		publishedNs.Store(int64(elapsed))
	}()
}

// SchedLatencyMs returns the most recent sample in milliseconds
// (ceil), matching rippled's getIOLatency() / lastSample_.load() +
// ceil<milliseconds> shape (Application.cpp:130,143-147). Returns 0
// before the first sample.
func SchedLatencyMs() int {
	ns := publishedNs.Load()
	if ns <= 0 {
		return 0
	}
	return int(math.Ceil(float64(ns) / float64(time.Millisecond)))
}

// resetForTest clears the published value and the started flag so
// repeated Start calls in unit tests don't no-op. Test-only.
func resetForTest() {
	publishedNs.Store(0)
	startedFlag.Store(false)
}
