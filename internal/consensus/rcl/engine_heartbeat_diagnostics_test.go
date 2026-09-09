package rcl

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type heartbeatDiagnosticClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *heartbeatDiagnosticClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *heartbeatDiagnosticClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type heartbeatDiagnosticAdaptor struct {
	*mockAdaptor

	pendingStarted   chan struct{}
	pendingRelease   chan struct{}
	pendingStartOnce sync.Once

	validatorStarted   chan struct{}
	validatorRelease   chan struct{}
	validatorStartOnce sync.Once
}

func (a *heartbeatDiagnosticAdaptor) GetPendingTxs() [][]byte {
	if a.pendingStarted != nil {
		a.pendingStartOnce.Do(func() { close(a.pendingStarted) })
	}
	if a.pendingRelease != nil {
		<-a.pendingRelease
	}
	return a.mockAdaptor.GetPendingTxs()
}

func (a *heartbeatDiagnosticAdaptor) IsValidator() bool {
	if a.validatorStarted != nil {
		a.validatorStartOnce.Do(func() { close(a.validatorStarted) })
	}
	if a.validatorRelease != nil {
		<-a.validatorRelease
	}
	return a.mockAdaptor.IsValidator()
}

func TestEngineHeartbeatSlowStageDiagnostics(t *testing.T) {
	t.Run("lock wait", func(t *testing.T) {
		base := newMockAdaptor()
		engine := newHeartbeatDiagnosticEngine(t, base, base)
		clock := &heartbeatDiagnosticClock{now: time.Now()}
		started := make(chan struct{})
		var startedOnce sync.Once
		engine.heartbeatNow = func() time.Time {
			now := clock.Now()
			startedOnce.Do(func() { close(started) })
			return now
		}

		out := captureHeartbeatDiagnosticLogs(func() {
			engine.mu.Lock()
			done := make(chan struct{})
			go func() {
				engine.TimerEntry()
				close(done)
			}()

			select {
			case <-started:
				clock.Advance(slowHeartbeatThreshold + time.Millisecond)
				engine.mu.Unlock()
			case <-time.After(time.Second):
				engine.mu.Unlock()
				t.Fatal("heartbeat did not begin waiting for the engine mutex")
			}
			waitForHeartbeatDiagnostic(t, done)
		})

		assertHeartbeatStageLog(t, out, "lock-wait", "open", "observing")
		assertOnlyHeartbeatStageLog(t, out, "lock-wait")
	})

	t.Run("check ledger", func(t *testing.T) {
		adaptor := newMockAdaptor()
		engine := newHeartbeatDiagnosticEngine(t, adaptor, adaptor)
		clock := &heartbeatDiagnosticClock{now: time.Now()}
		engine.heartbeatNow = clock.Now

		started := make(chan struct{})
		release := make(chan struct{})
		adaptor.mu.Lock()
		adaptor.lclStarted = started
		adaptor.lclRelease = release
		adaptor.mu.Unlock()

		out := captureHeartbeatDiagnosticLogs(func() {
			done := make(chan struct{})
			go func() {
				engine.TimerEntry()
				close(done)
			}()
			advanceSlowHeartbeatStage(t, clock, started, release, done)
		})

		assertHeartbeatStageLog(t, out, "check-ledger", "open", "observing")
		assertOnlyHeartbeatStageLog(t, out, "check-ledger")
	})

	t.Run("flush stale", func(t *testing.T) {
		adaptor := newMockAdaptor()
		engine := newHeartbeatDiagnosticEngine(t, adaptor, adaptor)
		clock := &heartbeatDiagnosticClock{now: time.Now()}
		engine.heartbeatNow = clock.Now

		started := make(chan struct{})
		release := make(chan struct{})
		var startedOnce sync.Once
		engine.validationTracker.SetNow(func() time.Time {
			startedOnce.Do(func() { close(started) })
			<-release
			return adaptor.Now()
		})

		out := captureHeartbeatDiagnosticLogs(func() {
			done := make(chan struct{})
			go func() {
				engine.TimerEntry()
				close(done)
			}()
			advanceSlowHeartbeatStage(t, clock, started, release, done)
		})

		assertHeartbeatStageLog(t, out, "flush-stale", "open", "observing")
		assertOnlyHeartbeatStageLog(t, out, "flush-stale")
	})

	t.Run("open phase", func(t *testing.T) {
		base := newMockAdaptor()
		adaptor := &heartbeatDiagnosticAdaptor{mockAdaptor: base}
		engine := newHeartbeatDiagnosticEngine(t, adaptor, base)
		clock := &heartbeatDiagnosticClock{now: time.Now()}
		engine.heartbeatNow = clock.Now

		base.lastLCL.(*mockLedger).closeTime = base.now.Add(-11 * time.Minute)
		adaptor.pendingStarted = make(chan struct{})
		adaptor.pendingRelease = make(chan struct{})

		out := captureHeartbeatDiagnosticLogs(func() {
			done := make(chan struct{})
			go func() {
				engine.TimerEntry()
				close(done)
			}()
			advanceSlowHeartbeatStage(t, clock, adaptor.pendingStarted, adaptor.pendingRelease, done)
		})

		assertHeartbeatStageLog(t, out, "phase-open", "open", "observing")
		assertOnlyHeartbeatStageLog(t, out, "phase-open")
	})

	t.Run("establish phase", func(t *testing.T) {
		base := newMockAdaptor()
		adaptor := &heartbeatDiagnosticAdaptor{mockAdaptor: base}
		engine := newHeartbeatDiagnosticEngine(t, adaptor, base)
		clock := &heartbeatDiagnosticClock{now: time.Now()}
		engine.heartbeatNow = clock.Now

		engine.mu.Lock()
		engine.setPhase(consensus.PhaseEstablish)
		engine.roundStartTime = engine.now().Add(-engine.timing.LedgerMinConsensus)
		engine.mu.Unlock()
		adaptor.validatorStarted = make(chan struct{})
		adaptor.validatorRelease = make(chan struct{})

		out := captureHeartbeatDiagnosticLogs(func() {
			done := make(chan struct{})
			go func() {
				engine.TimerEntry()
				close(done)
			}()
			advanceSlowHeartbeatStage(t, clock, adaptor.validatorStarted, adaptor.validatorRelease, done)
		})

		assertHeartbeatStageLog(t, out, "phase-establish", "establish", "observing")
		assertHeartbeatStageLog(t, out, "pause-validation", "establish", "observing")
		if count := strings.Count(out, "event=heartbeat-stage-slow"); count != 2 {
			t.Fatalf("got %d slow stage logs, want phase-establish and pause-validation:\n%s", count, out)
		}
	})
}

func TestEngineHeartbeatFastStagesStaySilent(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := newHeartbeatDiagnosticEngine(t, adaptor, adaptor)
	clock := &heartbeatDiagnosticClock{now: time.Now()}
	engine.heartbeatNow = clock.Now
	adaptor.opMode = consensus.OpModeDisconnected

	out := captureHeartbeatDiagnosticLogs(engine.TimerEntry)
	if strings.Contains(out, "event=heartbeat-stage-slow") || strings.Contains(out, "event=tick-slow") {
		t.Fatalf("fast heartbeat emitted slow diagnostics:\n%s", out)
	}
}

func TestRecordSlowHeartbeatStageThreshold(t *testing.T) {
	context := heartbeatContext{seq: 101, phase: consensus.PhaseOpen, mode: consensus.ModeObserving}
	stages := recordSlowHeartbeatStage(nil, "phase-open", slowHeartbeatThreshold, context)
	if len(stages) != 0 {
		t.Fatalf("threshold duration must stay silent, got %+v", stages)
	}

	stages = recordSlowHeartbeatStage(nil, "phase-open", slowHeartbeatThreshold+time.Nanosecond, context)
	if len(stages) != 1 || stages[0].name != "phase-open" || stages[0].context != context {
		t.Fatalf("slow stage was not retained with its context: %+v", stages)
	}
}

func TestEngineHeartbeatAcceptNestedStageDiagnostics(t *testing.T) {
	base := newMockAdaptor()
	base.opMode = consensus.OpModeConnected
	clock := &heartbeatDiagnosticClock{now: time.Now()}
	config := DefaultConfig()
	config.Clock = clock.Now
	config.ManualTick = true
	config.Timing.LedgerMinConsensus = time.Millisecond
	config.Timing.LedgerMaxConsensus = 10 * time.Millisecond
	engine := NewEngine(base, config)
	engine.heartbeatNow = clock.Now
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	round := consensus.RoundID{Seq: base.lastLCL.Seq() + 1, ParentHash: base.lastLCL.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	base.buildLedgerHook = func() {
		clock.Advance(slowHeartbeatThreshold + time.Millisecond)
	}

	engine.mu.Lock()
	engine.prevLedger = base.lastLCL
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = clock.Now().Add(-time.Second)
	engine.ourTxSet = &mockTxSet{id: consensus.TxSetID{0xA1}}
	engine.mu.Unlock()

	out := captureHeartbeatDiagnosticLogs(engine.TimerEntry)
	assertHeartbeatStageLog(t, out, "accept", "establish", "observing")
	assertHeartbeatStageLog(t, out, "convergence", "establish", "observing")
	if count := strings.Count(out, "event=heartbeat-stage-slow"); count != 3 {
		t.Fatalf("got %d slow stage logs, want phase-establish, convergence, and accept:\n%s", count, out)
	}
}

func TestClassifyHeartbeatDispatch(t *testing.T) {
	base := time.Unix(100, 0)
	interval := time.Second

	tests := []struct {
		name             string
		scheduledAt      time.Time
		receivedAt       time.Time
		previousReceived time.Time
		previousEnd      time.Time
		previousWork     time.Duration
		wantDispatch     time.Duration
		wantWait         time.Duration
		wantWork         time.Duration
		wantMissed       int64
		wantCause        string
	}{
		{
			name:             "dispatch wait",
			scheduledAt:      base.Add(time.Second),
			receivedAt:       base.Add(time.Second + 7*time.Millisecond),
			previousReceived: base,
			previousEnd:      base.Add(900 * time.Millisecond),
			previousWork:     100 * time.Millisecond,
			wantDispatch:     7 * time.Millisecond,
			wantWait:         7 * time.Millisecond,
			wantWork:         100 * time.Millisecond,
			wantCause:        "dispatch-wait",
		},
		{
			name:             "prior tick work",
			scheduledAt:      base.Add(time.Second),
			receivedAt:       base.Add(2*time.Second + 4*time.Millisecond),
			previousReceived: base.Add(time.Second),
			previousEnd:      base.Add(2 * time.Second),
			previousWork:     2 * time.Second,
			wantDispatch:     time.Second + 4*time.Millisecond,
			wantWait:         4 * time.Millisecond,
			wantWork:         2 * time.Second,
			wantCause:        "prior-tick-work",
		},
		{
			name:             "exactly one coalesced tick",
			scheduledAt:      base.Add(2 * time.Second),
			receivedAt:       base.Add(3*time.Second + 2*time.Millisecond),
			previousReceived: base.Add(time.Second),
			previousEnd:      base.Add(3 * time.Second),
			previousWork:     3 * time.Second,
			wantDispatch:     time.Second + 2*time.Millisecond,
			wantWait:         2 * time.Millisecond,
			wantWork:         3 * time.Second,
			wantMissed:       1,
			wantCause:        "prior-tick-work",
		},
		{
			name:             "material dispatch wait after prior work",
			scheduledAt:      base.Add(time.Second),
			receivedAt:       base.Add(2*time.Second + slowHeartbeatThreshold + time.Millisecond),
			previousReceived: base.Add(time.Second),
			previousEnd:      base.Add(2 * time.Second),
			previousWork:     2 * time.Second,
			wantDispatch:     time.Second + slowHeartbeatThreshold + time.Millisecond,
			wantWait:         slowHeartbeatThreshold + time.Millisecond,
			wantWork:         2 * time.Second,
			wantCause:        "prior-tick-work-and-dispatch-wait",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyHeartbeatDispatch(
				tt.scheduledAt,
				tt.receivedAt,
				tt.previousReceived,
				tt.previousEnd,
				tt.previousWork,
				interval,
			)
			if got.dispatchDelay != tt.wantDispatch || got.dispatchWait != tt.wantWait || got.priorTickWork != tt.wantWork ||
				got.missed != tt.wantMissed || got.cause != tt.wantCause {
				t.Fatalf("timing = %+v, want dispatch=%v wait=%v missed=%d cause=%q", got, tt.wantDispatch, tt.wantWait, tt.wantMissed, tt.wantCause)
			}
		})
	}
}

func TestClassifyHeartbeatDispatchAttributesMissedGapToDelayedTick(t *testing.T) {
	base := time.Unix(100, 0)
	interval := time.Second

	first := classifyHeartbeatDispatch(
		base.Add(time.Second),
		base.Add(time.Second),
		time.Time{},
		time.Time{},
		0,
		interval,
	)
	second := classifyHeartbeatDispatch(
		base.Add(2*time.Second),
		base.Add(6*time.Second),
		base.Add(time.Second),
		base.Add(6*time.Second),
		5*time.Second,
		interval,
	)
	third := classifyHeartbeatDispatch(
		base.Add(7*time.Second),
		base.Add(7*time.Second),
		base.Add(6*time.Second),
		base.Add(6*time.Second),
		0,
		interval,
	)

	if first.missed != 0 {
		t.Fatalf("first dispatch missed = %d, want 0", first.missed)
	}
	if second.missed != 4 || second.cause != "prior-tick-work" {
		t.Fatalf("delayed dispatch = %+v, want four misses attributed to prior work", second)
	}
	if third.missed != 0 || third.dispatchDelay != 0 || third.dispatchWait != 0 {
		t.Fatalf("healthy follow-up dispatch = %+v, want no delay or misses", third)
	}
}

func TestClassifyHeartbeatDispatchRoundsTickerJitter(t *testing.T) {
	base := time.Unix(100, 0)
	interval := time.Second
	tests := []struct {
		name string
		gap  time.Duration
		want int64
	}{
		{name: "one interval plus jitter", gap: interval + time.Nanosecond, want: 0},
		{name: "two intervals minus jitter", gap: 2*interval - time.Nanosecond, want: 0},
		{name: "two intervals plus jitter", gap: 2*interval + time.Nanosecond, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyHeartbeatDispatch(
				base.Add(tt.gap),
				base.Add(tt.gap),
				base,
				base,
				0,
				interval,
			)
			if got.missed != tt.want {
				t.Fatalf("missed = %d, want %d for gap %v", got.missed, tt.want, tt.gap)
			}
		})
	}
}

func TestHeartbeatContextAcceptedUsesNextRound(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := NewEngine(adaptor, DefaultConfig())
	engine.prevLedger = adaptor.lastLCL
	engine.state = &roundState{Round: consensus.RoundID{Seq: adaptor.lastLCL.Seq()}}

	context := engine.heartbeatContextLocked()
	if want := adaptor.lastLCL.Seq() + 1; context.seq != want {
		t.Fatalf("accepted heartbeat sequence = %d, want next round %d", context.seq, want)
	}
}

func newHeartbeatDiagnosticEngine(t *testing.T, adaptor consensus.Adaptor, base *mockAdaptor) *Engine {
	t.Helper()

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	round := consensus.RoundID{Seq: base.lastLCL.Seq() + 1, ParentHash: base.lastLCL.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	return engine
}

func captureHeartbeatDiagnosticLogs(run func()) string {
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)
	run()
	return buffer.String()
}

func waitForHeartbeatDiagnostic(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat diagnostic test hook")
	}
}

func advanceSlowHeartbeatStage(
	t *testing.T,
	clock *heartbeatDiagnosticClock,
	started <-chan struct{},
	release chan struct{},
	done <-chan struct{},
) {
	t.Helper()
	select {
	case <-started:
		clock.Advance(slowHeartbeatThreshold + time.Millisecond)
		close(release)
	case <-time.After(time.Second):
		close(release)
		waitForHeartbeatDiagnostic(t, done)
		t.Fatal("heartbeat did not enter the expected stage")
	}
	waitForHeartbeatDiagnostic(t, done)
}

func assertHeartbeatStageLog(t *testing.T, output, stage, phase, mode string) {
	t.Helper()
	var stageLine string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "event=heartbeat-stage-slow") && strings.Contains(line, "stage="+stage) {
			stageLine = line
			break
		}
	}
	if stageLine == "" {
		t.Fatalf("heartbeat diagnostic missing stage %q:\n%s", stage, output)
	}
	for _, field := range []string{
		"dur_ms=51",
		"seq=101",
		"phase=" + phase,
		"mode=" + mode,
	} {
		if !strings.Contains(stageLine, field) {
			t.Fatalf("heartbeat stage record missing %q:\n%s", field, stageLine)
		}
	}
	if !strings.Contains(output, "event=tick-slow") {
		t.Fatalf("heartbeat diagnostic missing total tick record:\n%s", output)
	}
}

func assertOnlyHeartbeatStageLog(t *testing.T, output, stage string) {
	t.Helper()
	if count := strings.Count(output, "event=heartbeat-stage-slow"); count != 1 {
		t.Fatalf("got %d slow stage logs, want one for %s:\n%s", count, stage, output)
	}
}
