package rcl

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrietest"
)

func TestValidationTrackerIssue1463_ExactHashAndSequenceFinality(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	vt := NewValidationTracker(2)
	vt.SetNow(func() time.Time { return now })
	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}}
	vt.SetTrustedAndQuorum(nodes, 2)

	type notification struct {
		id  consensus.LedgerID
		seq uint32
	}
	var got []notification
	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		got = append(got, notification{id: id, seq: seq})
	})

	ledger := consensus.LedgerID{0xA}
	add := func(node consensus.NodeID, seq uint32) {
		t.Helper()
		if status := vt.AddStatus(&consensus.Validation{
			LedgerID: ledger, LedgerSeq: seq, NodeID: node,
			SignTime: now, SeenTime: now, Full: true,
		}); status != ValStatusCurrent {
			t.Fatalf("validation %x@%d status=%s, want current", node, seq, status)
		}
	}

	// Two different sequences share one hash. Neither sequence may borrow the
	// other's evidence when crossing the quorum threshold.
	add(nodes[0], 10)
	add(nodes[1], 11)
	add(nodes[2], 10)
	add(nodes[3], 11)
	if len(got) != 2 || got[0] != (notification{id: ledger, seq: 10}) ||
		got[1] != (notification{id: ledger, seq: 11}) {
		t.Fatalf("finality notifications=%v, want exact hash/sequence notifications", got)
	}
	if got := vt.GetTrustedFullValidations(ledger, 10); len(got) != 2 {
		t.Fatalf("exact seq 10 lookup returned %d validations, want 2", len(got))
	}
	if got := vt.GetTrustedFullValidations(ledger, 11); len(got) != 2 {
		t.Fatalf("exact seq 11 lookup returned %d validations, want 2", len(got))
	}
	if got := vt.GetTrustedFullValidations(ledger, 9); len(got) != 0 {
		t.Fatalf("wrong-sequence lookup returned %d validations", len(got))
	}
}

func TestValidationTrackerIssue1463_TrustQuorumNegativeUNLRecheck(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}}
	ledger := consensus.LedgerID{0xB}
	vt := NewValidationTracker(2)
	vt.SetNow(func() time.Time { return now })
	fires := 0
	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		if id != ledger || seq != 7 {
			t.Errorf("callback got (%x, %d), want (%x, 7)", id, seq, ledger)
		}
		// Re-enter both read and write paths from the callback. This must not
		// deadlock on the tracker's mutex.
		if vt.LatestValidation(nodes[0]) == nil {
			t.Error("callback could not read the latest validation")
		}
		fires++
	})
	for _, node := range nodes {
		if status := vt.AddStatus(&consensus.Validation{
			LedgerID: ledger, LedgerSeq: 7, NodeID: node,
			SignTime: now, SeenTime: now, Full: true,
		}); status != ValStatusCurrent {
			t.Fatalf("untrusted validation status=%s, want current", status)
		}
	}
	if fires != 0 {
		t.Fatalf("untrusted evidence fired %d callbacks", fires)
	}

	vt.SetTrustedAndQuorum(nodes, 2)
	if fires != 1 {
		t.Fatalf("trust promotion fired %d callbacks, want 1", fires)
	}
	vt.SetNegativeUNL([]consensus.NodeID{nodes[0]})
	if fires != 1 {
		t.Fatalf("negative-UNL demotion fired %d callbacks", fires)
	}
	vt.SetNegativeUNL(nil)
	if fires != 2 {
		t.Fatalf("negative-UNL re-enable fired %d callbacks, want 2", fires)
	}
	vt.SetTrustedAndQuorum(nodes, 3)
	if fires != 2 {
		t.Fatalf("unreachable quorum fired %d callbacks", fires)
	}
	vt.SetTrustedAndQuorum(nodes, 2)
	if fires != 3 {
		t.Fatalf("quorum re-enable fired %d callbacks, want 3", fires)
	}
}

func TestValidationTrackerIssue1463_AtomicNegativeUNLTransition(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}, {3}}
	ledger := consensus.LedgerID{0xB1}
	vt := NewValidationTracker(3)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrustedAndQuorum(nodes, 3)
	fires := 0
	vt.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) { fires++ })

	for _, node := range nodes[:2] {
		if status := vt.AddStatus(&consensus.Validation{
			LedgerID: ledger, LedgerSeq: 8, NodeID: node,
			SignTime: now, SeenTime: now, Full: true,
		}); status != ValStatusCurrent {
			t.Fatalf("validation status=%s, want current", status)
		}
	}

	vt.SetTrustedQuorumAndNegativeUNL(nodes, 2, []consensus.NodeID{nodes[1]})
	if fires != 0 {
		t.Fatalf("atomic negative-UNL transition fired %d callbacks", fires)
	}

	vt.SetTrustedQuorumAndNegativeUNL(nodes, 2, nil)
	if fires != 1 {
		t.Fatalf("negative-UNL re-enable fired %d callbacks, want 1", fires)
	}
}

func TestValidationTrackerIssue1463_ZeroQuorumNeedsTrustedFullEvidence(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	n1, n2 := consensus.NodeID{1}, consensus.NodeID{2}
	ledger := consensus.LedgerID{0xC}
	vt := NewValidationTracker(0)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrustedAndQuorum([]consensus.NodeID{n1}, 0)
	fires := 0
	vt.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) { fires++ })

	if status := vt.AddStatus(&consensus.Validation{
		LedgerID: ledger, LedgerSeq: 8, NodeID: n1,
		SignTime: now, SeenTime: now, Full: false,
	}); status != ValStatusCurrent {
		t.Fatalf("partial validation status=%s, want current", status)
	}
	if status := vt.AddStatus(&consensus.Validation{
		LedgerID: ledger, LedgerSeq: 8, NodeID: n2,
		SignTime: now, SeenTime: now, Full: true,
	}); status != ValStatusCurrent {
		t.Fatalf("untrusted full validation status=%s, want current", status)
	}
	if fires != 0 {
		t.Fatalf("zero quorum promoted without trusted full evidence: fires=%d", fires)
	}
	vt.SetTrustedAndQuorum([]consensus.NodeID{n2}, 0)
	if fires != 1 {
		t.Fatalf("zero quorum trusted full evidence fired %d callbacks, want 1", fires)
	}
}

func TestValidationTrackerIssue1463_LiveGateRecheck(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	node := consensus.NodeID{1}
	ledger := consensus.LedgerID{0xC1}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})
	unavailable := true
	vt.SetQuorumUnavailableFunc(func() bool { return unavailable })
	fired := 0
	vt.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) { fired++ })

	if status := vt.AddStatus(&consensus.Validation{
		LedgerID: ledger, LedgerSeq: 12, NodeID: node,
		SignTime: now, SeenTime: now, Full: true,
	}); status != ValStatusCurrent {
		t.Fatalf("validation status=%s, want current", status)
	}
	if fired != 0 {
		t.Fatalf("live unavailable gate allowed finality: fired=%d", fired)
	}

	unavailable = false
	vt.RecheckFinality()
	if fired != 1 {
		t.Fatalf("opening the live gate did not promote stored evidence: fired=%d", fired)
	}
}

func TestValidationTrackerIssue1463_PendingResolverHonorsLiveGate(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	node := consensus.NodeID{1}
	ledger := consensus.LedgerID{0xC2}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})
	unavailable := true
	vt.SetQuorumUnavailableFunc(func() bool { return unavailable })
	if status := vt.AddStatus(&consensus.Validation{
		LedgerID: ledger, LedgerSeq: 13, NodeID: node,
		SignTime: now, SeenTime: now, Full: true,
	}); status != ValStatusCurrent {
		t.Fatalf("validation status=%s, want current", status)
	}

	validations, quorum, accepted := vt.RecheckFullyValidated(ledger, 13)
	if accepted || quorum != 1 || len(validations) != 1 {
		t.Fatalf("pending resolver accepted with gate closed: validations=%d quorum=%d accepted=%v", len(validations), quorum, accepted)
	}
	unavailable = false
	validations, quorum, accepted = vt.RecheckFullyValidated(ledger, 13)
	if !accepted || quorum != 1 || len(validations) != 1 {
		t.Fatalf("pending resolver did not accept after gate opened: validations=%d quorum=%d accepted=%v", len(validations), quorum, accepted)
	}
}

func TestValidationTrackerIssue1463_DemotionCancelsQueuedFinality(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	n1, n2 := consensus.NodeID{1}, consensus.NodeID{2}
	first, second := consensus.LedgerID{0xD1}, consensus.LedgerID{0xD2}
	vt := NewValidationTracker(2)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{n1, n2})
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var got []consensus.LedgerID
	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, _ uint32) {
		mu.Lock()
		got = append(got, id)
		firstCallback := len(got) == 1
		mu.Unlock()
		if firstCallback {
			if id != first {
				t.Errorf("first callback got %x, want %x", id, first)
			}
			close(entered)
			<-release
		}
	})

	add := func(node consensus.NodeID, id consensus.LedgerID, seq uint32, signTime time.Time) {
		t.Helper()
		if status := vt.AddStatus(&consensus.Validation{
			LedgerID: id, LedgerSeq: seq, NodeID: node,
			SignTime: signTime, SeenTime: now, Full: true,
		}); status != ValStatusCurrent {
			t.Fatalf("validation (%x,%d,%x) status=%s, want current", id, seq, node, status)
		}
	}
	add(n1, first, 20, now)
	firstDone := make(chan struct{})
	go func() {
		add(n2, first, 20, now)
		close(firstDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("timed out waiting for first finality callback")
	}

	// The active callback owns the drainer. These two additions enqueue the
	// second ledger but cannot invoke it until the first callback returns.
	add(n1, second, 21, now.Add(time.Second))
	add(n2, second, 21, now.Add(time.Second))
	vt.SetTrustedAndQuorum([]consensus.NodeID{n1}, 2)
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for finality drainer")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != first {
		t.Fatalf("demotion failed to cancel queued finality: got=%v", got)
	}
}

func TestValidationTrackerIssue1463_FinalityNotificationOrder(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	n1, n2, n3 := consensus.NodeID{1}, consensus.NodeID{2}, consensus.NodeID{3}
	// Same-sequence hashes must use lexical hash order; lower sequence wins
	// regardless of insertion/map iteration order.
	lowA, lowB, high := consensus.LedgerID{0x10}, consensus.LedgerID{0x20}, consensus.LedgerID{0x30}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{n1, n2, n3})
	add := func(node consensus.NodeID, id consensus.LedgerID, seq uint32) {
		t.Helper()
		if status := vt.AddStatus(&consensus.Validation{
			LedgerID: id, LedgerSeq: seq, NodeID: node,
			SignTime: now, SeenTime: now, Full: true,
		}); status != ValStatusCurrent {
			t.Fatalf("validation (%x,%d) status=%s, want current", id, seq, status)
		}
	}
	// Store evidence before installing the callback so all notifications are
	// queued by one sorted recheck rather than by message arrival order.
	add(n1, high, 22)
	add(n2, lowB, 21)
	add(n3, lowA, 21)
	var got []consensus.LedgerID
	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, _ uint32) { got = append(got, id) })
	want := []consensus.LedgerID{lowA, lowB, high}
	if len(got) != len(want) {
		t.Fatalf("notification count=%d, want %d (%v)", len(got), len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notification order=%v, want %v", got, want)
		}
	}
}

func TestValidationTrackerIssue1463_TrustedRemovalRearmsEvidence(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	node := consensus.NodeID{1}
	ledger := consensus.LedgerID{0xD3}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})
	fires := 0
	vt.SetFullyValidatedCallback(func(consensus.LedgerID, uint32) { fires++ })
	if status := vt.AddStatus(&consensus.Validation{
		LedgerID: ledger, LedgerSeq: 23, NodeID: node,
		SignTime: now, SeenTime: now, Full: true,
	}); status != ValStatusCurrent {
		t.Fatalf("validation status=%s, want current", status)
	}
	if fires != 1 {
		t.Fatalf("initial finality callback count=%d, want 1", fires)
	}
	vt.SetTrustedAndQuorum(nil, 1)
	if fires != 1 || vt.TrustedValidationCount(ledger) != 0 {
		t.Fatalf("trusted removal changed callback/count unexpectedly: fires=%d count=%d", fires, vt.TrustedValidationCount(ledger))
	}
	vt.SetTrustedAndQuorum([]consensus.NodeID{node}, 1)
	if fires != 2 {
		t.Fatalf("trusted re-add did not rearm stored evidence: fires=%d, want 2", fires)
	}
}

func TestValidationTrackerIssue1463_ValidationDeepClone(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	node := consensus.NodeID{1}
	ledger := consensus.LedgerID{0xD}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})

	var amendment [32]byte
	amendment[0] = 2
	input := &consensus.Validation{
		LedgerID: ledger, LedgerSeq: 9, NodeID: node,
		SignTime: now, SeenTime: now, Full: true,
		Signature:   []byte{1, 2},
		Amendments:  [][32]byte{amendment},
		SigningData: []byte{3, 4},
		Raw:         []byte{5, 6},
	}
	if status := vt.AddStatus(input); status != ValStatusCurrent {
		t.Fatalf("validation status=%s, want current", status)
	}

	input.Signature[0] = 9
	input.Amendments[0][0] = 9
	input.SigningData[0] = 9
	input.Raw[0] = 9
	input.Full = false
	latest := vt.LatestValidation(node)
	if latest == nil || !bytes.Equal(latest.Signature, []byte{1, 2}) ||
		latest.Amendments[0][0] != 2 || !bytes.Equal(latest.SigningData, []byte{3, 4}) ||
		!bytes.Equal(latest.Raw, []byte{5, 6}) || !latest.Full {
		t.Fatal("mutating the submitted validation changed tracker state")
	}

	latest.Signature[0] = 8
	latest.Amendments[0][0] = 8
	latest.SigningData[0] = 8
	latest.Raw[0] = 8
	retrieved := vt.GetTrustedFullValidations(ledger, 9)
	if len(retrieved) != 1 || !bytes.Equal(retrieved[0].Signature, []byte{1, 2}) ||
		retrieved[0].Amendments[0][0] != 2 || !bytes.Equal(retrieved[0].SigningData, []byte{3, 4}) ||
		!bytes.Equal(retrieved[0].Raw, []byte{5, 6}) {
		t.Fatal("mutating a returned validation changed tracker state")
	}
	retrieved[0].Signature[0] = 7
	retrieved[0].Amendments[0][0] = 7
	retrieved[0].SigningData[0] = 7
	retrieved[0].Raw[0] = 7
	retrieved = vt.GetTrustedFullValidations(ledger, 9)
	if len(retrieved) != 1 || !bytes.Equal(retrieved[0].Signature, []byte{1, 2}) ||
		retrieved[0].Amendments[0][0] != 2 || !bytes.Equal(retrieved[0].SigningData, []byte{3, 4}) ||
		!bytes.Equal(retrieved[0].Raw, []byte{5, 6}) {
		t.Fatal("mutating a trusted-validation snapshot changed tracker state")
	}
}

func TestValidationTrackerIssue1463_CurrentTipUsesSigningTime(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	node := consensus.NodeID{1}
	oldID := consensus.LedgerID{0xE}
	olderID := consensus.LedgerID{0xF}
	newerID := consensus.LedgerID{0x10}
	vt := NewValidationTracker(10)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})

	old := &consensus.Validation{LedgerID: oldID, LedgerSeq: 10, NodeID: node, SignTime: now, SeenTime: now, Full: true}
	if status := vt.AddStatus(old); status != ValStatusCurrent {
		t.Fatalf("initial validation status=%s, want current", status)
	}
	older := &consensus.Validation{LedgerID: olderID, LedgerSeq: 11, NodeID: node, SignTime: now.Add(-time.Second), SeenTime: now, Full: true}
	if status := vt.AddStatus(older); status != ValStatusStale {
		t.Fatalf("older higher-sequence validation status=%s, want stale", status)
	}
	if latest := vt.LatestValidation(node); latest == nil || latest.LedgerID != oldID || latest.LedgerSeq != 10 {
		t.Fatalf("older higher-sequence validation replaced current tip: %#v", latest)
	}
	if got := vt.GetTrustedFullValidations(olderID, 11); len(got) != 1 {
		t.Fatalf("stale current replacement was not retained in by-ledger evidence: %d", len(got))
	}

	equal := &consensus.Validation{LedgerID: consensus.LedgerID{0x11}, LedgerSeq: 12, NodeID: node, SignTime: now, SeenTime: now, Full: true}
	if status := vt.AddStatus(equal); status != ValStatusStale {
		t.Fatalf("equal-sign-time higher-sequence validation status=%s, want stale", status)
	}
	newer := &consensus.Validation{LedgerID: newerID, LedgerSeq: 13, NodeID: node, SignTime: now.Add(time.Nanosecond), SeenTime: now, Full: true}
	if status := vt.AddStatus(newer); status != ValStatusCurrent {
		t.Fatalf("newer-sign-time validation status=%s, want current", status)
	}
	if latest := vt.LatestValidation(node); latest == nil || latest.LedgerID != newerID || latest.LedgerSeq != 13 {
		t.Fatalf("newer-sign-time validation did not replace current tip: %#v", latest)
	}
}

func TestValidationTrackerIssue1463_NegativeUNLPreservesAcquiring(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	builder := ledgertrietest.NewTestLedgerBuilder()
	held := builder.Build("abc")
	missing := builder.Build("abcd")
	provider := newMapAncestryProvider()
	provider.add(held)
	node := consensus.NodeID{1}
	vt := NewValidationTracker(1)
	vt.SetNow(func() time.Time { return now })
	vt.SetTrusted([]consensus.NodeID{node})
	vt.SetLedgerAncestryProvider(provider)
	if !vt.Add(makeTrustedValidation(node, held.ID(), held.Seq(), now)) ||
		!vt.Add(makeTrustedValidation(node, missing.ID(), missing.Seq(), now)) {
		t.Fatal("failed to add trie validations")
	}
	vt.mu.RLock()
	before := len(vt.acquiring)
	vt.mu.RUnlock()
	if before != 1 {
		t.Fatalf("missing ledger acquisition entries=%d, want 1", before)
	}
	if id, seq, ok := vt.GetPreferred(0); !ok || id != held.ID() || seq != held.Seq() {
		t.Fatalf("preferred ledger before negative-UNL update = (%x, %d, %t), want held tip", id, seq, ok)
	}

	vt.SetNegativeUNL([]consensus.NodeID{node})
	vt.mu.RLock()
	after := len(vt.acquiring)
	vt.mu.RUnlock()
	if after != before {
		t.Fatalf("negative-UNL update rebuilt/dropped acquisition state: before=%d after=%d", before, after)
	}
	if id, seq, ok := vt.GetPreferred(0); !ok || id != held.ID() || seq != held.Seq() {
		t.Fatalf("preferred ledger after negative-UNL update = (%x, %d, %t), want held tip", id, seq, ok)
	}
	provider.add(missing)
	if id, seq, ok := vt.GetPreferred(0); !ok || id != missing.ID() || seq != missing.Seq() {
		t.Fatalf("preserved acquisition did not replay after ledger arrival: (%x, %d, %t)", id, seq, ok)
	}
}
