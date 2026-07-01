package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// chainLedger builds a mockLedger at seq with id first-byte idb and parent
// id first-byte pb, so a chain can be wired by matching child.parentID to
// parent.id. A current close time keeps the fixture inside the switch-site
// canBeCurrent plausibility window.
func chainLedger(seq uint32, idb, pb byte) *mockLedger {
	return &mockLedger{
		id:        consensus.LedgerID{idb},
		seq:       seq,
		parentID:  consensus.LedgerID{pb},
		closeTime: time.Now(),
	}
}

// Issue #724: while in ModeWrongLedger, OnLedger must jump prevLedger to the
// FURTHEST locally-available parent-hash-chained ledger (rippled
// findNewLedgersToPublish parity), instead of crawling one ledger per
// acquisition, and must never move backward on an out-of-order arrival.

func TestEngine_OnLedger_JumpsToFurthestChained(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}] // seq 100

	// Chain 101..106 forward from the initial ledger (id {1}).
	a.StoreLedger(chainLedger(101, 101, 1))
	a.StoreLedger(chainLedger(102, 102, 101))
	a.StoreLedger(chainLedger(103, 103, 102))
	a.StoreLedger(chainLedger(104, 104, 103))
	a.StoreLedger(chainLedger(105, 105, 104))
	a.StoreLedger(chainLedger(106, 106, 105))

	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 106 {
		t.Fatalf("prevLedger.Seq() = %d, want 106 (must jump to furthest chained, not crawl one at a time)", got)
	}
}

func TestEngine_OnLedger_NeverMovesBackward(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	a.StoreLedger(chainLedger(106, 106, 105))
	top := a.ledgers[consensus.LedgerID{106}]
	e.prevLedger = top
	e.mode = consensus.ModeWrongLedger

	// An out-of-order acquisition completion for an older seq must not
	// regress the round.
	a.StoreLedger(chainLedger(102, 102, 101))
	if err := e.OnLedger(consensus.LedgerID{102}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 106 {
		t.Fatalf("prevLedger.Seq() = %d, want 106 (must not move backward)", got)
	}
}

func TestEngine_OnLedger_StopsAtChainBreak(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	a.StoreLedger(chainLedger(101, 101, 1))
	a.StoreLedger(chainLedger(102, 102, 101))
	a.StoreLedger(chainLedger(103, 103, 0xFF)) // parent does not chain — sibling fork
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 102 {
		t.Fatalf("prevLedger.Seq() = %d, want 102 (must stop at the unchained fork, never jump onto seq 103)", got)
	}
}

func TestEngine_OnLedger_StopsAtGap(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	a.StoreLedger(chainLedger(101, 101, 1))
	a.StoreLedger(chainLedger(102, 102, 101))
	a.StoreLedger(chainLedger(104, 104, 103)) // seq 103 absent → gap
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 102 {
		t.Fatalf("prevLedger.Seq() = %d, want 102 (walk stops at the seq-103 gap)", got)
	}
}
