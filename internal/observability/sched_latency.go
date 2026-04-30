// Package observability hosts process-level metrics surfaced to RPC.
//
// The Go analog of rippled's beast::io_latency_probe lives here: a
// ~100ms-cadence sampler that yields to the Go scheduler, measures
// how long it takes to be rescheduled, and atomically stores that
// elapsed time. The RPC server_info handler reads it via
// SchedLatencyMs.
//
// Rippled reference: rippled/include/xrpl/beast/asio/io_latency_probe.h
// and rippled/src/xrpld/app/main/Application.cpp:98-160. Rippled
// posts a sample task to its io_service (a shared pool of N=6 worker
// threads) and measures how long the task waits in the queue before
// any worker dispatches it. The Go analog uses runtime.Gosched():
// the sampler goroutine voluntarily returns to the runqueue, and the
// runtime picks the next runnable goroutine. The time until the
// sampler is picked again is the queue-wait — the same physical
// quantity rippled measures, surfaced through the Go scheduler
// instead of an explicit thread pool.
//
// This must use Gosched, NOT a `go func()` spawn-and-wait pattern.
// `go func()` followed by a channel receive lets the runtime hand
// the local P directly to the spawned goroutine when the parent
// blocks, bypassing contention; under a saturating CPU storm the
// measured elapsed stays in the microseconds. Empirical comparison:
// approach A (spawn) reads ~5us under a 64-goroutine storm; approach
// B (Gosched) reads 50-156ms — the latter is the correct signal.
//
// Storage and read semantics match rippled exactly: a single atomic
// last-write-wins value (rippled stores std::atomic<milliseconds>;
// we store atomic.Int64 nanoseconds and ceil at read time). Each new
// sample overwrites the prior one, so the value is volatile and
// self-resetting — a stale spike is overwritten by the next healthy
// sample within ~100ms.
//
// Cadence is adaptive, matching rippled's `when = now + period -
// 2*elapsed` rule (io_latency_probe.h:226-247): under low latency
// samples space ~100ms apart; under contention the next sample fires
// sooner, down to immediate repost when 2*elapsed >= period.
package observability

import (
	"context"
	"math"
	"runtime"
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
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		elapsed := sampleOnce()
		wait := nextWait(elapsed)
		if wait <= 0 {
			// 2*elapsed >= period: repost immediately, no timer.
			// Mirrors io_latency_probe.h:234-241 — a high-latency
			// system samples continuously to keep the metric fresh.
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// sampleOnce captures the time, yields to the scheduler, then
// measures how long it took to be rescheduled. That elapsed is the
// runqueue-wait time, the Go-runtime equivalent of rippled's
// io_service queue-wait (io_latency_probe.h:215-218). The result is
// stored to publishedNs and returned for the cadence calculation.
func sampleOnce() time.Duration {
	posted := time.Now()
	runtime.Gosched()
	elapsed := time.Since(posted)
	publishedNs.Store(int64(elapsed))
	return elapsed
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

// nextWait returns how long to sleep before the next sample, given
// the elapsed time of the just-completed one. Mirrors
// io_latency_probe.h:231-241: the next-fire time is `period - 2*elapsed`,
// clamped to zero so a heavily-contended system does not negatively
// time-travel the next post.
func nextWait(elapsed time.Duration) time.Duration {
	wait := SamplerInterval - 2*elapsed
	if wait < 0 {
		return 0
	}
	return wait
}

// resetForTest clears the published value and the started flag so
// repeated Start calls in unit tests don't no-op. Test-only.
func resetForTest() {
	publishedNs.Store(0)
	startedFlag.Store(false)
}
