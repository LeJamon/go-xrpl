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
			ids, _, _, unlock := tracker.BeginValidationPublication(100)
			release := sync.OnceFunc(unlock)
			defer release()
			if len(ids) != 0 {
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
			ids, _, available := tracker.QuorumValidatedLedgers(100)
			if !available || len(ids) != 1 || ids[0] != id {
				t.Fatalf("configuration did not expose authenticated quorum: %x", ids)
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

	ids, quorum, available := tracker.QuorumValidatedLedgers(100)
	if !available || quorum != 2 {
		t.Fatalf("quorum view = ids=%x quorum=%d available=%v, want available quorum 2", ids, quorum, available)
	}
	if len(ids) != 1 || ids[0] != matching {
		t.Fatalf("exact quorum hashes = %x, want only matching hash %x", ids, matching)
	}

	add(nodes[4], conflict, 100, true)
	add(nodes[5], conflict, 100, true)
	ids, _, available = tracker.QuorumValidatedLedgers(100)
	if !available || len(ids) != 2 || ids[0] != matching || ids[1] != conflict {
		t.Fatalf("conflicting quorum hashes = %x, want sorted matching/conflict", ids)
	}

	unavailable := true
	tracker.setQuorumUnavailableFunc(func() bool { return unavailable })
	ids, _, available = tracker.QuorumValidatedLedgers(100)
	if available || len(ids) != 0 {
		t.Fatalf("unavailable quorum view = ids=%x available=%v, want empty/unavailable", ids, available)
	}
}
