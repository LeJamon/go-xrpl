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

// The consensus-island split observed in the 15k soak: the two goxrpl
// validators propose only to each other above the common validated tip (a
// 2-node island closing its own chain), while three rippled validators do
// the same on theirs. The proposal+peer-LCL tally deadlocks 3v3 (own vote
// counted, peer LCLs deduped against proposal hashes) and never switches
// back. rippled's getPrevLedger is PURE validations-preferred
// (RCLConsensus.cpp:301-303): 3 trusted validations beat 2, so the island
// node must target the majority branch — even though its own chain is
// AHEAD in sequence (Validations.h:892-895, lower-seq different-chain
// switch).
func TestEngine_CheckLedger_ValidationMajorityBreaksConsensusIsland(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Our island chain: common ancestor at seq 120, own ledgers 121..123.
	common := &mockLedger{id: consensus.LedgerID{0x20}, seq: 120, parentID: consensus.LedgerID{0x19}, closeTime: time.Now()}
	own121 := &mockLedger{id: consensus.LedgerID{0x21}, seq: 121, parentID: common.id, closeTime: time.Now()}
	own122 := &mockLedger{id: consensus.LedgerID{0x22}, seq: 122, parentID: own121.id, closeTime: time.Now()}
	adaptor.ledgers[common.id] = common
	adaptor.ledgers[own121.id] = own121
	adaptor.ledgers[own122.id] = own122

	// The rippled majority branch tip at a LOWER seq than our island tip,
	// not locally held.
	majorityTip := consensus.LedgerID{0xBB}

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	engine.StartRound(consensus.RoundID{Seq: 123, ParentHash: own122.id}, true)

	now := adaptor.now
	engine.mu.Lock()
	engine.prevLedger = own122

	// 3 trusted rippled validators on the majority branch at seq 121; our
	// island partner (and we) validated the island chain — 3 beats 2.
	partner := consensus.NodeID{0x51}
	adaptor.trusted[partner] = true
	islandVals := []*consensus.Validation{
		{NodeID: partner, LedgerID: own122.id, LedgerSeq: 122, Full: true, SignTime: now, SeenTime: now},
	}
	rippledNodes := []consensus.NodeID{{0x61}, {0x62}, {0x63}}
	trustedSet := make([]consensus.NodeID, 0, 1+len(rippledNodes))
	trustedSet = append(trustedSet, partner)
	for _, n := range rippledNodes {
		adaptor.trusted[n] = true
		trustedSet = append(trustedSet, n)
	}
	engine.validationTracker.SetTrusted(trustedSet)
	for _, v := range islandVals {
		engine.validationTracker.Add(v)
	}
	for _, n := range rippledNodes {
		engine.validationTracker.Add(&consensus.Validation{
			NodeID: n, LedgerID: majorityTip, LedgerSeq: 121, Full: true, SignTime: now, SeenTime: now,
		})
	}

	engine.checkLedger()
	gotMode := engine.mode
	engine.mu.Unlock()

	adaptor.mu.RLock()
	requested := append([]consensus.LedgerID{}, adaptor.ledgersRequested...)
	adaptor.mu.RUnlock()

	if gotMode != consensus.ModeWrongLedger {
		t.Fatalf("mode = %v, want WrongLedger (island node must chase the validation-majority branch)", gotMode)
	}
	sawTarget := false
	for _, id := range requested {
		if id == majorityTip {
			sawTarget = true
		}
	}
	if !sawTarget {
		t.Fatalf("island node must request the validation-majority tip %x, requested %v", majorityTip[:2], requested)
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
