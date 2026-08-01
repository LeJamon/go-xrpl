package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type resolutionLedger struct {
	*mockLedger
	resolution time.Duration
	closeAgree bool
}

func (l *resolutionLedger) CloseTimeResolution() time.Duration { return l.resolution }
func (l *resolutionLedger) CloseAgree() bool                   { return l.closeAgree }

func TestStartRound_SwitchedLedgerUsesConsensusParentResolution(t *testing.T) {
	adaptor := newMockAdaptor()
	parent := &resolutionLedger{
		mockLedger: &mockLedger{
			id:        consensus.LedgerID{0x20},
			seq:       16,
			closeTime: adaptor.Now(),
		},
		resolution: 20 * time.Second,
		closeAgree: true,
	}
	adaptor.ledgers[parent.ID()] = parent

	engine := NewEngine(adaptor, DefaultConfig())
	engine.mu.Lock()
	engine.prevLedger = parent
	err := engine.startRoundLocked(consensus.RoundID{
		Seq:        parent.Seq() + 1,
		ParentHash: parent.ID(),
	}, true, true)
	if err != nil {
		engine.mu.Unlock()
		t.Fatalf("start switched round: %v", err)
	}
	resolution := engine.currentCloseTimeResolution()
	rawClose := time.Date(2000, time.January, 1, 0, 0, 17, 0, time.UTC)
	engine.state.CloseTimes.Peers[rawClose] = 1
	determined := engine.determineCloseTime()
	closesAtThirtySeconds := engine.closeOnTimers(time.Second, 30*time.Second)
	engine.mu.Unlock()

	if resolution != 20*time.Second {
		t.Fatalf("round close resolution = %s, want switched parent's 20s", resolution)
	}
	if want := time.Date(2000, time.January, 1, 0, 0, 20, 0, time.UTC); !determined.Equal(want) {
		t.Fatalf("observer close time = %s, want parent-resolution rounding to %s", determined, want)
	}
	if closesAtThirtySeconds {
		t.Fatal("empty switched round closed before twice the parent's stored resolution")
	}
	if got := engine.GetJSON(true)["close_resolution"]; got != int64(20) {
		t.Fatalf("consensus JSON close_resolution = %v, want 20", got)
	}
}
