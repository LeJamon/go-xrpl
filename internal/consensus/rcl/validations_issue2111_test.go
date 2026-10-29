package rcl

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestValidationPublicationFencesConfigurationChanges(t *testing.T) {
	nodes := []consensus.NodeID{{1}, {2}}
	for _, kind := range []string{"trust", "combined", "negative UNL"} {
		t.Run(kind, func(t *testing.T) {
			tracker := NewValidationTracker(3)
			tracker.SetTrustedAndQuorum(nodes, 3)
			if kind == "negative UNL" {
				tracker.SetTrustedAndQuorum(nodes, 2)
				tracker.setNegativeUNL(nodes[:1])
			}
			id := consensus.LedgerID{9}
			now := time.Now()
			for _, node := range nodes {
				tracker.Add(&consensus.Validation{NodeID: node, LedgerID: id, LedgerSeq: 100,
					SignTime: now, SeenTime: now, Full: true})
			}
			publication := tracker.BeginValidationPublication(100)
			release := sync.OnceFunc(publication.Release)
			defer release()
			if len(publication.LedgerIDs) != 0 {
				t.Fatal("fixture already has quorum")
			}
			started, done := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				switch kind {
				case "trust":
					tracker.SetTrustedAndQuorum(nodes, 2)
				case "combined":
					tracker.updateTrustedQuorumAndNegativeUNL(nodes, 2, nil)
				case "negative UNL":
					tracker.setNegativeUNL(nil)
				}
				close(done)
			}()
			<-started
			select {
			case <-done:
				t.Fatal("configuration crossed quorum during publication")
			case <-time.After(20 * time.Millisecond):
			}
			release()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("configuration did not resume after publication")
			}
			snapshot := tracker.QuorumValidatedLedgers(100)
			if !snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
				t.Fatalf("configuration did not expose authenticated quorum: %x", snapshot.LedgerIDs)
			}
		})
	}
}

func TestValidationTracker_QuorumValidatedLedgersIsExactAndAuthenticated(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}, {5}, {6}}
	matching := consensus.LedgerID{0x10}
	conflict := consensus.LedgerID{0x20}

	tracker := NewValidationTracker(2)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum(nodes, 2)
	add := func(node consensus.NodeID, id consensus.LedgerID, seq uint32, full bool) {
		t.Helper()
		if !tracker.Add(&consensus.Validation{
			NodeID: node, LedgerID: id, LedgerSeq: seq, Full: full,
			SignTime: now, SeenTime: now,
		}) {
			t.Fatalf("validation from node %x was rejected", node)
		}
	}

	add(nodes[0], matching, 100, true)
	add(nodes[1], matching, 100, true)
	add(nodes[2], conflict, 100, false) // partials do not prove a fork
	add(nodes[3], matching, 99, true)   // a different sequence is irrelevant

	snapshot := tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || snapshot.Quorum != 2 {
		t.Fatalf("quorum view = ids=%x quorum=%d available=%v, want available quorum 2", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available)
	}
	if len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != matching {
		t.Fatalf("exact quorum hashes = %x, want only matching hash %x", snapshot.LedgerIDs, matching)
	}

	add(nodes[4], conflict, 100, true)
	add(nodes[5], conflict, 100, true)
	snapshot = tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || len(snapshot.LedgerIDs) != 2 || snapshot.LedgerIDs[0] != matching || snapshot.LedgerIDs[1] != conflict {
		t.Fatalf("conflicting quorum hashes = %x, want sorted matching/conflict", snapshot.LedgerIDs)
	}

	unavailable := true
	tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
	snapshot = tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != matching {
		t.Fatalf("unavailable quorum view = ids=%x available=%v, want retained matching/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}
}

func TestValidationTracker_RetainedQuorumSurvivesGateTrustAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}, {3}}
	id := consensus.LedgerID{0x31}
	unavailable := false

	tracker := NewValidationTracker(2)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum(nodes, 2)
	tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
	add := func(node consensus.NodeID) {
		t.Helper()
		if !tracker.Add(&consensus.Validation{
			NodeID: node, LedgerID: id, LedgerSeq: 100, Full: true,
			SignTime: now, SeenTime: now,
		}) {
			t.Fatalf("validation from node %x was rejected", node)
		}
	}
	add(nodes[0])
	add(nodes[1])

	unavailable = true
	snapshot := tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || snapshot.Quorum != 2 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("retained unavailable quorum = ids=%x quorum=%d available=%v, want %x/2/false", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}
	if snapshot.RetainedLedgerID != id || snapshot.RetainedLedgerSeq != 100 {
		t.Fatalf("retained quorum floor = %x/%d, want %x/100", snapshot.RetainedLedgerID, snapshot.RetainedLedgerSeq, id)
	}
	publication := tracker.BeginValidationPublication(100)
	if publication.Available || publication.Quorum != 2 || len(publication.LedgerIDs) != 1 || publication.LedgerIDs[0] != id ||
		publication.RetainedLedgerID != id || publication.RetainedLedgerSeq != 100 {
		publication.Release()
		t.Fatalf("retained publication snapshot = ids=%x quorum=%d available=%v, want %x/2/false", publication.LedgerIDs, publication.Quorum, publication.Available, id)
	}
	publication.Release()

	// Raising the live threshold removes the current quorum but cannot revoke
	// a proof that was already authenticated while the gate was open.
	tracker.SetTrustedAndQuorum(nodes, 3)
	snapshot = tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || snapshot.Quorum != 3 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("raised unavailable quorum = ids=%x quorum=%d available=%v, want retained %x/3/false", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}
	tracker.SetTrustedAndQuorum([]consensus.NodeID{nodes[2]}, 1)
	snapshot = tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || snapshot.Quorum != 1 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("removed-trust unavailable quorum = ids=%x quorum=%d available=%v, want retained %x/1/false", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}

	unavailable = false
	snapshot = tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || snapshot.Quorum != 1 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("removed-trust live quorum = ids=%x quorum=%d available=%v, want retained %x/1/true", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}

	// Expire the backing validation set. The bounded proof remains available
	// after the current evidence has left the tracker.
	now = now.Add(validationSetExpires + time.Second)
	tracker.ExpireOld(101)
	if got := tracker.getValidationCount(id); got != 0 {
		t.Fatalf("expired validation count = %d, want 0", got)
	}
	snapshot = tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || snapshot.Quorum != 1 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("post-expiry quorum = ids=%x quorum=%d available=%v, want retained %x/1/true", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}
	tracker.flush()
	snapshot = tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || snapshot.Quorum != 1 || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("post-flush quorum = ids=%x quorum=%d available=%v, want retained %x/1/true", snapshot.LedgerIDs, snapshot.Quorum, snapshot.Available, id)
	}
}

func TestValidationTracker_NoQuorumProofIsLatchedWhileUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}}
	id := consensus.LedgerID{0x41}
	unavailable := true

	tracker := NewValidationTracker(2)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum(nodes, 2)
	tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
	for _, node := range nodes {
		if !tracker.Add(&consensus.Validation{
			NodeID: node, LedgerID: id, LedgerSeq: 100, Full: true,
			SignTime: now, SeenTime: now,
		}) {
			t.Fatalf("validation from node %x was rejected", node)
		}
	}
	if snapshot := tracker.QuorumValidatedLedgers(100); snapshot.Available || len(snapshot.LedgerIDs) != 0 {
		t.Fatalf("closed-gate first quorum = ids=%x available=%v, want no proof/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}

	unavailable = false
	tracker.recheckFinality()
	snapshot := tracker.QuorumValidatedLedgers(100)
	if !snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("settled quorum = ids=%x available=%v, want proof/available", snapshot.LedgerIDs, snapshot.Available)
	}
	unavailable = true
	snapshot = tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != id {
		t.Fatalf("settled retained quorum = ids=%x available=%v, want proof/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}
}

func TestValidationTracker_RetainedQuorumExcludesPartialAndUntrusted(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	trusted := []consensus.NodeID{{1}, {2}}
	untrusted := consensus.NodeID{3}
	id := consensus.LedgerID{0x51}

	tracker := NewValidationTracker(2)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum(trusted, 2)
	if !tracker.Add(&consensus.Validation{
		NodeID: trusted[0], LedgerID: id, LedgerSeq: 100, Full: true,
		SignTime: now, SeenTime: now,
	}) {
		t.Fatal("trusted full validation was rejected")
	}
	if !tracker.Add(&consensus.Validation{
		NodeID: untrusted, LedgerID: id, LedgerSeq: 100, Full: true,
		SignTime: now, SeenTime: now,
	}) {
		t.Fatal("untrusted validation was rejected")
	}
	if !tracker.Add(&consensus.Validation{
		NodeID: trusted[1], LedgerID: id, LedgerSeq: 100, Full: false,
		SignTime: now, SeenTime: now,
	}) {
		t.Fatal("trusted partial validation was rejected")
	}
	if snapshot := tracker.QuorumValidatedLedgers(100); !snapshot.Available || len(snapshot.LedgerIDs) != 0 {
		t.Fatalf("partial/untrusted evidence = ids=%x available=%v, want no proof/available", snapshot.LedgerIDs, snapshot.Available)
	}
}

func TestValidationTracker_RetainedQuorumIsMonotonic(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}}
	first := consensus.LedgerID{0x61}
	second := consensus.LedgerID{0x62}
	higher := consensus.LedgerID{0x63}
	unavailable := false

	tracker := NewValidationTracker(2)
	tracker.SetNow(func() time.Time { return now })
	tracker.SetTrustedAndQuorum(nodes, 2)
	tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
	add := func(node consensus.NodeID, id consensus.LedgerID, seq uint32, signTime time.Time) {
		t.Helper()
		if !tracker.Add(&consensus.Validation{
			NodeID: node, LedgerID: id, LedgerSeq: seq, Full: true,
			SignTime: signTime, SeenTime: now,
		}) {
			t.Fatalf("validation from node %x was rejected", node)
		}
	}

	add(nodes[0], first, 100, now)
	add(nodes[1], first, 100, now)
	add(nodes[2], second, 100, now)
	add(nodes[3], second, 100, now)
	unavailable = true
	snapshot := tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != first {
		t.Fatalf("same-sequence retained proof = ids=%x available=%v, want first only/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}

	unavailable = false
	newSignTime := now.Add(time.Second)
	add(nodes[0], higher, 101, newSignTime)
	add(nodes[1], higher, 101, newSignTime)
	unavailable = true
	snapshot = tracker.QuorumValidatedLedgers(101)
	if snapshot.Available || len(snapshot.LedgerIDs) != 1 || snapshot.LedgerIDs[0] != higher {
		t.Fatalf("higher retained proof = ids=%x available=%v, want higher only/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}
	snapshot = tracker.QuorumValidatedLedgers(100)
	if snapshot.Available || len(snapshot.LedgerIDs) != 0 {
		t.Fatalf("lower retained proof = ids=%x available=%v, want no proof/unavailable", snapshot.LedgerIDs, snapshot.Available)
	}
}
