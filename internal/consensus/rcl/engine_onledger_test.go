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

func TestEngine_OnLedger_SelectsExactWrongLedgerTarget(t *testing.T) {
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
	e.wrongLedgerID = consensus.LedgerID{101}

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 101 {
		t.Fatalf("prevLedger.Seq() = %d, want exact target 101", got)
	}
}

func TestEngine_OnLedger_NeverMovesBackward(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	a.StoreLedger(chainLedger(106, 106, 105))
	top := a.ledgers[consensus.LedgerID{106}]
	e.prevLedger = top
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{0xFF}

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

func TestEngine_OnLedger_ExactTargetIgnoresStoredForkDescendant(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	a.StoreLedger(chainLedger(101, 101, 1))
	a.StoreLedger(chainLedger(102, 102, 101))
	a.StoreLedger(chainLedger(103, 103, 0xFF)) // parent does not chain — sibling fork
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{101}

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 101 {
		t.Fatalf("prevLedger.Seq() = %d, want exact target 101", got)
	}
}

// Issue #1207: adopting a different LCL must fire OnLedgerSwitched so peers
// are told the jump (SWITCHED_LEDGER) and drop our abandoned ledger from
// their tallies.
func TestEngine_OnLedger_AnnouncesSwitchedLedger(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	a.StoreLedger(chainLedger(101, 101, 1))
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{101}

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.switchedLedgers) != 1 {
		t.Fatalf("OnLedgerSwitched calls = %d, want 1", len(a.switchedLedgers))
	}
	if got := a.switchedLedgers[0].Seq(); got != 101 {
		t.Fatalf("switched ledger seq = %d, want 101", got)
	}
}

func TestEngine_OnLedger_ExactTargetIgnoresStoredDescendantsAcrossGap(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	a.StoreLedger(chainLedger(101, 101, 1))
	a.StoreLedger(chainLedger(102, 102, 101))
	a.StoreLedger(chainLedger(104, 104, 103)) // seq 103 absent → gap
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{101}

	if err := e.OnLedger(consensus.LedgerID{101}, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.Seq(); got != 101 {
		t.Fatalf("prevLedger.Seq() = %d, want exact target 101", got)
	}
}

func TestEngine_OnLedger_IgnoresIntermediateWrongLedgerCompletion(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(101, 101, 1)
	intermediate := chainLedger(105, 105, 104)
	a.StoreLedger(target)
	a.StoreLedger(intermediate)
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()

	if err := e.OnLedger(intermediate.ID(), nil); err != nil {
		t.Fatalf("OnLedger(intermediate): %v", err)
	}
	if e.prevLedger.ID() != initial.ID() || e.wrongLedgerID != target.ID() || e.mode != consensus.ModeWrongLedger {
		t.Fatal("intermediate completion changed exact WrongLedger recovery state")
	}
	if err := e.OnLedger(target.ID(), nil); err != nil {
		t.Fatalf("OnLedger(target): %v", err)
	}
	if e.prevLedger.ID() != target.ID() {
		t.Fatal("exact WrongLedger target was not selected")
	}
}

func TestEngine_OnLedger_AcceptsValidatedTipBehindMovingWrongLedgerTarget(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	validated := chainLedger(105, 105, 104)
	a.StoreLedger(validated)
	a.validatedLedgerHashOverride = validated.ID()
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{108}

	if err := e.OnLedger(validated.ID(), nil); err != nil {
		t.Fatalf("OnLedger(validated): %v", err)
	}
	if e.prevLedger.ID() != validated.ID() {
		t.Fatal("validated recovery point was not selected after the preferred target advanced")
	}
	if e.mode != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want switchedLedger", e.mode)
	}
}

func TestEngine_CheckLedgerUsesHeldValidatedRecoveryPoint(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	validated := chainLedger(105, 105, 104)
	a.StoreLedger(validated)
	a.validatedLedgerHashOverride = validated.ID()
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{108}

	e.checkLedger()

	if e.prevLedger.ID() != validated.ID() {
		t.Fatal("held validated recovery point was not selected on the next ledger check")
	}
	if e.mode != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want switchedLedger", e.mode)
	}
}

func TestEngine_QueuedRecoveryCannotReplaceChangedWrongLedgerTarget(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	oldTarget := chainLedger(105, 105, 104)
	newTarget := chainLedger(101, 101, 1)
	a.StoreLedger(oldTarget)
	a.StoreLedger(newTarget)
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = oldTarget.ID()
	e.buildInProgress = true

	if err := e.OnLedger(oldTarget.ID(), nil); err != nil {
		t.Fatalf("OnLedger(old target): %v", err)
	}
	e.wrongLedgerID = newTarget.ID()
	if err := e.OnLedger(newTarget.ID(), nil); err != nil {
		t.Fatalf("OnLedger(new target): %v", err)
	}
	if e.pendingRecoveryLedger == nil || e.pendingRecoveryLedger.ID() != newTarget.ID() {
		t.Fatal("lower-sequence exact target did not replace stale queued recovery")
	}
	e.buildInProgress = false
	if !e.processPendingRecoveryLedgerLocked() {
		t.Fatal("queued exact target was not selected")
	}
	if e.prevLedger.ID() != newTarget.ID() {
		t.Fatal("queued stale target replaced the current exact target")
	}
}

func TestEngine_OnLedger_DefersNewestRecoveryLedgerDuringBuild(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	older := chainLedger(101, 101, 1)
	newer := chainLedger(105, 105, 104)
	a.StoreLedger(older)
	a.StoreLedger(newer)

	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = newer.ID()
	e.buildInProgress = true

	if err := e.OnLedger(older.ID(), nil); err != nil {
		t.Fatalf("OnLedger(older): %v", err)
	}
	if err := e.OnLedger(newer.ID(), nil); err != nil {
		t.Fatalf("OnLedger(newer): %v", err)
	}
	if got := e.prevLedger.Seq(); got != initial.Seq() {
		t.Fatalf("prevLedger.Seq() during build = %d, want %d", got, initial.Seq())
	}
	if e.pendingRecoveryLedger == nil || e.pendingRecoveryLedger.ID() != newer.ID() {
		t.Fatal("build window must retain the newest completed recovery ledger")
	}

	e.mu.Lock()
	e.buildInProgress = false
	switched := e.processPendingRecoveryLedgerLocked()
	e.mu.Unlock()
	if !switched {
		t.Fatal("commit tail did not process the deferred recovery ledger")
	}
	if got := e.prevLedger.ID(); got != newer.ID() {
		want := newer.ID()
		t.Fatalf("prevLedger = %x, want deferred ledger %x", got[:2], want[:2])
	}
	if got := e.Mode(); got != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want switchedLedger", got)
	}
}

func TestEngine_OnLedger_UsesValidatedTipOutsideRecovery(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(105, 105, 104)
	a.StoreLedger(target)
	a.validatedLedgerHashOverride = target.ID()

	e.prevLedger = initial
	e.mode = consensus.ModeObserving

	if err := e.OnLedger(target.ID(), nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.ID(); got != target.ID() {
		want := target.ID()
		t.Fatalf("prevLedger = %x, want acquired validated tip %x", got[:2], want[:2])
	}
	if got := e.Mode(); got != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want switchedLedger", got)
	}
}

func TestEngine_OnLedger_IgnoresOrdinaryAcquisitionOutsideRecovery(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(105, 105, 104)
	a.StoreLedger(target)

	e.prevLedger = initial
	e.mode = consensus.ModeObserving

	if err := e.OnLedger(target.ID(), nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}
	if got := e.prevLedger.ID(); got != initial.ID() {
		want := initial.ID()
		t.Fatalf("prevLedger = %x, want unchanged ledger %x", got[:2], want[:2])
	}
}

func TestEngine_CurrentPreferredLCL_RejectsValidatedTipAhead(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	current := a.ledgers[consensus.LedgerID{1}]
	validated := chainLedger(105, 105, 104)
	a.StoreLedger(validated)
	a.validatedLedgerHashOverride = validated.ID()

	e.mu.Lock()
	preferred := e.isCurrentPreferredLCLLocked(current)
	e.mu.Unlock()
	if preferred {
		t.Fatal("a consensus parent behind the validated frontier must not be eligible for Full participation")
	}
}
