package rcl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrietest"
)

// TestValidationTracker_TrustedPartialSteersButNotQuorum pins the A1
// contract: a trusted PARTIAL validation is tracked and steers
// branch selection through the trie (GetTrustedSupport / GetPreferred),
// but is excluded from the full-validation quorum count, so it cannot by
// itself fire the fully-validated callback. A later FULL validation does.
func TestValidationTracker_TrustedPartialSteersButNotQuorum(t *testing.T) {
	vt := NewValidationTracker(1, 5*time.Minute) // quorum = 1
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
	abc := b.Build("abc")
	abcd := b.Build("abcd")
	provider := newMapAncestryProvider()
	provider.add(abc)
	provider.add(abcd)

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(provider)

	var fired int
	vt.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) { fired++ })

	// A trusted PARTIAL validation (Full=false).
	partial := makeTrustedValidation(n1, abc.ID(), abc.Seq(), now)
	partial.Full = false
	if !vt.Add(partial) {
		t.Fatal("trusted partial should be tracked, not dropped at the door")
	}

	// Steering: the partial contributes trie branchSupport and is the
	// preferred tip.
	if got := vt.TrustedSupport(abc.ID()); got != 1 {
		t.Errorf("partial should steer trie branchSupport(abc): got %d, want 1", got)
	}
	if id, _, ok := vt.GetPreferred(0); !ok || id != abc.ID() {
		t.Errorf("GetPreferred should steer to abc from the partial: ok=%v id=%x", ok, id)
	}

	// Quorum: the partial is excluded from the full-validation count and
	// must not fire finality.
	if got := vt.TrustedValidationCount(abc.ID()); got != 0 {
		t.Errorf("partial must be excluded from full quorum count: got %d, want 0", got)
	}
	if fired != 0 {
		t.Errorf("partial alone must not fire fully-validated callback; fired=%d", fired)
	}

	// A FULL validation at a higher seq crosses quorum and fires once.
	full := makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now)
	if !vt.Add(full) {
		t.Fatal("full validation should be accepted")
	}
	if got := vt.TrustedValidationCount(abcd.ID()); got != 1 {
		t.Errorf("full validation should count toward quorum: got %d, want 1", got)
	}
	if fired != 1 {
		t.Errorf("full validation at quorum should fire callback exactly once; fired=%d", fired)
	}
}

// TestValidationTracker_AddStatus_Classification covers the A6
// classification of double-signs. Non-monotonic seqs are cross-checked
// against the validation tracked at that exact seq — not just the node's
// latest tip — so equivocation is flagged even at superseded seqs. Steps
// run in order against one tracker, sharing evidence and enforcer state.
func TestValidationTracker_AddStatus_Classification(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	vt := NewValidationTracker(10, 5*time.Minute)
	vt.SetNow(func() time.Time { return now })

	node := consensus.NodeID{0x9}
	mk := func(seq uint32, ledger consensus.LedgerID, signTime time.Time, cookie uint64) *consensus.Validation {
		return &consensus.Validation{
			LedgerSeq: seq, LedgerID: ledger, NodeID: node,
			SignTime: signTime, Cookie: cookie, Full: true,
		}
	}
	ledgerA := consensus.LedgerID{0xA}
	ledgerB := consensus.LedgerID{0xB}
	ledgerC := consensus.LedgerID{0xC}

	steps := []struct {
		name  string
		at    time.Duration // tracker clock offset from base
		flush bool          // run the FlushStale heartbeat sweep first
		v     *consensus.Validation
		want  ValStatus
	}{
		{name: "first validation", v: mk(100, ledgerA, base, 1), want: ValStatusCurrent},
		// Freshness gate (isCurrent) rejects a validation signed far in the
		// past before any evidence or enforcer state is touched, so the row
		// order around it is immaterial.
		{name: "stale sign time", v: mk(105, ledgerA, base.Add(-time.Hour), 1), want: ValStatusStale},
		{name: "identical resend", v: mk(100, ledgerA, base, 1), want: ValStatusBadSeq},
		{name: "same seq different ledger", v: mk(100, ledgerB, base, 1), want: ValStatusConflicting},
		{name: "same seq same ledger different signtime", v: mk(100, ledgerA, base.Add(time.Second), 1), want: ValStatusConflicting},
		{name: "same seq same ledger different cookie", v: mk(100, ledgerA, base, 2), want: ValStatusMultiple},
		{name: "tip advances", v: mk(101, ledgerC, base, 1), want: ValStatusCurrent},
		// The deep-detector case: the tip is at 101, yet the double-sign
		// at the superseded seq 100 is still flagged.
		{name: "conflict at superseded seq", v: mk(100, ledgerB, base, 1), want: ValStatusConflicting},
		{name: "unseen lower seq", v: mk(99, ledgerB, base, 1), want: ValStatusBadSeq},
		// Tracked evidence signed >validationCurrentWall before a newer
		// submission is disregarded and replaced: the same (100, B) pair
		// that was conflicting above degrades to badSeq.
		{name: "stale evidence disregarded", at: 6 * time.Minute, v: mk(100, ledgerB, base.Add(6*time.Minute), 1), want: ValStatusBadSeq},
		// After validationSetExpires idle the enforcer floor resets (and
		// the heartbeat sweep drops the stale tip + aged evidence), so a
		// node may legitimately re-validate a lower seq, e.g. after a
		// network restart.
		{name: "idle reset readmits lower seq", at: 17 * time.Minute, flush: true, v: mk(100, ledgerB, base.Add(17*time.Minute), 1), want: ValStatusCurrent},
	}
	for _, tc := range steps {
		now = base.Add(tc.at)
		if tc.flush {
			vt.FlushStale()
		}
		if got := vt.AddStatus(tc.v); got != tc.want {
			t.Errorf("%s: AddStatus = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestValidationTracker_AddStatus_PartialDoubleSign checks the double-sign
// detector on PARTIAL validations: classification runs before any Full-based
// branch, so a partial equivocation is flagged just like a full one.
func TestValidationTracker_AddStatus_PartialDoubleSign(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	vt := NewValidationTracker(10, 5*time.Minute)
	vt.SetNow(func() time.Time { return base })

	node := consensus.NodeID{0x7}
	partial := func(seq uint32, ledger consensus.LedgerID) *consensus.Validation {
		return &consensus.Validation{
			LedgerSeq: seq, LedgerID: ledger, NodeID: node,
			SignTime: base, Full: false,
		}
	}

	if got := vt.AddStatus(partial(100, consensus.LedgerID{0xA})); got != ValStatusCurrent {
		t.Fatalf("first partial: AddStatus = %v, want current", got)
	}
	if got := vt.AddStatus(partial(100, consensus.LedgerID{0xB})); got != ValStatusConflicting {
		t.Errorf("partial same-seq different-ledger: AddStatus = %v, want conflicting", got)
	}
}

// TestSeqEnforcer_IdleResetBoundary pins the monotonic-seq invariant and the
// exact validationSetExpires idle-reset edge: at the boundary the floor still
// holds; one tick past it the floor resets and re-admits a lower seq.
func TestSeqEnforcer_IdleResetBoundary(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var enf seqEnforcer

	if !enf.advance(base, 1) {
		t.Fatal("seq 1 must advance from an empty floor")
	}
	if !enf.advance(base, 10) {
		t.Fatal("seq 10 must advance past floor 1")
	}
	if enf.advance(base, 5) {
		t.Error("seq 5 must be rejected below floor 10")
	}
	if enf.advance(base, 9) {
		t.Error("seq 9 must be rejected below floor 10")
	}

	// At exactly validationSetExpires the floor has NOT expired (strict >),
	// so a lower seq is still rejected.
	if enf.advance(base.Add(validationSetExpires), 1) {
		t.Error("at exactly validationSetExpires the floor holds; seq 1 must be rejected")
	}
	// One tick past the window the floor resets to 0 and re-admits seq 1.
	if !enf.advance(base.Add(validationSetExpires+time.Nanosecond), 1) {
		t.Error("past validationSetExpires the floor resets; seq 1 must be re-admitted")
	}
}

// TestEngine_OnValidation_ConflictingDoubleSign verifies A6 end-to-end at
// the engine seam: a trusted validator signing two different ledgers at
// one sequence yields a ByzantineValidationError (the router's signal to
// skip the catch-up acquire and NOT charge the delivering peer) and is
// kept out of quorum/trie (the tracked tip is unchanged) — but it IS still
// relayed, mirroring rippled, which forwards Byzantine validations so peers
// independently observe the misbehaving validator (RCLValidations.cpp:
// 214-247, NetworkOPs.cpp:2625-2627).
func TestEngine_OnValidation_ConflictingDoubleSign(t *testing.T) {
	adaptor := newMockAdaptor()
	n := consensus.NodeID{0x9}
	adaptor.setTrusted([]consensus.NodeID{n})
	engine := NewEngine(adaptor, DefaultConfig())
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	now := adaptor.Now()
	v1 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xA}, NodeID: n, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v1, 7); err != nil {
		t.Fatalf("first validation rejected: %v", err)
	}

	// Same seq, different ledger → conflicting double-sign.
	v2 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xB}, NodeID: n, SignTime: now, SeenTime: now, Full: true}
	err := engine.OnValidation(v2, 7)
	var bv *consensus.ByzantineValidationError
	if !errors.As(err, &bv) {
		t.Fatalf("expected *consensus.ByzantineValidationError, got %v", err)
	}
	if bv.Reason != "conflicting" {
		t.Errorf("reason = %q, want conflicting", bv.Reason)
	}

	// The conflict must NOT have been stored — the tracked tip stays at
	// ledger A, so it cannot count toward quorum or steer the trie.
	if tip := engine.validationTracker.LatestValidation(n); tip == nil || tip.LedgerID != (consensus.LedgerID{0xA}) {
		t.Errorf("tracked tip should remain ledger A; got %+v", tip)
	}

	// But it MUST still be relayed: rippled forwards Byzantine validations
	// so peers independently observe the misbehaving validator.
	adaptor.mu.RLock()
	relayed := append([]*consensus.Validation(nil), adaptor.validationsRelayed...)
	adaptor.mu.RUnlock()
	var relayedConflict bool
	for _, v := range relayed {
		if v.LedgerID == (consensus.LedgerID{0xB}) {
			relayedConflict = true
		}
	}
	if !relayedConflict {
		t.Error("conflicting validation must still be relayed (rippled forwards Byzantine validations)")
	}
}

// TestEngine_OnValidation_SupersededSeqDoubleSign pins the deep detector
// end-to-end: equivocation at a seq the validator's tip has already
// superseded is still flagged as Byzantine (the cross-check runs against
// the by-seq evidence, not just the node's latest tip), kept out of
// quorum/trie, and still relayed.
func TestEngine_OnValidation_SupersededSeqDoubleSign(t *testing.T) {
	adaptor := newMockAdaptor()
	n := consensus.NodeID{0x9}
	adaptor.setTrusted([]consensus.NodeID{n})
	engine := NewEngine(adaptor, DefaultConfig())
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	now := adaptor.Now()
	v1 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xA}, NodeID: n, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v1, 7); err != nil {
		t.Fatalf("seq-100 validation rejected: %v", err)
	}
	v2 := &consensus.Validation{LedgerSeq: 101, LedgerID: consensus.LedgerID{0xC}, NodeID: n, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v2, 7); err != nil {
		t.Fatalf("seq-101 validation rejected: %v", err)
	}

	// Double-sign at seq 100 — already superseded by the seq-101 tip.
	v3 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xB}, NodeID: n, SignTime: now, SeenTime: now, Full: true}
	err := engine.OnValidation(v3, 7)
	var bv *consensus.ByzantineValidationError
	if !errors.As(err, &bv) {
		t.Fatalf("expected *consensus.ByzantineValidationError, got %v", err)
	}
	if bv.Reason != "conflicting" {
		t.Errorf("reason = %q, want conflicting", bv.Reason)
	}

	// The tip must stay at the seq-101 ledger.
	if tip := engine.validationTracker.LatestValidation(n); tip == nil || tip.LedgerID != (consensus.LedgerID{0xC}) {
		t.Errorf("tracked tip should remain the seq-101 ledger; got %+v", tip)
	}

	// Still relayed so peers observe the equivocation too.
	adaptor.mu.RLock()
	relayed := append([]*consensus.Validation(nil), adaptor.validationsRelayed...)
	adaptor.mu.RUnlock()
	var relayedConflict bool
	for _, v := range relayed {
		if v.LedgerID == (consensus.LedgerID{0xB}) {
			relayedConflict = true
		}
	}
	if !relayedConflict {
		t.Error("superseded-seq double-sign must still be relayed")
	}
}

// TestEngine_RetentionWithoutArchive pins A4: ExpireOld must run off the
// fully-validated callback even when no on-disk archive is configured,
// using defaultInMemoryLedgers as the retention window.
func TestEngine_RetentionWithoutArchive(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{1}, {2}})
	adaptor.quorum = 2
	engine := NewEngine(adaptor, DefaultConfig())
	// Deliberately NO SetArchive / SetInMemoryLedgers: inMemoryLedgers
	// stays 0, so retention must fall back to defaultInMemoryLedgers.
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	now := time.Now()

	// Seed a validation far below the default retention window: the
	// callback at seq 300 computes cutoff 300-256 = 44, so seq 40 is stale.
	old := &consensus.Validation{LedgerSeq: 40, LedgerID: consensus.LedgerID{0xA}, NodeID: consensus.NodeID{0x1}, SignTime: now, Full: true}
	if !engine.validationTracker.Add(old) {
		t.Fatal("seed Add returned false")
	}
	if got := engine.validationTracker.GetValidationCount(consensus.LedgerID{0xA}); got != 1 {
		t.Fatalf("precondition: seed validation not tracked (got %d)", got)
	}

	// Move the tracker clock past the access-age window so the seed set
	// is cold when the retention floor sweeps it.
	future := now.Add(validationSetExpires + time.Second)
	engine.validationTracker.SetNow(func() time.Time { return future })

	// Drive a quorum at seq 300 to fire the fully-validated callback.
	for _, id := range []consensus.NodeID{{1}, {2}} {
		v := &consensus.Validation{LedgerSeq: 300, LedgerID: consensus.LedgerID{0xB}, NodeID: id, SignTime: future, Full: true}
		engine.validationTracker.Add(v)
	}

	if got := engine.validationTracker.GetValidationCount(consensus.LedgerID{0xA}); got != 0 {
		t.Errorf("seq-40 validation should be expired by default retention without an archive; %d still tracked", got)
	}
}

// TestEngine_OnProposal_DropsUntrusted pins A2: an untrusted proposal must
// not be buffered into recentProposals or stored into proposals — matching
// rippled, which never feeds untrusted proposals to the consensus object.
func TestEngine_OnProposal_DropsUntrusted(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{0x10}})
	engine := NewEngine(adaptor, DefaultConfig())

	parent, _ := adaptor.GetLastClosedLedger()
	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}
	now := adaptor.Now()
	untrusted := &consensus.Proposal{
		Round:          round,
		NodeID:         consensus.NodeID{0xEE},
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      now,
		PreviousLedger: parent.ID(),
		Timestamp:      now,
	}
	if err := engine.OnProposal(untrusted, 5); err != nil {
		t.Fatalf("OnProposal(untrusted): %v", err)
	}

	engine.mu.Lock()
	rp, pp := len(engine.proposalTracker.recentProposals), len(engine.proposalTracker.proposals)
	engine.mu.Unlock()
	if rp != 0 {
		t.Errorf("untrusted proposal buffered into recentProposals: %d entries", rp)
	}
	if pp != 0 {
		t.Errorf("untrusted proposal stored into proposals: %d entries", pp)
	}
}
