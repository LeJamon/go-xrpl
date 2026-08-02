// Package observability hosts process-level metrics surfaced to RPC.
package observability

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	samplerInterval = 100 * time.Millisecond
	latencyEventMs  = 10
	latencyWarnMs   = 500
)

var (
	latestSchedLatencyMs atomic.Int64
	ioLatencyEventCount  atomic.Uint64
)

// ErrSamplerRunning reports that a live context already owns the sampler.
var ErrSamplerRunning = errors.New("scheduler latency sampler is already running")

// SchedLatencySampler measures how long a goroutine remains runnable before the
// Go scheduler dispatches it again. Start and Stop may be called concurrently.
// Stop cancels and joins the active run; a stopped sampler may be restarted.
type SchedLatencySampler struct {
	mu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	interval time.Duration
	sample   func() time.Duration
	wait     func(context.Context, time.Duration) bool
	logger   *slog.Logger
}

// NewSchedLatencySampler returns an idle sampler. The process-level published
// value and event count are retained across sampler restarts.
func NewSchedLatencySampler() *SchedLatencySampler {
	return &SchedLatencySampler{
		interval: samplerInterval,
		sample:   sampleSchedLatency,
		wait:     waitForNextSample,
		logger:   slog.Default().With("component", "io_latency"),
	}
}

// Start launches the sampler for ctx. Starting an already-running sampler
// returns ErrSamplerRunning. If the prior context was canceled, Start joins
// that run before starting the replacement.
func (s *SchedLatencySampler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduler latency sampler context is nil")
	}

	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}

		s.mu.Lock()
		if s.done == nil {
			interval := s.interval
			if interval <= 0 {
				interval = samplerInterval
			}
			sample := s.sample
			if sample == nil {
				sample = sampleSchedLatency
			}
			wait := s.wait
			if wait == nil {
				wait = waitForNextSample
			}
			logger := s.logger
			if logger == nil {
				logger = slog.Default().With("component", "io_latency")
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			s.ctx = runCtx
			s.cancel = cancel
			s.done = done
			go s.run(runCtx, done, interval, sample, wait, logger)
			s.mu.Unlock()
			return nil
		}

		done := s.done
		select {
		case <-done:
			s.clearRunLocked(done)
			s.mu.Unlock()
			continue
		default:
		}
		if context.Cause(s.ctx) == nil {
			s.mu.Unlock()
			return ErrSamplerRunning
		}
		s.mu.Unlock()

		<-done
		s.mu.Lock()
		s.clearRunLocked(done)
		s.mu.Unlock()
	}
}

// Stop cancels and joins the active sampler. It is safe before Start and after
// a prior Stop.
func (s *SchedLatencySampler) Stop() {
	s.mu.Lock()
	if s.done == nil {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	done := s.done
	cancel()
	s.mu.Unlock()

	<-done
	s.mu.Lock()
	s.clearRunLocked(done)
	s.mu.Unlock()
}

func (s *SchedLatencySampler) clearRunLocked(done chan struct{}) {
	if s.done != done {
		return
	}
	s.ctx = nil
	s.cancel = nil
	s.done = nil
}

func (s *SchedLatencySampler) run(
	ctx context.Context,
	done chan struct{},
	interval time.Duration,
	sample func() time.Duration,
	waitForNext func(context.Context, time.Duration) bool,
	logger *slog.Logger,
) {
	defer close(done)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		elapsed := sample()
		recordSchedLatency(elapsed, logger)
		wait := nextWait(interval, elapsed)
		if wait == 0 {
			continue
		}
		if !waitForNext(ctx, wait) {
			return
		}
	}
}

func waitForNextSample(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func sampleSchedLatency() time.Duration {
	posted := time.Now()
	runtime.Gosched()
	return time.Since(posted)
}

func recordSchedLatency(elapsed time.Duration, logger *slog.Logger) {
	ms := ceilMs(elapsed)
	latestSchedLatencyMs.Store(ms)
	if ms >= latencyEventMs {
		incrementIOLatencyEventCount()
	}
	if ms >= latencyWarnMs {
		logger.Warn("io_service latency", "ms", ms)
	}
}

func incrementIOLatencyEventCount() {
	for {
		count := ioLatencyEventCount.Load()
		if count == ^uint64(0) {
			return
		}
		if ioLatencyEventCount.CompareAndSwap(count, count+1) {
			return
		}
	}
}

// SchedLatencyMs returns the latest sample rounded upward to milliseconds. It
// returns zero before the first sample and retains the last value after Stop.
func SchedLatencyMs() int64 {
	return latestSchedLatencyMs.Load()
}

// IOLatencyEventCount returns the process-lifetime number of samples whose
// rounded value reached the 10 millisecond reporting threshold.
func IOLatencyEventCount() uint64 {
	return ioLatencyEventCount.Load()
}

func ceilMs(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return 1 + int64((d-1)/time.Millisecond)
}

func nextWait(period, elapsed time.Duration) time.Duration {
	if period <= 0 {
		return 0
	}
	if elapsed <= 0 {
		return period
	}
	if elapsed >= period-elapsed {
		return 0
	}
	return period - 2*elapsed
}
