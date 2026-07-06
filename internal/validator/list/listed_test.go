package list

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestAggregator_IsListed pins the listed-vs-trusted split: a validator
// carried by one live publisher list is listed the moment counts recompute,
// even while it stays below the trust threshold, and delists once the
// carrying publisher stops contributing.
func TestAggregator_IsListed(t *testing.T) {
	pk1 := PublisherKey{1}
	pk2 := PublisherKey{2}
	k1 := [33]byte{9}
	nid := consensus.CalcNodeID(k1)
	now := time.Unix(1000, 0)

	a := &Aggregator{
		publishers: map[PublisherKey]struct{}{pk1: {}, pk2: {}},
		state: map[PublisherKey]*PublisherState{
			pk1: {MasterKey: pk1, Status: StatusUnavailable},
			pk2: {MasterKey: pk2, Status: StatusUnavailable},
		},
		threshold: 2,
		clock:     func() time.Time { return now },
	}

	if a.IsListed(nid) {
		t.Fatal("nothing is listed before any count recompute")
	}

	// One live publisher carries k1: below the threshold of 2 (untrusted)
	// but listed.
	s := a.state[pk1]
	s.Status = StatusAvailable
	s.Validators = [][33]byte{k1}
	s.Expiration = now.Add(time.Hour)
	a.Tick()

	if !a.IsListed(nid) {
		t.Fatal("validator carried by a live publisher list must be listed")
	}
	if nodes, _ := a.TrustedValidators(); len(nodes) != 0 {
		t.Fatalf("one listing below threshold 2 must not be trusted; got %d", len(nodes))
	}
	if a.IsListed(consensus.NodeID{0x77}) {
		t.Fatal("unknown validator must not be listed")
	}

	// The carrying list expires → the validator delists on the next
	// recompute.
	s.Expiration = now.Add(-time.Minute)
	a.Tick()
	if a.IsListed(nid) {
		t.Fatal("validator must delist once its only carrying list expires")
	}
}
