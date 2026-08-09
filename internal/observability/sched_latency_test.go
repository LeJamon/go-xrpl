package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetSchedLatencyMetricsForTest() {
	latestSchedLatencyMs.Store(0)
	ioLatencyEventCount.Store(0)
}

func newTestSampler(t *testing.T, interval time.Duration, sample func() time.Duration) *SchedLatencySampler {
	t.Helper()
	sampler := &SchedLatencySampler{
		interval: interval,
		sample:   sample,
		wait:     waitForNextSample,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(sampler.Stop)
	return sampler
}

func waitForSample(t *testing.T, sampled <-chan struct{}) {
	t.Helper()
	select {
	case <-sampled:
	case <-time.After(time.Second):
		t.Fatal("sampler did not run")
	}
}

func TestSchedLatencySamplerStartStop(t *testing.T) {
	resetSchedLatencyMetricsForTest()
	t.Cleanup(resetSchedLatencyMetricsForTest)

	var calls atomic.Int64
	recorded := make(chan struct{}, 1)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		calls.Add(1)
		return 12 * time.Millisecond
	})
	sampler.wait = func(ctx context.Context, _ time.Duration) bool {
		recorded <- struct{}{}
		<-ctx.Done()
		return false
	}

	sampler.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sampler.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForSample(t, recorded)
	if got := SchedLatencyMs(); got != 12 {
		t.Fatalf("SchedLatencyMs() = %d, want 12", got)
	}
	if got := IOLatencyEventCount(); got != 1 {
		t.Fatalf("IOLatencyEventCount() = %d, want 1", got)
	}

	sampler.Stop()
	sampler.Stop()
	if got := calls.Load(); got != 1 {
		t.Fatalf("samples after Stop() = %d, want 1", got)
	}
	select {
	case <-recorded:
		t.Fatal("sampler completed another cadence after Stop()")
	default:
	}
}

func TestSchedLatencySamplerRejectsCanceledContext(t *testing.T) {
	var calls atomic.Int64
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		calls.Add(1)
		return 0
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sampler.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(canceled context) error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("sample calls = %d, want 0", got)
	}
	sampler.Stop()
}

func TestSchedLatencySamplerRejectsNilContext(t *testing.T) {
	sampler := NewSchedLatencySampler()
	if err := sampler.Start(nil); err == nil {
		t.Fatal("Start(nil) succeeded")
	}
	sampler.Stop()
}

func TestSchedLatencySamplerConcurrentStartIsObservable(t *testing.T) {
	sampled := make(chan struct{}, 1)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		select {
		case sampled <- struct{}{}:
		default:
		}
		return 0
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const starters = 32
	start := make(chan struct{})
	errs := make(chan error, starters)
	var wg sync.WaitGroup
	wg.Add(starters)
	for range starters {
		go func() {
			defer wg.Done()
			<-start
			errs <- sampler.Start(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	started := 0
	alreadyRunning := 0
	for err := range errs {
		switch {
		case err == nil:
			started++
		case errors.Is(err, ErrSamplerRunning):
			alreadyRunning++
		default:
			t.Fatalf("Start() error = %v", err)
		}
	}
	if started != 1 || alreadyRunning != starters-1 {
		t.Fatalf("Start results = %d started, %d already running", started, alreadyRunning)
	}
	waitForSample(t, sampled)
	sampler.Stop()
}

func TestSchedLatencySamplerCancelAndImmediateRestart(t *testing.T) {
	var calls atomic.Int64
	sampled := make(chan struct{}, 2)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		calls.Add(1)
		sampled <- struct{}{}
		return time.Millisecond
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	if err := sampler.Start(firstCtx); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	waitForSample(t, sampled)
	cancelFirst()

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	if err := sampler.Start(secondCtx); err != nil {
		t.Fatalf("restart error = %v", err)
	}
	waitForSample(t, sampled)
	sampler.Stop()
	if got := calls.Load(); got != 2 {
		t.Fatalf("sample calls = %d, want 2", got)
	}
}

func TestSchedLatencySamplerRestartRechecksContextAfterJoin(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return time.Millisecond
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	if err := sampler.Start(firstCtx); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	<-entered
	cancelFirst()

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	restarted := make(chan error, 1)
	go func() { restarted <- sampler.Start(secondCtx) }()
	cancelSecond()
	close(release)

	if err := <-restarted; !errors.Is(err, context.Canceled) {
		t.Fatalf("restart error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("sample calls = %d, want 1", got)
	}
	sampler.Stop()
}

func TestSchedLatencySamplerStopJoinsActiveSample(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		close(entered)
		<-release
		return time.Millisecond
	})
	if err := sampler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	<-entered

	stopped := make(chan struct{})
	go func() {
		sampler.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop() returned before the active sample completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not join the completed sample")
	}
}

func TestSchedLatencySamplerCanRestartAfterStop(t *testing.T) {
	sampled := make(chan struct{}, 2)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		sampled <- struct{}{}
		return time.Millisecond
	})
	ctx := context.Background()

	if err := sampler.Start(ctx); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	waitForSample(t, sampled)
	sampler.Stop()
	if err := sampler.Start(ctx); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	waitForSample(t, sampled)
	sampler.Stop()
}

func TestSchedLatencySamplerConcurrentStop(t *testing.T) {
	sampled := make(chan struct{}, 1)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		sampled <- struct{}{}
		return 0
	})
	if err := sampler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForSample(t, sampled)

	const stoppers = 32
	var wg sync.WaitGroup
	wg.Add(stoppers)
	for range stoppers {
		go func() {
			defer wg.Done()
			sampler.Stop()
		}()
	}
	wg.Wait()

	if err := sampler.Start(context.Background()); err != nil {
		t.Fatalf("Start() after concurrent Stop() error = %v", err)
	}
	waitForSample(t, sampled)
	sampler.Stop()
}

func TestSchedLatencySamplerCadence(t *testing.T) {
	const wantSamples = 5
	sampled := make(chan struct{}, wantSamples)
	waits := make(chan time.Duration, wantSamples)
	advance := make(chan struct{}, wantSamples)
	sampler := newTestSampler(t, samplerInterval, func() time.Duration {
		sampled <- struct{}{}
		return 0
	})
	sampler.wait = func(ctx context.Context, delay time.Duration) bool {
		waits <- delay
		select {
		case <-ctx.Done():
			return false
		case <-advance:
			return true
		}
	}
	if err := sampler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer sampler.Stop()

	for i := range wantSamples {
		waitForSample(t, sampled)
		if i == wantSamples-1 {
			break
		}
		select {
		case got := <-waits:
			if got != samplerInterval {
				t.Fatalf("wait = %v, want %v", got, samplerInterval)
			}
		case <-time.After(time.Second):
			t.Fatal("sampler did not request its next wait")
		}
		advance <- struct{}{}
	}
}

func TestRecordSchedLatencyThresholds(t *testing.T) {
	resetSchedLatencyMetricsForTest()
	t.Cleanup(resetSchedLatencyMetricsForTest)
	handler := &recordingHandler{}
	logger := slog.New(handler)

	recordSchedLatency(9*time.Millisecond, logger)
	if got := IOLatencyEventCount(); got != 0 {
		t.Fatalf("count below event threshold = %d, want 0", got)
	}
	recordSchedLatency(9*time.Millisecond+time.Nanosecond, logger)
	if got := IOLatencyEventCount(); got != 1 {
		t.Fatalf("count at rounded event threshold = %d, want 1", got)
	}
	recordSchedLatency(499*time.Millisecond, logger)
	if got := handler.warnCount(); got != 0 {
		t.Fatalf("warnings below threshold = %d, want 0", got)
	}
	recordSchedLatency(499*time.Millisecond+time.Nanosecond, logger)
	if got := handler.warnCount(); got != 1 {
		t.Fatalf("warnings at rounded threshold = %d, want 1", got)
	}
	if got := SchedLatencyMs(); got != 500 {
		t.Fatalf("SchedLatencyMs() = %d, want 500", got)
	}
}

func TestIOLatencyEventCountConcurrentAndSaturating(t *testing.T) {
	resetSchedLatencyMetricsForTest()
	t.Cleanup(resetSchedLatencyMetricsForTest)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const writers = 64
	var wg sync.WaitGroup
	wg.Add(writers)
	for range writers {
		go func() {
			defer wg.Done()
			recordSchedLatency(10*time.Millisecond, logger)
		}()
	}
	wg.Wait()
	if got := IOLatencyEventCount(); got != writers {
		t.Fatalf("concurrent count = %d, want %d", got, writers)
	}

	ioLatencyEventCount.Store(^uint64(0) - 1)
	recordSchedLatency(10*time.Millisecond, logger)
	recordSchedLatency(10*time.Millisecond, logger)
	if got := IOLatencyEventCount(); got != ^uint64(0) {
		t.Fatalf("saturated count = %d, want %d", got, ^uint64(0))
	}
}

func TestSchedLatencyMetricsRetainedAcrossStops(t *testing.T) {
	resetSchedLatencyMetricsForTest()
	t.Cleanup(resetSchedLatencyMetricsForTest)
	recorded := make(chan struct{}, 1)
	sampler := newTestSampler(t, time.Hour, func() time.Duration {
		return 42 * time.Millisecond
	})
	sampler.wait = func(ctx context.Context, _ time.Duration) bool {
		recorded <- struct{}{}
		<-ctx.Done()
		return false
	}
	if err := sampler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForSample(t, recorded)
	sampler.Stop()

	if got := SchedLatencyMs(); got != 42 {
		t.Fatalf("retained latency = %d, want 42", got)
	}
	if got := IOLatencyEventCount(); got != 1 {
		t.Fatalf("retained event count = %d, want 1", got)
	}
}

func TestSchedLatencyMetricsZeroBeforeFirstSample(t *testing.T) {
	resetSchedLatencyMetricsForTest()
	t.Cleanup(resetSchedLatencyMetricsForTest)
	if got := SchedLatencyMs(); got != 0 {
		t.Fatalf("SchedLatencyMs() = %d, want 0", got)
	}
	if got := IOLatencyEventCount(); got != 0 {
		t.Fatalf("IOLatencyEventCount() = %d, want 0", got)
	}
}

func TestCeilMsExactAndPortable(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want int64
	}{
		{name: "negative", d: -time.Nanosecond, want: 0},
		{name: "zero", d: 0, want: 0},
		{name: "one nanosecond", d: time.Nanosecond, want: 1},
		{name: "sub millisecond", d: 500 * time.Microsecond, want: 1},
		{name: "last nanosecond below millisecond", d: time.Millisecond - time.Nanosecond, want: 1},
		{name: "exact millisecond", d: time.Millisecond, want: 1},
		{name: "past millisecond", d: time.Millisecond + time.Nanosecond, want: 2},
		{name: "float precision boundary", d: 9_007_199_255_000_001, want: 9_007_199_256},
		{name: "maximum duration", d: time.Duration(1<<63 - 1), want: 9_223_372_036_855},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ceilMs(tt.d); got != tt.want {
				t.Fatalf("ceilMs(%d) = %d, want %d", tt.d, got, tt.want)
			}
		})
	}
}

func TestNextWait(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{name: "zero", elapsed: 0, want: samplerInterval},
		{name: "negative", elapsed: -time.Second, want: samplerInterval},
		{name: "healthy", elapsed: 5 * time.Microsecond, want: samplerInterval - 10*time.Microsecond},
		{name: "mild load", elapsed: 25 * time.Millisecond, want: 50 * time.Millisecond},
		{name: "clamp boundary", elapsed: 50 * time.Millisecond, want: 0},
		{name: "heavy load", elapsed: 75 * time.Millisecond, want: 0},
		{name: "maximum duration", elapsed: time.Duration(1<<63 - 1), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextWait(samplerInterval, tt.elapsed); got != tt.want {
				t.Fatalf("nextWait(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

type recordingHandler struct {
	mu       sync.Mutex
	warnings int
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		h.mu.Lock()
		h.warnings++
		h.mu.Unlock()
	}
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

func (h *recordingHandler) warnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.warnings
}
