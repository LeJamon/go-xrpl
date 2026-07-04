package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// reporterLedger is a mockLedger that also reports close-time agreement,
// satisfying closeAgreementReporter so lastCloseBaseline can consult it.
type reporterLedger struct {
	*mockLedger
	closeAgree      bool
	parentCloseTime time.Time
}

func (l *reporterLedger) CloseAgree() bool           { return l.closeAgree }
func (l *reporterLedger) ParentCloseTime() time.Time { return l.parentCloseTime }

// TestEngine_LastCloseBaseline mirrors rippled's previousCloseCorrect branch
// (Consensus.h phaseOpen): the idle timer measures from the previous ledger's
// stored close time only when that close was reached by consensus; otherwise
// it measures from our own observed close (prevCloseTime).
func TestEngine_LastCloseBaseline(t *testing.T) {
	ledgerClose := time.Unix(1_000_000, 0).UTC()
	observed := time.Unix(1_234_567, 0).UTC()

	reporter := func(agree bool, parentClose time.Time) *reporterLedger {
		return &reporterLedger{
			mockLedger:      &mockLedger{id: consensus.LedgerID{9}, seq: 500, closeTime: ledgerClose},
			closeAgree:      agree,
			parentCloseTime: parentClose,
		}
	}
	nonDefaultParent := ledgerClose.Add(-10 * time.Second) // parentClose+1s != ledgerClose
	defaultParent := ledgerClose.Add(-time.Second)         // parentClose+1s == ledgerClose

	tests := []struct {
		name     string
		prev     consensus.Ledger
		mode     consensus.Mode
		wantBase time.Time
	}{
		{"agreed close uses ledger close", reporter(true, nonDefaultParent), consensus.ModeObserving, ledgerClose},
		{"wrong ledger uses observed close", reporter(true, nonDefaultParent), consensus.ModeWrongLedger, observed},
		{"no close agreement uses observed close", reporter(false, nonDefaultParent), consensus.ModeObserving, observed},
		{"defaulted parentClose+1s uses observed close", reporter(true, defaultParent), consensus.ModeObserving, observed},
		{"non-reporter ledger falls back to ledger close", &mockLedger{id: consensus.LedgerID{7}, seq: 500, closeTime: ledgerClose}, consensus.ModeObserving, ledgerClose},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine(newMockAdaptor(), DefaultConfig())
			e.prevLedger = tt.prev
			e.mode = tt.mode
			e.prevCloseTime = observed

			if got := e.lastCloseBaseline(); !got.Equal(tt.wantBase) {
				t.Fatalf("lastCloseBaseline() = %v, want %v", got, tt.wantBase)
			}
		})
	}
}

// TestEngine_PrevCloseTime_SeededAcrossRounds checks the cross-round carry of
// our own observed close time (rippled prevCloseTime_): seeded from the seed
// ledger on the first round, then from the self close time of the round that
// just ended.
func TestEngine_PrevCloseTime_SeededAcrossRounds(t *testing.T) {
	e := NewEngine(newMockAdaptor(), DefaultConfig())

	seedClose := time.Unix(1_000_000, 0).UTC()
	e.prevLedger = &mockLedger{id: consensus.LedgerID{1}, seq: 100, closeTime: seedClose}

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if !e.prevCloseTime.Equal(seedClose) {
		t.Fatalf("first round: prevCloseTime = %v, want seed ledger close %v", e.prevCloseTime, seedClose)
	}

	// Simulate closing our own ledger this round.
	selfClose := time.Unix(1_000_050, 0).UTC()
	e.state.CloseTimes.Self = selfClose

	round2 := consensus.RoundID{Seq: 102, ParentHash: consensus.LedgerID{1}}
	if err := e.StartRound(round2, false); err != nil {
		t.Fatalf("StartRound (round 2): %v", err)
	}
	if !e.prevCloseTime.Equal(selfClose) {
		t.Fatalf("second round: prevCloseTime = %v, want our observed close %v", e.prevCloseTime, selfClose)
	}
}
