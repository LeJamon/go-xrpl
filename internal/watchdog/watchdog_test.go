package watchdog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

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
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, record.Clone())
	h.mu.Unlock()
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func newTestWatchdog(t *testing.T) (*Watchdog, *fakeClock, *bytes.Buffer, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	clock := newFakeClock()
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w, err := New(10*time.Second, 90*time.Second, 600*time.Second, logger)
	if err != nil {
		t.Fatalf("new watchdog: %v", err)
	}
	var exits, stacks atomic.Int32
	w.now = clock.now
	w.exit = func() { exits.Add(1) }
	w.sync = func() {}
	w.stack = func(buffer []byte) int {
		stacks.Add(1)
		return copy(buffer, "STACKDUMP")
	}
	w.stackSink = io.Discard
	w.report = w.logStall
	return w, clock, &logBuffer, &exits, &stacks
}

func mustRegister(t *testing.T, w *Watchdog, loop string) *Registration {
	t.Helper()
	registration, err := w.Register(loop)
	if err != nil {
		t.Fatalf("register %q: %v", loop, err)
	}
	return registration
}

func TestNewValidatesThresholds(t *testing.T) {
	tests := []struct {
		name               string
		warn, fatal, abort time.Duration
	}{
		{name: "zero", warn: 0, fatal: time.Second, abort: 2 * time.Second},
		{name: "negative", warn: -time.Second, fatal: time.Second, abort: 2 * time.Second},
		{name: "warn equals fatal", warn: time.Second, fatal: time.Second, abort: 2 * time.Second},
		{name: "fatal after abort", warn: time.Second, fatal: 3 * time.Second, abort: 2 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.warn, test.fatal, test.abort, nil); err == nil {
				t.Fatal("invalid thresholds accepted")
			}
		})
	}
	if _, err := New(time.Second, 2*time.Second, 3*time.Second, nil); err != nil {
		t.Fatalf("valid thresholds rejected: %v", err)
	}
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	w, _, _, _, _ := newTestWatchdog(t)
	if _, err := w.Register(""); err == nil {
		t.Fatal("empty loop name accepted")
	}
}

func TestWatchdogHealthyHeartbeatStaysQuiet(t *testing.T) {
	w, clock, logBuffer, exits, stacks := newTestWatchdog(t)
	registration := mustRegister(t, w, "consensus")
	for range 700 {
		registration.Ping()
		clock.advance(time.Second)
		w.check()
	}
	if exits.Load() != 0 || stacks.Load() != 0 {
		t.Fatalf("healthy loop exited %d times and dumped %d stacks", exits.Load(), stacks.Load())
	}
	if strings.Contains(logBuffer.String(), "stalled") {
		t.Fatalf("healthy loop logged a stall: %s", logBuffer.String())
	}
}

func TestWatchdogStallEscalatesWarnFatalAbort(t *testing.T) {
	w, clock, logBuffer, exits, stacks := newTestWatchdog(t)
	var sink bytes.Buffer
	w.stackSink = &sink
	mustRegister(t, w, "ledger")

	var firstWarnAt, fatalAt, abortAt int
	for second := 1; second <= 600; second++ {
		clock.advance(time.Second)
		before := logBuffer.Len()
		terminal := w.check()
		line := logBuffer.String()[before:]
		if firstWarnAt == 0 && strings.Contains(line, "level=WARN") {
			firstWarnAt = second
		}
		if fatalAt == 0 && strings.Contains(line, "level=ERROR+4") {
			fatalAt = second
		}
		if terminal {
			abortAt = second
			break
		}
	}
	if firstWarnAt != 10 || fatalAt != 90 || abortAt != 600 {
		t.Fatalf("escalation = warn %d, fatal %d, abort %d", firstWarnAt, fatalAt, abortAt)
	}
	if exits.Load() != 1 || stacks.Load() != 1 {
		t.Fatalf("terminal actions = exits %d, stacks %d", exits.Load(), stacks.Load())
	}
	if !strings.Contains(sink.String(), "fatal-stall goroutine dump") || !strings.Contains(sink.String(), "STACKDUMP") {
		t.Fatalf("missing framed stack dump: %q", sink.String())
	}
}

func TestWatchdogUsesRealFatalLevel(t *testing.T) {
	handler := &recordHandler{}
	w, err := New(10*time.Second, 90*time.Second, 600*time.Second, slog.New(handler))
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	w.now = clock.now
	w.report = w.logStall
	w.exit = func() {}
	w.sync = func() {}
	w.stack = func([]byte) int { return 0 }
	w.stackSink = io.Discard
	mustRegister(t, w, "consensus")

	clock.advance(90 * time.Second)
	w.check()
	clock.advance(510 * time.Second)
	w.check()

	var periodicFatal, terminalFatal bool
	for _, record := range handler.snapshot() {
		if record.Level != xrpllog.LevelFatal {
			continue
		}
		switch record.Message {
		case "server loop stalled":
			periodicFatal = true
		case "fatal server stall detected — aborting":
			terminalFatal = true
		}
	}
	if !periodicFatal || !terminalFatal {
		t.Fatalf("fatal records missing: periodic=%v terminal=%v", periodicFatal, terminalFatal)
	}
}

func TestWatchdogReportsFirstCheckAfterBoundary(t *testing.T) {
	tests := []struct {
		name          string
		steps         []time.Duration
		wantLevels    []slog.Level
		wantLogCounts []int
	}{
		{
			name:          "warn 9 to 11",
			steps:         []time.Duration{9 * time.Second, 2 * time.Second},
			wantLevels:    []slog.Level{0, slog.LevelWarn},
			wantLogCounts: []int{0, 1},
		},
		{
			name:          "fatal 89 to 95",
			steps:         []time.Duration{89 * time.Second, 6 * time.Second},
			wantLevels:    []slog.Level{slog.LevelWarn, xrpllog.LevelFatal},
			wantLogCounts: []int{1, 2},
		},
		{
			name:          "multi interval jump",
			steps:         []time.Duration{95 * time.Second},
			wantLevels:    []slog.Level{xrpllog.LevelFatal},
			wantLogCounts: []int{1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &recordHandler{}
			w, err := New(10*time.Second, 90*time.Second, 600*time.Second, slog.New(handler))
			if err != nil {
				t.Fatal(err)
			}
			clock := newFakeClock()
			w.now = clock.now
			w.report = w.logStall
			mustRegister(t, w, "ledger")
			for index, step := range test.steps {
				clock.advance(step)
				w.check()
				records := handler.snapshot()
				if len(records) != test.wantLogCounts[index] {
					t.Fatalf("step %d logged %d records, want %d", index, len(records), test.wantLogCounts[index])
				}
				if len(records) > 0 && test.wantLevels[index] != 0 && records[len(records)-1].Level != test.wantLevels[index] {
					t.Fatalf("step %d level = %v, want %v", index, records[len(records)-1].Level, test.wantLevels[index])
				}
			}
		})
	}
}

func TestWatchdogReportsFatalCrossingBetweenWarnBuckets(t *testing.T) {
	handler := &recordHandler{}
	w, err := New(7*time.Second, 10*time.Second, 30*time.Second, slog.New(handler))
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	w.now = clock.now
	w.report = w.logStall
	mustRegister(t, w, "ledger")
	clock.advance(7 * time.Second)
	w.check()
	clock.advance(3 * time.Second)
	w.check()
	records := handler.snapshot()
	if len(records) != 2 || records[1].Level != xrpllog.LevelFatal {
		t.Fatalf("records = %+v, want warn then fatal", records)
	}
}

func TestWatchdogPingStartsNewReportingEpisode(t *testing.T) {
	handler := &recordHandler{}
	w, err := New(10*time.Second, 90*time.Second, 600*time.Second, slog.New(handler))
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	w.now = clock.now
	w.report = w.logStall
	registration := mustRegister(t, w, "ledger")
	clock.advance(11 * time.Second)
	w.check()
	registration.Ping()
	clock.advance(9 * time.Second)
	w.check()
	clock.advance(2 * time.Second)
	w.check()
	if got := len(handler.snapshot()); got != 2 {
		t.Fatalf("reported %d episodes, want 2", got)
	}
}

func TestRegistrationGenerationOwnership(t *testing.T) {
	w, clock, logBuffer, _, _ := newTestWatchdog(t)
	old := mustRegister(t, w, "ledger")
	clock.advance(5 * time.Second)
	current := mustRegister(t, w, "ledger")
	clock.advance(5 * time.Second)
	old.Ping()
	old.Close()
	clock.advance(6 * time.Second)
	w.check()
	if !strings.Contains(logBuffer.String(), "loop=ledger") {
		t.Fatal("stale registration masked the current generation")
	}
	current.Ping()
	current.Close()
	clock.advance(600 * time.Second)
	if w.check() {
		t.Fatal("closed registration remained active")
	}
}

func TestReplacementBeforeAbortObservationWins(t *testing.T) {
	w, clock, _, exits, _ := newTestWatchdog(t)
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)
	current := mustRegister(t, w, "ledger")

	if w.check() {
		t.Fatal("stale generation triggered terminal recovery")
	}
	if exits.Load() != 0 {
		t.Fatalf("exit called %d times", exits.Load())
	}
	current.Close()
}

func TestWatchdogDeterministicTiedStall(t *testing.T) {
	w, clock, logBuffer, _, _ := newTestWatchdog(t)
	mustRegister(t, w, "zeta")
	mustRegister(t, w, "alpha")
	clock.advance(10 * time.Second)
	w.check()
	if !strings.Contains(logBuffer.String(), "loop=alpha") || strings.Contains(logBuffer.String(), "loop=zeta") {
		t.Fatalf("tied stall selection was not lexical: %s", logBuffer.String())
	}
}

func TestWatchdogLifecycle(t *testing.T) {
	w, _, _, _, _ := newTestWatchdog(t)
	w.Stop()
	if err := w.Start(t.Context()); !errors.Is(err, errNoRegistrations) {
		t.Fatalf("empty start error = %v", err)
	}
	mustRegister(t, w, "ledger")
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Start(t.Context()); !errors.Is(err, errAlreadyStarted) {
		t.Fatalf("second start error = %v", err)
	}
	w.Stop()
	w.Stop()
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	w.Stop()
}

func TestWatchdogStartRejectsCanceledContext(t *testing.T) {
	w, _, _, _, _ := newTestWatchdog(t)
	mustRegister(t, w, "ledger")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want context canceled", err)
	}
	if w.done != nil {
		t.Fatal("canceled start published lifecycle state")
	}
}

func TestWatchdogStartRebasesHeartbeats(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start after pre-arm delay: %v", err)
	}
	if w.check() {
		t.Fatal("pre-arm delay counted as a stall")
	}
	w.Stop()

	clock.advance(600 * time.Second)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("restart after stopped interval: %v", err)
	}
	if w.check() {
		t.Fatal("stopped interval counted as a stall")
	}
	w.Stop()
}

func TestWatchdogStartRebaseCannotBeUndoneByDelayedPing(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	registration := mustRegister(t, w, "ledger")
	oldNow := clock.now()
	pingCaptured := make(chan struct{})
	releasePing := make(chan struct{})
	var calls atomic.Int32
	w.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(pingCaptured)
			<-releasePing
			return oldNow
		}
		return clock.now()
	}
	pingDone := make(chan struct{})
	go func() {
		registration.Ping()
		close(pingDone)
	}()
	<-pingCaptured
	clock.advance(600 * time.Second)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	clock.advance(95 * time.Second)
	first := w.observe()
	if !first.shouldLog || !first.fatalLevel || first.silence != 95*time.Second {
		t.Fatalf("unexpected first observation: %+v", first)
	}
	close(releasePing)
	<-pingDone
	second := w.observe()
	if second.shouldLog || !second.fatalLevel || second.silence != 95*time.Second {
		t.Fatalf("delayed ping changed the active stall episode: %+v", second)
	}
	w.Stop()
}

func TestWatchdogTerminalRecoveryIsSingleShot(t *testing.T) {
	w, clock, _, exits, stacks := newTestWatchdog(t)
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)
	if !w.check() || !w.check() {
		t.Fatal("terminal state was not latched")
	}
	if exits.Load() != 1 || stacks.Load() != 1 {
		t.Fatalf("terminal recovery repeated: exits=%d stacks=%d", exits.Load(), stacks.Load())
	}
}

func TestAbortObservationClaimsTerminal(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)

	result := w.observe()
	if !result.terminalOwner || !w.terminal.Load() {
		t.Fatalf("abort observation did not claim terminal recovery: %+v", result)
	}
}

func TestWatchdogTerminalDiagnosticPanicStillExits(t *testing.T) {
	w, clock, _, exits, _ := newTestWatchdog(t)
	w.stack = func([]byte) int { panic("stack capture failed") }
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)
	if !w.check() {
		t.Fatal("terminal recovery did not start")
	}
	if exits.Load() != 1 {
		t.Fatalf("exit called %d times", exits.Load())
	}
}

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

type blockingHandler struct {
	started chan struct{}
	release chan struct{}
	blocked atomic.Bool
	records chan slog.Record
}

func (h *blockingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *blockingHandler) Handle(_ context.Context, record slog.Record) error {
	if h.blocked.CompareAndSwap(false, true) {
		close(h.started)
		<-h.release
	}
	if h.records != nil {
		h.records <- record.Clone()
	}
	return nil
}

func (h *blockingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockingHandler) WithGroup(string) slog.Handler      { return h }

type allBlockingHandler struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *allBlockingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *allBlockingHandler) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.started) })
	<-h.release
	return nil
}

func (h *allBlockingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *allBlockingHandler) WithGroup(string) slog.Handler      { return h }

func TestBlockedWarningLoggerCannotPreventTerminalRecovery(t *testing.T) {
	w, clock, _, exits, _ := newTestWatchdog(t)
	started := make(chan struct{})
	release := make(chan struct{})
	w.logger = slog.New(&allBlockingHandler{started: started, release: release})
	w.report = w.logStallAsync
	scheduled := make(chan func(), 1)
	w.schedule = func(_ time.Duration, callback func()) func() {
		scheduled <- callback
		return func() {}
	}
	mustRegister(t, w, "ledger")
	clock.advance(10 * time.Second)
	w.check()
	<-started

	clock.advance(590 * time.Second)
	done := make(chan struct{})
	go func() {
		w.check()
		close(done)
	}()
	callback := <-scheduled
	callback()
	if exits.Load() != 1 {
		t.Fatalf("fallback exits = %d, want 1", exits.Load())
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal recovery did not finish after logger release")
	}
}

func TestBlockedWarningDoesNotSuppressFirstFatalReport(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	started := make(chan struct{})
	release := make(chan struct{})
	records := make(chan slog.Record, 2)
	defer close(release)
	w.logger = slog.New(&blockingHandler{started: started, release: release, records: records})
	w.report = w.logStallAsync
	mustRegister(t, w, "ledger")

	clock.advance(10 * time.Second)
	w.check()
	<-started
	clock.advance(80 * time.Second)
	w.check()

	select {
	case record := <-records:
		if record.Level != xrpllog.LevelFatal {
			t.Fatalf("level = %v, want %v", record.Level, xrpllog.LevelFatal)
		}
	case <-time.After(time.Second):
		t.Fatal("fatal report was suppressed by blocked warning")
	}
}

func TestResetReportSlotsAllowsNewWarningWhileOldLoggerBlocked(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	started := make(chan struct{})
	release := make(chan struct{})
	records := make(chan slog.Record, 2)
	defer close(release)
	w.logger = slog.New(&blockingHandler{started: started, release: release, records: records})
	w.report = w.logStallAsync
	mustRegister(t, w, "ledger")

	clock.advance(10 * time.Second)
	w.check()
	<-started
	w.resetReportSlots()
	clock.advance(10 * time.Second)
	w.check()

	select {
	case record := <-records:
		if record.Level != slog.LevelWarn {
			t.Fatalf("level = %v, want %v", record.Level, slog.LevelWarn)
		}
	case <-time.After(time.Second):
		t.Fatal("new warning was suppressed by an old blocked report")
	}
}

func TestWatchdogTerminalFallbackSurvivesBlockedDiagnostics(t *testing.T) {
	for _, stage := range []string{"logger", "stack", "sink", "sync"} {
		t.Run(stage, func(t *testing.T) {
			w, clock, _, exits, _ := newTestWatchdog(t)
			started := make(chan struct{})
			release := make(chan struct{})
			switch stage {
			case "logger":
				w.logger = slog.New(&blockingHandler{started: started, release: release})
			case "stack":
				w.stack = func([]byte) int {
					close(started)
					<-release
					return 0
				}
			case "sink":
				w.stackSink = &blockingWriter{started: started, release: release}
			case "sync":
				w.sync = func() {
					close(started)
					<-release
				}
			}

			scheduled := make(chan func(), 1)
			var fallbackCanceled atomic.Bool
			w.schedule = func(_ time.Duration, callback func()) func() {
				scheduled <- callback
				return func() { fallbackCanceled.Store(true) }
			}
			mustRegister(t, w, "ledger")
			clock.advance(600 * time.Second)
			done := make(chan struct{})
			go func() {
				w.check()
				close(done)
			}()

			callback := <-scheduled
			<-started
			callback()
			if exits.Load() != 1 {
				t.Fatalf("fallback exits = %d, want 1", exits.Load())
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("terminal check did not finish after diagnostic release")
			}
			if !fallbackCanceled.Load() {
				t.Fatal("fallback timer was not canceled after diagnostics")
			}
			if exits.Load() != 1 {
				t.Fatalf("exit called %d times", exits.Load())
			}
		})
	}
}

func TestWatchdogStackDumpIsBoundedAndMarked(t *testing.T) {
	w, clock, _, _, _ := newTestWatchdog(t)
	var stackBufferSize int
	w.stack = func(buffer []byte) int {
		stackBufferSize = len(buffer)
		for index := range buffer {
			buffer[index] = 'x'
		}
		return len(buffer)
	}
	var sink bytes.Buffer
	w.stackSink = &sink
	mustRegister(t, w, "ledger")
	clock.advance(600 * time.Second)
	w.check()
	if stackBufferSize != maxStackDumpBytes {
		t.Fatalf("stack buffer = %d, want %d", stackBufferSize, maxStackDumpBytes)
	}
	if !strings.Contains(sink.String(), "watchdog dump truncated") {
		t.Fatal("truncated dump was not marked")
	}
}

func TestWatchdogConcurrentRegistrationPingAndCheck(t *testing.T) {
	w, _, _, _, _ := newTestWatchdog(t)
	var current atomic.Pointer[Registration]
	current.Store(mustRegister(t, w, "ledger"))
	var group sync.WaitGroup
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 200 {
				current.Load().Ping()
				w.check()
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		for range 100 {
			next, err := w.Register("ledger")
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			previous := current.Swap(next)
			previous.Ping()
			previous.Close()
		}
	}()
	group.Wait()
}

func TestWatchdogTerminalFallbackSubprocess(t *testing.T) {
	mode := os.Getenv("GOXRPL_WATCHDOG_BLOCK_STAGE")
	if mode != "" {
		w, err := New(time.Second, 2*time.Second, 3*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			os.Exit(2)
		}
		clock := newFakeClock()
		w.now = clock.now
		w.terminalGrace = 50 * time.Millisecond
		started := make(chan struct{})
		switch mode {
		case "stack":
			w.stack = func([]byte) int { close(started); select {} }
		case "sink":
			w.stack = func([]byte) int { return 0 }
			w.stackSink = &blockingWriter{started: started, release: make(chan struct{})}
		case "sync":
			w.stack = func([]byte) int { return 0 }
			w.stackSink = io.Discard
			w.sync = func() { close(started); select {} }
		case "logger":
			w.logger = slog.New(&allBlockingHandler{started: started, release: make(chan struct{})})
		default:
			os.Exit(2)
		}
		if _, err := w.Register("ledger"); err != nil {
			os.Exit(2)
		}
		clock.advance(3 * time.Second)
		w.check()
		os.Exit(2)
	}

	for _, stage := range []string{"logger", "stack", "sink", "sync"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWatchdogTerminalFallbackSubprocess$")
			command.Env = append(os.Environ(), "GOXRPL_WATCHDOG_BLOCK_STAGE="+stage)
			err := command.Run()
			if ctx.Err() != nil {
				t.Fatalf("child exceeded terminal deadline: %v", ctx.Err())
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
				t.Fatalf("child exit = %v, want status 1", err)
			}
		})
	}
}

func TestAllGoroutineStacksUsesCallerBound(t *testing.T) {
	buffer := make([]byte, 1<<20)
	n := allGoroutineStacks(buffer)
	if n <= 0 || n > len(buffer) {
		t.Fatalf("stack length = %d, buffer = %d", n, len(buffer))
	}
	if !bytes.Contains(buffer[:n], []byte("goroutine")) {
		t.Fatal("real stack capture missing goroutine framing")
	}
}
