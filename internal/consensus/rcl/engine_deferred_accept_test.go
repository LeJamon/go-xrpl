package rcl

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type deferredAcceptAdaptor struct {
	*mockAdaptor

	deferMu sync.Mutex
	accept  func()
	calls   int

	broadcastHook func()
}

type lifecycleDeferredAcceptAdaptor struct {
	*deferredAcceptAdaptor
	stopEntered chan struct{}
	stopRelease chan struct{}
	stopOnce    sync.Once
}

func (a *lifecycleDeferredAcceptAdaptor) StopLedgerAccept() error {
	a.stopOnce.Do(func() { close(a.stopEntered) })
	<-a.stopRelease
	return nil
}

func (a *deferredAcceptAdaptor) DeferLedgerAccept(complete func()) bool {
	a.deferMu.Lock()
	defer a.deferMu.Unlock()
	a.calls++
	a.accept = complete
	return true
}

func (a *deferredAcceptAdaptor) BroadcastProposal(proposal *consensus.Proposal) error {
	if a.broadcastHook != nil {
		a.broadcastHook()
	}
	return a.mockAdaptor.BroadcastProposal(proposal)
}

func (a *deferredAcceptAdaptor) BroadcastValidation(validation *consensus.Validation) error {
	if a.broadcastHook != nil {
		a.broadcastHook()
	}
	return a.mockAdaptor.BroadcastValidation(validation)
}

func (a *deferredAcceptAdaptor) completion(t *testing.T) func() {
	t.Helper()
	a.deferMu.Lock()
	defer a.deferMu.Unlock()
	if a.accept == nil {
		t.Fatal("ledger acceptance was not deferred")
	}
	return a.accept
}

func (a *deferredAcceptAdaptor) deferCalls() int {
	a.deferMu.Lock()
	defer a.deferMu.Unlock()
	return a.calls
}

func startDeferredAccept(t *testing.T, adaptor *deferredAcceptAdaptor) *Engine {
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
	if err := engine.StartRound(consensus.RoundID{
		Seq:        adaptor.lastLCL.Seq() + 1,
		ParentHash: adaptor.lastLCL.ID(),
	}, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	engine.mu.Lock()
	engine.ourTxSet = &mockTxSet{id: consensus.TxSetID{0xA1}}
	engine.closeTime.haveConsensus = false
	engine.setPhase(consensus.PhaseEstablish)
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()
	return engine
}

func TestDeferredLedgerAcceptCompletesExactlyOnceOffLock(t *testing.T) {
	base := newMockAdaptor()
	base.standalone = true
	adaptor := &deferredAcceptAdaptor{mockAdaptor: base}

	var builds atomic.Int32
	var broadcasts atomic.Int32
	var engineRef atomic.Pointer[Engine]
	base.buildLedgerHook = func() {
		builds.Add(1)
		engine := engineRef.Load()
		if engine == nil {
			t.Error("BuildLedger called before the engine was available")
			return
		}
		if !engine.mu.TryLock() {
			t.Error("BuildLedger called with the engine lock held")
			return
		}
		engine.mu.Unlock()
	}
	adaptor.broadcastHook = func() {
		broadcasts.Add(1)
		engine := engineRef.Load()
		if engine == nil {
			t.Error("broadcast called before the engine was available")
			return
		}
		if !engine.mu.TryLock() {
			t.Error("deferred acceptance flushed a broadcast with the engine lock held")
			return
		}
		engine.mu.Unlock()
	}

	engine := startDeferredAccept(t, adaptor)
	engineRef.Store(engine)
	complete := adaptor.completion(t)
	if got := adaptor.deferCalls(); got != 1 {
		t.Fatalf("DeferLedgerAccept calls = %d, want 1", got)
	}

	engine.mu.RLock()
	phaseBefore := engine.phase
	buildingBefore := engine.buildInProgress
	engine.mu.RUnlock()
	if phaseBefore != consensus.PhaseAccepted || !buildingBefore {
		t.Fatalf("deferred state = (%v, building=%t), want (Accepted, true)", phaseBefore, buildingBefore)
	}
	if got := base.lastLCL.Seq(); got != 100 {
		t.Fatalf("ledger built before deferred callback: seq = %d, want 100", got)
	}

	complete()
	complete()

	if got := builds.Load(); got != 1 {
		t.Fatalf("BuildLedger calls = %d, want 1", got)
	}
	if got := broadcasts.Load(); got == 0 {
		t.Fatal("deferred commit did not flush its queued broadcasts")
	}
	if got := base.lastLCL.Seq(); got != 101 {
		t.Fatalf("accepted ledger seq = %d, want 101", got)
	}
	engine.mu.RLock()
	buildingAfter := engine.buildInProgress
	consensusCount := engine.consensusCount
	engine.mu.RUnlock()
	if buildingAfter {
		t.Fatal("buildInProgress remained set after deferred completion")
	}
	if consensusCount != 1 {
		t.Fatalf("consensus count = %d, want 1", consensusCount)
	}
}

func TestDeferredLedgerAcceptRejectsRoundChangesUntilCompletion(t *testing.T) {
	base := newMockAdaptor()
	adaptor := &deferredAcceptAdaptor{mockAdaptor: base}
	engine := startDeferredAccept(t, adaptor)

	engine.mu.RLock()
	roundBefore := engine.state.Round
	prevBefore := engine.prevLedger
	engine.mu.RUnlock()

	attemptedRound := consensus.RoundID{Seq: 999, ParentHash: consensus.LedgerID{0x99}}
	if err := engine.StartRound(attemptedRound, true); !errors.Is(err, errLedgerAcceptInProgress) {
		t.Fatalf("StartRound error = %v, want %v", err, errLedgerAcceptInProgress)
	}
	if err := engine.RestartRound(true); !errors.Is(err, errLedgerAcceptInProgress) {
		t.Fatalf("RestartRound error = %v, want %v", err, errLedgerAcceptInProgress)
	}

	engine.mu.RLock()
	roundAfter := engine.state.Round
	prevAfter := engine.prevLedger
	building := engine.buildInProgress
	engine.mu.RUnlock()
	if roundAfter != roundBefore || prevAfter != prevBefore || !building {
		t.Fatalf("rejected restart changed deferred round: round=%v prev=%v building=%t", roundAfter, prevAfter, building)
	}
	if got := base.lastLCL.Seq(); got != 100 {
		t.Fatalf("rejected restart changed last closed ledger to seq %d", got)
	}

	adaptor.completion(t)()
	if err := engine.RestartRound(true); err != nil {
		t.Fatalf("RestartRound after completion: %v", err)
	}
	engine.mu.RLock()
	restartedRound := engine.state.Round
	engine.mu.RUnlock()
	if restartedRound.Seq != 102 || restartedRound.ParentHash != base.lastLCL.ID() {
		t.Fatalf("restarted round = %v, want seq 102 on accepted LCL", restartedRound)
	}
}

func TestRestartRoundRejectsMissingLastClosedLedger(t *testing.T) {
	base := newMockAdaptor()
	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(base, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	base.mu.Lock()
	base.lastLCL = nil
	base.mu.Unlock()
	if err := engine.RestartRound(true); !errors.Is(err, errNoLastClosedLedger) {
		t.Fatalf("RestartRound error = %v, want %v", err, errNoLastClosedLedger)
	}
	if engine.state != nil {
		t.Fatalf("missing-LCL restart created round state: %+v", engine.state)
	}
}

func TestDeferredLedgerAcceptParksTimerUntilCompletion(t *testing.T) {
	base := newMockAdaptor()
	base.standalone = true
	adaptor := &deferredAcceptAdaptor{mockAdaptor: base}
	engine := startDeferredAccept(t, adaptor)

	engine.TimerEntry()

	engine.mu.RLock()
	phase := engine.phase
	building := engine.buildInProgress
	round := engine.state.Round
	engine.mu.RUnlock()
	if phase != consensus.PhaseAccepted || !building {
		t.Fatalf("timer advanced deferred state to (%v, building=%t)", phase, building)
	}
	if round.Seq != 101 {
		t.Fatalf("timer advanced round to %d while acceptance was deferred", round.Seq)
	}
	if got := base.lastLCL.Seq(); got != 100 {
		t.Fatalf("timer built ledger seq %d before completion", got)
	}

	adaptor.completion(t)()
	if got := base.lastLCL.Seq(); got != 101 {
		t.Fatalf("accepted ledger seq = %d, want 101", got)
	}
}

func TestDeferredLedgerAcceptBuildFailureRestoresEstablish(t *testing.T) {
	base := newMockAdaptor()
	base.buildLedgerErr = errors.New("build failed")
	adaptor := &deferredAcceptAdaptor{mockAdaptor: base}
	engine := startDeferredAccept(t, adaptor)

	adaptor.completion(t)()

	engine.mu.RLock()
	phase := engine.phase
	building := engine.buildInProgress
	consensusCount := engine.consensusCount
	engine.mu.RUnlock()
	if phase != consensus.PhaseEstablish {
		t.Fatalf("failed build phase = %v, want Establish", phase)
	}
	if building {
		t.Fatal("buildInProgress remained set after failed deferred build")
	}
	if consensusCount != 0 {
		t.Fatalf("consensus count = %d after failed build, want 0", consensusCount)
	}
	if got := base.lastLCL.Seq(); got != 100 {
		t.Fatalf("failed build changed last closed ledger to seq %d", got)
	}
}

func TestEngineStopJoinsAcceptanceDeferrerBeforeEventBusClose(t *testing.T) {
	base := newMockAdaptor()
	deferred := &deferredAcceptAdaptor{mockAdaptor: base}
	adaptor := &lifecycleDeferredAcceptAdaptor{
		deferredAcceptAdaptor: deferred,
		stopEntered:           make(chan struct{}),
		stopRelease:           make(chan struct{}),
	}
	engine := NewEngine(adaptor, DefaultConfig())
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- engine.Stop() }()
	select {
	case <-adaptor.stopEntered:
	case <-time.After(time.Second):
		t.Fatal("Engine.Stop did not join the acceptance deferrer")
	}

	if !engine.eventBus.Publish(&consensus.TimerFiredEvent{}) {
		t.Fatal("Engine.Stop closed the event bus before acceptance drain completed")
	}
	close(adaptor.stopRelease)
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Engine.Stop did not return after acceptance drain")
	}
}

func TestDeferredLedgerAcceptIgnoresLateTxSetDuringBuild(t *testing.T) {
	for _, failBuild := range []bool{false, true} {
		name := "success"
		if failBuild {
			name = "failed build retry"
		}
		t.Run(name, func(t *testing.T) {
			base := newMockAdaptor()
			base.standalone = true
			if failBuild {
				base.buildLedgerErr = errors.New("build failed")
			}
			adaptor := &deferredAcceptAdaptor{mockAdaptor: base}
			engine := startDeferredAccept(t, adaptor)
			lateID := consensus.TxSetID{1}
			lateTx := make([]byte, 32)
			lateTx[0] = 0x42
			base.buildLedgerHook = func() {
				if err := engine.OnTxSet(lateID, [][]byte{lateTx}); err != nil {
					t.Fatalf("late OnTxSet: %v", err)
				}
				base.mu.RLock()
				cached := base.txSets[lateID]
				base.mu.RUnlock()
				if cached == nil || cached.Size() != 1 {
					t.Fatal("late set was not cached for peers")
				}
				engine.mu.RLock()
				_, acquired := engine.acquiredTxSets[lateID]
				_, compared := engine.comparesTxSets[lateID]
				disputed := engine.disputeTracker.disputedNoTxs()
				engine.mu.RUnlock()
				if acquired || compared || len(disputed) != 0 {
					t.Fatalf("late set changed accepted round: acquired=%t compared=%t disputed=%d", acquired, compared, len(disputed))
				}
			}
			adaptor.completion(t)()

			if failBuild {
				engine.mu.RLock()
				phase := engine.phase
				building := engine.buildInProgress
				disputed := engine.disputeTracker.disputedNoTxs()
				engine.mu.RUnlock()
				if phase != consensus.PhaseEstablish || building || len(disputed) != 0 {
					t.Fatalf("failed build state: phase=%v building=%t disputed=%d", phase, building, len(disputed))
				}
				base.buildLedgerErr = nil
				engine.mu.Lock()
				engine.acceptLedger(consensus.ResultSuccess)
				engine.mu.Unlock()
				adaptor.completion(t)()
			}
			if got := base.lastLCL.Seq(); got != 101 {
				t.Fatalf("accepted ledger seq = %d, want 101", got)
			}
			if got := len(base.lastLCL.(*mockLedger).txs); got != 0 {
				t.Fatalf("accepted ledger contains %d transactions, want 0", got)
			}
		})
	}
}
