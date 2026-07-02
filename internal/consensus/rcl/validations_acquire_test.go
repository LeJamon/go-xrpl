package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// A trusted validation for an unresolvable ledger parks instead of
// dropping: the node's previous tip keeps steering the trie, and once
// the ledger is acquired the next GetPreferred poll replays the parked
// validations and the trie flips — no new validation needed.
func TestValidationTracker_UnresolvableValidationParksThenReplays(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")
	abcd := b.Build("abcd")

	provider := newMapAncestryProvider()
	provider.add(abc)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now)) {
		t.Fatal("Add(n1->abc) should succeed")
	}
	if !vt.Add(makeTrustedValidation(n2, abc.ID(), abc.Seq(), now)) {
		t.Fatal("Add(n2->abc) should succeed")
	}

	// Both advance to abcd, which is not locally resolvable yet.
	if !vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now)) {
		t.Fatal("Add(n1->abcd) should succeed")
	}
	if !vt.Add(makeTrustedValidation(n2, abcd.ID(), abcd.Seq(), now)) {
		t.Fatal("Add(n2->abcd) should succeed")
	}

	id, seq, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred must stay conclusive while validations are parked")
	}
	if id != abc.ID() || seq != abc.Seq() {
		t.Fatalf("GetPreferred while parked: got seq %d, want prior tip abc@%d", seq, abc.Seq())
	}

	// Acquisition lands; the next poll replays the parked validations.
	provider.add(abcd)
	id, seq, ok = vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred after acquisition: no result")
	}
	if id != abcd.ID() || seq != abcd.Seq() {
		t.Fatalf("GetPreferred after acquisition: got seq %d, want replayed abcd@%d", seq, abcd.Seq())
	}
}

// The consensus-island shape: the trie decides even when the majority is
// parked, and flips to the majority branch once its ledger is acquired.
func TestValidationTracker_TrieDecidesWithMajorityParked(t *testing.T) {
	vt := NewValidationTracker(3, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")   // island tip, locally held
	abde := b.Build("abde") // majority tip, not held yet

	provider := newMapAncestryProvider()
	provider.add(abc)

	island := []consensus.NodeID{{1}, {2}}
	majority := []consensus.NodeID{{3}, {4}, {5}}
	vt.SetTrusted(append(append([]consensus.NodeID{}, island...), majority...))
	vt.SetLedgerAncestryProvider(provider)

	for _, n := range island {
		vt.Add(makeTrustedValidation(n, abc.ID(), abc.Seq(), now))
	}
	for _, n := range majority {
		vt.Add(makeTrustedValidation(n, abde.ID(), abde.Seq(), now))
	}

	// 3 trusted validations parked vs 2 placed: the trie must decide
	// with what it holds.
	id, _, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred must not go inconclusive while the majority is parked")
	}
	if id != abc.ID() {
		t.Fatalf("GetPreferred while majority parked: want the placed island tip abc")
	}

	// Majority ledger acquired: replay flips the trie, 3 beats 2.
	provider.add(abde)
	id, seq, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred after majority acquisition: no result")
	}
	if id != abde.ID() || seq != abde.Seq() {
		t.Fatalf("GetPreferred after majority acquisition: got seq %d, want abde@%d (3v2)", seq, abde.Seq())
	}
}

// checkAcquired also polls on the Add path, mirroring rippled's
// updateTrie: an unrelated resolvable validation replays parked entries.
func TestValidationTracker_AddPollReplaysParked(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abc := b.Build("abc")
	abcd := b.Build("abcd")

	provider := newMapAncestryProvider()
	provider.add(abc)

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now))
	provider.add(abcd)

	// Resolvable but not replayed yet — GetTrustedSupport reads the trie
	// without polling, so n1's tip is still absent.
	if got := vt.GetTrustedSupport(abcd.ID()); got != 0 {
		t.Fatalf("branch support before replay: got %d, want 0", got)
	}

	vt.Add(makeTrustedValidation(n2, abc.ID(), abc.Seq(), now))
	if got := vt.GetTrustedSupport(abcd.ID()); got != 1 {
		t.Fatalf("branch support after Add-path replay: got %d, want 1", got)
	}
}

// With nothing placed at all, GetPreferred falls back to the majority
// over still-acquiring ledgers: most parked validators first, ties to
// the greater ledger ID.
func TestValidationTracker_GetPreferred_AcquiringMajorityFallback(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcx := b.Build("abcx")
	abcy := b.Build("abcy")

	vt.SetTrusted([]consensus.NodeID{{1}, {2}, {3}})
	vt.SetLedgerAncestryProvider(newMapAncestryProvider())

	vt.Add(makeTrustedValidation(consensus.NodeID{1}, abcx.ID(), abcx.Seq(), now))
	vt.Add(makeTrustedValidation(consensus.NodeID{2}, abcx.ID(), abcx.Seq(), now))
	vt.Add(makeTrustedValidation(consensus.NodeID{3}, abcy.ID(), abcy.Seq(), now))

	id, seq, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("acquiring-majority fallback: no result")
	}
	if id != abcx.ID() || seq != abcx.Seq() {
		t.Fatalf("acquiring-majority fallback: want abcx (2 parked beats 1)")
	}
}

func TestValidationTracker_GetPreferred_AcquiringTieBreaksOnGreaterID(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcx := b.Build("abcx")
	abcy := b.Build("abcy") // same seq, greater ID than abcx

	vt.SetTrusted([]consensus.NodeID{{1}, {2}})
	vt.SetLedgerAncestryProvider(newMapAncestryProvider())

	vt.Add(makeTrustedValidation(consensus.NodeID{1}, abcx.ID(), abcx.Seq(), now))
	vt.Add(makeTrustedValidation(consensus.NodeID{2}, abcy.ID(), abcy.Seq(), now))

	id, _, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("acquiring-majority tie: no result")
	}
	if id != abcy.ID() {
		t.Fatalf("acquiring-majority tie: want the greater ledger ID abcy")
	}
}

// A superseding validation removes the node from its prior parked entry,
// so abandoned acquiring entries don't linger.
func TestValidationTracker_SupersededValidationUnparks(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcz := b.Build("abcz")   // greater ID: would win the tie-break if it lingered
	abcwv := b.Build("abcwv") // higher seq, lesser ID at the fork byte

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(newMapAncestryProvider())

	vt.Add(makeTrustedValidation(n1, abcz.ID(), abcz.Seq(), now))
	vt.Add(makeTrustedValidation(n1, abcwv.ID(), abcwv.Seq(), now))

	id, seq, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("fallback after supersede: no result")
	}
	if id != abcwv.ID() || seq != abcwv.Seq() {
		t.Fatalf("fallback after supersede: got seq %d, want only the latest parked ledger abcwv@%d", seq, abcwv.Seq())
	}
}

// ExpireOld drops parked entries with the validations that reference
// them — acquiring_ never outlives its validations.
func TestValidationTracker_ExpireOldUnparks(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcd := b.Build("abcd")

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(newMapAncestryProvider())

	vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now))
	vt.ExpireOld(abcd.Seq() + 1)

	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("expired parked validation must not feed the acquiring fallback")
	}
}

// FlushStale drops parked entries with the flushed validation, mirroring
// rippled's stale sweep (removeTrie erases acquiring_, Validations.h:
// 363-372): a validator that goes silent while parked must stop feeding
// the acquiring fallback, and must not be resurrected into the trie as a
// phantom tip once its ledger is finally acquired.
func TestValidationTracker_FlushStaleUnparks(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcd := b.Build("abcd")

	provider := newMapAncestryProvider()

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now)) {
		t.Fatal("Add(n1->abcd) should succeed")
	}

	now = now.Add(validationCurrentEarly + time.Second)
	vt.FlushStale()

	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("flushed-stale parked validation must not feed the acquiring fallback")
	}

	// The parked ledger is acquired after the flush: the dead node's
	// validation must not replay.
	provider.add(abcd)
	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("flushed-stale parked validation must not replay into the trie")
	}
	if got := vt.GetTrustedSupport(abcd.ID()); got != 0 {
		t.Fatalf("phantom trie tip after flush+acquire: support %d, want 0", got)
	}
}

// A validation parked while trusted must not replay once the node is
// de-trusted, even after its ledger is acquired (rippled
// Validations_test.cpp:1098-1124, "Trusted but not acquired ->
// untrusted").
func TestValidationTracker_DetrustedParkedValidationNotReplayed(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcd := b.Build("abcd")

	provider := newMapAncestryProvider()

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now)) {
		t.Fatal("Add(n1->abcd) should succeed")
	}

	vt.SetTrusted([]consensus.NodeID{})
	provider.add(abcd)

	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("de-trusted parked validation must not replay after acquisition")
	}
	if got := vt.GetTrustedSupport(abcd.ID()); got != 0 {
		t.Fatalf("de-trusted parked validation counted as support: got %d, want 0", got)
	}
}

// Trust rotation rebuilds the parked set from byNode: a de-trusted
// node's parked entry drops, and re-trusting re-parks its latest
// validation.
func TestValidationTracker_TrustChangeReparksFromByNode(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrie.NewTestLedgerBuilder()
	abcd := b.Build("abcd")

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetLedgerAncestryProvider(newMapAncestryProvider())

	vt.Add(makeTrustedValidation(n1, abcd.ID(), abcd.Seq(), now))

	vt.SetTrusted([]consensus.NodeID{n2})
	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("de-trusted node's parked validation must not feed the acquiring fallback")
	}

	vt.SetTrusted([]consensus.NodeID{n1, n2})
	id, _, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("re-trusting must re-park the node's latest validation")
	}
	if id != abcd.ID() {
		t.Fatalf("re-parked fallback: want abcd")
	}
}
