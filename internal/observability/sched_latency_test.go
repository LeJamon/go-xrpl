package observability

import (
	"context"
	"math"
	"runtime"
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
	if got > 100 {
		t.Errorf("SchedLatencyMs() = %d, want <= 100 on a healthy runner", got)
	}
}

// TestSchedLatencyMs_RisesUnderCPUContention verifies the metric
// actually catches load. Under a saturating CPU storm (8x GOMAXPROCS
// busy goroutines), the sampler's elapsed should jump above the idle
// baseline because runtime.Gosched lands the sampler back in a
// crowded runqueue. This is the load case the previous spawn-and-wait
// approach silently missed.
//
// The magnitude of the rise is non-deterministic — it depends on the
// Go runtime version, GOMAXPROCS, OS scheduler quantum, and (most
// notably) whether -race is enabled, which alters scheduler heuristics
// enough to make Gosched return without a measurable wait. Skip under
// -race; the property is covered by non-race CI runs.
func TestSchedLatencyMs_RisesUnderCPUContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CPU-contention test in short mode")
	}
	if raceEnabled {
		t.Skip("scheduler-latency magnitude is non-deterministic under -race; non-race CI covers this")
	}
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartSchedLatencySampler(ctx)

	stop := make(chan struct{})
	workers := 8 * runtime.GOMAXPROCS(0)
	for i := 0; i < workers; i++ {
		go func() {
			x := 0.0
			for {
				select {
				case <-stop:
					return
				default:
				}
				for j := 0; j < 1000; j++ {
					x += math.Sqrt(float64(j))
				}
				_ = x
			}
		}()
	}
	defer close(stop)

	deadline := time.Now().Add(2 * time.Second)
	var observedMax int
	for time.Now().Before(deadline) {
		if v := SchedLatencyMs(); v > observedMax {
			observedMax = v
		}
		time.Sleep(20 * time.Millisecond)
	}

	const minLoadedMs = 5
	if observedMax < minLoadedMs {
		t.Errorf("expected SchedLatencyMs > %d under CPU storm, got max %d", minLoadedMs, observedMax)
	}
}

// TestSchedLatencyMs_SamplesContinuously verifies the sampler loops
// at the configured cadence over a window. Mirrors rippled's
// testSampleOngoing (rippled/src/test/beast/beast_io_latency_probe_test.cpp:178-217)
// which asserts a 99ms-period probe runs ~10 times in 1 second.
func TestSchedLatencyMs_SamplesContinuously(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)

	time.Sleep(1 * time.Second)

	n := samplesCountForTest()
	// At 100ms cadence over 1s, expect ~10 samples. Use a generous
	// window because timer resolution and CI VM scheduling can stretch
	// the interval, and a heavily-loaded run can shorten it.
	if n < 5 {
		t.Errorf("expected >= 5 samples in 1s at 100ms cadence, got %d", n)
	}
	if n > 50 {
		t.Errorf("expected <= 50 samples in 1s at 100ms cadence, got %d", n)
	}
}

func TestSampleOnce_PublishesElapsed(t *testing.T) {
	resetForTest()
	elapsed := sampleOnce()
	if elapsed <= 0 {
		t.Errorf("elapsed = %v, want > 0", elapsed)
	}
	if got := publishedNs.Load(); got <= 0 {
		t.Errorf("publishedNs = %d, want > 0 after sampleOnce", got)
	}
}

func TestSampleOnce_OverwritesPreviousValue(t *testing.T) {
	resetForTest()
	publishedNs.Store(int64(50 * time.Millisecond))

	sampleOnce()
	got := publishedNs.Load()
	if got == int64(50*time.Millisecond) {
		t.Error("publishedNs was not overwritten by sampleOnce")
	}
	if got <= 0 {
		t.Errorf("publishedNs = %d, want > 0 after overwrite", got)
	}
}

func TestNextWait_HealthyServer(t *testing.T) {
	if got := nextWait(0); got != SamplerInterval {
		t.Errorf("nextWait(0) = %v, want %v", got, SamplerInterval)
	}
	if got := nextWait(time.Microsecond); got >= SamplerInterval {
		t.Errorf("nextWait(1us) = %v, want < %v", got, SamplerInterval)
	}
}

func TestNextWait_AdaptsUnderLoad(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{"healthy idle", 0, 100 * time.Millisecond},
		{"5us elapsed", 5 * time.Microsecond, 100*time.Millisecond - 10*time.Microsecond},
		{"25ms elapsed (mild load)", 25 * time.Millisecond, 50 * time.Millisecond},
		{"49ms elapsed", 49 * time.Millisecond, 2 * time.Millisecond},
		{"50ms elapsed (clamp boundary)", 50 * time.Millisecond, 0},
		{"75ms elapsed (heavy load)", 75 * time.Millisecond, 0},
		{"200ms elapsed (severe stall)", 200 * time.Millisecond, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextWait(tt.elapsed); got != tt.want {
				t.Errorf("nextWait(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

func TestStartSchedLatencySampler_IdempotentStart(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartSchedLatencySampler(ctx)
	StartSchedLatencySampler(ctx)
	StartSchedLatencySampler(ctx)
}

// TestStartSchedLatencySampler_StopsOnCancel asserts the sampler
// goroutine actually exits after its context is cancelled. Mirrors
// rippled's testCanceled (beast_io_latency_probe_test.cpp:219-227)
// which verifies post-cancel behavior; here we verify the goroutine's
// done channel closes within a deterministic window.
func TestStartSchedLatencySampler_StopsOnCancel(t *testing.T) {
	resetForTest()
	ctx, cancel := context.WithCancel(context.Background())

	StartSchedLatencySampler(ctx)
	done := samplerDoneForTest()
	if done == nil {
		t.Fatal("samplerDoneForTest returned nil after Start")
	}

	time.Sleep(2 * SamplerInterval)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * SamplerInterval):
		t.Fatal("sampler did not exit within 2*SamplerInterval after cancel")
	}
}

func TestSchedLatencyMs_CeilSemantics(t *testing.T) {
	resetForTest()
	tests := []struct {
		ns   int64
		want int
	}{
		{0, 0},
		{500_000, 1},               // 500us → ceil to 1ms
		{1_000_000, 1},             // exactly 1ms
		{1_100_000, 2},             // 1.1ms → ceil to 2ms
		{int64(time.Second), 1000}, // 1s → 1000ms

		// Boundary cases that the warn-threshold check piggybacks on.
		// Rippled's `if (lastSample >= 500ms)` warn at
		// Application.cpp:136 fires whenever ceil-ms reaches 500, so
		// 499ms + 1ns must round to 500 here. Regression-guards the
		// shared ceilMs helper used by both SchedLatencyMs and
		// runSampler's threshold comparison.
		{499*int64(time.Millisecond) + 1, 500}, // just past 499ms → 500
		{500 * int64(time.Millisecond), 500},   // exactly 500ms → 500
	}
	for _, tt := range tests {
		publishedNs.Store(tt.ns)
		if got := SchedLatencyMs(); got != tt.want {
			t.Errorf("ns=%d: SchedLatencyMs()=%d want %d", tt.ns, got, tt.want)
		}
	}
}
