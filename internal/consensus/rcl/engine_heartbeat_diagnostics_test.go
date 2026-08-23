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
		assertOnlyHeartbeatStageLog(t, out, "phase-establish")
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
