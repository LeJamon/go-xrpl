package nodestore

import (
	"testing"
	"time"
)

func TestIOLatencyTracker_ZeroInitial(t *testing.T) {
	tracker := NewIOLatencyTracker(0.1)
	if got := tracker.Latency(); got != 0 {
		t.Errorf("expected 0 latency before any samples, got %v", got)
	}
	if got := tracker.LatencyMs(); got != 0 {
		t.Errorf("expected 0 ms before any samples, got %d", got)
	}
}

func TestIOLatencyTracker_SingleSample(t *testing.T) {
	tracker := NewIOLatencyTracker(0.5)
	tracker.Record(10 * time.Millisecond)
	if got := tracker.Latency(); got != 10*time.Millisecond {
		t.Errorf("expected 10ms after first sample, got %v", got)
	}
	if got := tracker.LatencyMs(); got != 10 {
		t.Errorf("expected 10 ms, got %d", got)
	}
}

func TestIOLatencyTracker_EWMA(t *testing.T) {
	tracker := NewIOLatencyTracker(0.5) // alpha = 0.5 for easy math
	tracker.Record(10 * time.Millisecond)
	tracker.Record(20 * time.Millisecond)

	// After second sample: 0.5 * 20ms + 0.5 * 10ms = 15ms
	got := tracker.Latency()
	expected := 15 * time.Millisecond
	if got != expected {
		t.Errorf("expected %v after EWMA, got %v", expected, got)
	}
}

func TestIOLatencyTracker_ConvergesToRecent(t *testing.T) {
	tracker := NewIOLatencyTracker(0.5)
	// Seed with 100ms
	tracker.Record(100 * time.Millisecond)
	// Send many 1ms samples — should converge towards 1ms
	for i := 0; i < 50; i++ {
		tracker.Record(1 * time.Millisecond)
	}
	got := tracker.LatencyMs()
	if got > 1 {
		t.Errorf("expected convergence to ~1ms, got %d ms", got)
	}
}

func TestIOLatencyTracker_InvalidAlpha(t *testing.T) {
	// Alpha out of range should default to 0.1
	tracker := NewIOLatencyTracker(0)
	tracker.Record(10 * time.Millisecond)
	if got := tracker.LatencyMs(); got != 10 {
		t.Errorf("expected 10 ms, got %d", got)
	}

	tracker2 := NewIOLatencyTracker(2.0)
	tracker2.Record(10 * time.Millisecond)
	if got := tracker2.LatencyMs(); got != 10 {
		t.Errorf("expected 10 ms, got %d", got)
	}
}
