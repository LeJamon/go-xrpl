package rcl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
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

	b := ledgertrie.NewTestLedgerBuilder()
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
	if got := vt.GetTrustedSupport(abc.ID()); got != 1 {
		t.Errorf("partial should steer trie branchSupport(abc): got %d, want 1", got)
	}
	if id, _, ok := vt.GetPreferred(0); !ok || id != abc.ID() {
		t.Errorf("GetPreferred should steer to abc from the partial: ok=%v id=%x", ok, id)
	}

	// Quorum: the partial is excluded from the full-validation count and
	// must not fire finality.
	if got := vt.GetTrustedValidationCount(abc.ID()); got != 0 {
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
	if got := vt.GetTrustedValidationCount(abcd.ID()); got != 1 {
		t.Errorf("full validation should count toward quorum: got %d, want 1", got)
	}
	if fired != 1 {
		t.Errorf("full validation at quorum should fire callback exactly once; fired=%d", fired)
	}
}

// TestValidationConflict covers the A6 classification of same-seq
// double-signs against the most recent tracked tip.
func TestValidationConflict(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	node := consensus.NodeID{0x9}
	mk := func(seq uint32, ledger consensus.LedgerID, signTime time.Time, cookie uint64) *consensus.Validation {
		return &consensus.Validation{
			LedgerSeq: seq, LedgerID: ledger, NodeID: node,
			SignTime: signTime, Cookie: cookie, Full: true,
		}
	}
	ledgerA := consensus.LedgerID{0xA}
	ledgerB := consensus.LedgerID{0xB}

	tests := []struct {
		name       string
		prev, next *consensus.Validation
		wantReason string
		wantConfl  bool
	}{
		{"nil prev", nil, mk(100, ledgerA, base, 0), "", false},
		{"different seq", mk(100, ledgerA, base, 0), mk(101, ledgerB, base, 0), "", false},
		{"identical resend", mk(100, ledgerA, base, 1), mk(100, ledgerA, base, 1), "", false},
		{"same seq different ledger", mk(100, ledgerA, base, 0), mk(100, ledgerB, base, 0), "conflicting", true},
		{"same seq same ledger different signtime", mk(100, ledgerA, base, 0), mk(100, ledgerA, base.Add(time.Second), 0), "conflicting", true},
		{"same seq same ledger different cookie", mk(100, ledgerA, base, 1), mk(100, ledgerA, base, 2), "multiple", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, confl := validationConflict(tc.prev, tc.next)
			if confl != tc.wantConfl || reason != tc.wantReason {
				t.Errorf("validationConflict = (%q, %v), want (%q, %v)", reason, confl, tc.wantReason, tc.wantConfl)
			}
		})
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
	if tip := engine.validationTracker.GetLatestValidation(n); tip == nil || tip.LedgerID != (consensus.LedgerID{0xA}) {
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

	// Drive a quorum at seq 300 to fire the fully-validated callback.
	for _, id := range []consensus.NodeID{{1}, {2}} {
		v := &consensus.Validation{LedgerSeq: 300, LedgerID: consensus.LedgerID{0xB}, NodeID: id, SignTime: now, Full: true}
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
	rp, pp := len(engine.recentProposals), len(engine.proposals)
	engine.mu.Unlock()
	if rp != 0 {
		t.Errorf("untrusted proposal buffered into recentProposals: %d entries", rp)
	}
	if pp != 0 {
		t.Errorf("untrusted proposal stored into proposals: %d entries", pp)
	}
}
