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
// without a heartbeat (repeated every reporting interval), fatal log at
// >=90s, and process abort at >=600s with a full goroutine dump as the
// terminal evidence. Like LoadManager, only loop LIVENESS is monitored —
// never ledger-close progress — so a live node that is behind or resyncing
// is left to self-heal. The abort path drains the async log queue and fsyncs
// the stdout/stderr/file descriptors so the final abort record survives
// os.Exit, which skips deferred Sync hooks. Only the stall-detection half of
// LoadManager is reproduced here; the local-fee raise/lower half already
// lives in internal/feetrack.
package watchdog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
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

	// now, sync, and exit are injectable so tests can drive a virtual clock and
	// observe the abort path without terminating the test process. In production
	// now is time.Now, sync best-effort fsyncs the log descriptors, and exit is
	// os.Exit(1). sync runs before exit on the abort path.
	now  func() time.Time
	sync func()
	exit func()

	// stack captures all goroutine stacks right before an abort. Injectable so
	// tests can assert it fires without parsing a real dump.
	stack func() string

	// stackSink receives the FULL goroutine dump verbatim, bypassing the
	// structured logger whose per-attribute encoding truncates a large dump
	// (the slog "stacks" field is capped well below a real 100+-goroutine
	// dump). Defaults to os.Stderr so a container captures the complete dump;
	// tests point it at a buffer.
	stackSink io.Writer

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
		sync:       syncLogDescriptors,
		stack:      allGoroutineStacks,
		stackSink:  os.Stderr,
		heartbeats: make(map[string]time.Time),
	}
	// exit terminates the process so an orchestrator can restart the wedged node
	// — rippled's LogicError abort. sync (called by check before exit) drains the
	// async log queue and fsyncs the descriptors so the abort record survives
	// os.Exit, which under async logging would otherwise leave it still queued.
	w.exit = func() { os.Exit(1) }
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
// aborts once it reaches Abort with a full goroutine dump as the terminal
// evidence. The dump is deliberately abort-only: runtime.Stack(all) stops the
// world, which would aggravate a merely-slow node at the 10s warn.
func (w *Watchdog) check(tick time.Duration) {
	loop, silence := w.stalled()
	if silence < w.cfg.Warn {
		return
	}

	// Fire only when the elapsed-tick count is a whole multiple of the warn
	// interval, so the report cadence is the warn interval rather than the tick.
	// When the tick is coarser than the warn interval every pass reports.
	warnTicks := max(w.cfg.Warn/tick, 1)
	if (silence/tick)%warnTicks == 0 {
		secs := int64(silence / time.Second)
		if silence < w.cfg.Fatal {
			w.logger.Warn("server loop stalled",
				"loop", loop, "stalled_s", secs)
		} else {
			// slog has no Fatal level; the level=fatal attr is the fatal marker,
			// matching the log package's canonical "fatal" name (LevelName).
			w.logger.Error("server loop stalled",
				"loop", loop, "stalled_s", secs, "level", "fatal")
		}
	}

	if silence >= w.cfg.Abort {
		secs := int64(silence / time.Second)
		// Straight to the sink, which the logger's attribute cap would
		// otherwise truncate.
		w.dumpToSink(loop, secs, w.stack())
		w.logger.Error("fatal server stall detected — aborting",
			"loop", loop, "stalled_s", secs)
		w.sync()
		w.exit()
	}
}

// dumpToSink writes the full goroutine dump verbatim to stackSink, framed by a
// greppable banner so a post-mortem can locate it.
func (w *Watchdog) dumpToSink(loop string, secs int64, dump string) {
	if w.stackSink == nil {
		return
	}
	fmt.Fprintf(w.stackSink,
		"\n=== watchdog fatal-stall goroutine dump (loop=%s stalled_s=%d) ===\n%s\n=== end watchdog dump ===\n",
		loop, secs, dump)
}

// syncLogDescriptors best-effort flushes the node's logs so the final abort
// record survives os.Exit, which does not flush kernel-buffered output.
// xrpllog.Sync runs first: it drains the async log queue into the destination
// (and fsyncs a file-backed [logging] output). The stdout/stderr fsyncs then
// cover the default and "stderr" config outputs. All errors are ignored — the
// process is aborting regardless.
func syncLogDescriptors() {
	_ = xrpllog.Sync()
	_ = os.Stderr.Sync()
	_ = os.Stdout.Sync()
}

// allGoroutineStacks returns a dump of every goroutine's stack — the Go
// equivalent of rippled dumping its JobQueue at the first stall warning. The
// buffer grows until the whole dump fits: runtime.Stack silently truncates to
// the buffer length, and a wedged node under load can hold far more goroutines
// than a fixed 1 MiB would capture (the overlay per-peer and worker tail is
// exactly what a stall post-mortem needs).
func allGoroutineStacks() string {
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			return string(buf[:n])
		}
	}
}
