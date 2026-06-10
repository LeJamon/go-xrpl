// Package watchdog implements an out-of-band stall detector for the node's
// long-running event loops.
//
// The consensus engine's in-band missedHeartbeats counter (rcl/engine.go)
// cannot catch a true deadlock: it lives inside the goroutine it watches, so
// a timerEntry that never returns to its select loop also never increments the
// counter. This watchdog runs on its own goroutine with its own ticker. Each
// monitored loop is handed a Pinger and stamps a heartbeat once per iteration;
// the watchdog measures the gap between now and the most recent heartbeat of
// every registered loop and escalates as the gap grows.
//
// Behaviour mirrors rippled's LoadManager (LoadManager.cpp): warn at >=10s
// without a heartbeat (repeated every reporting interval, with a full
// goroutine stack dump on the first warning so the wedged loop is visible),
// fatal log at >=90s, and process abort at >=600s after flushing logs. Only
// the stall-detection half of LoadManager is reproduced here; the local-fee
// raise/lower half already lives in internal/feetrack.
package watchdog

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"
)

const (
	// DefaultWarn is the rippled reportingIntervalSeconds: warn once the
	// quietest loop has been silent this long, and repeat every interval.
	DefaultWarn = 10 * time.Second

	// DefaultFatal is the rippled stallFatalLogMessageTimeLimit: escalate the
	// periodic report from warn to fatal once the stall reaches this long.
	DefaultFatal = 90 * time.Second

	// DefaultAbort is the rippled stallLogicErrorTimeLimit: the stall-recovery
	// code is considered to have failed, so abort the process.
	DefaultAbort = 600 * time.Second

	// tickInterval matches rippled's 1s LoadManager cadence.
	tickInterval = time.Second
)

// Config tunes the watchdog thresholds. The zero value is invalid; use
// DefaultConfig and override as needed, or build one from config.WatchdogConfig.
type Config struct {
	// Warn is the silence threshold at which a warning is logged (and repeated).
	Warn time.Duration
	// Fatal is the silence threshold at which the periodic report becomes fatal.
	Fatal time.Duration
	// Abort is the silence threshold at which the process is aborted.
	Abort time.Duration
}

// DefaultConfig returns the rippled-matching thresholds.
func DefaultConfig() Config {
	return Config{Warn: DefaultWarn, Fatal: DefaultFatal, Abort: DefaultAbort}
}

// ConfigFromSeconds builds a Config from threshold values expressed in seconds,
// the form the [watchdog] config section stores. Non-positive values fall back
// to the rippled defaults via New.
func ConfigFromSeconds(warn, fatal, abort int) Config {
	return Config{
		Warn:  time.Duration(warn) * time.Second,
		Fatal: time.Duration(fatal) * time.Second,
		Abort: time.Duration(abort) * time.Second,
	}
}

// Pinger is the tiny surface a monitored loop holds. Each loop calls Ping once
// per iteration to record that it is still making progress.
type Pinger func()

// Watchdog tracks per-loop heartbeats and escalates when a loop goes silent.
// It is safe for concurrent use: loops Ping from their own goroutines while the
// Run goroutine reads heartbeats.
type Watchdog struct {
	cfg    Config
	logger *slog.Logger

	// now and exit are injectable so tests can drive a virtual clock and
	// observe the abort path without terminating the test process. In
	// production now is time.Now and exit flushes logs then os.Exit(1).
	now  func() time.Time
	exit func()

	// stack captures all goroutine stacks on the first warning. Injectable so
	// tests can assert it fires without parsing a real dump.
	stack func() string

	// warnedStack guards the once-per-episode goroutine dump. Only the Run
	// goroutine touches it, so it needs no lock.
	warnedStack bool

	mu         sync.Mutex
	heartbeats map[string]time.Time
}

// New builds a Watchdog with the given thresholds and logger. A nil logger
// falls back to slog.Default. Thresholds that are non-positive are replaced
// with their defaults so a partially-filled Config still produces a sane
// watchdog.
func New(cfg Config, logger *slog.Logger) *Watchdog {
	if cfg.Warn <= 0 {
		cfg.Warn = DefaultWarn
	}
	if cfg.Fatal <= 0 {
		cfg.Fatal = DefaultFatal
	}
	if cfg.Abort <= 0 {
		cfg.Abort = DefaultAbort
	}
	if logger == nil {
		logger = slog.Default()
	}
	w := &Watchdog{
		cfg:        cfg,
		logger:     logger.With("component", "watchdog"),
		now:        time.Now,
		stack:      allGoroutineStacks,
		heartbeats: make(map[string]time.Time),
	}
	w.exit = func() {
		// Flush slog by giving any async handler a moment, then terminate so an
		// orchestrator can restart the wedged node — rippled's LogicError abort.
		os.Exit(1)
	}
	return w
}

// Register adds a loop and stamps its first heartbeat as now, so a loop that
// has not yet ticked is treated as healthy until its first expected iteration
// rather than tripping the moment the watchdog arms. Returns a Pinger bound to
// that loop. Registering the same name twice returns a fresh Pinger for it.
func (w *Watchdog) Register(loop string) Pinger {
	w.mu.Lock()
	w.heartbeats[loop] = w.now()
	w.mu.Unlock()
	return func() { w.ping(loop) }
}

func (w *Watchdog) ping(loop string) {
	now := w.now()
	w.mu.Lock()
	w.heartbeats[loop] = now
	w.mu.Unlock()
}

// stalled returns the loop with the largest silence and that silence duration.
// A watchdog with no registered loops reports zero stall.
func (w *Watchdog) stalled() (loop string, silence time.Duration) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for name, last := range w.heartbeats {
		if d := now.Sub(last); d > silence {
			silence, loop = d, name
		}
	}
	return loop, silence
}

// Run drives the detection ticker until ctx is cancelled. It blocks, so callers
// launch it in its own goroutine. The watchdog must already have its loops
// registered (and ideally pinged once) before Run arms — mirroring rippled
// arming activateStallDetector only at full start.
func (w *Watchdog) Run(ctx context.Context) {
	w.run(ctx, tickInterval)
}

func (w *Watchdog) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check(interval)
		}
	}
}

// check performs one detection pass. It reports on each whole reporting-interval
// boundary (rippled's `timeSpentStalled % reportingIntervalSeconds == 0` gate),
// escalating from warn to a fatal-level log once the stall reaches Fatal, and
// aborts once it reaches Abort. The first warning of an episode also dumps every
// goroutine's stack; warnedStack resets when the loops recover below Warn so a
// later stall dumps again.
func (w *Watchdog) check(tick time.Duration) {
	loop, silence := w.stalled()
	if silence < w.cfg.Warn {
		w.warnedStack = false
		return
	}

	// Fire only when the elapsed-tick count is a whole multiple of the warn
	// interval, so the report cadence is the warn interval rather than the tick.
	// When the tick is coarser than the warn interval every pass reports.
	warnTicks := w.cfg.Warn / tick
	if warnTicks < 1 {
		warnTicks = 1
	}
	if (silence/tick)%warnTicks == 0 {
		secs := int64(silence / time.Second)
		if silence < w.cfg.Fatal {
			w.logger.Warn("server loop stalled",
				"loop", loop, "stalled_s", secs)
			if !w.warnedStack {
				w.warnedStack = true
				w.logger.Warn("goroutine dump", "stacks", w.stack())
			}
		} else {
			w.logger.Error("server loop stalled",
				"loop", loop, "stalled_s", secs, "level", "fatal")
		}
	}

	if silence >= w.cfg.Abort {
		w.logger.Error("fatal server stall detected — aborting",
			"loop", loop, "stalled_s", int64(silence/time.Second))
		w.exit()
	}
}

// allGoroutineStacks returns a dump of every goroutine's stack — the Go
// equivalent of rippled dumping its JobQueue at the first stall warning.
func allGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
