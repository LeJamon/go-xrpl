package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestEngine_TimerRebasesWhenClosedLedgerAdvances(t *testing.T) {
	a := newMockAdaptor()
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	initial := a.lastLCL
	e.prevLedger = initial

	if err := e.StartRound(consensus.RoundID{Seq: initial.Seq() + 1, ParentHash: initial.ID()}, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	e.mu.Lock()
	e.setPhase(consensus.PhaseEstablish)
	e.mu.Unlock()

	adopted := chainLedger(115, 115, 114)
	a.mu.Lock()
	a.ledgers[adopted.ID()] = adopted
	a.lastLCL = adopted
	a.mu.Unlock()

	e.TimerEntry()

	if got := e.prevLedger.ID(); got != adopted.ID() {
		t.Fatalf("prev ledger = %x, want adopted ledger %x", got, adopted.ID())
	}
	if got := e.state.Round.Seq; got != adopted.Seq()+1 {
		t.Fatalf("round seq = %d, want %d", got, adopted.Seq()+1)
	}
	if got := e.Phase(); got != consensus.PhaseOpen {
		t.Fatalf("phase = %v, want Open", got)
	}
	if got := e.Mode(); got != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want SwitchedLedger", got)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.opMode != consensus.OpModeConnected {
		t.Fatalf("operating mode = %v, want Connected", a.opMode)
	}
	if len(a.switchedLedgers) != 1 || a.switchedLedgers[0].ID() != adopted.ID() {
		t.Fatalf("switched ledgers = %v, want adopted ledger", a.switchedLedgers)
	}
}

func TestEngine_TimerIgnoresStoredLedgerThatIsNotClosed(t *testing.T) {
	a := newMockAdaptor()
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	initial := a.lastLCL
	e.prevLedger = initial

	if err := e.StartRound(consensus.RoundID{Seq: initial.Seq() + 1, ParentHash: initial.ID()}, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	e.mu.Lock()
	e.setPhase(consensus.PhaseEstablish)
	e.mu.Unlock()

	held := chainLedger(115, 115, 114)
	a.mu.Lock()
	a.ledgers[held.ID()] = held
	a.mu.Unlock()

	e.TimerEntry()

	if got := e.prevLedger.ID(); got != initial.ID() {
		t.Fatalf("prev ledger = %x, want unchanged %x", got, initial.ID())
	}
	if got := e.state.Round.Seq; got != initial.Seq()+1 {
		t.Fatalf("round seq = %d, want %d", got, initial.Seq()+1)
	}
}
