package watchdog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic stall tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestWatchdog builds a watchdog with second-scale thresholds, a fake clock,
// a recording exit func, and a recording stack func, all wired through the same
// injection points production uses.
func newTestWatchdog(t *testing.T) (*Watchdog, *fakeClock, *bytes.Buffer, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	clk := newFakeClock()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var exits, stacks atomic.Int32
	w := New(Config{Warn: 10 * time.Second, Fatal: 90 * time.Second, Abort: 600 * time.Second}, logger)
	w.now = clk.now
	w.exit = func() { exits.Add(1) }
	w.sync = func() {}
	w.stack = func() string { stacks.Add(1); return "STACKDUMP" }
	// Default the full-dump sink to discard so tests don't spam stderr; tests
	// that assert on the dump override it with a buffer.
	w.stackSink = io.Discard
	return w, clk, &logBuf, &exits, &stacks
}

func TestNew_DefaultsFillNonPositiveThresholds(t *testing.T) {
	w := New(Config{}, nil)
	if w.cfg.Warn != DefaultWarn || w.cfg.Fatal != DefaultFatal || w.cfg.Abort != DefaultAbort {
		t.Fatalf("zero Config not defaulted: %+v", w.cfg)
	}
}

func TestConfigFromSeconds(t *testing.T) {
	c := ConfigFromSeconds(5, 30, 120)
	if c.Warn != 5*time.Second || c.Fatal != 30*time.Second || c.Abort != 120*time.Second {
		t.Fatalf("unexpected: %+v", c)
	}
}

// A loop that keeps pinging never trips any threshold.
func TestWatchdog_HealthyHeartbeatStaysQuiet(t *testing.T) {
	w, clk, logBuf, exits, stacks := newTestWatchdog(t)
	ping := w.Register("consensus")

	// Advance well past the abort threshold, but ping every tick.
	for i := 0; i < 700; i++ {
		ping()
		clk.advance(time.Second)
		w.check(time.Second)
	}
	if exits.Load() != 0 {
		t.Fatalf("healthy loop aborted")
	}
	if stacks.Load() != 0 {
		t.Fatalf("healthy loop dumped stacks")
	}
	if bytes.Contains(logBuf.Bytes(), []byte("stalled")) {
		t.Fatalf("healthy loop logged a stall: %s", logBuf.String())
	}
}

// An unregistered (empty) watchdog reports no stall.
func TestWatchdog_NoLoopsNeverTrips(t *testing.T) {
	w, clk, _, exits, _ := newTestWatchdog(t)
	for i := 0; i < 700; i++ {
		clk.advance(time.Second)
		w.check(time.Second)
	}
	if exits.Load() != 0 {
		t.Fatalf("empty watchdog aborted")
	}
}

// A silent loop escalates warn → fatal → abort at the right thresholds,
// dumping goroutine stacks exactly once — right before the abort — to the sink.
func TestWatchdog_StallEscalatesWarnFatalAbort(t *testing.T) {
	w, clk, logBuf, exits, stacks := newTestWatchdog(t)
	var sink bytes.Buffer
	w.stackSink = &sink
	w.Register("ledger") // registered, then never pinged again.

	var firstWarnAt, fatalAt, abortAt int
	for sec := 1; sec <= 600; sec++ {
		clk.advance(time.Second)
		before := logBuf.Len()
		exitsBefore := exits.Load()
		w.check(time.Second)
		line := logBuf.String()[before:]

		if firstWarnAt == 0 && strings.Contains(line, "server loop stalled") &&
			!strings.Contains(line, "level=fatal") {
			firstWarnAt = sec
		}
		if fatalAt == 0 && strings.Contains(line, "level=fatal") {
			fatalAt = sec
		}
		if exits.Load() > exitsBefore {
			abortAt = sec
			break
		}
	}

	if firstWarnAt != 10 {
		t.Errorf("first warn at %ds, want 10s", firstWarnAt)
	}
	if fatalAt != 90 {
		t.Errorf("fatal log at %ds, want 90s", fatalAt)
	}
	if abortAt != 600 {
		t.Errorf("abort at %ds, want 600s", abortAt)
	}
	// Exactly one dump, at abort only — the warn path must not stop the world.
	if got := stacks.Load(); got != 1 {
		t.Errorf("goroutine dump fired %d times, want exactly 1 (abort only)", got)
	}
	if !bytes.Contains(sink.Bytes(), []byte("fatal-stall goroutine dump")) {
		t.Errorf("fatal-stall dump not written to sink")
	}
	if !bytes.Contains(sink.Bytes(), []byte("STACKDUMP")) {
		t.Errorf("sink missing the goroutine dump body")
	}
}

// The abort path flushes the log descriptors before terminating, and does so in
// that order, so the final fatal record survives os.Exit.
func TestWatchdog_AbortFlushesBeforeExit(t *testing.T) {
	w, clk, _, exits, _ := newTestWatchdog(t)
	w.Register("ledger") // registered, then never pinged again.

	var seq []string
	w.sync = func() { seq = append(seq, "sync") }
	w.exit = func() { seq = append(seq, "exit"); exits.Add(1) }

	for sec := 1; sec <= 600; sec++ {
		clk.advance(time.Second)
		w.check(time.Second)
		if exits.Load() > 0 {
			break
		}
	}

	if exits.Load() != 1 {
		t.Fatalf("abort fired %d times, want 1", exits.Load())
	}
	if len(seq) != 2 || seq[0] != "sync" || seq[1] != "exit" {
		t.Fatalf("abort order = %v, want [sync exit]", seq)
	}
}

// The default (non-injected) sync hook is a real flush that runs without panic,
// proving the production abort path has a live flush rather than a no-op.
func TestWatchdog_DefaultSyncIsLive(t *testing.T) {
	w := New(Config{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if w.sync == nil {
		t.Fatal("default watchdog has a nil sync hook")
	}
	// Best-effort fsync of stdout/stderr/file; must not panic or block.
	w.sync()
}

// The periodic report fires on every warn-interval boundary, not every tick.
func TestWatchdog_ReportsOnWarnIntervalBoundaries(t *testing.T) {
	w, clk, logBuf, _, _ := newTestWatchdog(t)
	w.Register("ledger")

	warnLines := 0
	for sec := 1; sec <= 80; sec++ {
		clk.advance(time.Second)
		before := logBuf.Len()
		w.check(time.Second)
		if strings.Contains(logBuf.String()[before:], "server loop stalled") {
			warnLines++
		}
	}
	// Boundaries at 10,20,30,40,50,60,70,80 → 8 reports.
	if warnLines != 8 {
		t.Fatalf("got %d warn reports over 80s, want 8", warnLines)
	}
}

// A warn-level stall never dumps stacks: runtime.Stack(all) stops the world,
// so the dump is reserved for the abort path.
func TestWatchdog_WarnDoesNotDumpStacks(t *testing.T) {
	w, clk, _, _, stacks := newTestWatchdog(t)
	w.Register("ledger")

	// Stall well past warn and fatal, but short of abort.
	for sec := 1; sec <= 599; sec++ {
		clk.advance(time.Second)
		w.check(time.Second)
	}
	if stacks.Load() != 0 {
		t.Fatalf("pre-abort stall dumped stacks %d times, want 0", stacks.Load())
	}
}

// The slowest of several registered loops drives the report, and the report
// names that loop.
func TestWatchdog_NamesSlowestLoop(t *testing.T) {
	w, clk, logBuf, _, _ := newTestWatchdog(t)
	fast := w.Register("consensus")
	w.Register("ledger") // never pinged → slowest.

	for sec := 1; sec <= 10; sec++ {
		fast() // keep consensus healthy
		clk.advance(time.Second)
		w.check(time.Second)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("loop=ledger")) {
		t.Fatalf("report did not name the slowest loop: %s", logBuf.String())
	}
	if bytes.Contains(logBuf.Bytes(), []byte("loop=consensus")) {
		t.Fatalf("report named the healthy loop: %s", logBuf.String())
	}
}

// Run exits promptly on context cancellation and never aborts a healthy node.
func TestWatchdog_RunStopsOnContextCancel(t *testing.T) {
	w, _, _, exits, _ := newTestWatchdog(t)
	ping := w.Register("ledger")
	ping()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.run(ctx, time.Millisecond)
		close(done)
	}()

	// Let a few real ticks fire; the clock is frozen at the registration time
	// so silence stays zero and nothing trips.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if exits.Load() != 0 {
		t.Fatalf("healthy run aborted")
	}
}

// SetStallPing-style Register returns a working, concurrency-safe pinger.
func TestWatchdog_ConcurrentPing(t *testing.T) {
	w, clk, _, _, _ := newTestWatchdog(t)
	ping := w.Register("ledger")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ping()
			}
		}()
	}
	wg.Wait()
	// After concurrent pings the loop is at the current (frozen) clock time.
	clk.advance(5 * time.Second)
	if _, silence := w.stalled(); silence != 5*time.Second {
		t.Fatalf("silence = %v, want 5s", silence)
	}
}
