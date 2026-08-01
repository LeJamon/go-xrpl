package rcl

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type deferredAcceptAdaptor struct {
	*mockAdaptor

	deferMu sync.Mutex
	accept  func()
	calls   int

	broadcastHook func()
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
	var engine *Engine
	base.buildLedgerHook = func() {
		builds.Add(1)
		if !engine.mu.TryLock() {
			t.Error("BuildLedger called with the engine lock held")
			return
		}
		engine.mu.Unlock()
	}
	adaptor.broadcastHook = func() {
		broadcasts.Add(1)
		if !engine.mu.TryLock() {
			t.Error("deferred acceptance flushed a broadcast with the engine lock held")
			return
		}
		engine.mu.Unlock()
	}

	engine = startDeferredAccept(t, adaptor)
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
