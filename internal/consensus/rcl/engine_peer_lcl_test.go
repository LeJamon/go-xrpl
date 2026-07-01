package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// The #1161 wedge: a fallen-behind node self-closes a minority ledger while
// every peer reports LCL X via statusChange, but no trusted validation for X
// is locally placeable. The old trusted-backing gate dropped every peer vote
// (getNetworkLedger) and the netSupport gate blocked the switch (checkLedger),
// so the node churned its fork forever. rippled counts peer LCLs ungated
// (NetworkOPs.cpp:1915-1921) and protects the switch with acquire-then-verify
// instead — the node must adopt the unanimous, locally-held, compatible X.
func TestEngine_CheckLedger_UnanimousPeerLCL_NoTrustedBacking_Switches(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	ourID := consensus.LedgerID{0x0C}
	targetID := consensus.LedgerID{0xAA}
	adaptor.ledgers[targetID] = &mockLedger{
		id:        targetID,
		seq:       105,
		parentID:  consensus.LedgerID{0xA9},
		closeTime: time.Now(),
	}
	adaptor.peerLCLs = []consensus.LedgerID{targetID, targetID, targetID, targetID, targetID}

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: ourID}, true)

	engine.mu.Lock()
	engine.prevLedger = &mockLedger{
		id: ourID, seq: 100, parentID: consensus.LedgerID{0x99}, closeTime: adaptor.now,
	}
	// No trusted validations for targetID anywhere — the gate this test kills.
	engine.checkLedger()
	gotMode := engine.mode
	gotPrev := engine.prevLedger.ID()
	engine.mu.Unlock()

	if gotPrev != targetID {
		t.Fatalf("prevLedger = %x, want the unanimous peer LCL %x (trusted-backing gate must be gone)", gotPrev[:2], targetID[:2])
	}
	if gotMode != consensus.ModeSwitchedLedger {
		t.Fatalf("mode = %v, want SwitchedLedger after adopting the network ledger", gotMode)
	}
}

// The acquire-then-verify half of the rippled model (NetworkOPs.cpp:1953-1962):
// peer votes are counted ungated, but a candidate that rewinds behind the
// validated tip fails canBeCurrent and the switch is refused.
func TestEngine_CheckLedger_RefusesSwitchBehindValidated(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	ourID := consensus.LedgerID{0x0C}
	targetID := consensus.LedgerID{0xAA}
	validatedID := consensus.LedgerID{0xEE}
	adaptor.ledgers[targetID] = &mockLedger{
		id: targetID, seq: 103, parentID: consensus.LedgerID{0xA9}, closeTime: time.Now(),
	}
	adaptor.ledgers[validatedID] = &mockLedger{
		id: validatedID, seq: 110, parentID: consensus.LedgerID{0xA8}, closeTime: time.Now(),
	}
	adaptor.validatedLedgerHashOverride = validatedID
	adaptor.peerLCLs = []consensus.LedgerID{targetID, targetID, targetID, targetID, targetID}

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: ourID}, true)

	engine.mu.Lock()
	engine.prevLedger = &mockLedger{
		id: ourID, seq: 100, parentID: consensus.LedgerID{0x99}, closeTime: adaptor.now,
	}
	engine.checkLedger()
	gotPrev := engine.prevLedger.ID()
	engine.mu.Unlock()

	if gotPrev != ourID {
		t.Fatalf("prevLedger = %x — a candidate behind the validated tip must be refused (canBeCurrent)", gotPrev[:2])
	}
}
