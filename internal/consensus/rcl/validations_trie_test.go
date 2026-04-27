package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/ledgertrie"
)

// mapAncestryProvider is a tiny LedgerAncestryProvider backed by a
// map, used to wire ledgertrie.TestLedger instances into the
// ValidationTracker for these tests.
type mapAncestryProvider struct {
	byID map[consensus.LedgerID]ledgertrie.Ledger
}

func newMapAncestryProvider() *mapAncestryProvider {
	return &mapAncestryProvider{byID: make(map[consensus.LedgerID]ledgertrie.Ledger)}
}

func (m *mapAncestryProvider) add(l ledgertrie.Ledger) { m.byID[l.ID()] = l }

func (m *mapAncestryProvider) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	l, ok := m.byID[id]
	return l, ok
}

// makeTrustedValidation constructs a trusted validation at the given
// seq from nodeID pointing at ledgerID. Close enough to the isCurrent
// window that Add() will accept it with SetNow(time.Now).
func makeTrustedValidation(nodeID consensus.NodeID, ledgerID consensus.LedgerID, seq uint32, now time.Time) *consensus.Validation {
	return &consensus.Validation{
		LedgerID:  ledgerID,
		LedgerSeq: seq,
		NodeID:    nodeID,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}
}

// TestValidationTracker_TrieDeepestSharedAncestor exercises the core
// scenario from issue #268: a near-tip minority branch should NOT
// outrank a deeper-shared-ancestor majority when the trie is wired.
//
// Topology:
//
//	genesis --> ab --> abc  (1 validator)
//	              \-> abd --> abde  (2 validators at abde)
//
// Flat hash-count says: abc has 1, abde has 2. Under the flat
// approximation GetTrustedSupport(abde) = 2 > GetTrustedSupport(abd) = 0,
// and abd would lose to abc. Under the trie: branchSupport(abd) = 2 >
// branchSupport(abc) = 1, so the majority-deeper branch correctly
// wins.
func TestValidationTracker_TrieDeepestSharedAncestor(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)

	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	ab := b.Build("ab")
	abc := b.Build("abc")
	abd := b.Build("abd")
	abde := b.Build("abde")

	provider := newMapAncestryProvider()
	provider.add(b.Build(""))
	provider.add(b.Build("a"))
	provider.add(ab)
	provider.add(abc)
	provider.add(abd)
	provider.add(abde)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	n3 := consensus.NodeID{3}
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now)) {
		t.Fatal("Add(n1->abc) should succeed")
	}
	if !vt.Add(makeTrustedValidation(n2, abde.ID(), abde.Seq(), now)) {
		t.Fatal("Add(n2->abde) should succeed")
	}
	if !vt.Add(makeTrustedValidation(n3, abde.ID(), abde.Seq(), now)) {
		t.Fatal("Add(n3->abde) should succeed")
	}

	// Flat count: abc has 1, abd has 0, abde has 2.
	// Trie branchSupport: abc=1, abd=2 (via abde), abde=2.
	if got := vt.GetTrustedSupport(abd.ID()); got != 2 {
		t.Errorf("GetTrustedSupport(abd) via trie: got %d, want 2 (branchSupport)", got)
	}
	if got := vt.GetTrustedSupport(abde.ID()); got != 2 {
		t.Errorf("GetTrustedSupport(abde): got %d, want 2", got)
	}
	if got := vt.GetTrustedSupport(abc.ID()); got != 1 {
		t.Errorf("GetTrustedSupport(abc): got %d, want 1", got)
	}

	// Unknown ancestry falls back to flat count (zero here).
	unknown := consensus.LedgerID{0xff}
	if got := vt.GetTrustedSupport(unknown); got != 0 {
		t.Errorf("GetTrustedSupport(unknown) should fall back to 0, got %d", got)
	}
}

// TestValidationTracker_TrieNewerValidationReplacesOld verifies the
// "most recent trusted validation per node" rule: when n1 moves from
// abc to abde, the trie must remove abc's tip and insert abde's, so
// branchSupport reflects the actual current network state.
func TestValidationTracker_TrieNewerValidationReplacesOld(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")
	abde := b.Build("abde")

	provider := newMapAncestryProvider()
	provider.add(abc)
	provider.add(abde)

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now)) {
		t.Fatal("first Add should succeed")
	}
	if vt.GetTrustedSupport(abc.ID()) != 1 {
		t.Errorf("abc support after first validation should be 1")
	}

	// Newer validation from same node at a higher seq.
	if !vt.Add(makeTrustedValidation(n1, abde.ID(), abde.Seq(), now.Add(time.Second))) {
		t.Fatal("newer Add should succeed")
	}

	// abc's tip should have been removed; only abde contributes.
	if got := vt.GetTrustedSupport(abc.ID()); got != 0 {
		t.Errorf("abc support after switch: got %d, want 0", got)
	}
	if got := vt.GetTrustedSupport(abde.ID()); got != 1 {
		t.Errorf("abde support after switch: got %d, want 1", got)
	}
}

// TestValidationTracker_TrieNegUNLExcluded confirms a validator on
// the negUNL never enters the trie, matching the flat-count filter.
func TestValidationTracker_TrieNegUNLExcluded(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")
	provider := newMapAncestryProvider()
	provider.add(abc)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetNegativeUNL([]consensus.NodeID{n2})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now))
	vt.Add(makeTrustedValidation(n2, abc.ID(), abc.Seq(), now))

	// Only n1 counts; n2 on negUNL is excluded.
	if got := vt.GetTrustedSupport(abc.ID()); got != 1 {
		t.Errorf("negUNL validator should not contribute: got %d, want 1", got)
	}
}

// TestValidationTracker_TrieGetPreferred runs a simple 2-way
// competition through the full Add() path and asserts GetPreferred
// returns the trie's SpanTip.
func TestValidationTracker_TrieGetPreferred(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")
	abde := b.Build("abde")
	provider := newMapAncestryProvider()
	provider.add(abc)
	provider.add(abde)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	n3 := consensus.NodeID{3}
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now))
	vt.Add(makeTrustedValidation(n2, abde.ID(), abde.Seq(), now))
	vt.Add(makeTrustedValidation(n3, abde.ID(), abde.Seq(), now))

	id, seq, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred should return a result with trie wired")
	}
	if id != abde.ID() {
		t.Errorf("GetPreferred: got different ID, want abde")
	}
	if seq != abde.Seq() {
		t.Errorf("GetPreferred seq: got %d, want %d", seq, abde.Seq())
	}
}

// TestValidationTracker_TrieGetPreferred_LargestIssuedAffectsDescent
// verifies that a non-zero largestIssued actually changes the descent
// decision through the full Add() → trie path. Ports the structure of
// rippled's "Changing largestSeq perspective" case
// (LedgerTrie_test.cpp:506-591) at the ValidationTracker level.
//
// Topology after the 5 validations are accepted:
//
//	root -> a -> ab -> abde   (2 trusted at abde)
//	          \-> ac -> acf   (1 trusted at acf)
//
// At largestIssued=1 the trie descends to ab (its 3-2 branchSupport
// margin against ac exceeds the uncommitted at seq 1).
//
// At largestIssued=3 the seq-2 validations seed uncommitted before
// descent starts, so the same 3-2 margin no longer beats uncommitted
// and the descent stops at the common ancestor "a".
func TestValidationTracker_TrieGetPreferred_LargestIssuedAffectsDescent(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	a := b.Build("a")
	ab := b.Build("ab")
	ac := b.Build("ac")
	acf := b.Build("acf")
	abde := b.Build("abde")
	provider := newMapAncestryProvider()
	provider.add(a)
	provider.add(ab)
	provider.add(ac)
	provider.add(acf)
	provider.add(abde)

	n1 := consensus.NodeID{1} // votes ab
	n2 := consensus.NodeID{2} // votes ac
	n3 := consensus.NodeID{3} // votes acf
	n4 := consensus.NodeID{4} // votes abde
	n5 := consensus.NodeID{5} // votes abde
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3, n4, n5})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, ab.ID(), ab.Seq(), now))
	vt.Add(makeTrustedValidation(n2, ac.ID(), ac.Seq(), now))
	vt.Add(makeTrustedValidation(n3, acf.ID(), acf.Seq(), now))
	vt.Add(makeTrustedValidation(n4, abde.ID(), abde.Seq(), now))
	vt.Add(makeTrustedValidation(n5, abde.ID(), abde.Seq(), now))

	idAt1, _, ok := vt.GetPreferred(1)
	if !ok {
		t.Fatal("GetPreferred(1): no result")
	}
	if idAt1 != ab.ID() {
		t.Errorf("GetPreferred(1): want ab (3-2 margin descent), got different ID")
	}

	idAt3, _, ok := vt.GetPreferred(3)
	if !ok {
		t.Fatal("GetPreferred(3): no result")
	}
	if idAt3 != a.ID() {
		t.Errorf("GetPreferred(3): want a (descent halted by uncommitted), got different ID")
	}

	if idAt1 == idAt3 {
		t.Errorf("largestIssued must change the descent decision: both queries returned %v", idAt1)
	}
}

// TestValidationTracker_TrieDisabled_FallsBack keeps the existing
// flat-count behaviour when no ancestry provider is installed.
func TestValidationTracker_TrieDisabled_FallsBack(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})

	vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now))
	vt.Add(makeTrustedValidation(n2, abc.ID(), abc.Seq(), now))

	// Without ancestry provider, GetTrustedSupport returns flat count.
	if got := vt.GetTrustedSupport(abc.ID()); got != 2 {
		t.Errorf("without trie: got %d, want 2 (flat count)", got)
	}

	if _, _, ok := vt.GetPreferred(0); ok {
		t.Errorf("GetPreferred without trie should return ok=false")
	}
}

// TestValidationTracker_ExpireOldDropsTrieTip verifies that ExpireOld
// removes a stale validator's tip from the trie. Without this fix the
// validator's branchSupport would phantom-count on ancestors of the
// expired tip until the validator submitted a fresh validation.
// Mirrors rippled's removeTrie call in Validations::eraseFromCurrent
// (Validations.h:519-523).
func TestValidationTracker_ExpireOldDropsTrieTip(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	ab := b.Build("ab")   // seq 2 — common ancestor
	abc := b.Build("abc") // seq 3
	abd := b.Build("abd") // seq 3
	provider := newMapAncestryProvider()
	provider.add(ab) // GetTrustedSupport(ab) needs ab resolvable
	provider.add(abc)
	provider.add(abd)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now))
	vt.Add(makeTrustedValidation(n2, abd.ID(), abd.Seq(), now))

	// Both validators back the common ancestor "ab" through their tips.
	if got := vt.GetTrustedSupport(ab.ID()); got != 2 {
		t.Fatalf("pre-expire branchSupport(ab): got %d, want 2", got)
	}

	// Expire validations below seq 4 — drops both tips.
	vt.ExpireOld(4)

	// After expiry the trie must drop both tips. branchSupport on any
	// ancestor falls to 0 — no phantom support survives.
	if got := vt.GetTrustedSupport(ab.ID()); got != 0 {
		t.Errorf("post-expire branchSupport(ab): got %d, want 0 (trie tip leaked)", got)
	}
	if got := vt.GetTrustedSupport(abc.ID()); got != 0 {
		t.Errorf("post-expire branchSupport(abc): got %d, want 0", got)
	}
}
