package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestStartRoundLocked_RecoveryPreservesPreviousCloseTime(t *testing.T) {
	adaptor := newMockAdaptor()
	ledger := &mockLedger{
		id:        consensus.LedgerID{2},
		seq:       2,
		closeTime: adaptor.Now(),
	}
	engine := NewEngine(adaptor, DefaultConfig())
	engine.mu.Lock()
	engine.firstRound = false
	engine.prevLedger = ledger
	baseline := adaptor.Now().Add(-time.Second)
	engine.prevCloseTime = baseline
	engine.state = &roundState{
		CloseTimes: consensus.CloseTimes{Self: time.Time{}},
	}

	err := engine.startRoundLocked(consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}, true, true)
	got := engine.prevCloseTime
	engine.mu.Unlock()
	if err != nil {
		t.Fatalf("start recovery round: %v", err)
	}
	if !got.Equal(baseline) {
		t.Fatalf("previous close time = %v, want preserved baseline %v", got, baseline)
	}
}

func TestCheckLedger_SwitchedModeRetainsConsensusParentUntilAccept(t *testing.T) {
	adaptor := newMockAdaptor()
	accepted := &mockLedger{
		id:        consensus.LedgerID{2},
		seq:       2,
		closeTime: adaptor.Now(),
	}
	preferred := &mockLedger{
		id:        consensus.LedgerID{3},
		seq:       3,
		parentID:  accepted.ID(),
		closeTime: adaptor.Now(),
	}
	adaptor.lastLCL = accepted
	adaptor.ledgers[accepted.ID()] = accepted
	adaptor.ledgers[preferred.ID()] = preferred

	engine := NewEngine(adaptor, DefaultConfig())
	engine.prevLedger = preferred
	engine.mode = consensus.ModeSwitchedLedger

	engine.mu.Lock()
	engine.checkLedger()
	gotPrev := engine.prevLedger.ID()
	gotMode := engine.mode
	engine.mu.Unlock()

	wantPrev := preferred.ID()
	if gotPrev != wantPrev {
		t.Fatalf("consensus parent = %x, want switched ledger %x", gotPrev[:4], wantPrev[:4])
	}
	if gotMode != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want SwitchedLedger until recovery round accepts", gotMode)
	}
	if adaptor.lastLCL.ID() != accepted.ID() {
		t.Fatal("checkLedger changed the adaptor's accepted LCL")
	}
	if len(adaptor.switchedLedgers) != 0 {
		t.Fatalf("checkLedger announced %d spurious recovery switches", len(adaptor.switchedLedgers))
	}
}
