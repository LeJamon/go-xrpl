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

func stageQuorumValidatedLedger(e *Engine, a *mockAdaptor, l consensus.Ledger) {
	nodes := []consensus.NodeID{{0x21}, {0x22}}
	e.validationTracker = NewValidationTracker(len(nodes))
	e.validationTracker.SetTrustedAndQuorum(nodes, len(nodes))
	e.validationTracker.SetNow(a.Now)
	now := a.Now()
	for _, node := range nodes {
		e.validationTracker.Add(&consensus.Validation{
			NodeID:    node,
			LedgerID:  l.ID(),
			LedgerSeq: l.Seq(),
			Full:      true,
			SignTime:  now,
			SeenTime:  now,
		})
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

func TestEngine_TrySwitchToLedger_AcceptsEligibleLedger(t *testing.T) {
	tests := []struct {
		name         string
		selectLedger func(*Engine, *mockAdaptor, consensus.LedgerID)
	}{
		{
			name: "exact wrong-ledger target",
			selectLedger: func(e *Engine, _ *mockAdaptor, id consensus.LedgerID) {
				e.mode = consensus.ModeWrongLedger
				e.wrongLedgerID = id
			},
		},
		{
			name: "validated tip",
			selectLedger: func(e *Engine, a *mockAdaptor, id consensus.LedgerID) {
				e.mode = consensus.ModeObserving
				a.validatedLedgerHashOverride = id
			},
		},
		{
			name: "quorum validated candidate",
			selectLedger: func(e *Engine, a *mockAdaptor, id consensus.LedgerID) {
				e.mode = consensus.ModeObserving
				stageQuorumValidatedLedger(e, a, a.ledgers[id])
			},
		},
		{
			name: "network preference",
			selectLedger: func(e *Engine, a *mockAdaptor, id consensus.LedgerID) {
				e.mode = consensus.ModeObserving
				a.peerLCLs = []consensus.LedgerID{id, id}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newMockAdaptor()
			e := NewEngine(a, DefaultConfig())
			initial := a.ledgers[consensus.LedgerID{1}]
			target := chainLedger(101, 101, 1)
			a.ledgers[target.ID()] = target
			descendant := chainLedger(102, 102, 101)
			a.ledgers[descendant.ID()] = descendant
			e.prevLedger = initial
			tt.selectLedger(e, a, target.ID())

			got, err := e.TrySwitchToLedger(target.ID())
			if err != nil {
				t.Fatalf("TrySwitchToLedger: %v", err)
			}
			if got != consensus.LedgerSwitchAccepted {
				t.Fatalf("result = %v, want Accepted", got)
			}
			if e.prevLedger.ID() != target.ID() {
				t.Fatal("eligible ledger was not selected")
			}
			if got := e.Mode(); got != consensus.ModeSwitchedLedger {
				t.Fatalf("mode = %v, want switchedLedger", got)
			}
			if len(a.switchedLedgers) != 1 || a.switchedLedgers[0].ID() != target.ID() {
				t.Fatalf("switched ledgers = %v, want target", a.switchedLedgers)
			}
		})
	}
}

func TestEngine_TrySwitchToLedger_CurrentLedgerIsIdempotent(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	current := a.ledgers[consensus.LedgerID{1}]
	e.prevLedger = current
	e.setMode(consensus.ModeProposing)
	a.validatedLedgerHashOverride = current.ID()

	got, err := e.TrySwitchToLedger(current.ID())
	if err != nil {
		t.Fatalf("TrySwitchToLedger: %v", err)
	}
	if got != consensus.LedgerSwitchAccepted {
		t.Fatalf("result = %v, want Accepted", got)
	}
	if e.prevLedger.ID() != current.ID() {
		t.Fatal("idempotent switch changed the consensus parent")
	}
	if got := e.Mode(); got != consensus.ModeProposing {
		t.Fatalf("mode = %v, want proposing", got)
	}
	if len(a.switchedLedgers) != 0 {
		t.Fatal("idempotent switch announced a ledger change")
	}
}

func TestEngine_TrySwitchToLedger_CurrentLedgerRestartsWrongLedgerRecovery(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	current := a.ledgers[consensus.LedgerID{1}]
	e.prevLedger = current
	e.setMode(consensus.ModeWrongLedger)
	e.wrongLedgerID = current.ID()
	a.validatedLedgerHashOverride = current.ID()

	got, err := e.TrySwitchToLedger(current.ID())
	if err != nil {
		t.Fatalf("TrySwitchToLedger: %v", err)
	}
	if got != consensus.LedgerSwitchAccepted {
		t.Fatalf("result = %v, want Accepted", got)
	}
	if got := e.Mode(); got != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want switchedLedger", got)
	}
	if len(a.switchedLedgers) != 1 || a.switchedLedgers[0].ID() != current.ID() {
		t.Fatalf("switched ledgers = %v, want current recovery target", a.switchedLedgers)
	}
}

func TestEngine_TrySwitchToLedger_ReturnsBusyWithoutQueuing(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(101, 101, 1)
	a.ledgers[target.ID()] = target
	e.prevLedger = initial
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()
	e.buildInProgress = true

	got, err := e.TrySwitchToLedger(target.ID())
	if err != nil {
		t.Fatalf("TrySwitchToLedger: %v", err)
	}
	if got != consensus.LedgerSwitchBusy {
		t.Fatalf("result = %v, want Busy", got)
	}
	if e.prevLedger.ID() != initial.ID() {
		t.Fatal("busy switch changed the consensus parent")
	}
	if len(a.switchedLedgers) != 0 {
		t.Fatal("busy switch announced a ledger change")
	}
}

func TestEngine_TrySwitchToLedger_RejectsUnsafeLedger(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(101, 101, 1)
	target.closeTime = a.Now().Add(-6 * time.Minute)
	a.ledgers[target.ID()] = target
	a.validatedLedgerHashOverride = target.ID()
	e.prevLedger = initial
	e.mode = consensus.ModeObserving

	got, err := e.TrySwitchToLedger(target.ID())
	if err != nil {
		t.Fatalf("TrySwitchToLedger: %v", err)
	}
	if got != consensus.LedgerSwitchRejected {
		t.Fatalf("result = %v, want Rejected", got)
	}
	if e.prevLedger.ID() != initial.ID() {
		t.Fatal("rejected switch changed the consensus parent")
	}
	if len(a.switchedLedgers) != 0 {
		t.Fatal("rejected switch announced a ledger change")
	}
}

type fastLoadProvisionalMockAdaptor struct {
	*mockAdaptor
	provisional bool
}

func (a *fastLoadProvisionalMockAdaptor) IsFastLoadProvisional() bool {
	return a.provisional
}

func TestEngine_TrySwitchToLedger_FastLoadSameHeightReplacement(t *testing.T) {
	tests := []struct {
		name           string
		provisional    bool
		stageQuorum    bool
		selectRecovery bool
		want           consensus.LedgerSwitchResult
	}{
		{
			name:        "provisional with live quorum",
			provisional: true,
			stageQuorum: true,
			want:        consensus.LedgerSwitchAccepted,
		},
		{
			name:        "not provisional",
			provisional: false,
			stageQuorum: true,
			want:        consensus.LedgerSwitchRejected,
		},
		{
			name:           "no live quorum",
			provisional:    true,
			selectRecovery: true,
			want:           consensus.LedgerSwitchRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newMockAdaptor()
			a := &fastLoadProvisionalMockAdaptor{
				mockAdaptor: base,
				provisional: tt.provisional,
			}
			e := NewEngine(a, DefaultConfig())
			initial := base.ledgers[consensus.LedgerID{1}]
			target := chainLedger(initial.Seq(), 101, initial.ParentID()[0])
			base.ledgers[target.ID()] = target
			base.validatedLedgerHashOverride = initial.ID()
			e.prevLedger = initial
			e.mode = consensus.ModeObserving

			if tt.stageQuorum {
				stageQuorumValidatedLedger(e, base, target)
			}
			if tt.selectRecovery {
				e.mode = consensus.ModeWrongLedger
				e.wrongLedgerID = target.ID()
			}

			got, err := e.TrySwitchToLedger(target.ID())
			if err != nil {
				t.Fatalf("TrySwitchToLedger: %v", err)
			}
			if got != tt.want {
				t.Fatalf("result = %v, want %v", got, tt.want)
			}
			if tt.want == consensus.LedgerSwitchAccepted {
				if e.prevLedger.ID() != target.ID() {
					t.Fatal("accepted replacement did not become the consensus parent")
				}
				return
			}
			if e.prevLedger.ID() != initial.ID() {
				t.Fatal("rejected replacement changed the consensus parent")
			}
			if len(base.switchedLedgers) != 0 {
				t.Fatal("rejected replacement announced a ledger change")
			}
		})
	}
}

func TestEngine_TrySwitchToLedger_IgnoresUnselectedLedger(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	initial := a.ledgers[consensus.LedgerID{1}]
	target := chainLedger(101, 101, 1)
	a.ledgers[target.ID()] = target
	e.prevLedger = initial
	e.mode = consensus.ModeObserving

	got, err := e.TrySwitchToLedger(target.ID())
	if err != nil {
		t.Fatalf("TrySwitchToLedger: %v", err)
	}
	if got != consensus.LedgerSwitchIrrelevant {
		t.Fatalf("result = %v, want Irrelevant", got)
	}
	if e.prevLedger.ID() != initial.ID() {
		t.Fatal("irrelevant switch changed the consensus parent")
	}
	if len(a.switchedLedgers) != 0 {
		t.Fatal("irrelevant switch announced a ledger change")
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
