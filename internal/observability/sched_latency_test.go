package observability

import (
	"context"
	"testing"
	"time"
)

func TestSchedLatencyMs_ZeroBeforeFirstSample(t *testing.T) {
	resetForTest()
	if got := SchedLatencyMs(); got != 0 {
		t.Errorf("expected 0 before first sample, got %d", got)
	}
}

func TestSchedLatencyMs_HealthyServerReportsNearZero(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)

	// Wait for at least one sample tick + the spawned goroutine to
	// land. On any non-pathological CI runner the elapsed should be
	// well under 100ms.
	deadline := time.Now().Add(2 * SamplerInterval)
	for time.Now().Before(deadline) {
		if publishedNs.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := SchedLatencyMs()
	if got < 0 {
		t.Errorf("SchedLatencyMs() = %d, want >= 0", got)
	}
	// Healthy single-test runner should NEVER report >100ms — that
	// would mean the sampler waited longer than the sample interval
	// for a P, which on a non-saturated host doesn't happen.
	if got > 100 {
		t.Errorf("SchedLatencyMs() = %d, want <= 100 on a healthy runner", got)
	}
}

func TestPostSample_PublishesElapsed(t *testing.T) {
	resetForTest()
	postSample()

	// Spawned goroutine writes asynchronously. Poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if publishedNs.Load() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := publishedNs.Load(); got <= 0 {
		t.Errorf("publishedNs = %d, want > 0 after postSample", got)
	}
}

func TestPostSample_OverwritesPreviousValue(t *testing.T) {
	resetForTest()
	publishedNs.Store(int64(50 * time.Millisecond))

	postSample()
	deadline := time.Now().Add(500 * time.Millisecond)
	var got int64
	for time.Now().Before(deadline) {
		got = publishedNs.Load()
		if got != int64(50*time.Millisecond) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got == int64(50*time.Millisecond) {
		t.Error("publishedNs was not overwritten by postSample")
	}
	if got <= 0 {
		t.Errorf("publishedNs = %d, want > 0 after overwrite", got)
	}
}

func TestStartSchedLatencySampler_IdempotentStart(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)
	StartSchedLatencySampler(ctx)
	StartSchedLatencySampler(ctx)
	// Survival is the assertion; double-start would race the publish
	// loop and the test runner would flag the race detector.
}

func TestStartSchedLatencySampler_StopsOnCancel(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())

	StartSchedLatencySampler(ctx)
	time.Sleep(2 * SamplerInterval)
	cancel()
	// Goroutine exits on ctx.Done(); this test asserts no deadlock /
	// no panic on shutdown.
}

func TestSchedLatencyMs_CeilSemantics(t *testing.T) {
	tests := []struct {
		ns   int64
		want int
	}{
		{0, 0},
		{500_000, 1},               // 500us → ceil to 1ms
		{1_000_000, 1},             // exactly 1ms
		{1_100_000, 2},             // 1.1ms → ceil to 2ms
		{int64(time.Second), 1000}, // 1s → 1000ms
	}
	for _, tt := range tests {
		publishedNs.Store(tt.ns)
		if got := SchedLatencyMs(); got != tt.want {
			t.Errorf("ns=%d: SchedLatencyMs()=%d want %d", tt.ns, got, tt.want)
		}
	}
}
