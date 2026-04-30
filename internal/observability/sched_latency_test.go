package observability

import (
	"context"
	"math"
	"runtime/metrics"
	"testing"
	"time"
)

func TestSchedLatencyMs_ZeroBeforeFirstSample(t *testing.T) {
	resetForTest()
	if got := SchedLatencyMs(); got != 0 {
		t.Errorf("expected 0 before first sample, got %d", got)
	}
}

func TestSchedLatencyMs_NonNegative(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)

	deadline := time.Now().Add(2 * SamplerInterval)
	for time.Now().Before(deadline) {
		if got := SchedLatencyMs(); got >= 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := SchedLatencyMs(); got < 0 {
		t.Errorf("SchedLatencyMs() = %d, want >= 0", got)
	}
}

func TestStartSchedLatencySampler_IdempotentStart(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)
	StartSchedLatencySampler(ctx) // second call must be a no-op
	StartSchedLatencySampler(ctx)
}

func TestStartSchedLatencySampler_StopsOnCancel(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())

	StartSchedLatencySampler(ctx)
	time.Sleep(2 * SamplerInterval)
	cancel()
	// No assertion beyond "doesn't deadlock or panic" — the goroutine
	// is independent and observably stops by ctx.Done().
}

// Histogram bucket boundaries (seconds): [0, 1ms, 10ms, 100ms, 1s, +Inf]
var testBuckets = []float64{0, 0.001, 0.01, 0.1, 1.0, math.Inf(1)}

func histogram(counts []uint64) *metrics.Float64Histogram {
	return &metrics.Float64Histogram{
		Counts:  counts,
		Buckets: testBuckets,
	}
}

func TestMaxIntervalNs_NoNewSamples(t *testing.T) {
	h := histogram([]uint64{0, 100, 0, 0, 0})
	prev := []uint64{0, 100, 0, 0, 0}
	if got := maxIntervalNs(h, prev); got != 0 {
		t.Errorf("expected 0 when no new samples, got %d", got)
	}
}

func TestMaxIntervalNs_PicksHighestBucketWithDiff(t *testing.T) {
	// 100 new samples in the 1-10ms bucket → upper bound = 10ms.
	h := histogram([]uint64{0, 100, 0, 0, 0})
	prev := []uint64{0, 0, 0, 0, 0}
	want := int64(10 * time.Millisecond)
	if got := maxIntervalNs(h, prev); got != want {
		t.Errorf("expected %d ns (10ms), got %d", want, got)
	}
}

func TestMaxIntervalNs_DiffIgnoresOlderBuckets(t *testing.T) {
	// New samples landed in 10-100ms (5 new) and 100ms-1s (1 new).
	// Highest with diff is 100ms-1s → upper bound = 1s.
	h := histogram([]uint64{0, 100, 105, 1, 0})
	prev := []uint64{0, 100, 100, 0, 0}
	want := int64(1 * time.Second)
	if got := maxIntervalNs(h, prev); got != want {
		t.Errorf("expected %d ns (1s), got %d", want, got)
	}
}

func TestMaxIntervalNs_InfBucketClampsToLowerBound(t *testing.T) {
	// 50 new samples in the +Inf bucket → upper bound is +Inf, so
	// fall back to the lower bound = 1s.
	h := histogram([]uint64{0, 0, 0, 0, 50})
	prev := []uint64{0, 0, 0, 0, 0}
	want := int64(1 * time.Second)
	if got := maxIntervalNs(h, prev); got != want {
		t.Errorf("expected %d ns (1s, clamped from +Inf), got %d", want, got)
	}
}

func TestMaxIntervalNs_NilHistogram(t *testing.T) {
	if got := maxIntervalNs(nil, nil); got != 0 {
		t.Errorf("expected 0 for nil histogram, got %d", got)
	}
}

func TestMaxIntervalNs_PrevShorterThanCurrent(t *testing.T) {
	// prev was empty (e.g. process startup) — every bucket counts
	// as new. Highest non-empty is index 1 → upper = 10ms.
	h := histogram([]uint64{0, 5, 0, 0, 0})
	prev := []uint64{}
	want := int64(10 * time.Millisecond)
	if got := maxIntervalNs(h, prev); got != want {
		t.Errorf("expected %d ns, got %d", want, got)
	}
}

func TestSchedLatencyMs_CeilSemantics(t *testing.T) {
	tests := []struct {
		ns   int64
		want int
	}{
		{0, 0},
		{500_000, 1},               // 500us → ceil = 1ms
		{1_000_000, 1},             // exactly 1ms
		{1_100_000, 2},             // 1.1ms → ceil = 2ms
		{int64(time.Second), 1000}, // 1s → 1000ms
	}
	for _, tt := range tests {
		publishedNs.Store(tt.ns)
		if got := SchedLatencyMs(); got != tt.want {
			t.Errorf("ns=%d: SchedLatencyMs()=%d want %d", tt.ns, got, tt.want)
		}
	}
}
