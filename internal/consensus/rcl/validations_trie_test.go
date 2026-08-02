package rcl

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrietest"
)

type mapAncestryProvider struct {
	byID map[consensus.LedgerID]ledgertrie.Ledger
}

type nilAncestryProvider struct {
	ledger ledgertrie.Ledger
}

func (p nilAncestryProvider) LedgerByID(consensus.LedgerID) (ledgertrie.Ledger, bool) {
	return p.ledger, true
}

func newMapAncestryProvider() *mapAncestryProvider {
	return &mapAncestryProvider{byID: make(map[consensus.LedgerID]ledgertrie.Ledger)}
}

func (m *mapAncestryProvider) add(l ledgertrie.Ledger) { m.byID[l.ID()] = l }

func (m *mapAncestryProvider) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	l, ok := m.byID[id]
	return l, ok
}

type maxSequenceLedger struct{}

func (maxSequenceLedger) ID() consensus.LedgerID             { return consensus.LedgerID{1} }
func (maxSequenceLedger) Seq() uint32                        { return math.MaxUint32 }
func (maxSequenceLedger) MinSeq() uint32                     { return 0 }
func (maxSequenceLedger) Ancestor(uint32) consensus.LedgerID { return consensus.LedgerID{} }

// mismatchedLedger has an ID present in the trie but an incompatible
// sequence, making ledgertrie.Remove deterministically panic before it can
// mutate the node. It exercises the tracker-level rebuild seam without
// adding production-only failure hooks.
type mismatchedLedger struct {
	id  consensus.LedgerID
	seq uint32
}

func (l mismatchedLedger) ID() consensus.LedgerID             { return l.id }
func (l mismatchedLedger) Seq() uint32                        { return l.seq }
func (l mismatchedLedger) MinSeq() uint32                     { return 0 }
func (l mismatchedLedger) Ancestor(uint32) consensus.LedgerID { return l.id }

type panicAncestorLedger struct {
	id  consensus.LedgerID
	seq uint32
}

func (l panicAncestorLedger) ID() consensus.LedgerID { return l.id }
func (l panicAncestorLedger) Seq() uint32            { return l.seq }
func (l panicAncestorLedger) MinSeq() uint32         { return 0 }
func (l panicAncestorLedger) Ancestor(uint32) consensus.LedgerID {
	panic("hostile ancestry")
}

type panicProvider struct{}

func (panicProvider) LedgerByID(consensus.LedgerID) (ledgertrie.Ledger, bool) {
	panic("hostile provider")
}

type panicMetadataLedger struct {
	id         consensus.LedgerID
	seq        uint32
	panicID    bool
	panicSeq   bool
	panicRetry bool
}

func (l panicMetadataLedger) ID() consensus.LedgerID {
	if l.panicID {
		panic("hostile ledger ID")
	}
	return l.id
}

func (l panicMetadataLedger) Seq() uint32 {
	if l.panicSeq {
		panic("hostile ledger sequence")
	}
	return l.seq
}

func (l panicMetadataLedger) MinSeq() uint32 { return 0 }
func (l panicMetadataLedger) Ancestor(uint32) consensus.LedgerID {
	return l.id
}

func (l panicMetadataLedger) retryable() bool {
	if l.panicRetry {
		panic("hostile retry metadata")
	}
	return false
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

func TestValidationTracker_InsertTipDoesNotRecordRejectedLedger(t *testing.T) {
	vt := NewValidationTracker(1)
	vt.trie = ledgertrie.New(genesisLedger{})
	vt.trieTips = make(map[consensus.NodeID]ledgertrie.Ledger)

	nodeID := consensus.NodeID{1}
	valid := ledgertrietest.NewTestLedgerBuilder().Build("a")
	vt.insertTipLocked(nodeID, valid)
	if vt.trieTips[nodeID] != valid {
		t.Fatal("valid ledger was not recorded")
	}

	vt.insertTipLocked(nodeID, maxSequenceLedger{})
	if _, ok := vt.trieTips[nodeID]; ok {
		t.Fatal("rejected ledger was recorded as the node tip")
	}
	if vt.trie.TipSupport(valid) != 0 {
		t.Fatal("replaced ledger retained support after the new ledger was rejected")
	}
	if _, ok := vt.trie.GetPreferred(0); ok {
		t.Fatal("rejected ledger left support in the trie")
	}
}

func TestValidationTracker_TrieMutationPanicRebuildsForRecovery(t *testing.T) {
	vt := NewValidationTracker(1)
	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("valid")
	nodeID := consensus.NodeID{1}
	provider := newMapAncestryProvider()
	provider.add(valid)
	vt.ancestry = provider
	vt.trusted[nodeID] = true
	vt.trie = ledgertrie.New(genesisLedger{})
	vt.trieTips = make(map[consensus.NodeID]ledgertrie.Ledger)

	vt.mu.Lock()
	if !vt.insertTipLocked(nodeID, valid) {
		t.Fatal("initial trie insert failed")
	}
	// Keep the authoritative state aligned with the valid tip while replacing
	// only the derived pointer with a bad sequence. Remove now panics; the
	// tracker must discard the old trie rather than retain a phantom tip.
	vt.byNode[nodeID] = &consensus.Validation{LedgerID: valid.ID(), LedgerSeq: valid.Seq()}
	vt.trieTips[nodeID] = mismatchedLedger{id: valid.ID(), seq: valid.Seq() + 1}
	if vt.insertTipLocked(nodeID, valid) {
		t.Fatal("mismatched remove unexpectedly succeeded")
	}
	vt.mu.Unlock()

	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("panic recovery failed to rebuild authoritative support: got %d, want 1", got)
	}

	vt.mu.Lock()
	if !vt.insertTipLocked(nodeID, valid) {
		t.Fatal("tracker did not permit safe trie recovery")
	}
	vt.mu.Unlock()
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("safe trie recovery support: got %d, want 1", got)
	}
}

func TestValidationTracker_StaleCleanupPanicClearsDerivedState(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("stale")
	nodeID := consensus.NodeID{1}
	provider := newMapAncestryProvider()
	provider.add(valid)
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("precondition validation was rejected")
	}

	vt.mu.Lock()
	vt.trieTips[nodeID] = mismatchedLedger{id: valid.ID(), seq: valid.Seq() + 1}
	vt.mu.Unlock()
	now = now.Add(validationCurrentEarly + time.Second)
	vt.FlushStale()

	if got := vt.TrustedSupport(valid.ID()); got != 0 {
		t.Fatalf("stale cleanup left phantom support: got %d", got)
	}
	if _, _, ok := vt.GetPreferred(0); ok {
		t.Fatal("stale cleanup left a preferred tip")
	}
}

func TestValidationTracker_PartialAncestryRepairsPreferredSupport(t *testing.T) {
	tip, byHash := buildChain(5, 'p')
	var missingHash [32]byte
	var ancestor *fakeHeader
	for hash, header := range byHash {
		switch header.Sequence() {
		case 2:
			ancestor = header.(*fakeHeader)
		case 3:
			missingHash = hash
		}
	}
	missing := byHash[missingHash]
	delete(byHash, missingHash)

	provider := newTestProvider(byHash)
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	n3 := consensus.NodeID{3}
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(n1, consensus.LedgerID(tip.hash), tip.seq, now)) {
		t.Fatal("partial tip validation was rejected")
	}
	if !vt.Add(makeTrustedValidation(n2, consensus.LedgerID(tip.hash), tip.seq, now)) {
		t.Fatal("second partial tip validation was rejected")
	}
	if !vt.Add(makeTrustedValidation(n3, consensus.LedgerID(ancestor.hash), ancestor.seq, now)) {
		t.Fatal("ancestor validation was rejected")
	}
	if got := vt.TrustedSupport(consensus.LedgerID(ancestor.hash)); got != 1 {
		t.Fatalf("partial tip was treated as complete support: got %d, want 1", got)
	}

	byHash[missingHash] = missing
	if got := vt.TrustedSupport(consensus.LedgerID(ancestor.hash)); got != 3 {
		t.Fatalf("repaired preferred support = %d, want 3", got)
	}
	if id, seq, ok := vt.GetPreferred(tip.seq); !ok || id != consensus.LedgerID(tip.hash) || seq != tip.seq {
		t.Fatalf("preferred after ancestry repair = (%x, %d, %t), want tip", id, seq, ok)
	}
}

func TestValidationTracker_ProviderNilResultsStayParked(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	nodeID := consensus.NodeID{0xD1}
	ledgerID := consensus.LedgerID{0xE1}
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(nilAncestryProvider{})

	if !vt.Add(makeTrustedValidation(nodeID, ledgerID, 7, now)) {
		t.Fatal("validation with nil provider result was rejected")
	}
	if _, _, ok := vt.GetPreferred(0); !ok {
		t.Fatal("parked validation should remain available through acquiring fallback")
	}
	if got := vt.TrustedSupport(ledgerID); got != 1 {
		t.Fatalf("nil provider result should use flat fallback support: got %d", got)
	}
	vt.mu.RLock()
	if len(vt.trieTips) != 0 || len(vt.acquiring) != 1 {
		t.Fatalf("nil provider result changed derived indexes: tips=%d acquiring=%d", len(vt.trieTips), len(vt.acquiring))
	}
	vt.mu.RUnlock()

	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("valid-provider")
	provider := &mapAncestryProvider{byID: map[consensus.LedgerID]ledgertrie.Ledger{valid.ID(): valid}}
	// Keep the validation's ID/sequence aligned with the eventual provider
	// result in a fresh tracker; the first tracker above specifically covers
	// a nil interface result and must not dereference it.
	vt = NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("validation with corrected provider result was rejected")
	}
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("correct provider result did not restore support: got %d", got)
	}
}

func TestValidationTracker_ProviderTypedNilResultStaysParked(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	nodeID := consensus.NodeID{0xD2}
	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("typed-nil")
	var typedNil *ledgertrietest.TestLedger
	provider := &mapAncestryProvider{byID: map[consensus.LedgerID]ledgertrie.Ledger{valid.ID(): typedNil}}
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)

	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("validation with typed-nil provider result was rejected")
	}
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("typed-nil provider result should use flat fallback support: got %d", got)
	}

	provider.byID[valid.ID()] = valid
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("corrected typed-nil provider result did not restore support: got %d", got)
	}
}

func TestValidationTracker_TypedNilProviderDisablesSafely(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	nodeID := consensus.NodeID{0xD3}
	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("typed-provider")
	var provider *mapAncestryProvider
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)

	vt.mu.RLock()
	if vt.ancestry != nil || vt.trie != nil || vt.trieTips != nil || vt.acquiring != nil {
		t.Fatal("typed-nil provider was installed instead of disabling the trie")
	}
	vt.mu.RUnlock()

	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("validation after typed-nil provider installation was rejected")
	}
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("typed-nil provider altered flat support: got %d, want 1", got)
	}
	prev := &mockLedger{id: b.Genesis().ID(), seq: b.Genesis().Seq()}
	if got := vt.ProposersFinished(prev); got != 1 {
		t.Fatalf("typed-nil provider altered proposer fallback: got %d, want 1", got)
	}
}

func TestValidationTracker_ProviderPanicFallsBackWithoutCorruption(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	b := ledgertrietest.NewTestLedgerBuilder()
	tip := b.Build("provider-panic")
	nodeID := consensus.NodeID{0xD6}
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(panicProvider{})
	if !vt.Add(makeTrustedValidation(nodeID, tip.ID(), tip.Seq(), now)) {
		t.Fatal("validation with panicking provider was rejected")
	}
	if got := vt.TrustedSupport(tip.ID()); got != 1 {
		t.Fatalf("provider panic flat support = %d, want 1", got)
	}
	prev := &mockLedger{id: consensus.LedgerID{0xD7}, seq: tip.Seq() - 1}
	if got := vt.ProposersFinished(prev); got != 1 {
		t.Fatalf("provider panic proposer fallback = %d, want 1", got)
	}
}

func TestValidationTracker_MetadataPanicsParkAndRepair(t *testing.T) {
	metadataCases := []struct {
		name string
		make func(id consensus.LedgerID, seq uint32) ledgertrie.Ledger
	}{
		{name: "id", make: func(id consensus.LedgerID, seq uint32) ledgertrie.Ledger {
			return panicMetadataLedger{id: id, seq: seq, panicID: true}
		}},
		{name: "seq", make: func(id consensus.LedgerID, seq uint32) ledgertrie.Ledger {
			return panicMetadataLedger{id: id, seq: seq, panicSeq: true}
		}},
		{name: "retryable", make: func(id consensus.LedgerID, seq uint32) ledgertrie.Ledger {
			return panicMetadataLedger{id: id, seq: seq, panicRetry: true}
		}},
	}

	for _, tc := range metadataCases {
		t.Run(tc.name, func(t *testing.T) {
			vt := NewValidationTracker(1)
			now := time.Now()
			vt.SetNow(func() time.Time { return now })
			b := ledgertrietest.NewTestLedgerBuilder()
			ancestor := b.Build("a")
			tip := b.Build("ab")
			nodeID := consensus.NodeID{0xD8}
			provider := &mapAncestryProvider{byID: map[consensus.LedgerID]ledgertrie.Ledger{
				ancestor.ID(): ancestor,
				tip.ID():      tc.make(tip.ID(), tip.Seq()),
			}}
			vt.SetTrusted([]consensus.NodeID{nodeID})
			vt.SetLedgerAncestryProvider(provider)
			if !vt.Add(makeTrustedValidation(nodeID, tip.ID(), tip.Seq(), now)) {
				t.Fatal("validation with panicking metadata was rejected")
			}
			if got := vt.TrustedSupport(tip.ID()); got != 1 {
				t.Fatalf("metadata panic flat support = %d, want 1", got)
			}
			prevTip := &mockLedger{id: tip.ID(), seq: tip.Seq()}
			if got := vt.ProposersFinished(prevTip); got != 0 {
				t.Fatalf("metadata panic proposer fallback = %d, want 0", got)
			}

			provider.byID[tip.ID()] = tip
			if got := vt.TrustedSupport(tip.ID()); got != 1 {
				t.Fatalf("repaired metadata support = %d, want 1", got)
			}
			prevAncestor := &mockLedger{id: ancestor.ID(), seq: ancestor.Seq()}
			if got := vt.ProposersFinished(prevAncestor); got != 1 {
				t.Fatalf("repaired metadata proposer support = %d, want 1", got)
			}
		})
	}
}

func TestValidationTracker_TrustedSupportRecoversFromHostileTip(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	b := ledgertrietest.NewTestLedgerBuilder()
	valid := b.Build("hostile-support")
	nodeID := consensus.NodeID{0xD4}
	provider := newMapAncestryProvider()
	provider.add(valid)
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("precondition validation was rejected")
	}

	vt.mu.Lock()
	vt.trieTips[nodeID] = panicAncestorLedger{id: valid.ID(), seq: valid.Seq()}
	vt.mu.Unlock()

	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("hostile tip support = %d, want recovered support 1", got)
	}
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("support after hostile tip rebuild = %d, want 1", got)
	}
}

func TestValidationTracker_ProposersFinishedRecoversFromHostileTip(t *testing.T) {
	vt := NewValidationTracker(1)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })
	b := ledgertrietest.NewTestLedgerBuilder()
	ancestor := b.Build("a")
	valid := b.Build("ab")
	nodeID := consensus.NodeID{0xD5}
	provider := newMapAncestryProvider()
	provider.add(ancestor)
	provider.add(valid)
	vt.SetTrusted([]consensus.NodeID{nodeID})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(nodeID, valid.ID(), valid.Seq(), now)) {
		t.Fatal("precondition validation was rejected")
	}

	prev := &mockLedger{id: ancestor.ID(), seq: ancestor.Seq()}
	provider.byID[ancestor.ID()] = panicAncestorLedger{id: ancestor.ID(), seq: ancestor.Seq()}
	if got := vt.ProposersFinished(prev); got != 1 {
		t.Fatalf("hostile target proposer support = %d, want recovered fallback 1", got)
	}
	if got := vt.TrustedSupport(valid.ID()); got != 1 {
		t.Fatalf("hostile target corrupted descendant support: got %d, want 1", got)
	}

	provider.byID[ancestor.ID()] = ancestor
	if got := vt.ProposersFinished(prev); got != 1 {
		t.Fatalf("proposer support after hostile target repair = %d, want 1", got)
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
	vt := NewValidationTracker(2)

	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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
	if got := vt.TrustedSupport(abd.ID()); got != 2 {
		t.Errorf("GetTrustedSupport(abd) via trie: got %d, want 2 (branchSupport)", got)
	}
	if got := vt.TrustedSupport(abde.ID()); got != 2 {
		t.Errorf("GetTrustedSupport(abde): got %d, want 2", got)
	}
	if got := vt.TrustedSupport(abc.ID()); got != 1 {
		t.Errorf("GetTrustedSupport(abc): got %d, want 1", got)
	}

	// Unknown ancestry falls back to flat count (zero here).
	unknown := consensus.LedgerID{0xff}
	if got := vt.TrustedSupport(unknown); got != 0 {
		t.Errorf("GetTrustedSupport(unknown) should fall back to 0, got %d", got)
	}
}

// TestValidationTracker_TrieNewerValidationReplacesOld verifies the
// "most recent trusted validation per node" rule: when n1 moves from
// abc to abde, the trie must remove abc's tip and insert abde's, so
// branchSupport reflects the actual current network state.
func TestValidationTracker_TrieNewerValidationReplacesOld(t *testing.T) {
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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
	if vt.TrustedSupport(abc.ID()) != 1 {
		t.Errorf("abc support after first validation should be 1")
	}

	// Newer validation from same node at a higher seq.
	if !vt.Add(makeTrustedValidation(n1, abde.ID(), abde.Seq(), now.Add(time.Second))) {
		t.Fatal("newer Add should succeed")
	}

	// abc's tip should have been removed; only abde contributes.
	if got := vt.TrustedSupport(abc.ID()); got != 0 {
		t.Errorf("abc support after switch: got %d, want 0", got)
	}
	if got := vt.TrustedSupport(abde.ID()); got != 1 {
		t.Errorf("abde support after switch: got %d, want 1", got)
	}
}

// TestValidationTracker_TrieNegUNLExcluded confirms a validator on the
// negUNL is excluded from GetTrustedSupport's quorum count, even though
// (post-#939) it now enters the trie for preferred-ledger steering. The
// exclusion lives on the support read path, not on trie membership.
func TestValidationTracker_TrieNegUNLExcluded(t *testing.T) {
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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

	// Only n1 counts toward support; n2 on negUNL is excluded.
	if got := vt.TrustedSupport(abc.ID()); got != 1 {
		t.Errorf("negUNL validator should not contribute to support: got %d, want 1", got)
	}
}

// TestValidationTracker_TrieNegUNLSteersButExcludedFromQuorum is the
// issue #939 regression: a negUNL validator must STEER GetPreferred
// (rippled's updateTrie gates on trusted() only) while being EXCLUDED
// from GetTrustedSupport's quorum/peer-LCL count.
//
// Topology — n1 (good) backs ab; n2 + n3 (both negUNL) back ac:
//
//	root -> a -> ab   (n1, trusted)
//	          \-> ac  (n2, n3 — both on negUNL)
//
// Steering (negUNL-inclusive trie): ac has branchSupport 2 vs ab's 1, so
// GetPreferred descends to ac — driven entirely by the negUNL validators,
// exactly as rippled would. Quorum/support (negUNL-excluded): ac has 0
// backing, the shared ancestor a has only n1's 1. The pre-fix trie
// (which dropped negUNL at insert) would instead have steered to ab.
func TestValidationTracker_TrieNegUNLSteersButExcludedFromQuorum(t *testing.T) {
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
	a := b.Build("a")
	ab := b.Build("ab")
	ac := b.Build("ac")
	provider := newMapAncestryProvider()
	provider.add(a)
	provider.add(ab)
	provider.add(ac)

	n1 := consensus.NodeID{1} // trusted, backs ab
	n2 := consensus.NodeID{2} // negUNL, backs ac
	n3 := consensus.NodeID{3} // negUNL, backs ac
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3})
	vt.SetNegativeUNL([]consensus.NodeID{n2, n3})
	vt.SetLedgerAncestryProvider(provider)

	vt.Add(makeTrustedValidation(n1, ab.ID(), ab.Seq(), now))
	vt.Add(makeTrustedValidation(n2, ac.ID(), ac.Seq(), now))
	vt.Add(makeTrustedValidation(n3, ac.ID(), ac.Seq(), now))

	// Steering: the two negUNL validators outweigh n1, so GetPreferred
	// descends into their branch. Without negUNL steering it would be ab.
	id, _, ok := vt.GetPreferred(0)
	if !ok {
		t.Fatal("GetPreferred should return a result with trie wired")
	}
	if id != ac.ID() {
		t.Errorf("negUNL validators must steer GetPreferred to ac; got a different ID (pre-fix would yield ab)")
	}

	// Quorum/support: negUNL validators contribute nothing.
	if got := vt.TrustedSupport(ac.ID()); got != 0 {
		t.Errorf("GetTrustedSupport(ac) must exclude negUNL backers: got %d, want 0", got)
	}
	if got := vt.TrustedSupport(ab.ID()); got != 1 {
		t.Errorf("GetTrustedSupport(ab): got %d, want 1", got)
	}
	// Even at the shared ancestor, only the single non-negUNL validator
	// counts — the descendant negUNL tips are filtered out.
	if got := vt.TrustedSupport(a.ID()); got != 1 {
		t.Errorf("GetTrustedSupport(a) must exclude negUNL descendants: got %d, want 1", got)
	}
}

// TestValidationTracker_TrieGetPreferred runs a simple 2-way
// competition through the full Add() path and asserts GetPreferred
// returns the trie's SpanTip.
func TestValidationTracker_TrieGetPreferred(t *testing.T) {
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
	abc := b.Build("abc")

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})

	vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now))
	vt.Add(makeTrustedValidation(n2, abc.ID(), abc.Seq(), now))

	// Without ancestry provider, GetTrustedSupport returns flat count.
	if got := vt.TrustedSupport(abc.ID()); got != 2 {
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
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
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
	if got := vt.TrustedSupport(ab.ID()); got != 2 {
		t.Fatalf("pre-expire branchSupport(ab): got %d, want 2", got)
	}

	// Expire validations below seq 4 — drops both tips. Jump the clock
	// first so the access-age guard doesn't retain the sets.
	now = now.Add(validationSetExpires + time.Second)
	vt.ExpireOld(4)

	// After expiry the trie must drop both tips. branchSupport on any
	// ancestor falls to 0 — no phantom support survives.
	if got := vt.TrustedSupport(ab.ID()); got != 0 {
		t.Errorf("post-expire branchSupport(ab): got %d, want 0 (trie tip leaked)", got)
	}
	if got := vt.TrustedSupport(abc.ID()); got != 0 {
		t.Errorf("post-expire branchSupport(abc): got %d, want 0", got)
	}
}

// TestValidationTracker_ProposersFinishedIncludesNegUNL pins #939's
// getNodesAfter-equivalent: ProposersFinished counts negUNL validators
// (rippled's getNodesAfter reads the trusted()-only trie), unlike the
// quorum count. Exercises the seq-only fallback (no ancestry provider)
// where the prior code wrongly skipped negUNL.
func TestValidationTracker_ProposersFinishedIncludesNegUNL(t *testing.T) {
	vt := NewValidationTracker(2)
	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	n1 := consensus.NodeID{1}
	n2 := consensus.NodeID{2}
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	vt.SetNegativeUNL([]consensus.NodeID{n2})

	// Both validators advance past prev (seq 100). With no ancestry
	// provider, ProposersFinished takes the seq-only fallback.
	vt.Add(makeTrustedValidation(n1, consensus.LedgerID{0xA1}, 101, now))
	vt.Add(makeTrustedValidation(n2, consensus.LedgerID{0xA2}, 101, now))

	prev := &mockLedger{id: consensus.LedgerID{0x10}, seq: 100}
	if got := vt.ProposersFinished(prev); got != 2 {
		t.Errorf("ProposersFinished must include negUNL validators: got %d, want 2", got)
	}
}

// TestValidationTracker_GetJSONTrie verifies the debug introspection dump:
// nil while the trie is disabled, and a marshalable support snapshot once an
// ancestry provider is wired and a trusted validation is inserted.
func TestValidationTracker_GetJSONTrie(t *testing.T) {
	vt := NewValidationTracker(2)

	if js := vt.GetJSONTrie(); js != nil {
		t.Fatalf("GetJSONTrie with no ancestry provider must be nil, got %v", js)
	}

	now := time.Now()
	vt.SetNow(func() time.Time { return now })

	b := ledgertrietest.NewTestLedgerBuilder()
	abc := b.Build("abc")
	provider := newMapAncestryProvider()
	provider.add(abc)

	n1 := consensus.NodeID{1}
	vt.SetTrusted([]consensus.NodeID{n1})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(n1, abc.ID(), abc.Seq(), now)) {
		t.Fatal("Add(n1->abc) should succeed")
	}

	js := vt.GetJSONTrie()
	if js == nil {
		t.Fatal("GetJSONTrie with a wired trie must be non-nil")
	}
	if _, err := json.Marshal(js); err != nil {
		t.Fatalf("GetJSONTrie output not JSON-marshalable: %v", err)
	}
	seqSupport, ok := js["seq_support"].(map[string]uint32)
	if !ok || seqSupport["3"] != 1 {
		t.Errorf("seq_support = %v, want 3:1", js["seq_support"])
	}
}
