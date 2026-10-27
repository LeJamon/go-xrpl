package rcl

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestEngine_FailedBuildRequestsAuthenticatedLedger(t *testing.T) {
	a := newMockAdaptor()
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	if err := e.StartRound(consensus.RoundID{Seq: a.lastLCL.Seq() + 1, ParentHash: a.lastLCL.ID()}, true); err != nil {
		t.Fatal(err)
	}
	e.phase = consensus.PhaseAccepted
	e.buildInProgress = true
	canonical := consensus.LedgerID{9}
	seedAcceptanceQuorum(t, e, canonical, a.lastLCL.Seq()+1)
	e.mu.Lock()
	e.commitAcceptedLedgerLocked(ledgerAcceptWork{prevLedger: a.lastLCL}, nil, errors.New("publication rejected"))
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	runPostUnlock(pending)
	if e.buildInProgress || e.buildingLedgerSeq.Load() != 0 {
		t.Fatal("rejected build retained acceptance ownership")
	}
	if len(a.ledgersRequested) != 1 || a.ledgersRequested[0] != canonical {
		t.Fatalf("recovery requests = %x, want authenticated ledger %x", a.ledgersRequested, canonical)
	}
	if len(a.validationsBroadcast) != 0 {
		t.Fatal("rejected build emitted a validation")
	}
}

func TestEngine_QueuedValidationRechecksAuthenticatedConflict(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	e.mu.Lock()
	e.pendingValidationBroadcasts = append(e.pendingValidationBroadcasts,
		&consensus.Validation{LedgerID: consensus.LedgerID{8}, LedgerSeq: 101, Full: true})
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	seedAcceptanceQuorum(t, e, consensus.LedgerID{9}, 101)
	runPostUnlock(pending)
	if len(a.validationsBroadcast) != 0 {
		t.Fatal("queued validation ignored a newly authenticated conflict")
	}
}

func seedAcceptanceQuorum(t *testing.T, e *Engine, id consensus.LedgerID, seq uint32) {
	t.Helper()
	nodes := []consensus.NodeID{{2}, {3}}
	tracker := NewValidationTracker(2)
	tracker.SetTrustedAndQuorum(nodes, 2)
	e.validationTracker = tracker
	now := time.Now()
	for _, node := range nodes {
		if !tracker.Add(&consensus.Validation{NodeID: node, LedgerID: id, LedgerSeq: seq,
			SignTime: now, SeenTime: now, Full: true}) {
			t.Fatal("quorum fixture validation was rejected")
		}
	}
}

func TestEngine_AcceptLedger_DiscardsBuildAfterAuthenticatedConflict(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{2}, {3}})
	adaptor.quorum = 2

	canonical := chainLedger(101, 0xCC, 1)
	adaptor.mu.Lock()
	adaptor.ledgers[canonical.ID()] = canonical
	adaptor.mu.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	adaptor.mu.Lock()
	adaptor.buildLedgerHook = func() {
		enterOnce.Do(func() { close(entered) })
		<-release
	}
	adaptor.mu.Unlock()

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		if err := engine.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	if err := engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: adaptor.lastLCL.ID()}, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	driveToEstablish(t, engine, adaptor)

	acceptDone := make(chan struct{})
	go func() {
		engine.mu.Lock()
		engine.acceptLedger(consensus.ResultSuccess)
		engine.mu.Unlock()
		close(acceptDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("BuildLedger did not enter blocked phase")
	}

	validationTime := adaptor.Now()
	for _, node := range []consensus.NodeID{{2}, {3}} {
		if err := engine.OnValidation(&consensus.Validation{
			LedgerID: canonical.ID(), LedgerSeq: canonical.Seq(), NodeID: node,
			SignTime: validationTime, SeenTime: validationTime, Full: true,
		}, 0); err != nil {
			t.Fatalf("OnValidation(%x): %v", node, err)
		}
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-acceptDone:
	case <-time.After(time.Second):
		t.Fatal("acceptLedger did not finish after releasing the build")
	}

	engine.mu.RLock()
	gotPrev := engine.prevLedger
	gotPhase := engine.phase
	engine.mu.RUnlock()
	if gotPrev == nil || gotPrev.ID() != canonical.ID() {
		t.Fatalf("prev ledger = %v, want canonical %x", gotPrev, canonical.ID())
	}
	if gotPhase != consensus.PhaseOpen {
		t.Fatalf("phase after canonical recovery = %s, want open", gotPhase)
	}
	adaptor.mu.RLock()
	broadcasts := len(adaptor.validationsBroadcast)
	adaptor.mu.RUnlock()
	if broadcasts != 0 {
		t.Fatalf("stale build emitted %d validations, want none", broadcasts)
	}
}

func TestEngine_AuthenticatedQuorumConflictIsSequenceAndGateScoped(t *testing.T) {
	tests := []struct {
		name            string
		ledgerID        consensus.LedgerID
		ledgerSeq       uint32
		unavailable     bool
		validationCount int
		wantID          consensus.LedgerID
		wantConflict    bool
	}{
		{
			name:            "matching quorum",
			ledgerID:        consensus.LedgerID{0x65},
			ledgerSeq:       101,
			validationCount: 2,
			wantConflict:    false,
		},
		{
			name:            "no quorum",
			ledgerID:        consensus.LedgerID{0xCC},
			ledgerSeq:       101,
			validationCount: 1,
			wantConflict:    false,
		},
		{
			name:            "behind sequence",
			ledgerID:        consensus.LedgerID{0xCC},
			ledgerSeq:       100,
			validationCount: 2,
			wantConflict:    false,
		},
		{
			name:            "same-sequence conflict",
			ledgerID:        consensus.LedgerID{0xCC},
			ledgerSeq:       101,
			validationCount: 2,
			wantID:          consensus.LedgerID{0xCC},
			wantConflict:    true,
		},
		{
			name:            "quorum unavailable",
			ledgerID:        consensus.LedgerID{0xCC},
			ledgerSeq:       101,
			unavailable:     true,
			validationCount: 2,
			wantConflict:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newMockAdaptor()
			e := NewEngine(a, DefaultConfig())
			tracker := NewValidationTracker(2)
			tracker.SetNow(a.Now)
			tracker.SetTrustedAndQuorum([]consensus.NodeID{{2}, {3}}, 2)
			unavailable := tt.unavailable
			tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
			e.validationTracker = tracker
			validationCount := tt.validationCount
			if validationCount == 0 {
				validationCount = 2
			}
			nodes := []consensus.NodeID{{2}, {3}}
			for _, node := range nodes[:validationCount] {
				if !tracker.Add(&consensus.Validation{
					NodeID: node, LedgerID: tt.ledgerID, LedgerSeq: tt.ledgerSeq,
					Full: true, SignTime: a.Now(), SeenTime: a.Now(),
				}) {
					t.Fatalf("validation from node %x was rejected", node)
				}
			}
			candidate := chainLedger(101, 0x65, 1)
			gotID, gotConflict := e.authenticatedQuorumConflictLocked(candidate)
			if gotConflict != tt.wantConflict || gotID != tt.wantID {
				t.Fatalf("conflict = (%x, %v), want (%x, %v)", gotID, gotConflict, tt.wantID, tt.wantConflict)
			}
		})
	}
}
