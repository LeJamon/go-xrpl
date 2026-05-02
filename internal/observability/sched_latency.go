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
// Caveat: the underlying physical quantity is not strictly
// equivalent. Rippled's probe measures wait time on a fixed pool of
// io_service worker threads doing peer I/O / consensus work; the Go
// analog measures the entire process's goroutine scheduler. In a
// goXRPL where most work happens on goroutines without an explicit
// "IO worker" pool, the Go signal is a superset that conflates IO
// scheduling with general goroutine scheduling. The JSON field name
// io_latency_ms is preserved for wire-format compatibility with
// rippled clients.
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
//
// Per-sample emission matches rippled's Application.cpp:130-140
// flow exactly: ceil the elapsed to ms, store as the published
// value, notify the metrics Collector under the "ios_latency" event
// at >=10ms (Application.cpp:134-135), and emit slog.Warn at >=500ms
// (Application.cpp:136-140). The Collector abstraction is defined in
// collector.go; the default implementation aggregates count, sum,
// min, max, and last ms in atomic counters readable via
// IOLatencyEventStats. Production deployments can swap the default
// for a StatsD/Prometheus forwarder via SetCollector without
// touching this file.
package observability

import (
	"context"
	"log/slog"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// SamplerInterval matches rippled's 100ms io_latency_probe period
	// (Application.cpp:473).
	SamplerInterval = 100 * time.Millisecond

	// latencyWarnMs matches rippled's 500ms warn threshold
	// (Application.cpp:136). Compared against ceil-ms (not raw
	// elapsed) so the boundary lines up with rippled exactly: rippled
	// computes `lastSample = ceil<milliseconds>(elapsed)` and warns
	// when `lastSample >= 500ms`, so an elapsed of 499ms+1ns must
	// also warn (ceil = 500ms).
	latencyWarnMs = 500

	// latencyEventMs is the threshold at which rippled emits a
	// metrics event via beast::insight (Application.cpp:134-135).
	// Currently dormant — wired only as a TODO marker until goXRPL
	// has a metrics collector.
	latencyEventMs = 10
)

var (
	// publishedNs holds the most recent sample's elapsed time in
	// nanoseconds. Zero before the first sample. Last-write-wins —
	// mirrors rippled's lastSample_ atomic (Application.cpp:104,132).
	publishedNs atomic.Int64

	// samplesCount counts iterations of the sampler loop. Used by
	// tests to verify the sampler is actually looping at the
	// configured cadence (rippled's testSampleOngoing analog).
	samplesCount atomic.Int64

	samplerMu sync.Mutex
	// samplerDone is closed when the active sampler goroutine exits.
	// Nil before the first Start, and reset to nil by resetForTest
	// after the prior goroutine has exited.
	samplerDone chan struct{}
)

// StartSchedLatencySampler launches the sampler goroutine. The
// goroutine exits when ctx is cancelled. If a sampler is already
// running, this is a no-op. After the running sampler exits, a
// fresh call will start a new one.
func StartSchedLatencySampler(ctx context.Context) {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	if samplerDone != nil {
		select {
		case <-samplerDone:
			// Prior goroutine already exited; allow a fresh start.
		default:
			return
		}
	}
	logger := slog.Default().With("component", "io_latency")
	done := make(chan struct{})
	samplerDone = done
	go runSampler(ctx, logger, done)
}

func runSampler(ctx context.Context, logger *slog.Logger, done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(SamplerInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		elapsed := sampleOnce()
		ms := ceilMs(elapsed)
		// Order mirrors rippled Application.cpp:130-140: store the
		// ceil-ms value (publishedNs above), notify the metrics
		// collector at >=10ms, then warn at >=500ms. Both threshold
		// comparisons use the ceil-ms (not raw elapsed) so the
		// boundaries line up with rippled exactly.
		if ms >= latencyEventMs {
			DefaultCollector().NotifyEvent(IOSLatencyEventName, elapsed)
		}
		if ms >= latencyWarnMs {
			logger.Warn("io_service latency", "ms", ms)
		}
		wait := nextWait(elapsed)
		if wait <= 0 {
			// 2*elapsed >= period: repost immediately, no timer.
			// Mirrors io_latency_probe.h:234-241 — a high-latency
			// system samples continuously to keep the metric fresh.
			continue
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
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
	samplesCount.Add(1)
	return elapsed
}

// SchedLatencyMs returns the most recent sample in milliseconds
// (ceil), matching rippled's getIOLatency() / lastSample_.load() +
// ceil<milliseconds> shape (Application.cpp:130,143-147). Returns 0
// before the first sample. The return is always >= 0; the JSON
// emission in server_info maps directly to rippled's Json::UInt
// (NetworkOPs.cpp:2776-2777).
func SchedLatencyMs() int {
	return ceilMs(time.Duration(publishedNs.Load()))
}

// ceilMs returns ceil(d / 1ms) as an int, mirroring rippled's
// ceil<milliseconds>(elapsed) at Application.cpp:130. Used at every
// rippled-comparison site (the published ms value, the 500ms warn,
// and the dormant 10ms event hook) so the Go threshold semantics
// match rippled's exactly.
func ceilMs(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(float64(d.Nanoseconds()) / float64(time.Millisecond)))
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

// SetSampleForTest forces the published sample to ns. Test-only
// helper for cross-package tests (e.g., RPC handler tests) that
// need to assert wiring without running the live sampler.
func SetSampleForTest(ns int64) {
	publishedNs.Store(ns)
}

// resetForTest stops any running sampler, waits up to 2*SamplerInterval
// for it to exit, then clears the published value, sample count, and
// done channel so a fresh sampler can be started in the next test.
//
// Tests that start a sampler MUST cancel its context (via t.Cleanup
// or defer cancel()) before the test returns; resetForTest will then
// observe the closed done channel immediately.
func resetForTest() {
	samplerMu.Lock()
	done := samplerDone
	samplerMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * SamplerInterval):
			// The prior test leaked its sampler. We can't force-cancel
			// here, so accept the leak; the publishedNs atomic is
			// last-write-wins so callers should still see their own
			// stores within their own time slice.
		}
	}
	samplerMu.Lock()
	samplerDone = nil
	samplerMu.Unlock()
	publishedNs.Store(0)
	samplesCount.Store(0)
	// Replace the default collector with a fresh MemoryCollector so
	// tests get a clean baseline. Tests that installed a custom
	// collector via SetCollector should re-install it after calling
	// resetForTest.
	SetCollector(NewMemoryCollector())
}

// samplerDoneForTest returns the active sampler's done channel, or
// nil if no sampler is running. A test that calls cancel() on its
// context can then receive on this channel to deterministically wait
// for the goroutine to exit.
func samplerDoneForTest() <-chan struct{} {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	return samplerDone
}

// samplesCountForTest returns the number of iterations the sampler
// loop has completed since the last resetForTest. Used by cadence
// tests (rippled's testSampleOngoing analog).
func samplesCountForTest() int64 {
	return samplesCount.Load()
}
