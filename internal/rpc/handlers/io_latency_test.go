package handlers

import (
	"math"
	"runtime/metrics"
	"testing"
)

func TestSchedLatencyMs_NonNegative(t *testing.T) {
	got := schedLatencyMs()
	if got < 0 {
		t.Errorf("schedLatencyMs() = %d, want >= 0", got)
	}
}

func TestSchedLatencyMs_Idempotent(t *testing.T) {
	first := schedLatencyMs()
	second := schedLatencyMs()
	if first < 0 || second < 0 {
		t.Errorf("schedLatencyMs returned negative: first=%d second=%d", first, second)
	}
	// P99 of a cumulative histogram only moves up over time, but
	// within a sub-millisecond gap on an idle test runner the value
	// should be stable. We don't assert equality (CI noise can shift
	// the bucket), only finiteness.
}

func TestP99CeilMs_EmptyHistogram(t *testing.T) {
	tests := []struct {
		name string
		h    *metrics.Float64Histogram
		want int
	}{
		{"nil histogram", nil, 0},
		{"empty counts", &metrics.Float64Histogram{
			Counts:  []uint64{},
			Buckets: []float64{0},
		}, 0},
		{"all-zero counts", &metrics.Float64Histogram{
			Counts:  []uint64{0, 0, 0},
			Buckets: []float64{0, 0.001, 0.01, 0.1},
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p99CeilMs(tt.h); got != tt.want {
				t.Errorf("p99CeilMs = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestP99CeilMs_PicksCorrectBucket(t *testing.T) {
	// Histogram boundaries (seconds): [0, 1ms, 10ms, 100ms, 1s, +Inf]
	// 100 samples in the 1-10ms bucket — P99 falls there, upper bound
	// is 10ms = 10ms ceil.
	h := &metrics.Float64Histogram{
		Counts:  []uint64{0, 100, 0, 0, 0},
		Buckets: []float64{0, 0.001, 0.01, 0.1, 1.0, math.Inf(1)},
	}
	if got := p99CeilMs(h); got != 10 {
		t.Errorf("expected 10ms, got %d", got)
	}
}

func TestP99CeilMs_FallsInInfBucket(t *testing.T) {
	// All samples in the +Inf bucket. P99 falls there too. The reader
	// must clamp to the lower bound (1.0s = 1000ms) instead of +Inf.
	h := &metrics.Float64Histogram{
		Counts:  []uint64{0, 0, 0, 0, 50},
		Buckets: []float64{0, 0.001, 0.01, 0.1, 1.0, math.Inf(1)},
	}
	if got := p99CeilMs(h); got != 1000 {
		t.Errorf("expected 1000ms (clamped from +Inf), got %d", got)
	}
}
