// Package watchdog detects stalled node event loops and guarantees terminal
// recovery when a stall reaches the abort threshold.
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

const (
	tickInterval          = time.Second
	terminalRecoveryGrace = 5 * time.Second
	maxStackDumpBytes     = 8 << 20
)

var (
	errNoRegistrations = errors.New("watchdog has no active registrations")
	errAlreadyStarted  = errors.New("watchdog is already started")
)

type thresholds struct {
	warn  time.Duration
	fatal time.Duration
	abort time.Duration
}

type heartbeat struct {
	generation     uint64
	last           time.Time
	reportedBucket int64
	fatalReported  bool
}

// Registration owns one generation of a named heartbeat.
type Registration struct {
	watchdog   *Watchdog
	loop       string
	generation uint64
}

// Ping records progress while this registration still owns the loop name.
func (r *Registration) Ping() {
	if r == nil || r.watchdog == nil {
		return
	}
	w := r.watchdog
	now := w.now()
	w.mu.Lock()
	state, ok := w.heartbeats[r.loop]
	if ok && state.generation == r.generation {
		if now.After(state.last) {
			state.last = now
			state.reportedBucket = 0
			state.fatalReported = false
		}
		w.heartbeats[r.loop] = state
	}
	w.mu.Unlock()
}

// Close releases the loop name if this registration still owns it.
func (r *Registration) Close() {
	if r == nil || r.watchdog == nil {
		return
	}
	w := r.watchdog
	w.mu.Lock()
	if state, ok := w.heartbeats[r.loop]; ok && state.generation == r.generation {
		delete(w.heartbeats, r.loop)
	}
	w.mu.Unlock()
}

type scheduleFunc func(time.Duration, func()) func()
type reportToken byte

// Watchdog owns heartbeat registrations and its monitoring goroutine.
type Watchdog struct {
	cfg    thresholds
	logger *slog.Logger

	now            func() time.Time
	sync           func()
	exit           func()
	stack          func([]byte) int
	stackSink      io.Writer
	schedule       scheduleFunc
	terminalGrace  time.Duration
	report         func(observation)
	warnReporting  atomic.Pointer[reportToken]
	fatalReporting atomic.Pointer[reportToken]

	mu             sync.Mutex
	heartbeats     map[string]heartbeat
	nextGeneration uint64

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}

	terminal  atomic.Bool
	abortOnce sync.Once
}

// New constructs a watchdog with validated positive, ordered thresholds.
func New(warn, fatal, abort time.Duration, logger *slog.Logger) (*Watchdog, error) {
	if warn <= 0 || fatal <= 0 || abort <= 0 {
		return nil, fmt.Errorf("watchdog thresholds must be positive")
	}
	if !(warn < fatal && fatal < abort) {
		return nil, fmt.Errorf("watchdog thresholds must satisfy warn < fatal < abort, got %s < %s < %s", warn, fatal, abort)
	}
	if logger == nil {
		logger = slog.Default()
	}
	w := &Watchdog{
		cfg: thresholds{
			warn:  warn,
			fatal: fatal,
			abort: abort,
		},
		logger:        logger.With("component", "watchdog"),
		now:           time.Now,
		sync:          syncLogDescriptors,
		exit:          func() { os.Exit(1) },
		stack:         allGoroutineStacks,
		stackSink:     os.Stderr,
		schedule:      scheduleAfter,
		terminalGrace: terminalRecoveryGrace,
		heartbeats:    make(map[string]heartbeat),
	}
	w.report = w.logStallAsync
	return w, nil
}

// Register replaces any prior generation for loop and returns its new owner.
func (w *Watchdog) Register(loop string) (*Registration, error) {
	if loop == "" {
		return nil, errors.New("watchdog loop name is empty")
	}
	w.mu.Lock()
	now := w.now()
	w.nextGeneration++
	if w.nextGeneration == 0 {
		w.nextGeneration++
	}
	generation := w.nextGeneration
	w.heartbeats[loop] = heartbeat{generation: generation, last: now}
	w.mu.Unlock()
	return &Registration{watchdog: w, loop: loop, generation: generation}, nil
}

// Start begins monitoring. At least one active registration is required.
func (w *Watchdog) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("watchdog start context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("watchdog start context: %w", err)
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.done != nil {
		return errAlreadyStarted
	}
	if w.terminal.Load() {
		return errors.New("watchdog has already entered terminal recovery")
	}
	w.mu.Lock()
	if len(w.heartbeats) == 0 {
		w.mu.Unlock()
		return errNoRegistrations
	}
	now := w.now()
	for name, state := range w.heartbeats {
		state.last = now
		state.reportedBucket = 0
		state.fatalReported = false
		w.heartbeats[name] = state
	}
	w.mu.Unlock()
	w.resetReportSlots()

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel = cancel
	w.done = done
	go func() {
		defer close(done)
		w.run(runCtx, tickInterval)
	}()
	w.logger.Info("stall watchdog armed",
		"warn_s", int64(w.cfg.warn/time.Second),
		"fatal_s", int64(w.cfg.fatal/time.Second),
		"abort_s", int64(w.cfg.abort/time.Second),
	)
	return nil
}

// Stop cancels monitoring and waits for the goroutine to finish.
func (w *Watchdog) Stop() {
	w.lifecycleMu.Lock()
	cancel, done := w.cancel, w.done
	if done == nil {
		w.lifecycleMu.Unlock()
		return
	}
	cancel()
	w.lifecycleMu.Unlock()

	<-done

	w.lifecycleMu.Lock()
	if w.done == done {
		w.cancel = nil
		w.done = nil
	}
	w.lifecycleMu.Unlock()
}

func (w *Watchdog) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.check() {
				return
			}
		}
	}
}

type observation struct {
	loop          string
	silence       time.Duration
	shouldLog     bool
	fatalLevel    bool
	terminalOwner bool
}

func (w *Watchdog) observe() observation {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()

	var result observation
	for name, state := range w.heartbeats {
		silence := now.Sub(state.last)
		if silence > result.silence || (silence == result.silence && (result.loop == "" || name < result.loop)) {
			result.loop = name
			result.silence = silence
		}
	}
	if result.loop == "" || result.silence < w.cfg.warn {
		return result
	}

	state := w.heartbeats[result.loop]
	bucket := int64(result.silence / w.cfg.warn)
	if bucket > state.reportedBucket {
		state.reportedBucket = bucket
		result.shouldLog = true
	}
	if result.silence >= w.cfg.fatal {
		result.fatalLevel = true
		if !state.fatalReported {
			state.fatalReported = true
			result.shouldLog = true
		}
	}
	if result.silence >= w.cfg.abort {
		result.terminalOwner = w.terminal.CompareAndSwap(false, true)
	}
	w.heartbeats[result.loop] = state
	return result
}

// check performs one detection pass and reports whether terminal recovery began.
func (w *Watchdog) check() bool {
	if w.terminal.Load() {
		return true
	}
	result := w.observe()
	if w.terminal.Load() {
		if result.terminalOwner {
			w.abortOnce.Do(func() { w.terminate(result) })
		}
		return true
	}
	if result.loop == "" || result.silence < w.cfg.warn {
		return false
	}
	if result.shouldLog {
		w.report(result)
	}
	return false
}

func (w *Watchdog) logStallAsync(result observation) {
	reporting := &w.warnReporting
	if result.fatalLevel {
		reporting = &w.fatalReporting
	}
	token := new(reportToken)
	if !reporting.CompareAndSwap(nil, token) {
		return
	}
	go func() {
		defer reporting.CompareAndSwap(token, nil)
		w.logStall(result)
	}()
}

func (w *Watchdog) resetReportSlots() {
	w.warnReporting.Store(nil)
	w.fatalReporting.Store(nil)
}

func (w *Watchdog) logStall(result observation) {
	args := []any{"loop", result.loop, "stalled_s", int64(result.silence / time.Second)}
	if result.fatalLevel {
		w.logger.Log(context.Background(), xrpllog.LevelFatal, "server loop stalled", args...)
		return
	}
	w.logger.Warn("server loop stalled", args...)
}

func (w *Watchdog) terminate(result observation) {
	var exitOnce sync.Once
	hardExit := func() { exitOnce.Do(w.exit) }
	stopFallback := w.schedule(w.terminalGrace, hardExit)
	defer func() {
		if stopFallback != nil {
			stopFallback()
		}
		hardExit()
		_ = recover()
	}()

	if result.shouldLog {
		w.logStall(result)
	}
	seconds := int64(result.silence / time.Second)
	w.logger.Log(context.Background(), xrpllog.LevelFatal, "fatal server stall detected — aborting",
		"loop", result.loop,
		"stalled_s", seconds,
	)

	buffer := make([]byte, maxStackDumpBytes)
	n := w.stack(buffer)
	if n < 0 {
		n = 0
	}
	if n > len(buffer) {
		n = len(buffer)
	}
	w.dumpToSink(result.loop, seconds, buffer[:n], n == len(buffer))
	w.sync()
}

func (w *Watchdog) dumpToSink(loop string, seconds int64, dump []byte, truncated bool) {
	if w.stackSink == nil {
		return
	}
	_, _ = fmt.Fprintf(w.stackSink,
		"\n=== watchdog fatal-stall goroutine dump (loop=%s stalled_s=%d) ===\n",
		loop,
		seconds,
	)
	_, _ = w.stackSink.Write(dump)
	if truncated {
		_, _ = io.WriteString(w.stackSink, "\n=== watchdog dump truncated ===")
	}
	_, _ = io.WriteString(w.stackSink, "\n=== end watchdog dump ===\n")
}

func scheduleAfter(delay time.Duration, callback func()) func() {
	timer := time.AfterFunc(delay, callback)
	return func() { timer.Stop() }
}

func syncLogDescriptors() {
	_ = xrpllog.Sync()
	_ = os.Stderr.Sync()
	_ = os.Stdout.Sync()
}

func allGoroutineStacks(buffer []byte) int {
	return runtime.Stack(buffer, true)
}
